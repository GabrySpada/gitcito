package registry

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Walking a developer's home directory naively means a million files. Three
// rules keep a scan to a fraction of a second: stop at a repository (its own
// contents are never interesting), never enter a dot-directory, and skip the
// dependency folders that dwarf everything else.

var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "Pods": true, "target": true, "dist": true, "build": true, "out": true,
}

// MaxScanDepth: the renderer's depth input clamps to this range too, but a
// renderer is not a trustworthy source of limits — an unbounded depth here is
// an unbounded recursive filesystem walk, so the ceiling is enforced again at
// the source.
const MaxScanDepth = 10

// ScanForRepos returns the absolute paths of every repository under root, to
// depth levels. A cancelled context stops the walk where it is.
func ScanForRepos(ctx context.Context, root string, depth float64) []string {
	// The TS compared a fractional depth to 0 and so never stopped on one;
	// truncating keeps the walk bounded whatever the renderer sends.
	d := 0
	if !math.IsNaN(depth) {
		d = int(math.Min(MaxScanDepth, math.Max(0, depth)))
	}
	found := []string{}
	walk(ctx, root, d, &found)
	return found
}

func walk(ctx context.Context, dir string, depth int, found *[]string) {
	if ctx.Err() != nil {
		return
	}
	if GitDirOf(dir) != "" {
		// A repository's own contents are never scanned: a vendored checkout is
		// part of its parent, not a repo the user is looking for.
		*found = append(*found, dir)
		return
	}
	if depth == 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // unreadable (permissions, a vanished folder) — not an error
	}
	for _, e := range entries {
		// Dirent semantics: a symlink to a directory is not a directory here.
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		walk(ctx, filepath.Join(dir, name), depth-1, found)
	}
}
