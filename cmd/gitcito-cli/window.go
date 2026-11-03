package main

// The launcher half of `gitcito`: everything that needs the window. It is the
// Electron-era resources/cli/gitcito shell script, ported line for line so the
// verbs, errors and exit codes stay what users scripted against.
//
//   - `gitcito [verb] [path] [-n name] [-g group] [-l line]` opens (or
//     focuses) the app on a repository, optionally at a surface.
//   - `gitcito --wait <file>` is the git-editor mode: it hands the file and a
//     sentinel to the app and blocks until the app deletes the sentinel, which
//     is what lets `core.editor = gitcito --wait` work at all.
//
// The request reaches the app as single-token `--flag=value` switches
// (internal/shared.ParseCliOpenArgs): a second launch hands its argv to the
// running instance, and a single token survives any reordering on the way.

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// pollInterval is how often --wait looks for its sentinel.
const pollInterval = 200 * time.Millisecond

type launcher struct {
	stdout, stderr io.Writer
}

func newLauncher(stdout, stderr io.Writer) *launcher {
	return &launcher{stdout: stdout, stderr: stderr}
}

func (l *launcher) errln(s string) { _, _ = io.WriteString(l.stderr, s+"\n") }

func (l *launcher) main(argv []string) int {
	var dir, name, group, view, arg, line string
	waitMode, byName := false, false

	// A leading verb selects the surface; `open` is the explicit form of the
	// default. Anything else is treated as a path, so `gitcito .` still works.
	if len(argv) > 0 {
		if shared.IsCliView(argv[0]) {
			view = argv[0]
			argv = argv[1:]
		} else if argv[0] == "open" {
			argv = argv[1:]
			byName = true
		}
	}

	next := func(i int) string {
		if i+1 < len(argv) {
			return argv[i+1]
		}
		return ""
	}
	for i := 0; i < len(argv); {
		a := argv[i]
		switch {
		case a == "-n" || a == "--name":
			name = next(i)
			i += 2
		case a == "-g" || a == "--group":
			group = next(i)
			i += 2
		case a == "-l" || a == "--line":
			line = next(i)
			i += 2
		case a == "-w" || a == "--wait":
			waitMode = true
			i++
		case a == "--":
			i++
		case strings.HasPrefix(a, "-"):
			l.errln("gitcito: unknown option: " + a)
			l.errln("Try 'gitcito --help'.")
			return 2
		default:
			// The first bare word after a surface verb is that surface's subject —
			// a ref for `show`, a file for `blame`, a query for `search`. Views
			// that take no subject treat it as the repository path instead.
			switch view {
			case "show", "blame", "file", "search":
				if arg == "" {
					arg = a
				} else {
					dir = a
				}
			default:
				dir = a
			}
			i++
		}
	}

	// ─── --wait: git's editor ──────────────────────────────────────────────
	//
	// git hands us a path (COMMIT_EDITMSG, git-rebase-todo, …) and blocks on our
	// exit. We pass the file plus a sentinel to the app, then poll: the app
	// deletes the sentinel when the user is done, and only then do we return.
	editFile, waitFile := "", ""
	if waitMode {
		target := arg
		if target == "" {
			target = dir
		}
		if target == "" {
			l.errln("gitcito: --wait needs a file to edit")
			return 2
		}
		if strings.HasPrefix(target, "/") || filepath.IsAbs(target) {
			editFile = target
		} else {
			// Not cleaned: the shim built `$PWD/$target` verbatim.
			editFile = cwd() + string(filepath.Separator) + target
		}
		if _, err := os.Stat(editFile); err != nil {
			l.errln("gitcito: no such file: " + editFile)
			return 2
		}
		f, err := os.CreateTemp(tmpDir(), "gitcito-wait.")
		if err != nil {
			return 2
		}
		waitFile = f.Name()
		_ = f.Close()
		// The repository the file belongs to, so the app can open it in context.
		dir = repoTop(filepath.Dir(editFile))
		if dir == "" {
			dir = filepath.Dir(editFile)
		}
	}

	// `gitcito open api` — a word that is not a directory is a repository
	// Gitcito already knows by tab name.
	if byName && dir != "" && !isDir(dir) {
		hit := shared.ResolveRepo(shared.KnownRepos(readSettings()), strings.ToLower(dir))
		if hit == nil {
			l.errln("gitcito: no repository matching \"" + dir + "\" — try 'gitcito repos'")
			return 1
		}
		dir = hit.Path
	}

	shown := dir
	if shown == "" {
		shown = "."
	}
	target := ""
	if isDir(shown) {
		target = resolvePath(shown)
	}
	if target == "" {
		// A bare word that is neither a directory nor a known verb is almost
		// always a mistyped verb, so point at the list rather than only at the
		// missing path.
		l.errln("gitcito: no such directory: " + shown)
		if dir != "" && dir != "." && dir != ".." && !strings.Contains(dir, "/") {
			l.errln("If you meant a command, 'gitcito help verbs' lists them.")
		}
		return 1
	}

	// Each flag/value pair is one `--flag=value` token, never two argv entries:
	// a single token survives any reordering of argv between this process and
	// the running instance it is handed to.
	open := []string{"--open=" + target}
	if name != "" {
		open = append(open, "--name="+name)
	}
	if group != "" {
		open = append(open, "--group="+group)
	}
	if view != "" {
		open = append(open, "--view="+view)
	}
	if arg != "" {
		open = append(open, "--arg="+arg)
	}
	if line != "" {
		open = append(open, "--line="+line)
	}
	if waitFile != "" {
		open = append(open, "--edit="+editFile, "--wait="+waitFile)
	}

	if err := launchApp(open); err != nil {
		if errors.Is(err, errNoApp) {
			l.errln("gitcito: could not find the Gitcito application binary")
		}
		// A launch that never happens must not leave a sentinel behind for a
		// `git` that is about to be told the editor failed.
		if waitFile != "" {
			_ = os.Remove(waitFile)
		}
		return 1
	}

	if waitFile != "" {
		// Poll rather than block on a fifo: the app may already be running (the
		// launch returns immediately) or may still be starting, and a fifo would
		// deadlock if it never opened the write end. A deleted sentinel is the
		// single signal, so an app crash releases git rather than hanging it.
		for {
			if _, err := os.Lstat(waitFile); err != nil {
				break
			}
			time.Sleep(pollInterval)
		}
	}
	return 0
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// tmpDir is ${TMPDIR:-/tmp}, which is what the shim's mktemp used.
func tmpDir() string {
	if runtime.GOOS != "windows" {
		if d := os.Getenv("TMPDIR"); d != "" {
			return d
		}
		return "/tmp"
	}
	return os.TempDir()
}

// repoTop is `cd <dir> && git rev-parse --show-toplevel`, "" on failure.
func repoTop(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

var errNoApp = errors.New("no app binary")

// appBinary finds the Gitcito executable this CLI ships with: next to it in a
// build output directory or a macOS bundle's Contents/MacOS, resolved through
// the PATH symlink. On Linux a package may put the app on PATH as gitcito-app.
func appBinary() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		names := []string{"Gitcito", "gitcito"}
		if runtime.GOOS == "windows" {
			names = []string{"Gitcito.exe"}
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 && !sameFile(p, exe) {
				return p, nil
			}
		}
	}
	if p, err := exec.LookPath("gitcito-app"); err == nil {
		return p, nil
	}
	return "", errNoApp
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// macBundle is the .app this CLI lives in, or "".
func macBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	contents := filepath.Dir(filepath.Dir(exe))
	if filepath.Base(filepath.Dir(exe)) == "MacOS" && filepath.Base(contents) == "Contents" &&
		strings.HasSuffix(filepath.Dir(contents), ".app") {
		return filepath.Dir(contents)
	}
	return ""
}

// launchApp starts the app with the request, detached. A running instance
// takes the request over through the single-instance hand-off and the new
// process exits.
func launchApp(open []string) error {
	if runtime.GOOS == "darwin" {
		// `-n` forces a new process even when Gitcito is already running:
		// without it macOS only re-activates the running app and drops --args
		// entirely. The new process hands its argv to the running window and
		// quits.
		app := macBundle()
		if app == "" {
			app = "Gitcito"
		}
		cmd := exec.Command("open", append([]string{"-n", "-a", app, "--args"}, open...)...)
		return cmd.Run()
	}
	bin, err := appBinary()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, open...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
