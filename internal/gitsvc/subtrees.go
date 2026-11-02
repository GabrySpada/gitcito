package gitsvc

// Subtrees.
//
// `git subtree` vendors another repository into a subdirectory. The files are
// really in the tree — a plain clone gets them, with no submodule dance — but
// git keeps no manifest of where they came from, only a `git-subtree-dir:`
// trailer on the import commit. So this discovers prefixes from history and
// remembers the url/ref alongside, in the repo's own config.

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// SubtreeInfo is SubtreeInfo in src/shared/types.ts.
type SubtreeInfo struct {
	// Prefix is the directory inside this repo, e.g. `vendor/parser`.
	Prefix string `json:"prefix"`
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	// Present is true when the prefix still exists in the working tree.
	Present bool `json:"present"`
	// LastSplit is the upstream commit last imported, from the subtree trailer.
	LastSplit string `json:"lastSplit"`
}

var (
	dSubtreeDirRe   = regexp.MustCompile(`(?m)^[` + dJSSpace + `]*git-subtree-dir:[` + dJSSpace + `]*(` + dJSDot + `+)`)
	dSubtreeSplitRe = regexp.MustCompile(`(?m)^[` + dJSSpace + `]*git-subtree-split:[` + dJSSpace + `]*([0-9a-f]+)`)
	dSubtreeConfRe  = regexp.MustCompile(`^gitcito\.subtree\.(` + dJSDot + `+)\.(url|ref)[` + dJSSpace + `]+(` + dJSDot + `*)$`)
)

// dRememberSubtree records where a subtree came from — git itself keeps no
// such record.
func dRememberSubtree(ctx context.Context, repoPath, prefix, url, ref string) error {
	key := gitx.EncodeSubtreeKey(prefix)
	if _, err := gitx.Raw(ctx, repoPath, []string{"config", "gitcito.subtree." + key + ".url", url}, gitx.RunOpts{}); err != nil {
		return err
	}
	_, err := gitx.Raw(ctx, repoPath, []string{"config", "gitcito.subtree." + key + ".ref", ref}, gitx.RunOpts{})
	return err
}

// Subtrees lists the subtrees found in history plus the ones remembered in
// config, sorted by prefix.
//
// gitsvc: subtrees
func (s *GitService) Subtrees(ctx context.Context, repoPath string) ([]SubtreeInfo, error) {
	return locked(s, ctx, "subtrees", repoPath, func(ctx context.Context) ([]SubtreeInfo, error) {
		// The trailers git subtree writes are the only trace in history.
		body, err := gitx.Raw(ctx, repoPath, []string{"log", "--no-merges", "--grep=git-subtree-dir:", "--pretty=%b%x00"}, gitx.RunOpts{})
		if err != nil {
			body = ""
		}
		merged, err := gitx.Raw(ctx, repoPath, []string{"log", "--merges", "--grep=git-subtree-dir:", "--pretty=%b%x00"}, gitx.RunOpts{})
		if err != nil {
			merged = ""
		}
		var order []string
		found := map[string]string{}
		put := func(prefix, split string) {
			if _, ok := found[prefix]; !ok {
				found[prefix] = split
				order = append(order, prefix)
			}
		}
		for _, record := range strings.Split(body+merged, "\x00") {
			m := dSubtreeDirRe.FindStringSubmatch(record)
			if m == nil {
				continue
			}
			dir := strings.TrimRight(gitx.TrimJS(m[1]), "/")
			if dir == "" {
				continue
			}
			split := ""
			if sm := dSubtreeSplitRe.FindStringSubmatch(record); sm != nil {
				split = sm[1]
			}
			// `log` is newest-first, so the first sighting is the latest import.
			put(dir, split)
		}

		// Anything the user told us about, including prefixes added by another tool.
		remembered, err := gitx.Raw(ctx, repoPath, []string{"config", "--get-regexp", `^gitcito\.subtree\.`}, gitx.RunOpts{})
		if err != nil {
			remembered = ""
		}
		type meta struct{ url, ref string }
		metas := map[string]*meta{}
		for _, line := range dLines(remembered) {
			m := dSubtreeConfRe.FindStringSubmatch(gitx.TrimJS(line))
			if m == nil {
				continue
			}
			prefix := gitx.DecodeSubtreeKey(m[1])
			e, ok := metas[prefix]
			if !ok {
				e = &meta{}
				metas[prefix] = e
			}
			if m[2] == "url" {
				e.url = m[3]
			} else {
				e.ref = m[3]
			}
			put(prefix, "")
		}

		out := make([]SubtreeInfo, 0, len(order))
		for _, prefix := range order {
			info := SubtreeInfo{Prefix: prefix, Present: dExists(filepath.Join(repoPath, prefix)), LastSplit: found[prefix]}
			if e := metas[prefix]; e != nil {
				info.URL, info.Ref = e.url, e.ref
			}
			out = append(out, info)
		}
		sort.SliceStable(out, func(i, j int) bool { return dLocaleCompare(out[i].Prefix, out[j].Prefix) < 0 })
		return out, nil
	})
}

// SubtreeAdd vendors a repository into prefix (squashed unless squash is
// false), and remembers where it came from.
//
// gitsvc: subtreeAdd
func (s *GitService) SubtreeAdd(ctx context.Context, repoPath, prefix, url, ref string, squash *bool) error {
	return lockedErr(s, ctx, "subtreeAdd", repoPath, func(ctx context.Context) error {
		dir := strings.Trim(gitx.TrimJS(prefix), "/")
		u, r := gitx.TrimJS(url), gitx.TrimJS(ref)
		if dir == "" || u == "" || r == "" {
			return dErr("A prefix, a repository and a ref are all required.")
		}
		if dExists(filepath.Join(repoPath, dir)) {
			return dErrf("\"%s\" already exists — subtree add needs a free path.", dir)
		}
		args := []string{"add", "--prefix=" + dir}
		if squash == nil || *squash {
			args = append(args, "--squash")
		}
		if _, err := gitx.RunSubtree(ctx, repoPath, append(args, u, r)); err != nil {
			return err
		}
		return dRememberSubtree(ctx, repoPath, dir, u, r)
	})
}

// SubtreePull pulls upstream changes into an existing subtree.
//
// gitsvc: subtreePull
func (s *GitService) SubtreePull(ctx context.Context, repoPath, prefix, url, ref string, squash *bool) error {
	return lockedErr(s, ctx, "subtreePull", repoPath, func(ctx context.Context) error {
		u, r := gitx.TrimJS(url), gitx.TrimJS(ref)
		if u == "" || r == "" {
			return dErr("This subtree has no remembered repository — enter one.")
		}
		args := []string{"pull", "--prefix=" + prefix}
		if squash == nil || *squash {
			args = append(args, "--squash")
		}
		if _, err := gitx.RunSubtree(ctx, repoPath, append(args, u, r)); err != nil {
			return err
		}
		return dRememberSubtree(ctx, repoPath, prefix, u, r)
	})
}

// SubtreePush sends local changes under prefix back to the source repository.
//
// gitsvc: subtreePush
func (s *GitService) SubtreePush(ctx context.Context, repoPath, prefix, url, ref string) error {
	return lockedErr(s, ctx, "subtreePush", repoPath, func(ctx context.Context) error {
		u, r := gitx.TrimJS(url), gitx.TrimJS(ref)
		if u == "" || r == "" {
			return dErr("This subtree has no remembered repository — enter one.")
		}
		_, err := gitx.RunSubtree(ctx, repoPath, []string{"push", "--prefix=" + prefix, u, r})
		return err
	})
}

// SubtreeSplit extracts the subtree's own history into a local branch and
// returns the split commit.
//
// gitsvc: subtreeSplit
func (s *GitService) SubtreeSplit(ctx context.Context, repoPath, prefix, branch string) (string, error) {
	return locked(s, ctx, "subtreeSplit", repoPath, func(ctx context.Context) (string, error) {
		name := gitx.TrimJS(branch)
		if name == "" {
			return "", dErr("A branch name is required.")
		}
		out, err := gitx.RunSubtree(ctx, repoPath, []string{"split", "--prefix=" + prefix, "-b", name})
		if err != nil {
			return "", err
		}
		return gitx.TrimJS(out), nil
	})
}

// SubtreeForget forgets the remembered url/ref. Touches no files and no history.
//
// gitsvc: subtreeForget
func (s *GitService) SubtreeForget(ctx context.Context, repoPath, prefix string) error {
	return lockedErr(s, ctx, "subtreeForget", repoPath, func(ctx context.Context) error {
		key := gitx.EncodeSubtreeKey(prefix)
		for _, field := range []string{"url", "ref"} {
			_, _ = gitx.Raw(ctx, repoPath, []string{"config", "--unset", "gitcito.subtree." + key + "." + field}, gitx.RunOpts{})
		}
		return nil
	})
}
