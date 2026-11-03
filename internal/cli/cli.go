// Package cli installs the `gitcito` command into PATH — src/main/cli.ts.
//
// The command is the gitcito-cli companion binary (cmd/gitcito-cli), which a
// build places next to the app executable. Install links it into a PATH
// directory under the name `gitcito`; the binary itself cannot be called that
// inside the bundle, because macOS file systems are case-insensitive and
// `gitcito` would collide with the `Gitcito` executable beside it.
//
// The link points at the binary directly rather than at a shell shim: the
// launcher logic the Electron-era shim held now lives in the binary, so one
// fewer hop and nothing that needs a POSIX shell.
//
// cli:finishEdit (the `gitcito --wait` editor hand-off) lives in internal/app.
package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// LinkName is the name the command has on PATH.
const LinkName = "gitcito"

// BinaryName is the companion binary's file name inside the app.
const BinaryName = "gitcito-cli"

// Result mirrors cli:install / cli:uninstall's { ok, error? }.
type Result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Installer holds what install/uninstall need, injected so tests never touch
// the real /usr/local/bin or ~/.local/bin.
type Installer struct {
	// Target is the binary the link points at.
	Target string
	// Dirs are the candidate PATH directories, in order.
	Dirs []string
	// GOOS is runtime.GOOS unless a test says otherwise.
	GOOS string
	// Privileged runs a shell command with administrator rights (macOS only);
	// nil means no escalation is available.
	Privileged func(ctx context.Context, command string) error
}

// NewInstaller builds the production installer: the gitcito-cli binary next
// to this executable, the platform's candidate directories, and the macOS
// authorization dialog for escalation.
func NewInstaller() *Installer {
	home, _ := os.UserHomeDir()
	return &Installer{
		Target:     defaultTarget(),
		Dirs:       candidateDirs(runtime.GOOS, home),
		GOOS:       runtime.GOOS,
		Privileged: runPrivileged,
	}
}

// candidateDirs are the standard-ish PATH locations, checked in order — what
// VS Code / Sublime install to. /usr/local/bin is the traditional choice and
// exists out of the box on Intel Macs; Apple Silicon Homebrew installs favor
// /opt/homebrew/bin, which is usually already on PATH for brew users. Linux
// prefers the per-user directory, which needs no escalation and is on PATH by
// default on every modern distribution; /usr/local/bin stays as the fallback
// for a system-wide install.
func candidateDirs(goos, home string) []string {
	if goos == "darwin" {
		return []string{"/usr/local/bin", "/opt/homebrew/bin"}
	}
	return []string{filepath.Join(home, ".local", "bin"), "/usr/local/bin"}
}

// defaultTarget is the gitcito-cli binary beside the running executable
// (bin/ in a build, Contents/MacOS in a macOS bundle).
func defaultTarget() string {
	exe, err := os.Executable()
	if err != nil {
		return BinaryName
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	name := BinaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(exe), name)
}

// targetDir is the first candidate directory that already exists, so we do
// not create top-level directories the user never asked for. Falls back to
// the first candidate (created on demand) when none exists yet.
func (i *Installer) targetDir() string {
	for _, d := range i.Dirs {
		if _, err := os.Lstat(d); err == nil {
			return d
		}
	}
	return i.Dirs[0]
}

func (i *Installer) linkPath() string { return filepath.Join(i.targetDir(), LinkName) }

// IsInstalled is true only if the link exists and already points at our
// binary — so a same-named file installed by something else is not reported
// as "installed" (or clobbered on uninstall).
func (i *Installer) IsInstalled() bool {
	link := i.linkPath()
	st, err := os.Lstat(link)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return false
	}
	dest, err := os.Readlink(link)
	return err == nil && dest == i.Target
}

// Install links the binary into PATH, escalating on macOS when the directory
// is not writable.
func (i *Installer) Install(ctx context.Context) Result {
	// Windows has no symlink-into-PATH convention and needs an installer-written
	// shim instead; until that exists, saying so is better than failing at the
	// first link.
	if i.GOOS == "windows" {
		return Result{Error: "Not supported on Windows yet"}
	}
	dir := i.targetDir()
	link := filepath.Join(dir, LinkName)
	err := func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		_ = os.Remove(link) // nothing to remove — fine
		return os.Symlink(i.Target, link)
	}()
	if err == nil {
		return Result{OK: true}
	}
	// Likely EACCES/EPERM — retry with an admin-privileged shell command.
	if perr := i.privileged(ctx, "mkdir -p "+shellQuote(dir)+" && ln -sf "+shellQuote(i.Target)+" "+shellQuote(link)); perr == nil {
		return Result{OK: true}
	}
	return Result{Error: nodeMessage(err)}
}

// Uninstall removes the link — only ours.
func (i *Installer) Uninstall(ctx context.Context) Result {
	link := i.linkPath()
	if !i.IsInstalled() {
		return Result{OK: true} // nothing to do
	}
	if err := os.Remove(link); err != nil {
		if perr := i.privileged(ctx, "rm -f "+shellQuote(link)); perr != nil {
			return Result{Error: nodeMessage(err)}
		}
	}
	return Result{OK: true}
}

func (i *Installer) privileged(ctx context.Context, command string) error {
	if i.Privileged == nil || i.GOOS != "darwin" {
		return errors.New("no privilege escalation available")
	}
	return i.Privileged(ctx, command)
}

// runPrivileged runs a shell command with administrator privileges via the
// macOS Authorization dialog — the same escalation path Finder and VS Code use
// when writing outside the user's home directory.
func runPrivileged(ctx context.Context, command string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("no privilege escalation available")
	}
	script := "do shell script " + appleScriptString(command) + " with administrator privileges"
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

// shellQuote single-quotes a path for /bin/sh. The Electron build interpolated
// paths bare; a bundle path with a space (or worse) would have split the
// command, so this is the one deliberate change.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appleScriptString is an AppleScript string literal (JSON.stringify in the
// TS, which AppleScript reads the same way for these characters).
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// nodeMessage renders a file-system error the way Node's fs/promises did:
// `EACCES: permission denied, symlink '<target>' -> '<link>'`.
func nodeMessage(err error) string {
	var le *os.LinkError
	if errors.As(err, &le) {
		return nodeCode(le.Err) + ", " + le.Op + " '" + le.Old + "' -> '" + le.New + "'"
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return nodeCode(pe.Err) + ", " + pe.Op + " '" + pe.Path + "'"
	}
	return err.Error()
}

func nodeCode(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		if strings.Contains(err.Error(), "not permitted") {
			return "EPERM: operation not permitted"
		}
		return "EACCES: permission denied"
	case errors.Is(err, os.ErrExist):
		return "EEXIST: file already exists"
	case errors.Is(err, syscall.ENOTDIR):
		return "ENOTDIR: not a directory"
	case errors.Is(err, os.ErrNotExist):
		return "ENOENT: no such file or directory"
	}
	return err.Error()
}
