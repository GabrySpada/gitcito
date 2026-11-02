package gitsvc

// Untracked files (`git clean`), discarding changes, .gitignore and untrack.
//
// Removing untracked files is the one destructive git operation with no
// recovery: the content was never in an object, so no reflog, no stash, no
// undo. Hence a preview that says exactly what would go and how big it is, a
// trash option that takes the finality out of it, and a guard snapshot taken
// before anything is removed.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// CleanEntry is CleanEntry in src/shared/types.ts.
type CleanEntry struct {
	// Path is repo-relative; a directory keeps git's trailing slash.
	Path string `json:"path"`
	// Kind: 'file' | 'dir'.
	Kind string `json:"kind"`
	// Ignored: matched by .gitignore. Never selected by default.
	Ignored bool  `json:"ignored"`
	Bytes   int64 `json:"bytes"`
	// Nested: a directory with its own .git. `git clean` refuses these; the trash does not.
	Nested bool `json:"nested"`
}

// CleanPreview is CleanPreview in src/shared/types.ts.
type CleanPreview struct {
	Entries []CleanEntry `json:"entries"`
	// Truncated is true when the scan hit its entry cap and the list is partial.
	Truncated bool `json:"truncated"`
}

// CleanResult is CleanResult in src/shared/types.ts.
type CleanResult struct {
	Removed int   `json:"removed"`
	Bytes   int64 `json:"bytes"`
	// Trashed is true when the paths went to the OS trash rather than being deleted.
	Trashed bool `json:"trashed"`
}

func dFileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// CleanPreview says what `git clean` would remove, sized, with ignored paths
// kept apart.
//
// gitsvc: cleanPreview
func (s *GitService) CleanPreview(ctx context.Context, repoPath string) (CleanPreview, error) {
	return locked(s, ctx, "cleanPreview", repoPath, func(ctx context.Context) (CleanPreview, error) {
		untracked, ignored := gitx.CleanCandidates(ctx, repoPath)
		type cand struct {
			path    string
			ignored bool
		}
		var all []cand
		for _, p := range untracked {
			all = append(all, cand{p, false})
		}
		for _, p := range ignored {
			all = append(all, cand{p, true})
		}
		kept := all
		if len(kept) > gitx.CleanEntryCap {
			kept = kept[:gitx.CleanEntryCap]
		}
		budget := gitx.CleanWalkCap
		entries := []CleanEntry{}
		for _, c := range kept {
			isDir := strings.HasSuffix(c.path, "/")
			full := dResolve(repoPath, c.path)
			e := CleanEntry{Path: c.path, Kind: "file", Ignored: c.ignored}
			if isDir {
				e.Kind = "dir"
				e.Bytes = gitx.DirBytes(ctx, full, &budget)
				e.Nested = dExists(filepath.Join(full, ".git"))
			} else {
				e.Bytes = dFileSize(full)
			}
			entries = append(entries, e)
		}
		return CleanPreview{Entries: entries, Truncated: len(all) > len(kept)}, nil
	})
}

// Clean removes untracked paths, either to the OS trash or for good.
//
// Every path is checked against git's own candidate list first: the trash
// route does not go through git, so without that check a tracked file could be
// deleted by passing its name.
//
// gitsvc: clean
func (s *GitService) Clean(ctx context.Context, repoPath string, paths []string, trash bool) (CleanResult, error) {
	return locked(s, ctx, "clean", repoPath, func(ctx context.Context) (CleanResult, error) {
		var targets []string
		for _, p := range paths {
			if p != "" {
				targets = append(targets, p)
			}
		}
		if len(targets) == 0 {
			return CleanResult{}, dErr("Nothing selected to remove.")
		}
		dGuardSnapshot(ctx, repoPath)

		untracked, ignored := gitx.CleanCandidates(ctx, repoPath)
		removable := map[string]bool{}
		for _, p := range append(untracked, ignored...) {
			removable[p] = true
		}
		root := dResolve(repoPath)
		sep := string(filepath.Separator)
		for _, p := range targets {
			if !removable[p] {
				return CleanResult{}, dErrf("Not an untracked path in this repository: %s", p)
			}
			full := dResolve(repoPath, p)
			if full != root && !strings.HasPrefix(full, root+sep) {
				return CleanResult{}, dErrf("Refusing to remove outside the repository: %s", p)
			}
		}

		budget := gitx.CleanWalkCap
		var failed []string
		res := CleanResult{Trashed: trash}
		for _, p := range targets {
			full := dResolve(repoPath, p)
			var size int64
			if strings.HasSuffix(p, "/") {
				size = gitx.DirBytes(ctx, full, &budget)
			} else {
				size = dFileSize(full)
			}
			err := func() error {
				if trash {
					if err := dTrashItem(full); err != nil {
						return err
					}
				} else if _, err := gitx.RunGit(ctx, repoPath, []string{"clean", "-f", "-d", "-x", "--", p}, gitx.RunOpts{}); err != nil {
					// `-x` so an explicitly chosen ignored path goes too; the
					// pathspec keeps that from meaning "and everything else ignored".
					return err
				}
				// git skips a directory that is its own repository and still exits 0.
				if dExists(full) {
					return dErr("git skipped it — a nested repository can only go to the trash")
				}
				return nil
			}()
			if err != nil {
				failed = append(failed, p+" — "+err.Error())
				continue
			}
			res.Removed++
			res.Bytes += size
		}
		if len(failed) > 0 {
			return CleanResult{}, dErrf("Could not remove %d of %d:\n%s", len(failed), len(targets), strings.Join(failed, "\n"))
		}
		return res, nil
	})
}

// Discard throws away working-tree changes to files (untracked: delete them
// with `git clean -f`). A guard snapshot is taken first.
//
// gitsvc: discard
func (s *GitService) Discard(ctx context.Context, repoPath string, files []string, untracked bool) error {
	return lockedErr(s, ctx, "discard", repoPath, func(ctx context.Context) error {
		dGuardSnapshot(ctx, repoPath)
		if untracked {
			_, err := gitx.Raw(ctx, repoPath, append([]string{"clean", "-f", "--"}, files...), gitx.RunOpts{})
			return err
		}
		// Per-file so one tricky path doesn't fail the whole batch:
		//  1. `checkout -- f` restores a modified file from the index (keeps staged
		//     changes — the common case).
		//  2. staged deletions/renames aren't in the index, so fall back to
		//     `checkout HEAD -- f` to bring the file back from the last commit.
		//  3. staged-new files don't exist in HEAD either, so drop them from the
		//     index and disk with `rm -f`.
		for _, f := range files {
			if _, err := gitx.Raw(ctx, repoPath, []string{"checkout", "--", f}, gitx.RunOpts{}); err == nil {
				continue
			}
			if _, err := gitx.Raw(ctx, repoPath, []string{"checkout", "HEAD", "--", f}, gitx.RunOpts{}); err == nil {
				continue
			}
			_, _ = gitx.Raw(ctx, repoPath, []string{"rm", "-f", "--", f}, gitx.RunOpts{})
		}
		return nil
	})
}

// AddToGitignore appends repo-relative patterns to the repository's root
// `.gitignore`, skipping any already present, and returns the ones actually
// added. Patterns come pre-formatted (anchored with a leading `/`, folders
// with a trailing `/`).
//
// gitsvc: addToGitignore
func (s *GitService) AddToGitignore(ctx context.Context, repoPath string, patterns []string) ([]string, error) {
	return locked(s, ctx, "addToGitignore", repoPath, func(ctx context.Context) ([]string, error) {
		return dAddToGitignoreAt(repoPath, "", patterns)
	})
}

// AddToGitignoreAt appends patterns to the .gitignore in dir (relative to the
// repo; "" = root), creating the file if absent and skipping entries already
// present.
//
// gitsvc: addToGitignoreAt
func (s *GitService) AddToGitignoreAt(ctx context.Context, repoPath, dir string, patterns []string) ([]string, error) {
	return locked(s, ctx, "addToGitignoreAt", repoPath, func(ctx context.Context) ([]string, error) {
		return dAddToGitignoreAt(repoPath, dir, patterns)
	})
}

var dCRLFRe = regexp.MustCompile(`\r?\n`)

func dAddToGitignoreAt(repoPath, dir string, patterns []string) ([]string, error) {
	file := filepath.Join(repoPath, dir, ".gitignore")
	current, err := dReadUTF8(file)
	if err != nil {
		current = ""
	}
	existing := map[string]bool{}
	for _, l := range dCRLFRe.Split(current, -1) {
		if t := gitx.TrimJS(l); t != "" {
			existing[t] = true
		}
	}
	toAdd := []string{}
	for _, p := range patterns {
		if p = gitx.TrimJS(p); p != "" && !existing[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return toAdd, nil
	}
	next := current
	if current != "" && !strings.HasSuffix(current, "\n") {
		next += "\n"
	}
	next += strings.Join(toAdd, "\n") + "\n"
	_ = os.MkdirAll(filepath.Join(repoPath, dir), 0o777)
	if err := os.WriteFile(file, []byte(next), 0o666); err != nil {
		return nil, dNodeFSError("open", file, "", err)
	}
	return toAdd, nil
}

// Untrack stops tracking files/folders. By default they are removed from the
// index only (kept on disk); with deleteFromDisk they also leave the working
// tree. `-r` allows folders; `--ignore-unmatch` keeps the call safe if a path
// was already untracked.
//
// gitsvc: untrack
func (s *GitService) Untrack(ctx context.Context, repoPath string, files []string, deleteFromDisk bool) error {
	return lockedErr(s, ctx, "untrack", repoPath, func(ctx context.Context) error {
		if len(files) == 0 {
			return nil
		}
		args := []string{"rm", "-r", "--ignore-unmatch"}
		if !deleteFromDisk {
			args = append(args, "--cached")
		}
		args = append(append(args, "--"), files...)
		_, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
		return err
	})
}
