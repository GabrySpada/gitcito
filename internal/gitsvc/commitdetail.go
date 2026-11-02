package gitsvc

// Commit details: which branches and tags hold a commit, and the batched
// probe behind the commit context menu.
//
// Renderer channels served (through the 'git' dispatcher): commitBranches,
// commitTags, commitMenuProbe.

import (
	"context"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// CommitBranchInfo is CommitBranchInfo in src/shared/types.ts.
type CommitBranchInfo struct {
	Name    string   `json:"name"`
	IsLocal bool     `json:"isLocal"`
	Remotes []string `json:"remotes"`
}

// gitsvc: commitBranches
//
// Branches whose history contains this commit, grouped like the graph's ref
// badges: a local branch and its remote-tracking counterpart collapse into one
// entry so `main` + `origin/main` render as a single badge.
func (s *GitService) CommitBranches(ctx context.Context, repoPath, hash string) ([]CommitBranchInfo, error) {
	return locked(s, ctx, "commitBranches", repoPath, func(ctx context.Context) ([]CommitBranchInfo, error) {
		// simple-git's getRemotes(): `git remote`, one trimmed name per line.
		remotesOut, err := bRaw(ctx, repoPath, "remote")
		if err != nil {
			return nil, err
		}
		remoteNames := map[string]bool{}
		for _, name := range gitx.SplitTrimmedLines(remotesOut) {
			remoteNames[name] = true
		}
		// Full refname (not :short) so a remote's symbolic HEAD —
		// refs/remotes/origin/HEAD, which :short collapses to the bare string
		// "origin" — can be told apart from an actual local branch literally
		// named "origin".
		out := bRawOr(ctx, repoPath, "", "branch", "-a", "--contains", hash, "--format=%(refname)")
		var order []*CommitBranchInfo
		byName := map[string]*CommitBranchInfo{}
		entry := func(name string) *CommitBranchInfo {
			g, ok := byName[name]
			if !ok {
				g = &CommitBranchInfo{Name: name, Remotes: []string{}}
				byName[name] = g
				order = append(order, g)
			}
			return g
		}
		for _, line := range gitx.SplitTrimmedLines(out) {
			switch {
			case strings.HasPrefix(line, "refs/heads/"):
				entry(strings.TrimPrefix(line, "refs/heads/")).IsLocal = true
			case strings.HasPrefix(line, "refs/remotes/"):
				rest := strings.TrimPrefix(line, "refs/remotes/")
				slash := strings.IndexByte(rest, '/')
				if slash <= 0 {
					continue
				}
				remote, name := rest[:slash], rest[slash+1:]
				// origin's symbolic default-branch pointer, not a real branch.
				if name == "HEAD" || !remoteNames[remote] {
					continue
				}
				g := entry(name)
				if !bContains(g.Remotes, remote) {
					g.Remotes = append(g.Remotes, remote)
				}
			}
		}
		result := make([]CommitBranchInfo, 0, len(order))
		for _, g := range order {
			result = append(result, *g)
		}
		bStableSortBy(result, func(a, b CommitBranchInfo) bool {
			if a.IsLocal != b.IsLocal {
				return a.IsLocal
			}
			return bLocaleCompare(a.Name, b.Name) < 0
		})
		return result, nil
	})
}

func bContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// gitsvc: commitTags
//
// Tags that point exactly at this commit.
func (s *GitService) CommitTags(ctx context.Context, repoPath, hash string) ([]string, error) {
	return locked(s, ctx, "commitTags", repoPath, func(ctx context.Context) ([]string, error) {
		return gitx.SplitTrimmedLines(bRawOr(ctx, repoPath, "", "tag", "--points-at", hash)), nil
	})
}

// CommitMenuProbe is CommitMenuProbe in src/shared/types.ts.
type CommitMenuProbe struct {
	IsHead          bool `json:"isHead"`
	IsOnLocalBranch bool `json:"isOnLocalBranch"`
	// IsPublished: reachable from at least one remote-tracking branch — not tags.
	IsPublished      bool `json:"isPublished"`
	IsAncestorOfHead bool `json:"isAncestorOfHead"`
	// IsWithinResetBoundary: a local (unpushed) ancestor, or the first
	// published/base ancestor of HEAD.
	IsWithinResetBoundary bool    `json:"isWithinResetBoundary"`
	IsRoot                bool    `json:"isRoot"`
	ParentSha             *string `json:"parentSha"`
	OperationInProgress   bool    `json:"operationInProgress"`
	Message               string  `json:"message"`
	HeadSha               string  `json:"headSha"`
	Branch                string  `json:"branch"`
}

// gitsvc: commitMenuProbe
//
// One round-trip for the commit context menu: ancestry, remote reachability,
// reset boundary, in-progress ops, and the full message for amend/undo
// prefill. (TS ran the seven independent reads with Promise.all; they run in
// sequence here — the answer is the same and the menu waits on all of them
// either way.)
func (s *GitService) CommitMenuProbe(ctx context.Context, repoPath, sha string) (CommitMenuProbe, error) {
	return locked(s, ctx, "commitMenuProbe", repoPath, func(ctx context.Context) (CommitMenuProbe, error) {
		trimmedOr := func(args ...string) string { return gitx.TrimJS(bGitOr(ctx, repoPath, "", args...)) }
		headSha := trimmedOr("rev-parse", "HEAD")
		symbolic := trimmedOr("symbolic-ref", "-q", "HEAD")
		mergeKind, err := bMergeState(ctx, repoPath)
		if err != nil {
			return CommitMenuProbe{}, err // Promise.all rejected on mergeState alone
		}
		bisect := bBisectInProgress(ctx, repoPath)
		message := bRawOr(ctx, repoPath, "", "log", "-1", "--format=%B", sha)
		parentLine := trimmedOr("rev-list", "-n", "1", "--parents", sha)
		remoteContains := trimmedOr("branch", "-r", "--contains", sha)

		parentParts := bSplitWS(parentLine)
		var parentSha *string
		if len(parentParts) > 1 {
			p := parentParts[1]
			parentSha = &p
		}
		isRoot := len(parentParts) <= 1
		fullSha := ""
		if len(parentParts) > 0 {
			fullSha = parentParts[0]
		}
		if fullSha == "" {
			out, err := bGit(ctx, repoPath, "rev-parse", sha)
			if err != nil {
				fullSha = sha
			} else {
				fullSha = gitx.TrimJS(out)
			}
		}
		isHead := headSha != "" && fullSha == headSha
		isOnLocalBranch := strings.HasPrefix(symbolic, "refs/heads/")
		isPublished := remoteContains != ""
		isAncestorOfHead := isHead
		if !isAncestorOfHead && headSha != "" {
			_, err := bGit(ctx, repoPath, "merge-base", "--is-ancestor", sha, "HEAD")
			isAncestorOfHead = err == nil
		}
		localSet := map[string]bool{}
		for _, l := range gitx.SplitTrimmedLines(bGitOr(ctx, repoPath, "", "rev-list", "HEAD", "--not", "--remotes")) {
			localSet[l] = true
		}
		firstPublished := ""
		if headSha != "" && (len(localSet) == 0 || !localSet[headSha]) {
			// HEAD itself is published (or there are no remotes producing
			// "local" commits other than walking off the tip). The first
			// published ancestor is HEAD.
			if !localSet[headSha] {
				firstPublished = headSha
			}
		}
		if firstPublished == "" && headSha != "" && len(localSet) > 0 {
			cur := headSha
			seen := map[string]bool{}
			for cur != "" && !seen[cur] {
				seen[cur] = true
				if !localSet[cur] {
					firstPublished = cur
					break
				}
				line := bGitOr(ctx, repoPath, "", "rev-list", "-n", "1", "--parents", cur)
				next := ""
				if parts := strings.FieldsFunc(gitx.TrimJS(line), gitx.IsJSSpace); len(parts) > 1 {
					next = parts[1]
				}
				if next == "" {
					break
				}
				cur = next
			}
		}
		isWithinResetBoundary := isAncestorOfHead && !isHead &&
			(!isPublished || firstPublished != "" && (fullSha == firstPublished || sha == firstPublished))
		branch := ""
		if isOnLocalBranch {
			branch = strings.TrimPrefix(symbolic, "refs/heads/")
		}
		return CommitMenuProbe{
			IsHead:                isHead,
			IsOnLocalBranch:       isOnLocalBranch,
			IsPublished:           isPublished,
			IsAncestorOfHead:      isAncestorOfHead,
			IsWithinResetBoundary: isWithinResetBoundary,
			IsRoot:                isRoot,
			ParentSha:             parentSha,
			OperationInProgress:   mergeKind != "" || bisect,
			Message:               message,
			HeadSha:               headSha,
			Branch:                branch,
		}, nil
	})
}
