package gitsvc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// cTrashItem moves a path to the OS trash — Electron's shell.trashItem, which
// fsDelete and the "Replace" branch of a tree drop relied on so that both stay
// recoverable.
//
//   - Linux: the freedesktop.org home trash ($XDG_DATA_HOME/Trash, else
//     ~/.local/share/Trash), with the .trashinfo file desktop trash cans read
//     for "Restore". A path on another filesystem than the trash cannot be
//     renamed into it; `gio trash` (which knows the per-volume trash dirs) is
//     asked instead.
//   - macOS: ~/.Trash, under a free name.
//   - Windows: the Recycle Bin, through the VisualBasic FileSystem API.
func cTrashItem(ctx context.Context, target string) error {
	if _, err := os.Lstat(target); err != nil {
		return cNodeFsErr(err)
	}
	switch runtime.GOOS {
	case "windows":
		return cTrashWindows(ctx, target)
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		_, err = cMoveFree(target, filepath.Join(home, ".Trash"))
		return err
	}
	return cTrashFreedesktop(ctx, target)
}

func cTrashFreedesktop(ctx context.Context, target string) error {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	trash := filepath.Join(dataHome, "Trash")
	filesDir, infoDir := filepath.Join(trash, "files"), filepath.Join(trash, "info")
	for _, d := range []string{filesDir, infoDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return cNodeFsErr(err)
		}
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	// Reserve the name with its .trashinfo first (O_EXCL), as the spec asks,
	// so two trashings of the same base name cannot collide.
	base := filepath.Base(abs)
	for i := 1; i < 10000; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s.%d", base, i)
		}
		infoPath := filepath.Join(infoDir, name+".trashinfo")
		f, err := os.OpenFile(infoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return cNodeFsErr(err)
		}
		info := "[Trash Info]\nPath=" + cTrashInfoPath(abs) + "\nDeletionDate=" +
			time.Now().Format("2006-01-02T15:04:05") + "\n"
		_, werr := f.WriteString(info)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(infoPath)
			return errors.Join(werr, cerr)
		}
		err = os.Rename(abs, filepath.Join(filesDir, name))
		if err == nil {
			return nil
		}
		_ = os.Remove(infoPath)
		if errors.Is(err, syscall.EXDEV) && gitx.HasBinary("gio") {
			if _, gerr := gitx.Exec(ctx, "", "gio", []string{"trash", "--", abs}, gitx.RunOpts{PlainEnv: true}); gerr == nil {
				return nil
			}
		}
		return cNodeFsErr(err)
	}
	return errors.New("no free name in the trash for " + base)
}

// cTrashInfoPath percent-encodes a path for a .trashinfo `Path=` line,
// keeping the slashes.
func cTrashInfoPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// cMoveFree renames target into dir under its own name, or "name 2" etc.
func cMoveFree(target, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", cNodeFsErr(err)
	}
	base := filepath.Base(target)
	stem, ext := base, ""
	if dot := strings.LastIndexByte(base, '.'); dot > 0 {
		stem, ext = base[:dot], base[dot:]
	}
	for i := 1; i < 10000; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s %d%s", stem, i, ext)
		}
		dest := filepath.Join(dir, name)
		if _, err := os.Lstat(dest); err == nil {
			continue
		}
		return dest, cNodeFsErr(os.Rename(target, dest))
	}
	return "", errors.New("no free name in the trash for " + base)
}

func cTrashWindows(ctx context.Context, target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return cNodeFsErr(err)
	}
	method := "DeleteFile"
	if st.IsDir() {
		method = "DeleteDirectory"
	}
	// The path travels as an environment variable, never inside the script,
	// so no quoting of it is needed.
	script := "Add-Type -AssemblyName Microsoft.VisualBasic; " +
		"[Microsoft.VisualBasic.FileIO.FileSystem]::" + method +
		"($env:GITCITO_TRASH_PATH, 'OnlyErrorDialogs', 'SendToRecycleBin')"
	_, err = gitx.Exec(ctx, "", "powershell", []string{"-NoProfile", "-NonInteractive", "-Command", script},
		gitx.RunOpts{PlainEnv: true, Env: []string{"GITCITO_TRASH_PATH=" + abs}})
	return err
}
