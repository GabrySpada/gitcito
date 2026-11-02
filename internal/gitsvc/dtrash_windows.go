//go:build windows

package gitsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// dTrashItem moves path to the Recycle Bin — Electron's shell.trashItem —
// through the .NET VisualBasic FileSystem API, which is what SHFileOperation's
// "allow undo" does without cgo.
func dTrashItem(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return dNodeFSError("lstat", abs, "", err)
	}
	fn := "DeleteFile"
	if st.IsDir() {
		fn = "DeleteDirectory"
	}
	quoted := "'" + strings.ReplaceAll(abs, "'", "''") + "'"
	script := "Add-Type -AssemblyName Microsoft.VisualBasic; [Microsoft.VisualBasic.FileIO.FileSystem]::" + fn +
		"(" + quoted + ", 'OnlyErrorDialogs', 'SendToRecycleBin')"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = gitx.Exec(ctx, "", "powershell", []string{"-NoProfile", "-NonInteractive", "-Command", script}, gitx.RunOpts{PlainEnv: true})
	return err
}
