package gitx

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultMaxBuffer is Node's execFile default `maxBuffer` (1 MiB per stream).
// runGit never raised it unless a caller asked, so a read that prints more
// than this fails in TS — and fails the same way here. Callers that expect
// big output pass their own MaxBuffer, as the TS call sites did.
const DefaultMaxBuffer = 1024 * 1024

// waitDelay bounds how long Wait waits for stdio to close after the process
// is gone: a grandchild that inherited the pipes (a hook's background job)
// must not hang the call forever.
const waitDelay = 5 * time.Second

// RunOpts tunes one process run. The zero value is execFile's defaults plus
// GIT_TERMINAL_PROMPT=0 — what runGit used.
type RunOpts struct {
	// Env holds "KEY=VALUE" overrides applied over the base environment —
	// `{ ...noPromptEnv(), ...extraEnv }` in TS.
	Env []string
	// PlainEnv makes the base environment the process environment as is,
	// without GIT_TERMINAL_PROMPT=0: TS call sites that passed no `env`
	// (pexecFile without options, spawn without env, simple-git).
	PlainEnv bool
	// Stdin is written to the child's stdin, which is then closed. Nil means
	// no stdin at all (/dev/null).
	Stdin []byte
	// MaxBuffer caps stdout and stderr separately, like Node: 0 means
	// DefaultMaxBuffer, negative means unlimited (spawn-based TS helpers).
	MaxBuffer int
	// Timeout kills the process (group) after this long; 0 = no timeout.
	Timeout time.Duration
	// OnStdout / OnStderr receive output chunks as they arrive — the `data`
	// listeners of a spawned child. Calls are serialized across both streams,
	// in arrival order, so one collector can take both safely.
	OnStdout func(chunk []byte)
	OnStderr func(chunk []byte)
	// AllowUnsafe lists simple-git unsafe-operation categories (e.g.
	// "allowUnsafeEditor") that Raw lets through. Ignored by every other runner.
	AllowUnsafe []string

	// prepare lets platform code adjust the command before it starts (the
	// Windows shell needs a verbatim command line).
	prepare func(*exec.Cmd)
}

// Result is what a finished process printed. Stdout/Stderr are raw bytes in a
// string — not decoded, so binary blobs survive.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ErrMaxBuffer is the cause of an ExecError whose output overflowed MaxBuffer.
var ErrMaxBuffer = errors.New("maxBuffer length exceeded")

// ExecError is a failed run, shaped like the error Node's execFile rejects
// with: its message is byte-identical to Node's, and the captured output and
// exit status ride along (`err.stdout`, `err.stderr`, `err.code` in TS).
type ExecError struct {
	// Cmd is the command line as Node prints it: file and args joined by spaces.
	Cmd string
	// Code is the exit code; -1 when the process could not start or was
	// killed by a signal (Node's `code` was a string or null there).
	Code int
	// Signal names the signal that killed the process ("" if none).
	Signal string
	// Killed is true when this package killed the process (timeout, context,
	// output overflow).
	Killed bool
	// Stdout and Stderr are whatever was captured (truncated at MaxBuffer).
	Stdout string
	Stderr string
	// Err is the underlying cause: a start error, ErrMaxBuffer, or the
	// context error that killed the process. Nil for a plain non-zero exit.
	Err error

	msg        string
	notStarted bool
}

func (e *ExecError) Error() string { return e.msg }

// Unwrap exposes the cause, so errors.Is(err, context.Canceled) works.
func (e *ExecError) Unwrap() error { return e.Err }

// Started reports whether the process ran at all (false: not found, bad cwd).
func (e *ExecError) Started() bool { return !e.notStarted }

// ExitCode returns the exit status carried by err when the process ran to a
// normal exit: (code, true). Anything else — nil, a start failure, a kill —
// is (0, false). This is TS's `typeof e.code === 'number'` test.
func ExitCode(err error) (int, bool) {
	var ee *ExecError
	if !errors.As(err, &ee) || ee.notStarted || ee.Signal != "" || ee.Killed {
		return 0, false
	}
	return ee.Code, true
}

// limitedBuffer collects one stream up to max bytes and reports overflow once.
type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow func()
	over     bool
	onChunk  func([]byte)
	mu       *sync.Mutex
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.over {
		return len(p), nil // keep draining so the child never blocks on a full pipe
	}
	chunk := p
	if b.max >= 0 && b.buf.Len()+len(p) > b.max {
		chunk = p[:b.max-b.buf.Len()]
		b.over = true
	}
	b.buf.Write(chunk)
	if b.onChunk != nil && len(chunk) > 0 {
		b.onChunk(append([]byte(nil), chunk...))
	}
	if b.over {
		b.overflow()
	}
	return len(p), nil
}

// Exec runs name with args in dir and waits for it — the primitive under every
// runner, equivalent to Node's execFile (or a spawn whose output is
// collected). A non-zero exit, a signal, an overflow or a start failure
// returns an *ExecError alongside the partial Result.
func Exec(ctx context.Context, dir, name string, args []string, opts RunOpts) (Result, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	runCtx, kill := context.WithCancelCause(ctx)
	defer kill(nil)

	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = dir
	base := NoPromptEnv()
	if opts.PlainEnv {
		base = os.Environ()
	}
	cmd.Env = MergeEnv(base, opts.Env...)
	if opts.Stdin != nil {
		cmd.Stdin = bytes.NewReader(opts.Stdin)
	}
	cmd.WaitDelay = waitDelay
	configureProcess(cmd)
	if opts.prepare != nil {
		opts.prepare(cmd)
	}

	max := opts.MaxBuffer
	if max == 0 {
		max = DefaultMaxBuffer
	}
	var mu sync.Mutex
	overflowed := ""
	stdout := &limitedBuffer{max: max, onChunk: opts.OnStdout, mu: &mu}
	stderr := &limitedBuffer{max: max, onChunk: opts.OnStderr, mu: &mu}
	stdout.overflow = func() {
		if overflowed == "" {
			overflowed = "stdout"
			kill(ErrMaxBuffer)
		}
	}
	stderr.overflow = func() {
		if overflowed == "" {
			overflowed = "stderr"
			kill(ErrMaxBuffer)
		}
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()

	mu.Lock()
	res := Result{Stdout: stdout.buf.String(), Stderr: stderr.buf.String()}
	over := overflowed
	mu.Unlock()
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	cmdline := name
	if len(args) > 0 {
		cmdline += " " + strings.Join(args, " ")
	}
	ee := &ExecError{Cmd: cmdline, Code: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr}

	switch {
	case cmd.ProcessState == nil && runErr != nil:
		// Never started. Node reports a missing binary and a missing cwd alike
		// as `spawn <file> ENOENT`.
		ee.notStarted = true
		ee.Code = -1
		ee.Err = runErr
		if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, fs.ErrNotExist) {
			ee.msg = "spawn " + name + " ENOENT"
		} else {
			ee.msg = "spawn " + name + " " + runErr.Error()
		}
		return res, ee
	case over != "":
		ee.Killed = true
		ee.Err = ErrMaxBuffer
		ee.Code = -1
		ee.Signal = signalName(cmd.ProcessState)
		ee.msg = over + " maxBuffer length exceeded"
		return res, ee
	case runCtx.Err() != nil && (runErr != nil || !cmd.ProcessState.Success()):
		ee.Killed = true
		ee.Err = context.Cause(runCtx)
		ee.Code = -1
		ee.Signal = signalName(cmd.ProcessState)
		ee.msg = "Command failed: " + cmdline + "\n" + res.Stderr
		return res, ee
	case runErr != nil:
		ee.Signal = signalName(cmd.ProcessState)
		if ee.Signal != "" {
			ee.Code = -1
		}
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			// Wait-side failure (stdio copy, WaitDelay) on an otherwise exited
			// process: keep it as the cause so it is not lost.
			ee.Err = runErr
		}
		ee.msg = "Command failed: " + cmdline + "\n" + res.Stderr
		return res, ee
	}
	return res, nil
}

// Run is `pexecFile('git', ['-C', repoPath, ...args], opts)`: git in repoPath,
// failure returned as the raw *ExecError (whose Error() is Node's "Command
// failed: …" text). An empty repoPath drops `-C` — the TS call sites that ran
// git with no repository (ls-remote).
func Run(ctx context.Context, repoPath string, args []string, opts RunOpts) (Result, error) {
	full := args
	if repoPath != "" {
		full = append([]string{"-C", repoPath}, args...)
	}
	return Exec(ctx, "", "git", full, opts)
}

// GitError is a git failure whose message has already been shaped by the
// runner that produced it. Exec carries the process details when there are
// any, so a caller can still read the exit code (errors.As / ExitCode).
type GitError struct {
	Message string
	Exec    *ExecError
}

func (e *GitError) Error() string { return e.Message }

// Unwrap exposes the process failure, when there was one.
func (e *GitError) Unwrap() error {
	if e.Exec == nil {
		return nil
	}
	return e.Exec
}

// RunGit runs a git command non-interactively, surfacing stderr as the
// returned error's message — runGit() in git.ts.
//
// Prefer this over Raw on any read a refresh performs. (In TS, simple-git's
// completion-detection plugin slept a flat 50ms after a command that wrote
// nothing, and the clean repo — where reads return nothing — paid it per call.)
//
// The message is `(stderr || nodeMessage || 'git command failed').trim()`
// with credentials redacted, byte for byte.
func RunGit(ctx context.Context, repoPath string, args []string, opts RunOpts) (string, error) {
	res, err := Run(ctx, repoPath, args, opts)
	if err != nil {
		return "", gitErrorFrom(err, "git command failed")
	}
	return res.Stdout, nil
}

// gitErrorFrom is the `(e.stderr || e.message || fallback).trim()` + redact
// shape shared by runGit and remoteBranches.
func gitErrorFrom(err error, fallback string) error {
	var ee *ExecError
	msg := ""
	if errors.As(err, &ee) {
		msg = ee.Stderr
		if msg == "" {
			msg = ee.Error()
		}
	} else {
		msg = err.Error()
	}
	if msg == "" {
		msg = fallback
	}
	return &GitError{Message: RedactCredentials(TrimJS(msg)), Exec: ee}
}

// RunGitFallback is RunGit with a different last-resort message — the TS
// sites that inlined runGit's catch with their own text, e.g. remoteBranches'
// 'git ls-remote failed'.
func RunGitFallback(ctx context.Context, repoPath string, args []string, opts RunOpts, fallback string) (string, error) {
	res, err := Run(ctx, repoPath, args, opts)
	if err != nil {
		return "", gitErrorFrom(err, fallback)
	}
	return res.Stdout, nil
}

// errSimpleGitNoDir is simple-git's GitConstructError for a missing baseDir.
const errSimpleGitNoDir = "Cannot use simple-git on a directory that does not exist"

// Raw is `simpleGit(repoPath).raw(args)` — the `gitFor(repo).raw(...)` calls
// that make up most of git.ts. Its rules differ from RunGit's in ways callers
// depend on, so they are kept:
//
//   - cwd is repoPath (no `-C`); a repoPath that is not a directory fails with
//     simple-git's "Cannot use simple-git on a directory that does not exist".
//     An empty repoPath runs in the process cwd, like simpleGit().
//   - the environment is the process env, untouched — no GIT_TERMINAL_PROMPT.
//   - an error needs BOTH a non-zero exit AND something on stderr. A non-zero
//     exit with a silent stderr is a success returning stdout: this is why
//     `config --get <missing>` reads as ” and `merge-base --is-ancestor`
//     never throws through Raw.
//   - the error message is stdout followed by stderr, untrimmed.
//   - no output cap.
//   - simple-git's unsafe-operation guard runs first (see CheckUnsafe); pass
//     opts.AllowUnsafe for the categories a call site enabled.
//
// Only AllowUnsafe, Timeout, OnStdout and OnStderr are honoured from opts.
func Raw(ctx context.Context, repoPath string, args []string, opts RunOpts) (string, error) {
	if repoPath != "" {
		if st, err := os.Stat(repoPath); err != nil || !st.IsDir() {
			return "", &GitError{Message: errSimpleGitNoDir}
		}
	}
	if err := CheckUnsafe(args, opts.AllowUnsafe...); err != nil {
		return "", err
	}
	res, err := Exec(ctx, repoPath, "git", args, RunOpts{
		PlainEnv:  true,
		MaxBuffer: -1,
		Timeout:   opts.Timeout,
		OnStdout:  opts.OnStdout,
		OnStderr:  opts.OnStderr,
	})
	if err == nil {
		return res.Stdout, nil
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		return "", err
	}
	if ee.notStarted {
		// simple-git pushes the spawn error's stack onto stderr; the first
		// line of that stack is all that can be reproduced.
		return "", &GitError{Message: res.Stdout + "Error: " + ee.msg, Exec: ee}
	}
	if ee.Killed {
		// No TS analogue (simple-git calls were never cancelled): report the
		// kill as such rather than inventing a git message.
		return "", ee
	}
	if res.Stderr == "" {
		return res.Stdout, nil
	}
	return "", &GitError{Message: res.Stdout + res.Stderr, Exec: ee}
}

// WithStdin runs git with input on stdin and hands back whatever it wrote to
// stdout — gitWithStdin() in git.ts. A non-zero exit is not an error here:
// `merge-tree --stdin` aborts the batch on the first fatal merge, and the
// records it already emitted are still good. No output cap.
func WithStdin(ctx context.Context, repoPath string, args []string, input string) string {
	res, _ := Run(ctx, repoPath, args, RunOpts{Stdin: []byte(input), MaxBuffer: -1})
	return res.Stdout
}

// ReadBlob reads `git show <spec>` as raw bytes (no encoding loss) —
// readBlobBuffer() in git.ts. The error message is git's stderr as is, or
// "git show exited <code>" when stderr was empty. No output cap.
// opts.PlainEnv selects readFileDataUrl's variant, which spawned without env.
func ReadBlob(ctx context.Context, repoPath, spec string, opts RunOpts) ([]byte, error) {
	opts.MaxBuffer = -1
	res, err := Run(ctx, repoPath, []string{"show", spec}, opts)
	if err == nil {
		return []byte(res.Stdout), nil
	}
	var ee *ExecError
	if errors.As(err, &ee) && ee.notStarted {
		return nil, ee // the child 'error' event rejected with the spawn error
	}
	if res.Stderr != "" {
		return nil, &GitError{Message: res.Stderr, Exec: ee}
	}
	return nil, &GitError{Message: "git show exited " + exitLabel(ee), Exec: ee}
}

// exitLabel renders a close-event code the way `${code}` did: the number, or
// "null" when a signal ended the process.
func exitLabel(ee *ExecError) string {
	if ee == nil {
		return "0"
	}
	if ee.Signal != "" || ee.Killed {
		return "null"
	}
	return strconv.Itoa(ee.Code)
}
