package gitsvc

// Project tree (working-directory file explorer), and the same listing for
// any past commit.
//
// Renderer channels served (through the 'git' dispatcher): listDir,
// listDirAt, listFiles.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TreeEntry is TreeEntry in src/shared/types.ts: one immediate child of a
// directory in the project tree.
type TreeEntry struct {
	// Name is the base name (no path).
	Name string `json:"name"`
	// Path is the repo-relative POSIX path.
	Path string `json:"path"`
	// Dir is true for directories.
	Dir bool `json:"dir"`
}

// bSortTree sorts folders first, then by name (localeCompare).
func bSortTree(entries []TreeEntry) {
	bStableSortBy(entries, func(a, b TreeEntry) bool {
		if a.Dir != b.Dir {
			return a.Dir
		}
		return bLocaleCompare(a.Name, b.Name) < 0
	})
}

// gitsvc: listDir
//
// Immediate children of relDir (repo-relative POSIX path; "" = root). The
// `.git` directory is hidden. Sorted folders-first, then by name. Lazy
// per-directory listing keeps huge trees (node_modules) responsive.
func (s *GitService) ListDir(ctx context.Context, repoPath, relDir string) ([]TreeEntry, error) {
	return locked(s, ctx, "listDir", repoPath, func(ctx context.Context) ([]TreeEntry, error) {
		// A folder can vanish or become unreadable between listing and a
		// re-read (e.g. a tool regenerates .husky/_, a watcher refresh races a
		// delete). Treat that as an empty folder rather than surfacing a scary
		// ENOENT.
		ents, _ := os.ReadDir(filepath.Join(repoPath, relDir))
		out := []TreeEntry{}
		for _, e := range ents {
			if relDir == "" && e.Name() == ".git" {
				continue
			}
			path := e.Name()
			if relDir != "" {
				path = relDir + "/" + e.Name()
			}
			out = append(out, TreeEntry{Name: e.Name(), Path: path, Dir: e.IsDir()})
		}
		bSortTree(out)
		return out, nil
	})
}

// A path (or ref) that simply isn't in that tree is an ordinary answer: the
// folder didn't exist yet at that point in history.
var bNotInTreeRe = regexp.MustCompile(`(?i)not a valid object name|does not exist|exists on disk, but not in`)

// gitsvc: listDirAt
//
// The same listing, but for a repo as it was at `ref` — read straight from
// the object database (`git ls-tree`), so the Time Machine can walk any past
// commit's tree without checking anything out or touching the working copy.
func (s *GitService) ListDirAt(ctx context.Context, repoPath, ref, relDir string) ([]TreeEntry, error) {
	return locked(s, ctx, "listDirAt", repoPath, func(ctx context.Context) ([]TreeEntry, error) {
		// `<ref>:<dir>` lists *inside* the directory rather than returning
		// the directory entry itself.
		spec := ref + ":"
		if relDir != "" {
			spec = ref + ":" + relDir
		}
		raw, err := bRaw(ctx, repoPath, "ls-tree", "-z", "--full-name", spec)
		if err != nil {
			// Anything but "not in that tree" is a real failure and must reach
			// the caller instead of quietly rendering as an empty directory.
			if !bNotInTreeRe.MatchString(err.Error()) {
				return nil, err
			}
			raw = ""
		}
		out := []TreeEntry{}
		for _, record := range strings.Split(raw, "\x00") {
			if record == "" {
				continue
			}
			// `<mode> <type> <oid>\t<path>`
			tab := strings.IndexByte(record, '\t')
			if tab < 0 {
				continue
			}
			meta := bSplitWSKeepEmpty(record[:tab])
			typ := ""
			if len(meta) > 1 {
				typ = meta[1]
			}
			// ls-tree names are relative to the rev's prefix (`HEAD:src`), so
			// the repo-relative path has to be rebuilt from the directory asked for.
			name := record[tab+1:]
			path := name
			if relDir != "" {
				path = relDir + "/" + name
			}
			// Submodules (`commit` entries) have no tree to descend into here.
			out = append(out, TreeEntry{Name: name, Path: path, Dir: typ == "tree"})
		}
		bSortTree(out)
		return out, nil
	})
}

// gitsvc: listFiles
//
// Flat list of every searchable file path (repo-relative POSIX): tracked plus
// untracked-but-not-ignored, the same scope VSCode searches by default. Fast —
// `ls-files` skips node_modules/etc via the ignore rules, no fs walk.
func (s *GitService) ListFiles(ctx context.Context, repoPath string) ([]string, error) {
	return locked(s, ctx, "listFiles", repoPath, func(ctx context.Context) ([]string, error) {
		raw := bRawOr(ctx, repoPath, "", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		// De-dupe (a path can appear in both cached + others briefly during edits).
		seen := map[string]bool{}
		out := []string{}
		for _, p := range strings.Split(raw, "\x00") {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		return out, nil
	})
}
