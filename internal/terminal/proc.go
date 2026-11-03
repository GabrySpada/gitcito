package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	gopty "github.com/aymanbagabas/go-pty"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Spec describes a process to start attached to a terminal (or, in the
// fallback, to a pair of pipes).
type Spec struct {
	// Name is the program; Args its arguments (without argv[0]).
	Name string
	Args []string
	// Dir is the working directory; Env the complete environment.
	Dir string
	Env []string
	// Cols and Rows size the pseudo-terminal; ignored by the pipe fallback.
	Cols, Rows int
	// OnData receives output in arrival order, always from one goroutine at a
	// time and never after OnExit.
	OnData func(data string)
	// OnExit runs exactly once, after the last OnData, with the exit code — 0
	// when a signal ended the process, as node-pty and `code ?? 0` reported.
	OnExit func(code int)
	// CRLF turns every bare "\n" of a chunk into "\r\n": a pipe has no line
	// discipline to do it, and xterm needs the carriage return.
	CRLF bool
}

// exitDrain is how long the exit waits for the output stream to end on its
// own once the process is gone. A background job that inherited the terminal
// can keep it open forever; node-pty gave the socket 200ms, then destroyed it.
const exitDrain = 200 * time.Millisecond

// killGrace is how long a polite kill (SIGHUP/SIGTERM) gets before the whole
// group is killed outright.
const killGrace = 2 * time.Second

// Proc is a running process with its output pump. Its methods are safe for
// concurrent use, and every one of them tolerates a process that already died.
type Proc struct {
	mu    sync.Mutex
	pty   gopty.Pty      // nil in pipe mode
	stdin io.WriteCloser // pipe mode only
	pid   int
	file  string

	outMu   sync.Mutex // serializes OnData/OnExit and guards dropped
	dropped bool

	done chan struct{} // closed after OnExit returned
}

// Pid is the process id (the session/group leader).
func (p *Proc) Pid() int { return p.pid }

// Done is closed once the process exited and OnExit returned.
func (p *Proc) Done() <-chan struct{} { return p.done }

// IsPTY reports whether the process runs on a pseudo-terminal.
func (p *Proc) IsPTY() bool { return p.pty != nil }

// Write sends input to the process. Errors (a dying session) are swallowed by
// callers, as the TS did.
func (p *Proc) Write(data string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pty != nil {
		_, err := p.pty.Write([]byte(data))
		return err
	}
	if p.stdin == nil {
		return errors.New("no stdin")
	}
	_, err := io.WriteString(p.stdin, data)
	return err
}

// Resize changes the terminal size. The pty can go invalid between the exit
// and this call (or after a sleep/wake cycle); node-pty's resize threw ENOTTY
// synchronously there and would have crashed main, so non-positive sizes are
// ignored and errors returned for the caller to drop.
func (p *Proc) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || p.pty == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return errors.New("exited")
	default:
	}
	return p.pty.Resize(cols, rows)
}

// Kill ends the process and everything in its group: SIGHUP for a terminal
// (node-pty's default, which an interactive shell forwards to its jobs),
// SIGTERM for a pipe child (child.kill()), then SIGKILL for whatever is still
// alive after a grace period.
func (p *Proc) Kill() {
	select {
	case <-p.done:
		return
	default:
	}
	killTree(p.pid, p.pty != nil)
	go func() {
		select {
		case <-p.done:
		case <-time.After(killGrace):
			forceKillTree(p.pid)
		}
	}()
}

// Signal pauses (SIGSTOP) or resumes (SIGCONT) the process group. No-op on
// Windows, as in TS.
func (p *Proc) Signal(pause bool) {
	select {
	case <-p.done:
		return
	default:
	}
	stopGroup(p.pid, pause)
}

// ForegroundName is node-pty's `process` getter: the name of the process in
// the terminal's foreground (zsh, vim, claude …), falling back to the file the
// session was started with.
func (p *Proc) ForegroundName() string {
	if p.pty == nil {
		return p.file
	}
	if name := foregroundName(p.pty); name != "" {
		return name
	}
	return p.file
}

// emit delivers one chunk unless the exit already went out.
func (p *Proc) emit(spec *Spec, data string) {
	if data == "" || spec.OnData == nil {
		return
	}
	if spec.CRLF {
		data = toCRLF(data)
	}
	p.outMu.Lock()
	defer p.outMu.Unlock()
	if !p.dropped {
		spec.OnData(data)
	}
}

func (p *Proc) finish(spec *Spec, code int) {
	p.outMu.Lock()
	p.dropped = true
	if spec.OnExit != nil {
		spec.OnExit(code)
	}
	p.outMu.Unlock()
	close(p.done)
}

// pump reads r until it fails, decoding UTF-8 without splitting characters.
func (p *Proc) pump(spec *Spec, r io.Reader, done chan<- struct{}) {
	defer close(done)
	var dec shared.UTF8Stream
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.emit(spec, dec.Decode(buf[:n]))
		}
		if err != nil {
			break
		}
	}
	p.emit(spec, dec.Flush())
}

// StartPTY starts spec on a new pseudo-terminal (creack/pty on Unix, ConPTY on
// Windows). The child is a session leader with the terminal as its
// controlling tty, so its pid is also its process group.
func StartPTY(spec Spec) (*Proc, error) {
	pt, err := gopty.New()
	if err != nil {
		return nil, err
	}
	if spec.Cols > 0 && spec.Rows > 0 {
		_ = pt.Resize(spec.Cols, spec.Rows)
	}
	c := pt.Command(spec.Name, spec.Args...)
	c.Dir = spec.Dir
	c.Env = spec.Env
	if err := c.Start(); err != nil {
		_ = pt.Close()
		return nil, err
	}
	// The child has its own handle on the tty; ours would keep the master from
	// ever seeing EOF once the child is gone.
	if up, ok := pt.(gopty.UnixPty); ok {
		_ = up.Slave().Close()
	}
	p := &Proc{pty: pt, pid: c.Process.Pid, file: spec.Name, done: make(chan struct{})}
	readerDone := make(chan struct{})
	go p.pump(&spec, pt, readerDone)
	go func() {
		werr := c.Wait()
		code := exitCode(c.ProcessState, werr)
		select {
		case <-readerDone:
		case <-time.After(exitDrain):
		}
		p.mu.Lock()
		_ = pt.Close()
		p.mu.Unlock()
		select {
		case <-readerDone:
		case <-time.After(exitDrain):
		}
		p.finish(&spec, code)
	}()
	return p, nil
}

// StartPipe starts spec as a plain child process with stdout and stderr on one
// pipe — the "no node-pty" fallback. One pipe for both streams keeps their
// relative order, which two Node `data` listeners only approximated.
func StartPipe(spec Spec) (*Proc, error) {
	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	configureGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, err
	}
	_ = w.Close()
	p := &Proc{stdin: stdin, pid: cmd.Process.Pid, file: spec.Name, done: make(chan struct{})}
	readerDone := make(chan struct{})
	go p.pump(&spec, r, readerDone)
	go func() {
		werr := cmd.Wait()
		code := exitCode(cmd.ProcessState, werr)
		select {
		case <-readerDone:
		case <-time.After(exitDrain):
			_ = r.Close()
			<-readerDone
		}
		p.finish(&spec, code)
		_ = r.Close()
	}()
	return p, nil
}

// exitCode is node-pty's exitCode / child_process's `code ?? 0`: a signal
// death reports 0.
func exitCode(ps *os.ProcessState, err error) int {
	if ps == nil {
		return 0
	}
	if c := ps.ExitCode(); c >= 0 {
		return c
	}
	return 0
}

// toCRLF is `text.replace(/(?<!\r)\n/g, '\r\n')`, applied per chunk as TS did.
func toCRLF(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && (i == 0 || s[i-1] != '\r') {
			b.WriteString("\r\n")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
