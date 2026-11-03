// Package terminal ports src/main/terminal.ts: the integrated terminal's
// shells, one pseudo-terminal per panel, streamed to the window as
// `term:data:<id>` / `term:exit:<id>`. Its process layer (StartPTY, StartPipe)
// is shared with internal/launch.
package terminal

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Emitter sends a named event to the window.
type Emitter interface {
	Emit(name string, data any)
}

// DefaultShell is the user's shell: $SHELL (zsh when unset) on Unix,
// %COMSPEC% (PowerShell when unset) on Windows.
func DefaultShell() string {
	if runtime.GOOS == "windows" {
		if s := os.Getenv("COMSPEC"); s != "" {
			return s
		}
		return "powershell.exe"
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/zsh"
}

// shellArgs make the shell a *login* shell.
//
// Without them zsh/bash skip ~/.zprofile, ~/.zlogin and ~/.bash_profile — which
// is where version managers and `brew shellenv` put themselves on PATH. The
// symptom is a tool that runs fine in Terminal.app (always a login shell)
// failing here with `command not found`.
func shellArgs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{"-l"}
}

// Options configure a Manager. The zero value is production behaviour.
type Options struct {
	// Shell overrides the program and arguments a new terminal runs (tests).
	Shell func() (name string, args []string)
	// ForcePipe skips the pseudo-terminal and uses the basic fallback (tests).
	ForcePipe bool
}

// Manager owns the terminal sessions.
type Manager struct {
	emit Emitter
	opts Options

	mu       sync.Mutex
	nextID   int
	sessions map[int]*session
}

// NewManager builds a session manager that emits on e.
func NewManager(e Emitter, opts Options) *Manager {
	return &Manager{emit: e, opts: opts, nextID: 1, sessions: map[int]*session{}}
}

type session struct {
	mu   sync.Mutex
	proc *Proc
	// Fallback line editor state (nil proc.pty only).
	fallback bool
	buffer   []rune
	shell    string
	send     func(string)
}

func (m *Manager) shell() (string, []string) {
	if m.opts.Shell != nil {
		return m.opts.Shell()
	}
	return DefaultShell(), shellArgs()
}

// Create starts a shell in cwd and returns its id. A pseudo-terminal is tried
// first; when that fails the shell runs on pipes behind a minimal line editor.
func (m *Manager) Create(cwd string, cols, rows int) (int, error) {
	m.mu.Lock()
	id := m.nextID
	m.nextID++
	s := &session{}
	m.sessions[id] = s
	m.mu.Unlock()

	dataEvent := "term:data:" + strconv.Itoa(id)
	exitEvent := "term:exit:" + strconv.Itoa(id)
	onExit := func(int) {
		m.remove(id, s)
		m.emit.Emit(exitEvent, nil)
	}
	name, args := m.shell()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !m.opts.ForcePipe {
		// TERM is often unset when the app is launched from Finder/Dock.
		p, err := StartPTY(Spec{
			Name: name, Args: args, Dir: cwd, Cols: cols, Rows: rows,
			Env:    gitx.MergeEnv(os.Environ(), "TERM=xterm-256color"),
			OnData: func(d string) { m.emit.Emit(dataEvent, d) },
			OnExit: onExit,
		})
		if err == nil {
			s.proc = p
			return id, nil
		}
	}
	send := func(text string) { m.emit.Emit(dataEvent, text) }
	p, err := StartPipe(Spec{
		Name: name, Args: args, Dir: cwd,
		Env:    gitx.MergeEnv(os.Environ(), "TERM=dumb"),
		OnData: send,
		OnExit: onExit,
		CRLF:   true,
	})
	if err != nil {
		m.remove(id, s)
		return 0, err
	}
	s.proc, s.fallback, s.shell, s.send = p, true, name, send
	send("\x1b[90m(basic shell mode — no pseudo-terminal available on this system)\x1b[0m\r\n")
	send("\x1b[36m" + cwd + "\x1b[0m $ ")
	return id, nil
}

func (m *Manager) remove(id int, s *session) {
	m.mu.Lock()
	if m.sessions[id] == s {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
}

func (m *Manager) get(id int) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// ProcName is the foreground process of a session, or "".
func (m *Manager) ProcName(id int) string {
	s := m.get(id)
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return ""
	}
	if s.fallback {
		return filepath.Base(strings.ReplaceAll(s.shell, "\\", "/"))
	}
	return s.proc.ForegroundName()
}

// Input writes keystrokes to a session.
func (m *Manager) Input(id int, data string) {
	s := m.get(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return
	}
	if !s.fallback {
		_ = s.proc.Write(data) // session dying or already dead
		return
	}
	// Minimal line editor: echo locally, send full lines to the shell's stdin.
	for _, ch := range data {
		switch {
		case ch == '\r' || ch == '\n':
			s.send("\r\n")
			_ = s.proc.Write(string(s.buffer) + "\n")
			s.buffer = s.buffer[:0]
		case ch == '\x7f' || ch == '\b':
			if len(s.buffer) > 0 {
				s.buffer = s.buffer[:len(s.buffer)-1]
				s.send("\b \b")
			}
		case ch == '\x03':
			s.buffer = s.buffer[:0]
			s.send("^C\r\n$ ")
		case ch >= ' ' || ch == '\t':
			s.buffer = append(s.buffer, ch)
			s.send(string(ch))
		}
	}
}

// Resize changes a session's terminal size; errors from a dying pty are dropped.
func (m *Manager) Resize(id, cols, rows int) {
	s := m.get(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != nil {
		_ = s.proc.Resize(cols, rows)
	}
}

// Kill ends a session and forgets it.
func (m *Manager) Kill(id int) {
	s := m.get(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.proc != nil {
		s.proc.Kill()
	}
	s.mu.Unlock()
	m.remove(id, s)
}

// KillAll ends every session (app shutdown).
func (m *Manager) KillAll() {
	m.mu.Lock()
	ids := make([]int, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Kill(id)
	}
}
