package gitx

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// Clean-preview bounds: how many removable paths a preview sizes and shows,
// and how many files a directory walk stats before giving up on an exact size.
const (
	CleanEntryCap = 400
	CleanWalkCap  = 20000
)

// DiskBytes is the space a file actually occupies, not the length of its
// contents.
//
// The difference is not academic here: a loose git object is a few hundred
// bytes in a 4 KB block, so a repository with a thousand of them costs 4 MB
// of disk and reports 250 KB of content. `git count-objects` answers in
// blocks, and a panel that mixes the two makes the parts add up to more than
// the whole. Block counts are absent on Windows — fall back to the length.
func DiskBytes(fi fs.FileInfo) int64 {
	if b := allocatedBlocks(fi); b > 0 {
		return b * 512
	}
	return fi.Size()
}

// DirBytes sums DiskBytes under dir. Capped by *budget files: an untracked
// `node_modules` holds hundreds of thousands of files, and a preview that
// stats all of them is a preview nobody waits for. Hitting the cap
// under-reports rather than stalls. Symlinks are never followed: their target
// may sit outside the repo. The budget is shared across calls, as in TS.
func DirBytes(ctx context.Context, dir string, budget *int) int64 {
	var total int64
	stack := []string{dir}
	for len(stack) > 0 && *budget > 0 {
		if ctx.Err() != nil {
			return total
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(current)
		if err != nil {
			continue
		}
		for _, e := range entries {
			// `budget.files-- <= 0`: test, then decrement either way.
			left := *budget
			*budget--
			if left <= 0 {
				break
			}
			full := filepath.Join(current, e.Name())
			switch {
			case e.IsDir():
				stack = append(stack, full)
			case e.Type().IsRegular():
				if st, err := os.Stat(full); err == nil {
					total += DiskBytes(st)
				}
			}
		}
	}
	return total
}
