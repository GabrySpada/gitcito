package gitsvc

// Worktrees, submodules and the repository's user identity.

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// WorktreeInfo is WorktreeInfo in src/shared/types.ts.
type WorktreeInfo struct {
	Path string `json:"path"`
	// Branch is null for a detached worktree.
	Branch    *string `json:"branch"`
	Head      string  `json:"head"`
	IsMain    bool    `json:"isMain"`
	IsCurrent bool    `json:"isCurrent"`
	Locked    bool    `json:"locked"`
	Detached  bool    `json:"detached"`
}

// SubmoduleInfo is SubmoduleInfo in src/shared/types.ts.
type SubmoduleInfo struct {
	// Name is the logical name from `.gitmodules`.
	Name string `json:"name"`
	Path string `json:"path"`
	URL  string `json:"url"`
	// Sha is the checked-out commit (or the recorded one when uninitialized).
	Sha string `json:"sha"`
	// RecordedSha is the gitlink the superproject's HEAD pins.
	RecordedSha string  `json:"recordedSha"`
	Branch      *string `json:"branch"`
	// Describe is git's human-readable ref, e.g. `heads/main`.
	Describe *string `json:"describe"`
	// Status: 'initialized' | 'modified' | 'uninitialized' | 'conflict'.
	Status string `json:"status"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
}

// GitUser is getUser's `{ name, email }`.
type GitUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Worktrees lists the repository's worktrees, the main one first.
//
// gitsvc: worktrees
func (s *GitService) Worktrees(ctx context.Context, repoPath string) ([]WorktreeInfo, error) {
	return locked(s, ctx, "worktrees", repoPath, func(ctx context.Context) ([]WorktreeInfo, error) {
		out, err := gitx.Raw(ctx, repoPath, []string{"worktree", "list", "--porcelain"}, gitx.RunOpts{})
		if err != nil {
			out = ""
		}
		result := []WorktreeInfo{}
		var cur *WorktreeInfo
		flush := func() {
			if cur != nil && cur.Path != "" {
				result = append(result, *cur)
			}
			cur = nil
		}
		for _, line := range dLines(out) {
			switch {
			case strings.HasPrefix(line, "worktree "):
				flush()
				cur = &WorktreeInfo{Path: gitx.TrimJS(line[len("worktree "):])}
			case cur == nil:
				continue
			case strings.HasPrefix(line, "HEAD "):
				cur.Head = gitx.TrimJS(line[len("HEAD "):])
			case strings.HasPrefix(line, "branch "):
				b := strings.Replace(gitx.TrimJS(line[len("branch "):]), "refs/heads/", "", 1)
				cur.Branch = &b
			case line == "detached":
				cur.Detached = true
			case line == "locked" || strings.HasPrefix(line, "locked "):
				cur.Locked = true
			}
		}
		flush()
		normalizedRepo := strings.TrimRight(repoPath, "/")
		if len(result) > 0 {
			result[0].IsMain = true
		}
		for i := range result {
			if strings.TrimRight(result[i].Path, "/") == normalizedRepo {
				result[i].IsCurrent = true
			}
		}
		return result, nil
	})
}

// WorktreeAdd adds a worktree at path: on a new branch (from startPoint, when
// given) or on an existing one.
//
// gitsvc: worktreeAdd
func (s *GitService) WorktreeAdd(ctx context.Context, repoPath, path, branch string, newBranch bool, startPoint string) error {
	return lockedErr(s, ctx, "worktreeAdd", repoPath, func(ctx context.Context) error {
		args := []string{"worktree", "add"}
		if newBranch {
			args = append(args, "-b", branch, path)
			if startPoint != "" {
				args = append(args, startPoint)
			}
		} else {
			args = append(args, path, branch)
		}
		_, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
		return err
	})
}

// WorktreeRemove removes a linked worktree; force discards its local changes.
//
// gitsvc: worktreeRemove
func (s *GitService) WorktreeRemove(ctx context.Context, repoPath, path string, force bool) error {
	return lockedErr(s, ctx, "worktreeRemove", repoPath, func(ctx context.Context) error {
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		_, err := gitx.Raw(ctx, repoPath, append(args, path), gitx.RunOpts{})
		return err
	})
}

var (
	dSubmoduleKeyRe    = regexp.MustCompile(`^submodule\.(` + dJSDot + `+)\.(path|url|branch)$`)
	dSubmoduleStatusRe = regexp.MustCompile(`^([ +\-U])([0-9a-f]{7,40})[` + dJSSpace + `]+(` + dJSDot + `+?)(?:[` + dJSSpace + `]+\((` + dJSDot + `+)\))?$`)
	dGitlinkRe         = regexp.MustCompile(`^160000 commit ([0-9a-f]{40})\t(` + dJSDot + `+)$`)
)

// Submodules lists the registered submodules with their live state, sorted
// by path.
//
// gitsvc: submodules
func (s *GitService) Submodules(ctx context.Context, repoPath string) ([]SubmoduleInfo, error) {
	return locked(s, ctx, "submodules", repoPath, func(ctx context.Context) ([]SubmoduleInfo, error) {
		// `.gitmodules` is the source of truth for registered submodules; without
		// it there is nothing to show (and `git submodule status` would print nothing).
		config, err := gitx.Raw(ctx, repoPath, []string{"config", "--file", ".gitmodules", "--list"}, gitx.RunOpts{})
		if err != nil || gitx.TrimJS(config) == "" {
			return []SubmoduleInfo{}, nil
		}

		// name → { path, url, branch }, built from `submodule.<name>.<key>=<value>`.
		type meta struct {
			path, url, branch          string
			hasPath, hasURL, hasBranch bool
		}
		var names []string
		metas := map[string]*meta{}
		for _, line := range dLines(config) {
			eq := strings.IndexByte(line, '=')
			if line == "" || eq < 0 {
				continue
			}
			m := dSubmoduleKeyRe.FindStringSubmatch(line[:eq])
			if m == nil {
				continue
			}
			e, ok := metas[m[1]]
			if !ok {
				e = &meta{}
				metas[m[1]] = e
				names = append(names, m[1])
			}
			value := line[eq+1:]
			switch m[2] {
			case "path":
				e.path, e.hasPath = value, true
			case "url":
				e.url, e.hasURL = value, true
			default:
				e.branch, e.hasBranch = value, true
			}
		}

		// `git submodule status` reports the live state. The leading char encodes
		// status, followed by the SHA, the path, and an optional `(describe)`.
		type live struct {
			sha, status, describe string
			hasDescribe           bool
		}
		statusOut, err := gitx.Raw(ctx, repoPath, []string{"submodule", "status"}, gitx.RunOpts{})
		if err != nil {
			statusOut = ""
		}
		statusByPath := map[string]live{}
		for _, line := range dLines(statusOut) {
			m := dSubmoduleStatusRe.FindStringSubmatch(line)
			if line == "" || m == nil {
				continue
			}
			st := "initialized"
			switch m[1] {
			case "+":
				st = "modified"
			case "-":
				st = "uninitialized"
			case "U":
				st = "conflict"
			}
			statusByPath[m[3]] = live{sha: m[2], status: st, describe: m[4], hasDescribe: m[4] != ""}
		}

		// The commit each submodule is *pinned* to lives as a gitlink (mode
		// 160000) in the superproject's HEAD tree. Used to measure drift.
		tree, err := gitx.Raw(ctx, repoPath, []string{"ls-tree", "-r", "HEAD"}, gitx.RunOpts{})
		if err != nil {
			tree = ""
		}
		recordedByPath := map[string]string{}
		for _, line := range dLines(tree) {
			if m := dGitlinkRe.FindStringSubmatch(line); m != nil {
				recordedByPath[m[2]] = m[1]
			}
		}

		result := []SubmoduleInfo{}
		for _, name := range names {
			e := metas[name]
			if e.path == "" {
				continue
			}
			st, hasStatus := statusByPath[e.path]
			recorded := recordedByPath[e.path]
			ahead, behind := 0, 0
			// For a checked-out submodule sitting off its recorded commit, count the
			// divergence so the UI can render an out-of-sync "↑n / ↓n" indicator.
			if hasStatus && st.status == "modified" && recorded != "" && st.sha != "" && recorded != st.sha {
				counts, err := gitx.Raw(ctx, filepath.Join(repoPath, e.path),
					[]string{"rev-list", "--left-right", "--count", recorded + "..." + st.sha}, gitx.RunOpts{})
				if err != nil {
					counts = ""
				}
				if parts := dSplitWS(gitx.TrimJS(counts)); len(parts) == 2 {
					behind, _ = strconv.Atoi(parts[0])
					ahead, _ = strconv.Atoi(parts[1])
				}
			}
			info := SubmoduleInfo{Name: name, Path: e.path, URL: e.url, RecordedSha: recorded, Status: "uninitialized", Ahead: ahead, Behind: behind}
			if e.hasBranch {
				b := e.branch
				info.Branch = &b
			}
			if hasStatus {
				info.Sha, info.Status = st.sha, st.status
				if st.hasDescribe {
					d := st.describe
					info.Describe = &d
				}
			}
			result = append(result, info)
		}
		sort.SliceStable(result, func(i, j int) bool { return dLocaleCompare(result[i].Path, result[j].Path) < 0 })
		return result, nil
	})
}

// SubmoduleAdd adds a submodule at path, optionally tracking branch.
//
// gitsvc: submoduleAdd
func (s *GitService) SubmoduleAdd(ctx context.Context, repoPath, url, path, branch string) error {
	return lockedErr(s, ctx, "submoduleAdd", repoPath, func(ctx context.Context) error {
		args := []string{"submodule", "add"}
		if branch != "" {
			args = append(args, "-b", branch)
		}
		_, err := gitx.Raw(ctx, repoPath, append(args, "--", url, path), gitx.RunOpts{})
		return err
	})
}

// SubmoduleSetUrl updates a submodule's remote URL in `.gitmodules` and syncs
// the live config.
//
// gitsvc: submoduleSetUrl
func (s *GitService) SubmoduleSetUrl(ctx context.Context, repoPath, name, url string) error {
	return lockedErr(s, ctx, "submoduleSetUrl", repoPath, func(ctx context.Context) error {
		if _, err := gitx.Raw(ctx, repoPath, []string{"config", "--file", ".gitmodules", "submodule." + name + ".url", url}, gitx.RunOpts{}); err != nil {
			return err
		}
		_, _ = gitx.Raw(ctx, repoPath, []string{"submodule", "sync", "--", name}, gitx.RunOpts{})
		return nil
	})
}

// SubmoduleUpdate checks submodules out at their recorded commits, recursively
// (all of them, or the one at path). init (default true) initializes them first.
//
// gitsvc: submoduleUpdate
func (s *GitService) SubmoduleUpdate(ctx context.Context, repoPath, path string, init *bool) error {
	return lockedErr(s, ctx, "submoduleUpdate", repoPath, func(ctx context.Context) error {
		args := []string{"submodule", "update"}
		if init == nil || *init {
			args = append(args, "--init")
		}
		args = append(args, "--recursive")
		if path != "" {
			args = append(args, "--", path)
		}
		_, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
		return err
	})
}

// SubmoduleSync copies `.gitmodules` URLs into the live config, recursively.
//
// gitsvc: submoduleSync
func (s *GitService) SubmoduleSync(ctx context.Context, repoPath, path string) error {
	return lockedErr(s, ctx, "submoduleSync", repoPath, func(ctx context.Context) error {
		args := []string{"submodule", "sync", "--recursive"}
		if path != "" {
			args = append(args, "--", path)
		}
		_, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
		return err
	})
}

// SubmoduleDeinit unregisters a submodule and empties its working tree; force
// discards local changes in it.
//
// gitsvc: submoduleDeinit
func (s *GitService) SubmoduleDeinit(ctx context.Context, repoPath, path string, force bool) error {
	return lockedErr(s, ctx, "submoduleDeinit", repoPath, func(ctx context.Context) error {
		args := []string{"submodule", "deinit"}
		if force {
			args = append(args, "--force")
		}
		_, err := gitx.Raw(ctx, repoPath, append(args, "--", path), gitx.RunOpts{})
		return err
	})
}

// SubmoduleRemove fully removes a submodule: deinit, drop the working tree
// from the index, and strip its config section so it no longer shows up.
//
// gitsvc: submoduleRemove
func (s *GitService) SubmoduleRemove(ctx context.Context, repoPath, path string) error {
	return lockedErr(s, ctx, "submoduleRemove", repoPath, func(ctx context.Context) error {
		_, _ = gitx.Raw(ctx, repoPath, []string{"submodule", "deinit", "--force", "--", path}, gitx.RunOpts{})
		_, _ = gitx.Raw(ctx, repoPath, []string{"rm", "--force", "--", path}, gitx.RunOpts{})
		// `git rm` of a submodule may leave the stale .git metadata behind.
		_, _ = gitx.Raw(ctx, repoPath, []string{"config", "--remove-section", "submodule." + path}, gitx.RunOpts{})
		return nil
	})
}

// GetUser reads the identity git would commit as here (`user.name`,
// `user.email`, any scope).
//
// gitsvc: getUser
func (s *GitService) GetUser(ctx context.Context, repoPath string) (GitUser, error) {
	return locked(s, ctx, "getUser", repoPath, func(ctx context.Context) (GitUser, error) {
		get := func(key string) string {
			out, err := gitx.Raw(ctx, repoPath, []string{"config", "--get", key}, gitx.RunOpts{})
			if err != nil {
				return ""
			}
			return gitx.TrimJS(out)
		}
		return GitUser{Name: get("user.name"), Email: get("user.email")}, nil
	})
}

// SetUser sets the repository's own identity (local config).
//
// gitsvc: setUser
func (s *GitService) SetUser(ctx context.Context, repoPath, name, email string) error {
	return lockedErr(s, ctx, "setUser", repoPath, func(ctx context.Context) error {
		if _, err := gitx.Raw(ctx, repoPath, []string{"config", "--local", "user.name", name}, gitx.RunOpts{}); err != nil {
			return err
		}
		_, err := gitx.Raw(ctx, repoPath, []string{"config", "--local", "user.email", email}, gitx.RunOpts{})
		return err
	})
}
