package gitsvc

// Sync: fetch, pull, push and the repairs around them (upstream tracking,
// branch-level pulls, leftover lock files). Every network call goes through
// gitx.WithRemoteAuth (the active profile's token, injected into the remote URL
// only for the duration of the call) and runs with GIT_TERMINAL_PROMPT=0, so a
// missing credential fails fast instead of hanging on a TTY the app does not
// have.

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// ForcedRefUpdate is ForcedRefUpdate in src/shared/types.ts: a remote-tracking
// ref that was rewritten under us (history was force-pushed).
type ForcedRefUpdate struct {
	// Ref is the short form, e.g. `origin/feature`.
	Ref    string `json:"ref"`
	OldSha string `json:"oldSha"`
	NewSha string `json:"newSha"`
}

// UpstreamSuggestion is UpstreamSuggestion in src/shared/types.ts.
type UpstreamSuggestion struct {
	Branch          string `json:"branch"`
	Remote          string `json:"remote"`
	RemoteRefExists bool   `json:"remoteRefExists"`
}

// GitLockFile is GitLockFile in src/shared/types.ts.
type GitLockFile struct {
	// Path is relative to the git directory: `index.lock`, `refs/remotes/origin/x.lock`.
	Path string `json:"path"`
	// AgeSeconds since the file was last touched — a young lock is a live git process.
	AgeSeconds int `json:"ageSeconds"`
	// Kind: 'index' | 'ref' | 'config' | 'other'.
	Kind string `json:"kind"`
}

// PushRemoteResult is PushRemoteResult in src/shared/types.ts.
type PushRemoteResult struct {
	Remote string `json:"remote"`
	OK     bool   `json:"ok"`
	// Error is git's message when OK is false.
	Error string `json:"error"`
}

// PushOptions is push's `opts: { force?, remote? }`.
type PushOptions struct {
	Force bool `json:"force,omitempty"`
	// Remote defaults to 'origin' when absent (nil), not when empty.
	Remote *string `json:"remote,omitempty"`
}

// PushToRemotesOptions is pushToRemotes' `opts: { force?, tags? }`.
type PushToRemotesOptions struct {
	Force bool `json:"force,omitempty"`
	Tags  bool `json:"tags,omitempty"`
}

// dParseForcedUpdates decodes `git fetch --porcelain` (src/shared/fetchPorcelain.ts):
//
//	<flag> <old-oid> <new-oid> <local-ref>
//
// The flag is a space for a fast-forward, `*` for a new ref, `-` for a pruned
// one, `t` for a tag update and **`+` for a forced update** — the one that
// matters here, because it means someone rewrote history on the remote and the
// commits you already reviewed are gone.
func dParseForcedUpdates(out string) []ForcedRefUpdate {
	forced := []ForcedRefUpdate{}
	for _, line := range dLines(out) {
		if !strings.HasPrefix(line, "+") {
			continue
		}
		f := strings.Split(line, " ")
		at := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		oldSha, newSha, ref := at(1), at(2), at(3)
		// A zero old-oid would mean "brand new ref", which is not a rewrite.
		if oldSha == "" || newSha == "" || ref == "" || strings.Trim(oldSha, "0") == "" {
			continue
		}
		forced = append(forced, ForcedRefUpdate{Ref: strings.TrimPrefix(ref, "refs/remotes/"), OldSha: oldSha, NewSha: newSha})
	}
	return forced
}

var dPorcelainUnsupportedRe = regexp.MustCompile(`(?i)unknown option|porcelain`)

// dRunAuthed runs git args under WithRemoteAuth for remote and returns stdout.
func (s *GitService) dRunAuthed(ctx context.Context, repoPath, remote string, args []string) (string, error) {
	var out string
	err := gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
		o, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
		out = o
		return err
	})
	return out, err
}

// FetchAll fetches every remote, pruning, and returns the refs that were
// **force-updated** by this fetch — history the remote rewrote under us.
// `--porcelain` is what makes that knowable without diffing every ref by
// hand; on a git too old to support it we simply fetch and report nothing.
//
// gitsvc: fetchAll
func (s *GitService) FetchAll(ctx context.Context, repoPath string) ([]ForcedRefUpdate, error) {
	return locked(s, ctx, "fetchAll", repoPath, func(ctx context.Context) ([]ForcedRefUpdate, error) {
		// `--all` spans every remote; authenticate the common case (origin). Other
		// private https remotes without a matching PAT fail fast rather than hang.
		out, err := s.dRunAuthed(ctx, repoPath, "origin", []string{"fetch", "--all", "--prune", "--porcelain"})
		if err == nil {
			return dParseForcedUpdates(out), nil
		}
		if !dPorcelainUnsupportedRe.MatchString(err.Error()) {
			return nil, err
		}
		if _, err := s.dRunAuthed(ctx, repoPath, "origin", []string{"fetch", "--all", "--prune"}); err != nil {
			return nil, err
		}
		return []ForcedRefUpdate{}, nil
	})
}

// LastFetchAt is when this repository last fetched, from FETCH_HEAD's mtime
// (milliseconds). Reading the file rather than trusting our own bookkeeping is
// the point: a `git fetch` run in a terminal, or by another Gitcito window,
// counts the same as ours. Nil when the repository has never fetched.
//
// gitsvc: lastFetchAt
func (s *GitService) LastFetchAt(ctx context.Context, repoPath string) (*float64, error) {
	return locked(s, ctx, "lastFetchAt", repoPath, func(ctx context.Context) (*float64, error) {
		raw, err := gitx.Raw(ctx, repoPath, []string{"rev-parse", "--git-path", "FETCH_HEAD"}, gitx.RunOpts{})
		if err != nil {
			return nil, nil
		}
		rel := gitx.TrimJS(raw)
		p := rel
		if !filepath.IsAbs(rel) {
			p = filepath.Join(repoPath, rel)
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, nil
		}
		ms := dMtimeMs(st)
		return &ms, nil
	})
}

// dRerere holds, per repository, the files git resolved from a memorised
// resolution during the operation that is currently conflicted —
// rerereReplayed in git.ts. Git only says so once ("Resolved 'x' using
// previous resolution." on the output of the merge/rebase/pull that hit the
// conflict) and nothing in the repository records it afterwards, so it is
// captured when it is said. Session state on purpose: after a restart the hint
// is simply gone, which is better than showing a guess.
//
// Shared with slice C (merge, rebase, rerereStatus): the lead hoists this into
// one place.
var dRerere = struct {
	sync.Mutex
	byRepo map[string][]string
}{byRepo: map[string][]string{}}

// dWithRerereCapture runs an operation that may conflict, noting any rerere
// replays its error output announces.
func dWithRerereCapture(repoPath string, op func() error) error {
	err := op()
	if err != nil {
		if files := gitx.ParseRerereResolved(err.Error()); len(files) > 0 {
			dRerere.Lock()
			dRerere.byRepo[repoPath] = files
			dRerere.Unlock()
		}
	}
	return err
}

// dPull is pull's body, callable from pullBranch without re-taking the lock.
func (s *GitService) dPull(ctx context.Context, repoPath, mode string) error {
	remote := gitx.UpstreamRemote(ctx, repoPath)
	args := []string{"pull"}
	if mode == "ff-only" {
		args = append(args, "--ff-only")
	}
	if mode == "rebase" {
		args = append(args, "--rebase")
	}
	return dWithRerereCapture(repoPath, func() error {
		return gitx.WithAutoStash(ctx, repoPath, "pull", func(ctx context.Context) error {
			_, err := s.dRunAuthed(ctx, repoPath, remote, args)
			return err
		})
	})
}

// Pull pulls the current branch from its upstream: mode is 'default' |
// 'ff-only' | 'rebase' (” behaves as 'default'). A dirty working tree is
// shelved under a named stash for the duration.
//
// gitsvc: pull
func (s *GitService) Pull(ctx context.Context, repoPath string, mode string) error {
	return lockedErr(s, ctx, "pull", repoPath, func(ctx context.Context) error {
		return s.dPull(ctx, repoPath, mode)
	})
}

// dRemoteNames lists remote names in `git remote -v` order — what
// gitService.remotes() returned, reduced to the names.
func dRemoteNames(ctx context.Context, repoPath string) ([]string, error) {
	out, err := gitx.Raw(ctx, repoPath, []string{"remote", "-v"}, gitx.RunOpts{})
	if err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	for _, line := range dLines(out) {
		name := dSplitWS(gitx.TrimJS(line))[0]
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// UpstreamSuggestion says what a "there is no tracking information" pull is
// actually missing. Git prints the two repairs as CLI incantations and leaves
// the user to pick; this answers the question the choice hinges on — whether
// the remote already carries the branch — so the UI can offer one button
// instead of a man page. Nil when there is nothing to repair (detached HEAD, an
// upstream already set, or no remote at all).
//
// gitsvc: upstreamSuggestion
func (s *GitService) UpstreamSuggestion(ctx context.Context, repoPath string) (*UpstreamSuggestion, error) {
	return locked(s, ctx, "upstreamSuggestion", repoPath, func(ctx context.Context) (*UpstreamSuggestion, error) {
		branch, err := gitx.RunGit(ctx, repoPath, []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, gitx.RunOpts{})
		if err != nil {
			branch = ""
		}
		branch = gitx.TrimJS(branch)
		if branch == "" {
			return nil, nil
		}
		if gitx.BranchUpstream(ctx, repoPath, branch) != "" {
			return nil, nil
		}
		remotes, err := dRemoteNames(ctx, repoPath)
		if err != nil {
			return nil, err
		}
		if len(remotes) == 0 {
			return nil, nil
		}
		remote := remotes[0]
		for _, r := range remotes {
			if r == "origin" {
				remote = "origin"
			}
		}
		// Reads the remote-tracking ref, not the network: a fetch has just run, so
		// this is current, and an offline repo still gets a sensible answer.
		out, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", "--verify", "--quiet", "refs/remotes/" + remote + "/" + branch}, gitx.RunOpts{})
		exists := err == nil && gitx.TrimJS(out) != ""
		return &UpstreamSuggestion{Branch: branch, Remote: remote, RemoteRefExists: exists}, nil
	})
}

// MinLockAgeSeconds: below this age a lock is presumed to belong to a git that
// is still running, and is never removed on the user's behalf. Git's own writes
// finish in milliseconds; a fetch of a large repo holding `packed-refs.lock` is
// the slow end, and stays well inside this.
const dMinLockAgeSeconds = 30

// dLockKind says what a lock file guards, from its path inside the git directory.
func dLockKind(rel string) string {
	switch {
	case rel == "index.lock":
		return "index"
	case rel == "config.lock":
		return "config"
	case rel == "packed-refs.lock" || strings.HasPrefix(rel, "refs/") || strings.HasPrefix(rel, "logs/"):
		return "ref"
	}
	return "other"
}

// dLockDirs resolves the repository's git directory and common directory
// (a linked worktree has both), in that order, skipping failures.
func dLockDirs(ctx context.Context, repoPath string) []string {
	var dirs []string
	for _, arg := range []string{"--absolute-git-dir", "--git-common-dir"} {
		raw, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", arg}, gitx.RunOpts{})
		if err != nil {
			raw = ""
		}
		if raw = gitx.TrimJS(raw); raw != "" {
			dirs = append(dirs, dResolve(repoPath, raw))
		}
	}
	return dirs
}

// StaleLocks lists every `*.lock` file left inside the repository's git
// directory, newest first, with its age.
//
// Git writes a lock next to whatever it is about to change and removes it when
// the write lands. A lock that outlives its process — a crashed editor, a `git
// commit` whose terminal was closed, a killed fetch mid-prune — is invisible
// from the UI and turns every later write into "Unable to create '...lock':
// File exists". The age is the whole story: a lock two seconds old belongs to a
// running git, one twenty minutes old does not.
//
// Submodule git directories (`modules/`), the pack directory and `lfs/` are
// skipped — the first belongs to another repository, the others hold no lock a
// user can act on. Directory entries are visited in name order (Node's readdir
// order was the filesystem's).
//
// gitsvc: staleLocks
func (s *GitService) StaleLocks(ctx context.Context, repoPath string) ([]GitLockFile, error) {
	return locked(s, ctx, "staleLocks", repoPath, func(ctx context.Context) ([]GitLockFile, error) {
		var dirs []string
		seen := map[string]bool{}
		for _, d := range dLockDirs(ctx, repoPath) {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
		now := time.Now()
		var found []GitLockFile
		var walk func(dir, rel string)
		walk = func(dir, rel string) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range entries {
				child := e.Name()
				if rel != "" {
					child = rel + "/" + e.Name()
				}
				if e.IsDir() {
					if child == "modules" || child == "objects/pack" || child == "lfs" {
						continue
					}
					walk(filepath.Join(dir, e.Name()), child)
					continue
				}
				if !strings.HasSuffix(e.Name(), ".lock") {
					continue
				}
				st, err := os.Stat(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				age := math.Floor((float64(now.UnixNano())/1e6-dMtimeMs(st))/1000 + 0.5)
				found = append(found, GitLockFile{Path: child, AgeSeconds: int(math.Max(0, age)), Kind: dLockKind(child)})
			}
		}
		for _, d := range dirs {
			walk(d, "")
		}
		// Same file reachable through both git dirs (a plain repo answers the same
		// path twice) — one entry per path, at its first position, last value.
		index := map[string]int{}
		out := []GitLockFile{}
		for _, f := range found {
			if i, ok := index[f.Path]; ok {
				out[i] = f
				continue
			}
			index[f.Path] = len(out)
			out = append(out, f)
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].AgeSeconds < out[j].AgeSeconds })
		return out, nil
	})
}

// ClearLocks deletes lock files by the relative paths StaleLocks reported and
// returns how many actually went.
//
// Three guards, because this deletes files inside `.git` on a path that starts
// in the renderer: the name must end in `.lock`, it must resolve inside one of
// the repository's git directories (no `..` escape), and it must be older than
// dMinLockAgeSeconds — a young lock is a git process still running, and
// removing it from under a live write is how a repository gets a torn index.
//
// gitsvc: clearLocks
func (s *GitService) ClearLocks(ctx context.Context, repoPath string, paths []string) (int, error) {
	return locked(s, ctx, "clearLocks", repoPath, func(ctx context.Context) (int, error) {
		dirs := dLockDirs(ctx, repoPath)
		removed := 0
		for _, rel := range paths {
			if !strings.HasSuffix(rel, ".lock") || strings.Contains(rel, "\x00") {
				continue
			}
			for _, gitDir := range dirs {
				target := dResolve(gitDir, rel)
				if !strings.HasPrefix(target, gitDir+string(filepath.Separator)) {
					continue
				}
				st, err := os.Stat(target)
				if err != nil || !st.Mode().IsRegular() {
					continue
				}
				if (float64(time.Now().UnixNano())/1e6-dMtimeMs(st))/1000 < dMinLockAgeSeconds {
					continue
				}
				_ = os.Remove(target)
				removed++
				break
			}
		}
		return removed, nil
	})
}

// SetUpstream points branch at `<remote>/<branch>`; a nil remote unsets the
// tracking.
//
// gitsvc: setUpstream
func (s *GitService) SetUpstream(ctx context.Context, repoPath, branch string, remote *string) error {
	return lockedErr(s, ctx, "setUpstream", repoPath, func(ctx context.Context) error {
		if remote == nil {
			_, err := gitx.RunGit(ctx, repoPath, []string{"branch", "--unset-upstream", branch}, gitx.RunOpts{})
			return err
		}
		_, err := gitx.RunGit(ctx, repoPath, []string{"branch", "--set-upstream-to=" + *remote + "/" + branch, branch}, gitx.RunOpts{})
		return err
	})
}

// Push pushes branch to opts.Remote (default origin) and sets it as the
// upstream; Force is `--force-with-lease`, never a bare `--force`.
//
// gitsvc: push
func (s *GitService) Push(ctx context.Context, repoPath, branch string, opts PushOptions) error {
	return lockedErr(s, ctx, "push", repoPath, func(ctx context.Context) error {
		remote := "origin"
		if opts.Remote != nil {
			remote = *opts.Remote
		}
		args := []string{"push"}
		if opts.Force {
			args = append(args, "--force-with-lease")
		}
		args = append(args, "--set-upstream", remote, branch)
		_, err := s.dRunAuthed(ctx, repoPath, remote, args)
		return err
	})
}

var dNonFastForwardRe = regexp.MustCompile(`(?i)non-fast-forward|rejected`)

// PullBranch updates a local branch from its upstream **without checking it
// out**.
//
// `git pull` only ever moves HEAD, which is why a branch you are not standing
// on looks unpullable. A refspec fetch moves the ref directly, and git refuses
// it unless the update is a fast-forward — that refusal is the point: a
// diverged branch needs a merge or rebase the user picked, not a silent one.
//
// gitsvc: pullBranch
func (s *GitService) PullBranch(ctx context.Context, repoPath, branch string) error {
	return lockedErr(s, ctx, "pullBranch", repoPath, func(ctx context.Context) error {
		head, err := gitx.RunGit(ctx, repoPath, []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, gitx.RunOpts{})
		if err != nil {
			head = ""
		}
		head = gitx.TrimJS(head)
		// Standing on it? Then the ordinary pull is both possible and better — it
		// updates the working tree too.
		if head != "" && head == branch {
			return s.dPull(ctx, repoPath, "default")
		}
		upstream := gitx.BranchUpstream(ctx, repoPath, branch)
		if upstream == "" {
			return dErrf("'%s' has no upstream branch to pull from.", branch)
		}
		slash := strings.IndexByte(upstream, '/')
		remote, remoteBranch := upstream[:slash], upstream[slash+1:]
		_, err = s.dRunAuthed(ctx, repoPath, remote, []string{"fetch", remote, remoteBranch + ":" + branch})
		if err != nil && dNonFastForwardRe.MatchString(err.Error()) {
			return dErrf("'%s' has diverged from %s. Check it out and merge or rebase to reconcile them.", branch, upstream)
		}
		return err
	})
}

// SetBranchRef moves a local branch to sha without checking it out — `git
// branch -f`. The undo for a branch-level pull. Git refuses to move a branch
// that is checked out in this worktree or another one, which is the guard we
// want.
//
// gitsvc: setBranchRef
func (s *GitService) SetBranchRef(ctx context.Context, repoPath, branch, sha string) error {
	return lockedErr(s, ctx, "setBranchRef", repoPath, func(ctx context.Context) error {
		_, err := gitx.RunGit(ctx, repoPath, []string{"branch", "-f", branch, sha}, gitx.RunOpts{})
		return err
	})
}

// PushToRemotes pushes one branch to several remotes in turn.
//
// A failure against one remote does not cancel the others: pushing a fork and
// its upstream is the whole point, and "upstream rejected it" is no reason to
// leave the fork behind. Each result is reported separately.
//
// gitsvc: pushToRemotes
func (s *GitService) PushToRemotes(ctx context.Context, repoPath, branch string, remotes []string, opts PushToRemotesOptions) ([]PushRemoteResult, error) {
	return locked(s, ctx, "pushToRemotes", repoPath, func(ctx context.Context) ([]PushRemoteResult, error) {
		var targets []string
		for _, r := range remotes {
			if r = gitx.TrimJS(r); r != "" {
				targets = append(targets, r)
			}
		}
		if len(targets) == 0 {
			return nil, dErr("Choose at least one remote.")
		}
		results := []PushRemoteResult{}
		for _, remote := range targets {
			args := []string{"push"}
			if opts.Force {
				args = append(args, "--force-with-lease")
			}
			// Only the first remote's push sets the upstream — a branch has one, and
			// the last remote pushed to is not automatically the one to track.
			if remote == targets[0] {
				args = append(args, "--set-upstream")
			}
			if opts.Tags {
				args = append(args, "--follow-tags")
			}
			args = append(args, remote, branch)
			if _, err := s.dRunAuthed(ctx, repoPath, remote, args); err != nil {
				results = append(results, PushRemoteResult{Remote: remote, OK: false, Error: err.Error()})
				continue
			}
			results = append(results, PushRemoteResult{Remote: remote, OK: true, Error: ""})
		}
		return results, nil
	})
}

// PushAllTags publishes every local tag to a remote (default origin).
// Annotated and lightweight alike.
//
// gitsvc: pushAllTags
func (s *GitService) PushAllTags(ctx context.Context, repoPath string, remote *string) error {
	return lockedErr(s, ctx, "pushAllTags", repoPath, func(ctx context.Context) error {
		r := "origin"
		if remote != nil {
			r = *remote
		}
		_, err := s.dRunAuthed(ctx, repoPath, r, []string{"push", r, "--tags"})
		return err
	})
}
