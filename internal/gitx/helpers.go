package gitx

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ResolveHooksDir resolves a repo's hooks directory, honouring a custom
// `core.hooksPath` (`~` expands to the home directory; a relative value is
// relative to the work tree). custom reports whether core.hooksPath set it.
func ResolveHooksDir(ctx context.Context, repoPath string) (dir string, custom bool) {
	configured, _ := Raw(ctx, repoPath, []string{"config", "--get", "core.hooksPath"}, RunOpts{})
	if p := TrimJS(configured); p != "" {
		if p == "~" || strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[1:])
		} else if !strings.HasPrefix(p, "/") {
			p = filepath.Join(repoPath, p)
		}
		return p, true
	}
	gitDir, _ := Raw(ctx, repoPath, []string{"rev-parse", "--git-path", "hooks"}, RunOpts{})
	gitDir = TrimJS(gitDir)
	switch {
	case gitDir == "":
		return filepath.Join(repoPath, ".git", "hooks"), false
	case strings.HasPrefix(gitDir, "/"):
		return gitDir, false
	default:
		return filepath.Join(repoPath, gitDir), false
	}
}

// LocalBranches lists local branch names, short form; [] on any failure.
func LocalBranches(ctx context.Context, repoPath string) []string {
	raw, err := Raw(ctx, repoPath, []string{"for-each-ref", "--format=%(refname:short)", "refs/heads"}, RunOpts{})
	if err != nil {
		raw = ""
	}
	return SplitTrimmedLines(raw)
}

// GetRemoteURL is `git remote get-url <remote>`, ” when it fails.
func GetRemoteURL(ctx context.Context, repoPath, remote string) string {
	res, err := Run(ctx, repoPath, []string{"remote", "get-url", remote}, RunOpts{PlainEnv: true})
	if err != nil {
		return ""
	}
	return TrimJS(res.Stdout)
}

// BranchUpstream is the full upstream ref of branch (e.g. `origin/main`), or
// ” when it tracks nothing.
func BranchUpstream(ctx context.Context, repoPath, branch string) string {
	res, err := Run(ctx, repoPath, []string{"rev-parse", "--abbrev-ref", "--symbolic-full-name", branch + "@{u}"},
		RunOpts{PlainEnv: true})
	if err != nil {
		return ""
	}
	ref := TrimJS(res.Stdout)
	if strings.Contains(ref, "/") {
		return ref
	}
	return ""
}

// UpstreamRemote is the remote a plain `git pull` would use (the current
// branch's upstream), or 'origin' when no upstream is configured.
func UpstreamRemote(ctx context.Context, repoPath string) string {
	res, err := Run(ctx, repoPath, []string{"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"},
		RunOpts{PlainEnv: true})
	if err == nil {
		out := TrimJS(res.Stdout)
		if slash := strings.IndexByte(out, '/'); slash > 0 {
			return out[:slash]
		}
	}
	return "origin"
}

// TokenFunc returns the active profile's token for a hosting provider and a
// repository (” when none) — activeProfileToken(host, repoPath) in TS.
type TokenFunc func(ctx context.Context, host, repoPath string) string

// WithRemoteAuth runs a network git operation (push/pull/fetch) against
// remote, injecting the active profile's PAT for the duration of the call. The
// token is matched to the remote's host and written into the remote URL only
// transiently — reset afterwards — so it is never persisted to `.git/config`,
// mirroring clone. When no token applies (ssh remote, unknown host, or none
// configured) op runs as is and relies on GIT_TERMINAL_PROMPT=0 to fail fast.
//
// The restore runs on a context detached from cancellation: a cancelled push
// must still take the token back out of the config.
func WithRemoteAuth(ctx context.Context, repoPath, remote string, token TokenFunc, op func(context.Context) error) error {
	remoteURL := GetRemoteURL(ctx, repoPath, remote)
	host := ""
	if remoteURL != "" {
		host = HostFromURL(remoteURL)
	}
	tok := ""
	if host != "" && token != nil {
		tok = token(ctx, host, repoPath)
	}
	authed := remoteURL
	if tok != "" {
		authed = AuthedCloneURL(remoteURL, host, tok)
	}
	if authed == "" || authed == remoteURL {
		return op(ctx)
	}
	if _, err := Run(ctx, repoPath, []string{"remote", "set-url", remote, authed}, RunOpts{PlainEnv: true}); err != nil {
		return err
	}
	defer func() {
		// Restore the token-free URL so the PAT does not linger on disk.
		_, _ = Run(context.WithoutCancel(ctx), repoPath, []string{"remote", "set-url", remote, remoteURL},
			RunOpts{PlainEnv: true})
	}()
	return op(ctx)
}

var whitespaceRunRe = regexp.MustCompile(`[` + jsSpaceClass + `]+`)

// Divergence counts the commits each side has that the other lacks, computed
// without checking out either ref. Ahead = commits on local not on remote;
// Behind = the reverse. Both > 0 means the branches have diverged and cannot
// be fast-forwarded.
func Divergence(ctx context.Context, repoPath, local, remote string) (Track, error) {
	out, err := Raw(ctx, repoPath, []string{"rev-list", "--left-right", "--count", local + "..." + remote}, RunOpts{})
	if err != nil {
		return Track{}, err
	}
	parts := whitespaceRunRe.Split(TrimJS(out), -1)
	var t Track
	if len(parts) > 0 {
		t.Ahead = jsNumberInt(parts[0])
	}
	if len(parts) > 1 {
		t.Behind = jsNumberInt(parts[1])
	}
	return t, nil
}

// jsNumberInt is `Number(s) || 0` for the integer strings git prints.
func jsNumberInt(s string) int {
	n, err := strconv.Atoi(TrimJS(s))
	if err != nil {
		return 0
	}
	return n
}

// Identity is a commit's author identity and full message, for faithful rewrites.
type Identity struct {
	Name    string
	Email   string
	Date    string // ISO 8601 (%aI)
	Message string
}

var trailingNewlinesRe = regexp.MustCompile(`\n+$`)

// CommitIdentity reads sha's author name, email, date and message.
func CommitIdentity(ctx context.Context, repoPath, sha string) (Identity, error) {
	raw, err := RunGit(ctx, repoPath, []string{"log", "-1", "--format=%an%x00%ae%x00%aI%x00%B", sha}, RunOpts{})
	if err != nil {
		return Identity{}, err
	}
	parts := strings.Split(raw, "\x00")
	get := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	msg := ""
	if len(parts) > 3 {
		msg = strings.Join(parts[3:], "\x00")
	}
	return Identity{Name: get(0), Email: get(1), Date: get(2), Message: trailingNewlinesRe.ReplaceAllString(msg, "")}, nil
}

// AuthorEnv re-commits as the original author (the committer stays the local user).
func AuthorEnv(id Identity) []string {
	return []string{"GIT_AUTHOR_NAME=" + id.Name, "GIT_AUTHOR_EMAIL=" + id.Email, "GIT_AUTHOR_DATE=" + id.Date}
}

// BlobSize is the byte size of a working-tree file (ref == "") or of the blob
// at `ref:file`; ok is false when it is unknowable.
func BlobSize(ctx context.Context, repoPath, file, ref string) (size int64, ok bool) {
	if ref == "" {
		st, err := os.Stat(filepath.Join(repoPath, file))
		if err != nil {
			return 0, false
		}
		return st.Size(), true
	}
	out, err := Raw(ctx, repoPath, []string{"cat-file", "-s", ref + ":" + file}, RunOpts{})
	if err != nil {
		return 0, false
	}
	s := TrimJS(out)
	if s == "" {
		return 0, true // Number('') is 0
	}
	n, perr := strconv.ParseFloat(s, 64)
	if perr != nil {
		return 0, false
	}
	return int64(n), true
}

// AssertUnderCap refuses a whole-file read over cap with the
// FileTooLargePrefix marker the renderer turns into "load anyway".
func AssertUnderCap(ctx context.Context, repoPath, file, ref string, cap int64) error {
	if size, ok := BlobSize(ctx, repoPath, file, ref); ok && size > cap {
		return errors.New(FileTooLargePrefix + strconv.FormatInt(size, 10))
	}
	return nil
}

// ReadFileDataURL reads a file as a base64 data URL (mime by extension), from
// the working tree (ref == "") or at ref. ok is false when the file is missing
// at that ref (e.g. an added/deleted side of a diff) instead of an error.
func ReadFileDataURL(ctx context.Context, repoPath, file, ref string) (dataURL string, ok bool) {
	var buf []byte
	var err error
	if ref == "" {
		buf, err = os.ReadFile(filepath.Join(repoPath, file))
	} else {
		buf, err = ReadBlob(ctx, repoPath, ref+":"+file, RunOpts{PlainEnv: true})
	}
	if err != nil {
		return "", false
	}
	return "data:" + FileMime(file) + ";base64," + base64.StdEncoding.EncodeToString(buf), true
}

// IsDirty reports whether `git status` lists any entry (staged, unstaged or
// untracked; ignored files do not count) — simple-git's `status().files.length > 0`.
func IsDirty(ctx context.Context, repoPath string) (bool, error) {
	out, err := Raw(ctx, repoPath, []string{"status", "--porcelain", "-b", "-u", "--null"}, RunOpts{})
	if err != nil {
		return false, err
	}
	for _, rec := range strings.Split(out, "\x00") {
		if rec != "" && !strings.HasPrefix(rec, "## ") {
			return true, nil
		}
	}
	return false, nil
}

// WithAutoStash runs op with a dirty working tree shelved under a NAMED stash
// (visible in the stash list), then restores it.
//
//   - Clean tree → runs op directly, no stash.
//   - op fails   → the named stash is left untouched so the user's changes
//     stay recoverable; op's error is returned.
//   - Pop conflicts on restore → git keeps the named stash and the conflict
//     surfaces to the user to resolve (their changes aren't lost).
func WithAutoStash(ctx context.Context, repoPath, label string, op func(context.Context) error) error {
	dirty, err := IsDirty(ctx, repoPath)
	if err != nil {
		return err
	}
	if !dirty {
		return op(ctx)
	}
	if _, err := Raw(ctx, repoPath, []string{"stash", "push", "--include-untracked", "-m", "Auto-stash before " + label}, RunOpts{}); err != nil {
		return err
	}
	if err := op(ctx); err != nil {
		return err // the named stash is left for recovery
	}
	_, err = Raw(ctx, repoPath, []string{"stash", "pop"}, RunOpts{}) // a pop conflict errors; git keeps the stash
	return err
}

var subtreeMissingRe = regexp.MustCompile(`(?i)is not a git command|subtree` + jsDot + `*not found`)

// ErrNoSubtree is the message for a git without the `subtree` contrib script.
const ErrNoSubtree = "This git installation has no `git subtree` — it ships as a contrib script and is missing here."

// RunSubtree runs `git subtree <args>`.
//
// `git subtree` is a shell script shipped alongside git, not a builtin, so a
// stripped-down install can be missing it. Say that plainly instead of
// letting "'subtree' is not a git command" reach a toast.
func RunSubtree(ctx context.Context, repoPath string, args []string) (string, error) {
	// `git subtree` checks for local modifications with a plumbing command
	// that trusts the index's stat cache. A repository whose files were touched
	// without being changed — a checkout, a copy, a restored backup — then
	// looks dirty and the command refuses. Porcelain refreshes first; so do we.
	_, _ = RunGit(ctx, repoPath, []string{"update-index", "-q", "--refresh"}, RunOpts{})
	out, err := RunGit(ctx, repoPath, append([]string{"subtree"}, args...), RunOpts{})
	if err != nil && subtreeMissingRe.MatchString(err.Error()) {
		//lint:ignore ST1005 the message is shown to the user verbatim, as in TS
		return "", errors.New(ErrNoSubtree)
	}
	return out, err
}

// CleanCandidates is everything `git clean` could remove, as git itself sees
// it. `--directory` collapses a wholly untracked folder into one entry
// (`dist/`) instead of listing every file inside it — the same grouping
// `git clean -d` removes by, and the only listing short enough to read.
func CleanCandidates(ctx context.Context, repoPath string) (untracked, ignored []string) {
	list := func(extra ...string) []string {
		args := append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--directory", "--no-empty-directory"}, extra...)
		out, err := RunGit(ctx, repoPath, args, RunOpts{})
		if err != nil {
			out = ""
		}
		items := []string{}
		for _, p := range strings.Split(out, "\x00") {
			if p != "" {
				items = append(items, p)
			}
		}
		return items
	}
	return list(), list("--ignored")
}
