//go:build windows

package gitx

import (
	"io/fs"
	"os"
)

func allocatedBlocks(_ fs.FileInfo) int64 { return 0 }

// CanExecute: Node's accessSync(X_OK) on Windows only checks that the file
// exists, so this does the same.
func CanExecute(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
