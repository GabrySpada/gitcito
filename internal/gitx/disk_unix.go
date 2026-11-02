//go:build !windows

package gitx

import (
	"io/fs"
	"syscall"
)

func allocatedBlocks(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks)
	}
	return 0
}

// CanExecute reports whether path is executable by this process (access X_OK).
func CanExecute(path string) bool {
	return syscall.Access(path, 0x1) == nil
}
