//go:build !windows && !darwin

package gitsvc

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// dTrashItem moves path to the user's trash — Electron's shell.trashItem. On
// Linux and the BSDs that is the freedesktop.org home trash
// ($XDG_DATA_HOME/Trash, else ~/.local/share/Trash): the item lands in
// `files/` under a free name, with a `.trashinfo` record in `info/` that file
// managers use to restore it. A path on another filesystem than the trash
// fails rather than being copied (GIO would use that volume's own trash).
func dTrashItem(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err != nil {
		return dNodeFSError("lstat", abs, "", err)
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		base = filepath.Join(home, ".local", "share")
	}
	trash := filepath.Join(base, "Trash")
	files, info := filepath.Join(trash, "files"), filepath.Join(trash, "info")
	for _, d := range []string{files, info} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	name := filepath.Base(abs)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		candidate := name
		if i > 1 {
			candidate = stem + "." + strconv.Itoa(i) + ext
		}
		// The .trashinfo is created exclusively first: it is what reserves the name.
		record := filepath.Join(info, candidate+".trashinfo")
		f, err := os.OpenFile(record, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		escaped := (&url.URL{Path: abs}).EscapedPath()
		_, werr := f.WriteString("[Trash Info]\nPath=" + escaped + "\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n")
		cerr := f.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(abs, filepath.Join(files, candidate))
		}
		if werr != nil {
			_ = os.Remove(record)
			return werr
		}
		return nil
	}
}
