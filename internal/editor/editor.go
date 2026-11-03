// Package editor finds and launches the user's external editor — the port of
// src/main/editor.ts.
//
// Detection prefers the editor's CLI over its application bundle, because only
// the CLI accepts a line number. gitx.FixPath has already merged the login
// shell's PATH into this process, so a `code` installed by VS Code's "Install
// 'code' command in PATH" is visible here even when the app was opened from the
// Dock.
//
// Nothing here ever goes through a shell: the command and its arguments are
// passed to exec as an argv array, so a path with a space, a quote or a `;` is
// data, not syntax.
package editor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// NoEditorMessage is what open resolves to when nothing is configured.
const NoEditorMessage = "No editor is configured."

// Editors detects and launches editors. The zero value is not usable; New
// builds one wired to this machine.
type Editors struct {
	goos   string
	getenv func(string) string
	// appRoots are where macOS bundles live: the system and per-user
	// Applications folders.
	appRoots func() []string
}

// New builds Editors for this machine.
func New() *Editors {
	return &Editors{
		goos:   runtime.GOOS,
		getenv: os.Getenv,
		appRoots: func() []string {
			return []string{"/Applications", filepath.Join(homeDir(), "Applications")}
		},
	}
}

// homeDir is Node's os.homedir(): $HOME (USERPROFILE on Windows), read at call time.
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// onPath is the first directory on PATH holding an executable called bin.
func (e *Editors) onPath(bin string) string {
	names := []string{bin}
	if e.goos == "windows" {
		names = []string{bin + ".cmd", bin + ".exe", bin + ".bat"}
	}
	for _, dir := range strings.Split(e.getenv("PATH"), string(filepath.ListSeparator)) {
		if dir == "" {
			continue
		}
		for _, name := range names {
			full := filepath.Join(dir, name)
			// access(X_OK), as accessSync did: a directory passes too.
			if gitx.CanExecute(full) {
				return full
			}
		}
	}
	return ""
}

// macApp finds a bundle in either the system or the per-user Applications folder.
func (e *Editors) macApp(bundle string) string {
	for _, root := range e.appRoots() {
		if full := filepath.Join(root, bundle); exists(full) {
			return full
		}
	}
	return ""
}

func (e *Editors) winExe(relative string) string {
	roots := []string{}
	for _, r := range []string{e.getenv("ProgramFiles"), e.getenv("ProgramFiles(x86)")} {
		if r != "" {
			roots = append(roots, r)
		}
	}
	if local := e.getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "Programs"))
	}
	for _, root := range roots {
		if full := filepath.Join(root, relative); exists(full) {
			return full
		}
	}
	return ""
}

func first(items []string, find func(string) string) string {
	for _, it := range items {
		if found := find(it); found != "" {
			return found
		}
	}
	return ""
}

// Detect lists every editor found on this machine, CLI installs first. Cheap
// enough to run on demand (a handful of `stat` calls), so Settings re-scans
// each time it opens rather than caching a stale answer.
func (e *Editors) Detect() []shared.DetectedEditor {
	found := []shared.DetectedEditor{}
	for _, preset := range shared.EditorPresets {
		if cli := first(preset.Bins, e.onPath); cli != "" {
			found = append(found, shared.DetectedEditor{ID: preset.ID, Name: preset.Name, Command: cli, Source: "cli"})
			continue
		}
		if e.goos == "darwin" {
			if bundle := first(preset.MacApps, e.macApp); bundle != "" {
				found = append(found, shared.DetectedEditor{ID: preset.ID, Name: preset.Name, Command: bundle, Source: "app"})
				continue
			}
		}
		if e.goos == "windows" {
			if exe := first(preset.WinPaths, e.winExe); exe != "" {
				found = append(found, shared.DetectedEditor{ID: preset.ID, Name: preset.Name, Command: exe, Source: "cli"})
			}
		}
	}
	return found
}

// Open launches the configured editor on a target. It returns an empty string
// on success, or the failure message — the renderer toasts it rather than
// throwing, since "my editor did not open" is a nuisance, not an error state.
func (e *Editors) Open(ctx context.Context, setting shared.EditorSetting, target shared.EditorTarget) string {
	plan := shared.EditorLaunch(setting, target)
	if plan == nil {
		return NoEditorMessage
	}
	// A macOS bundle is not an executable; `open -a` is the supported way in,
	// and it takes the path only — hence no line jumping for bundles.
	if plan.Source == "app" {
		if _, err := gitx.Exec(ctx, "", "open", []string{"-a", plan.Command, target.Path}, gitx.RunOpts{PlainEnv: true}); err != nil {
			return err.Error()
		}
		return ""
	}
	cmd := exec.Command(plan.Command, plan.Args...)
	// stdio ignored (nil = the null device), and detached so quitting Gitcito
	// never takes the editor with it.
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return spawnError(plan.Command, err)
	}
	// Reap in the background so the editor never lingers as a zombie of ours.
	go func() { _ = cmd.Wait() }()
	return ""
}

// spawnError words a start failure the way Node's spawn error did.
func spawnError(command string, err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return "spawn " + command + " ENOENT"
	case errors.Is(err, fs.ErrPermission):
		return "spawn " + command + " EACCES"
	}
	return "spawn " + command + " " + err.Error()
}
