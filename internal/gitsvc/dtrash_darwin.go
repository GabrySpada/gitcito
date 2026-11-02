//go:build darwin

package gitsvc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// dTrashItem moves path to the user's trash — Electron's shell.trashItem.
// On macOS that is ~/.Trash, under a free name. (Electron asked
// NSFileManager, which also records Finder's "Put Back"; a plain move does
// not, but needs no Automation permission prompt.)
func dTrashItem(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err != nil {
		return dNodeFSError("lstat", abs, "", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	trash := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	name := filepath.Base(abs)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		candidate := name
		if i > 1 {
			candidate = stem + " " + strconv.Itoa(i) + ext
		}
		dest := filepath.Join(trash, candidate)
		if _, err := os.Lstat(dest); err == nil {
			continue
		}
		return os.Rename(abs, dest)
	}
}
