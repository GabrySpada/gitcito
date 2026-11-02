package gitsvc

// Removing a path from history. The last resort after the secret guard has
// already failed: a token, or a 400 MB file, that is committed and cannot be
// un-committed by reverting. Every commit from the first touch onwards is
// rewritten, so this is measured first, backed up second, and only then run.
//
// Renderer channels served (through the 'git' dispatcher): historyPaths,
// historyPurgePreview, historyPurge, historyPurgeBackups,
// historyPurgeRestore, historyPurgeDropBackup.

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

const bPurgeRoot = "refs/gitcito/pre-purge/"

// HistoryPathEntry is HistoryPathEntry in src/shared/types.ts.
type HistoryPathEntry struct {
	Path string `json:"path"`
	// Bytes held by every version of it, across all history.
	Bytes int64 `json:"bytes"`
	// Versions is how many distinct blobs — i.e. how often it changed.
	Versions int `json:"versions"`
	// Deleted is true when the path is no longer in the working tree's index.
	Deleted bool `json:"deleted"`
}

// gitsvc: historyPaths
//
// Every path that has ever been committed, heaviest first. A file dialog
// cannot answer this question: the interesting paths are the ones already
// deleted from the working tree, which is exactly what a picker over the
// filesystem cannot see. Sorting by bytes also makes the other reason for a
// purge — "why is this clone two gigabytes" — self-answering.
//
// max nil (undefined) is 400.
func (s *GitService) HistoryPaths(ctx context.Context, repoPath string, max *int) ([]HistoryPathEntry, error) {
	return locked(s, ctx, "historyPaths", repoPath, func(ctx context.Context) ([]HistoryPathEntry, error) {
		// One pass for sizes of every object in the database…
		sizes := map[string]int64{}
		sized := bGitOr(ctx, repoPath, "", "cat-file", "--batch-all-objects",
			"--batch-check=%(objectname) %(objecttype) %(objectsize)")
		for _, line := range strings.Split(sized, "\n") {
			f := strings.Split(line, " ")
			if len(f) > 1 && f[1] == "blob" {
				size := ""
				if len(f) > 2 {
					size = f[2]
				}
				sizes[f[0]] = bJSInt(size)
			}
		}

		// …and one for which of them were ever recorded under which name.
		type acc struct {
			bytes int64
			blobs map[string]bool
		}
		byPath := map[string]*acc{}
		var order []string
		named := bGitOr(ctx, repoPath, "", "rev-list", "--objects", "--all")
		for _, line := range strings.Split(named, "\n") {
			space := strings.IndexByte(line, ' ')
			if space <= 0 {
				continue
			}
			sha := line[:space]
			size, ok := sizes[sha]
			if !ok {
				continue // a tree, not a blob
			}
			path := gitx.TrimJS(line[space+1:])
			if path == "" {
				continue
			}
			e := byPath[path]
			if e == nil {
				e = &acc{blobs: map[string]bool{}}
				byPath[path] = e
				order = append(order, path)
			}
			// The same blob under the same path is one copy, not several.
			if !e.blobs[sha] {
				e.blobs[sha] = true
				e.bytes += size
			}
		}

		tracked := map[string]bool{}
		for _, p := range gitx.SplitTrimmedLines(bGitOr(ctx, repoPath, "", "ls-files")) {
			tracked[p] = true
		}
		out := make([]HistoryPathEntry, 0, len(order))
		for _, p := range order {
			e := byPath[p]
			out = append(out, HistoryPathEntry{Path: p, Bytes: e.bytes, Versions: len(e.blobs), Deleted: !tracked[p]})
		}
		bStableSortBy(out, func(a, b HistoryPathEntry) bool { return a.Bytes > b.Bytes })
		return out[:bSliceEnd(len(out), bIntOr(max, 400))], nil
	})
}

// HistoryPurgeCommit is HistoryPurgePreview.firstCommit.
type HistoryPurgeCommit struct {
	Sha     string `json:"sha"`
	Subject string `json:"subject"`
	Date    int64  `json:"date"`
}

// HistoryPurgePreview is HistoryPurgePreview in src/shared/types.ts: what
// removing a path from history would cost, measured before agreeing to it.
type HistoryPurgePreview struct {
	Paths   []string `json:"paths"`
	Commits int      `json:"commits"`
	// FirstCommit is the oldest touching commit — everything after it gets a new hash.
	FirstCommit *HistoryPurgeCommit `json:"firstCommit"`
	Branches    []string            `json:"branches"`
	Tags        []string            `json:"tags"`
	Bytes       int64               `json:"bytes"`
	// Blocked says why the rewrite cannot start right now — empty when it can.
	Blocked string `json:"blocked"`
}

func bCleanPaths(paths []string) []string {
	clean := []string{}
	for _, p := range paths {
		if p = gitx.TrimJS(p); p != "" {
			clean = append(clean, p)
		}
	}
	return clean
}

// bBlobBytesForPaths is the total size of every blob ever stored at paths.
// It walks the objects of every branch and tag once and keeps the ones whose
// recorded name matches — `rev-list --objects` prints `<sha> <path>` for
// anything that has a name.
func bBlobBytesForPaths(ctx context.Context, repoPath string, paths []string) int64 {
	wanted := map[string]bool{}
	for _, p := range paths {
		wanted[p] = true
	}
	args := append([]string{"rev-list", "--objects", "--branches", "--tags", "--"}, paths...)
	listed := bGitOr(ctx, repoPath, "", args...)
	var shas []string
	seen := map[string]bool{}
	for _, line := range strings.Split(listed, "\n") {
		space := strings.IndexByte(line, ' ')
		if space <= 0 {
			continue
		}
		if wanted[gitx.TrimJS(line[space+1:])] && !seen[line[:space]] {
			seen[line[:space]] = true
			shas = append(shas, line[:space])
		}
	}
	// `cat-file --batch-check` would need a stdin pipe; a handful of blobs is
	// cheaper to size one at a time than to wire that up.
	var total int64
	for _, sha := range shas {
		total += bJSInt(bGitOr(ctx, repoPath, "", "cat-file", "-s", sha))
	}
	return total
}

// bPurgeRefs lists `refs/heads` and `refs/tags` names (`%(refname)`).
func bPurgeRefs(ctx context.Context, repoPath string) []string {
	return gitx.SplitTrimmedLines(bRawOr(ctx, repoPath, "", "for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags"))
}

// bHistoryPurgePreview is historyPurgePreview without the lock (historyPurge
// measures before it rewrites).
func bHistoryPurgePreview(ctx context.Context, repoPath string, paths []string) (HistoryPurgePreview, error) {
	clean := bCleanPaths(paths)
	if len(clean) == 0 {
		return HistoryPurgePreview{Paths: clean, Branches: []string{}, Tags: []string{}, Blocked: "Choose at least one path."}, nil
	}

	// A rewrite rewrites the checkout too, so anything uncommitted would be at
	// risk — and a half-finished merge has no single history to rewrite.
	dirty, err := gitx.IsDirty(ctx, repoPath)
	if err != nil {
		return HistoryPurgePreview{}, bDecodeErr(err)
	}
	state, err := bMergeState(ctx, repoPath)
	if err != nil {
		return HistoryPurgePreview{}, err
	}
	blocked := ""
	switch {
	case state != "":
		blocked = "Finish or abort the operation in progress first."
	case dirty:
		blocked = "Commit or stash your changes first."
	}

	// `--branches --tags` mirrors exactly what the rewrite will touch. `--all`
	// would also walk `refs/gitcito/pre-purge/*`, so a second purge in the same
	// repository would count commits that only a previous backup still holds
	// and promise work the rewrite is not going to do.
	args := append([]string{"log", "--branches", "--tags", "--format=%H%x00%s%x00%at", "--"}, clean...)
	var entries [][]string
	for _, line := range strings.Split(bRawOr(ctx, repoPath, "", args...), "\n") {
		if parts := strings.Split(line, "\x00"); len(parts) == 3 {
			entries = append(entries, parts)
		}
	}
	var first *HistoryPurgeCommit
	if n := len(entries); n > 0 {
		oldest := entries[n-1]
		first = &HistoryPurgeCommit{Sha: oldest[0], Subject: oldest[1], Date: bJSInt(oldest[2])}
	}

	branches, tags := []string{}, []string{}
	for _, r := range bPurgeRefs(ctx, repoPath) {
		switch {
		case strings.HasPrefix(r, "refs/heads/"):
			branches = append(branches, strings.TrimPrefix(r, "refs/heads/"))
		case strings.HasPrefix(r, "refs/tags/"):
			tags = append(tags, strings.TrimPrefix(r, "refs/tags/"))
		}
	}
	return HistoryPurgePreview{
		Paths:       clean,
		Commits:     len(entries),
		FirstCommit: first,
		Branches:    branches,
		Tags:        tags,
		Bytes:       bBlobBytesForPaths(ctx, repoPath, clean),
		Blocked:     blocked,
	}, nil
}

// gitsvc: historyPurgePreview
func (s *GitService) HistoryPurgePreview(ctx context.Context, repoPath string, paths []string) (HistoryPurgePreview, error) {
	return locked(s, ctx, "historyPurgePreview", repoPath, func(ctx context.Context) (HistoryPurgePreview, error) {
		return bHistoryPurgePreview(ctx, repoPath, paths)
	})
}

// HistoryPurgeBackup is HistoryPurgeBackup in src/shared/types.ts: a safety
// copy of every ref as it stood before a purge.
type HistoryPurgeBackup struct {
	// Prefix is `refs/gitcito/pre-purge/<timestamp>`.
	Prefix string `json:"prefix"`
	// At is epoch seconds, the timestamp in the prefix.
	At int64 `json:"at"`
	// Refs is how many refs it holds.
	Refs int `json:"refs"`
	// Paths are the paths that were purged, recorded alongside for the UI.
	Paths []string `json:"paths"`
}

// HistoryPurgeResult is HistoryPurgeResult in src/shared/types.ts.
type HistoryPurgeResult struct {
	Backup HistoryPurgeBackup `json:"backup"`
	// Rewritten is the number of refs the rewrite changed.
	Rewritten int `json:"rewritten"`
}

// bRefShaPairs parses `%(refname) %(objectname)` lines.
func bRefShaPairs(out string) [][2]string {
	var pairs [][2]string
	for _, line := range strings.Split(out, "\n") {
		if parts := strings.Split(gitx.TrimJS(line), " "); len(parts) == 2 {
			pairs = append(pairs, [2]string{parts[0], parts[1]})
		}
	}
	return pairs
}

// bShellQuote single-quotes a path for the filter-branch index filter, which
// git runs through a shell.
func bShellQuote(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// gitsvc: historyPurge
//
// Rewrite every branch and tag without the given paths, after copying each
// ref to `refs/gitcito/pre-purge/<timestamp>/…`.
//
// The backup is deliberately outside `--all`'s reach, so the rewrite cannot
// eat its own safety net — and it keeps the old objects alive, which is why
// space is only reclaimed once the backup is dropped.
func (s *GitService) HistoryPurge(ctx context.Context, repoPath string, paths []string) (HistoryPurgeResult, error) {
	return locked(s, ctx, "historyPurge", repoPath, func(ctx context.Context) (HistoryPurgeResult, error) {
		clean := bCleanPaths(paths)
		if len(clean) == 0 {
			return HistoryPurgeResult{}, bErr("Choose at least one path.")
		}
		preview, err := bHistoryPurgePreview(ctx, repoPath, clean)
		if err != nil {
			return HistoryPurgeResult{}, err
		}
		if preview.Blocked != "" {
			return HistoryPurgeResult{}, bErr(preview.Blocked)
		}
		if preview.Commits == 0 {
			return HistoryPurgeResult{}, bErr("No commit touches that path — nothing to rewrite.")
		}

		at := time.Now().Unix()
		prefix := bPurgeRoot + strconv.FormatInt(at, 10)
		listing, err := bRaw(ctx, repoPath, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags")
		if err != nil {
			return HistoryPurgeResult{}, err
		}
		refs := bRefShaPairs(listing)
		for _, r := range refs {
			if _, err := bRaw(ctx, repoPath, "update-ref", prefix+"/"+strings.TrimPrefix(r[0], "refs/"), r[1]); err != nil {
				return HistoryPurgeResult{}, err
			}
		}
		// Record what was purged, so the UI can label the backup later. A git
		// config key has to start with a letter, hence the `at` prefix.
		if _, err := bRaw(ctx, repoPath, "config", gitx.PrePurgeKey(at), strings.Join(clean, "\n")); err != nil {
			return HistoryPurgeResult{}, err
		}

		// `--index-filter` rewrites the index directly — no checkout per
		// commit, which is the difference between minutes and hours on a real
		// history. `--branches --tags` rather than `--all`: `--all` would
		// include the backup.
		rm := []string{"git", "rm", "--cached", "--ignore-unmatch", "--"}
		for _, p := range clean {
			rm = append(rm, bShellQuote(p))
		}
		_, err = gitx.Run(ctx, repoPath, []string{
			"filter-branch", "--force", "--index-filter", strings.Join(rm, " "), "--prune-empty",
			// Without this, tags keep pointing at the pre-rewrite commits.
			"--tag-name-filter", "cat", "--", "--branches", "--tags",
		}, gitx.RunOpts{Env: []string{"FILTER_BRANCH_SQUELCH_WARNING=1"}, MaxBuffer: 64 * 1024 * 1024})
		if err != nil {
			return HistoryPurgeResult{}, bDecodeErr(err)
		}

		// filter-branch leaves its own copies under refs/original; ours
		// supersedes them, and leaving both means twice the confusion about
		// which to restore.
		originals := gitx.SplitTrimmedLines(bRawOr(ctx, repoPath, "", "for-each-ref", "--format=%(refname)", "refs/original"))
		for _, ref := range originals {
			_, _ = bRaw(ctx, repoPath, "update-ref", "-d", ref)
		}
		return HistoryPurgeResult{
			Backup:    HistoryPurgeBackup{Prefix: prefix, At: at, Refs: len(refs), Paths: clean},
			Rewritten: len(refs),
		}, nil
	})
}

// gitsvc: historyPurgeBackups
//
// Every purge backup still in the repository, newest first.
func (s *GitService) HistoryPurgeBackups(ctx context.Context, repoPath string) ([]HistoryPurgeBackup, error) {
	return locked(s, ctx, "historyPurgeBackups", repoPath, func(ctx context.Context) ([]HistoryPurgeBackup, error) {
		refs := gitx.SplitTrimmedLines(bRawOr(ctx, repoPath, "", "for-each-ref", "--format=%(refname)", "refs/gitcito/pre-purge"))
		type stamp struct {
			at    float64
			count int
		}
		var stamps []*stamp
		byAt := map[float64]*stamp{}
		for _, ref := range refs {
			seg := ""
			if parts := strings.Split(ref, "/"); len(parts) > 3 {
				seg = parts[3]
			} else {
				continue // Number(undefined) is NaN
			}
			at := bJSNumber(seg)
			if math.IsNaN(at) || math.IsInf(at, 0) {
				continue
			}
			st := byAt[at]
			if st == nil {
				st = &stamp{at: at}
				byAt[at] = st
				stamps = append(stamps, st)
			}
			st.count++
		}
		out := []HistoryPurgeBackup{}
		for _, st := range stamps {
			atText := strconv.FormatFloat(st.at, 'f', -1, 64)
			recorded := gitx.TrimJS(bRawOr(ctx, repoPath, "", "config", "--get", "gitcito.prepurge.at"+atText))
			paths := []string{}
			if recorded != "" {
				paths = strings.Split(recorded, "\n")
			}
			out = append(out, HistoryPurgeBackup{Prefix: bPurgeRoot + atText, At: int64(st.at), Refs: st.count, Paths: paths})
		}
		bStableSortBy(out, func(a, b HistoryPurgeBackup) bool { return a.At > b.At })
		return out, nil
	})
}

// gitsvc: historyPurgeRestore
//
// Put every branch and tag back where the backup says it was.
func (s *GitService) HistoryPurgeRestore(ctx context.Context, repoPath, prefix string) error {
	return lockedErr(s, ctx, "historyPurgeRestore", repoPath, func(ctx context.Context) error {
		if !strings.HasPrefix(prefix, bPurgeRoot) {
			return bErr("Not a purge backup.")
		}
		listing, err := bRaw(ctx, repoPath, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
		if err != nil {
			return err
		}
		saved := bRefShaPairs(listing)
		if len(saved) == 0 {
			return bErr("That backup is empty.")
		}
		// Detach first: the branch we are standing on is about to move under us.
		_, _ = bGit(ctx, repoPath, "checkout", "--detach")
		for _, r := range saved {
			original := "refs/" + bSliceFrom(r[0], len(prefix)+1)
			if _, err := bRaw(ctx, repoPath, "update-ref", original, r[1]); err != nil {
				return err
			}
		}
		for _, r := range saved {
			if strings.Contains(r[0], "/heads/") {
				_, err := bGit(ctx, repoPath, "checkout", strings.Split(r[0], "/heads/")[1])
				return err
			}
		}
		return nil
	})
}

// bSliceFrom is `s.slice(n)`: past the end is "".
func bSliceFrom(s string, n int) string {
	if n >= len(s) {
		return ""
	}
	return s[n:]
}

// gitsvc: historyPurgeDropBackup
//
// Delete a backup and garbage-collect. This is what actually reclaims the
// space — and what makes the purge irreversible.
func (s *GitService) HistoryPurgeDropBackup(ctx context.Context, repoPath, prefix string) error {
	return lockedErr(s, ctx, "historyPurgeDropBackup", repoPath, func(ctx context.Context) error {
		if !strings.HasPrefix(prefix, bPurgeRoot) {
			return bErr("Not a purge backup.")
		}
		refs := gitx.SplitTrimmedLines(bRawOr(ctx, repoPath, "", "for-each-ref", "--format=%(refname)", prefix))
		for _, ref := range refs {
			_, _ = bRaw(ctx, repoPath, "update-ref", "-d", ref)
		}
		stamp := ""
		if parts := strings.Split(prefix, "/"); len(parts) > 3 {
			stamp = bJSNumberString(parts[3])
		} else {
			stamp = "NaN"
		}
		_, _ = bRaw(ctx, repoPath, "config", "--unset", "gitcito.prepurge.at"+stamp)
		// The old commits are only unreachable once the reflog forgets them too.
		_, _ = bRaw(ctx, repoPath, "reflog", "expire", "--expire=now", "--all")
		_, _ = bRaw(ctx, repoPath, "gc", "--prune=now")
		return nil
	})
}
