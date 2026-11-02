package app

// ShellService serves the renderer channels that hand something to the OS:
//   shell:openExternal, shell:showItemInFolder, shell:openPath,
//   shell:openWithPicker, shell:pickApplication, shell:openWithApp,
//   shell:writeFiles
// It mirrors the handlers that lived in src/main/index.ts.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ShellService is bound to the page. Every exported method is reachable from
// JavaScript, so each one validates its input as if the caller were hostile.
type ShellService struct{}

var httpURL = regexp.MustCompile(`^https?://`)

// OpenExternal opens a web link in the default browser. Anything other than
// http(s) is ignored silently, exactly as before: a `file:` or custom-scheme URL
// from repository content must never reach the OS opener.
func (s *ShellService) OpenExternal(url string) error {
	if !httpURL.MatchString(url) {
		return nil
	}
	a := application.Get()
	if a == nil {
		return nil
	}
	return a.Browser.OpenURL(url)
}

// ShowItemInFolder reveals a file in the platform file manager.
func (s *ShellService) ShowItemInFolder(fullPath string) error {
	a := application.Get()
	if a == nil {
		return nil
	}
	if err := a.Env.OpenFileManager(fullPath, true); err != nil {
		return revealFallback(fullPath)
	}
	return nil
}

// OpenPath opens a file or folder with its default application. Like Electron's
// shell.openPath it reports failure as a message string, empty on success.
func (s *ShellService) OpenPath(fullPath string) string {
	a := application.Get()
	if a == nil {
		return "application not running"
	}
	if err := a.Browser.OpenFile(fullPath); err != nil {
		return err.Error()
	}
	return ""
}

// OpenWithPicker shows the OS-native "Open With…" app picker for a file or
// folder. macOS and Windows have first-class support; other platforms fall back
// to the default opener since there's no universal picker across Linux desktop
// environments.
func (s *ShellService) OpenWithPicker(fullPath string) string {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`tell application "Finder" to open (POSIX file %s) using (choose application)`, jsonQuote(fullPath))
		out, err := exec.Command("osascript", "-e", script).CombinedOutput()
		// -128 is AppleScript's "user canceled" — not a real failure.
		if err != nil && !strings.Contains(string(out), "-128") {
			return err.Error()
		}
		return ""
	case "windows":
		if err := exec.Command("rundll32.exe", "shell32.dll,OpenAs_RunDLL", fullPath).Run(); err != nil {
			return err.Error()
		}
		return ""
	}
	return s.OpenPath(fullPath)
}

// AppChoice is what PickApplication returns: the display name and the path.
type AppChoice struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var appSuffix = regexp.MustCompile(`(?i)\.(app|exe)$`)

// PickApplication lets the user browse to and pick a specific application (e.g.
// VS Code), to be remembered as the default "Open with <App>" target. Returns
// nil if cancelled.
func (s *ShellService) PickApplication() (*AppChoice, error) {
	a := application.Get()
	if a == nil {
		return nil, nil
	}
	d := a.Dialog.OpenFile().SetTitle("Choose application").CanChooseFiles(true)
	switch runtime.GOOS {
	case "darwin":
		// An .app is a directory bundle: the picker has to treat it as a file.
		d = d.SetDirectory("/Applications").AddFilter("Applications", "*.app").CanChooseDirectories(false)
	case "windows":
		d = d.AddFilter("Programs", "*.exe")
	default:
		d = d.AddFilter("All files", "*")
	}
	appPath, err := d.PromptForSingleSelection()
	if err != nil || appPath == "" {
		return nil, nil
	}
	base := filepath.Base(appPath)
	return &AppChoice{Name: appSuffix.ReplaceAllString(base, ""), Path: appPath}, nil
}

// OpenWithApp launches a specific application with a file/folder path, e.g. the
// equivalent of running `code <path>` for VS Code, without relying on a
// shell/PATH lookup. Returns an error message, empty on success.
func (s *ShellService) OpenWithApp(targetPath, appPath string) string {
	if runtime.GOOS == "darwin" {
		if err := exec.Command("open", "-a", appPath, targetPath).Run(); err != nil {
			return err.Error()
		}
		return ""
	}
	cmd := exec.Command(appPath, targetPath)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	// Detached: reap in the background so it never becomes a zombie.
	go func() { _ = cmd.Wait() }()
	return ""
}

// FileToWrite is one entry of a shell:writeFiles batch.
type FileToWrite struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFiles writes generated files into a repository.
func (s *ShellService) WriteFiles(ctx context.Context, repoPath string, files []FileToWrite) error {
	return writeRepoFiles(repoPath, files)
}

func writeRepoFiles(repoPath string, files []FileToWrite) error {
	// These paths can come from an LLM (the project-config wizard), so a
	// "../.." would otherwise write anywhere on disk. Refuse rather than clamp —
	// a path that needed rewriting was not the one that was asked for — and
	// check the whole batch first so a bad entry writes nothing at all.
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	for _, f := range files {
		full, err := filepath.Abs(filepath.Join(repoPath, f.Path))
		if err != nil || !shared.IsSafeRepoPath(f.Path) ||
			(full != root && !strings.HasPrefix(full, root+string(filepath.Separator))) {
			return fmt.Errorf("Refusing to write outside the repository: %s", f.Path)
		}
	}
	for _, f := range files {
		full := filepath.Join(repoPath, f.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(f.Content), 0o644); err != nil {
			return err
		}
		if strings.HasPrefix(f.Path, ".git/hooks/") {
			if err := os.Chmod(full, 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

// revealFallback covers platforms where the Wails file-manager call fails
// (for example a Linux desktop without the FileManager1 D-Bus interface).
func revealFallback(p string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", p)
	case "windows":
		cmd = exec.Command("explorer", "/select,", p)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(p))
	}
	if err := cmd.Start(); err != nil {
		return errors.Join(errors.New("could not reveal item"), err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// jsonQuote produces the same quoting JSON.stringify gives a string, which is
// also a valid AppleScript string literal for every path we pass it.
func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
