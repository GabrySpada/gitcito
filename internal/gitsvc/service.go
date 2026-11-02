// Package gitsvc is the git service bound to the renderer: one typed method
// per gitService method of src/main/git.ts, each wrapped by the per-repo
// read/write lock (lock.go, classify.go) and the operation log (oplog.go).
//
// Renderer channels served: the `'git'` IPC dispatcher (one bound method per
// gitService method), plus the events `clone:progress` and `bisect:output`.
//
// Every exported method on *GitService is callable from the page (Wails
// binds them all), so:
//   - exported methods are git methods and nothing else — helpers, setters
//     and plumbing are unexported or live in internal/gitx;
//   - every exported method must be classified in classify.go (a test fails
//     otherwise);
//   - a method takes ctx first, then the TS positional arguments in order,
//     the repository path first.
//
// How a method is written:
//
//	func (s *GitService) Log(ctx context.Context, repoPath string, maxCount, skip int) ([]GraphCommit, error) {
//		return locked(s, ctx, "log", repoPath, func(ctx context.Context) ([]GraphCommit, error) {
//			…
//		})
//	}
//
// Methods with no repository argument (`version`) use lockedNoRepo; the
// app-level ones (`clone`, `init`, `remoteBranches`) use locked with their
// first argument — they are recognised by name and never take a lock.
package gitsvc

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/logbook"
)

// Emitter delivers an event to the renderer: `webContents.send(name, data)`
// in Electron, `app.Event.Emit` in Wails. Names stay the Electron channel
// names. An Emitter must tolerate being called after the window is gone (TS
// checked `sender.isDestroyed()`).
type Emitter interface {
	Emit(name string, data any)
}

// ActivityRecorder counts a successful operation in the activity analytics —
// recordEvent() of src/main/analytics.ts. Optional.
type ActivityRecorder interface {
	RecordEvent(event logbook.ActivityEvent)
}

// Option configures a GitService at construction.
type Option func(*GitService)

// WithActivityRecorder wires the analytics ledger.
func WithActivityRecorder(r ActivityRecorder) Option {
	return func(s *GitService) { s.activity = r }
}

// WithTokenFunc wires the active hosting profile's token lookup used for
// network operations (activeProfileToken in TS).
func WithTokenFunc(f gitx.TokenFunc) Option {
	return func(s *GitService) { s.token = f }
}

// GitService is the bound git facade. Construct it with New.
type GitService struct {
	emitter  Emitter
	log      *logbook.Logbook
	activity ActivityRecorder
	token    gitx.TokenFunc
	locks    lockRegistry
}

// New builds the service. emitter and log may be nil (tests); a nil emitter
// drops events, a nil logbook records nothing.
func New(emitter Emitter, log *logbook.Logbook, opts ...Option) *GitService {
	s := &GitService{emitter: emitter, log: log}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CloneProgress is CloneProgress in src/shared/types.ts, the payload of the
// `clone:progress` event.
type CloneProgress struct {
	// Stage is git's phase: 'counting' | 'compressing' | 'receiving' | 'resolving' | 'checking out' | …
	Stage string `json:"stage"`
	// Progress is 0–100 for the current stage.
	Progress  int `json:"progress"`
	Processed int `json:"processed"`
	Total     int `json:"total"`
}

// Event channel names, unchanged from Electron.
const (
	EventCloneProgress = "clone:progress"
	EventBisectOutput  = "bisect:output"
)

func (s *GitService) emit(name string, data any) {
	if s.emitter != nil {
		s.emitter.Emit(name, data)
	}
}

// cloneProgress is the callback the dispatcher appended to `clone`'s
// arguments in TS: it streams `git clone --progress` to the renderer.
func (s *GitService) cloneProgress() func(CloneProgress) {
	return func(p CloneProgress) { s.emit(EventCloneProgress, p) }
}

// bisectOutput is the same shape for a scripted bisect: it runs for minutes
// and its output is the only sign it is alive. gitx.RunOpts serializes its
// chunk callbacks, so chunks reach Emit in order.
func (s *GitService) bisectOutput() func(string) {
	return func(chunk string) { s.emit(EventBisectOutput, chunk) }
}

// CallError is what a dispatched method returns on failure: the message is
// the original error's, with credentials redacted and ANSI codes stripped (the
// renderer and the log see this text), and the original stays reachable via
// errors.Is / errors.As.
type CallError struct {
	Message string
	Cause   error
}

func (e *CallError) Error() string { return e.Message }

// Unwrap exposes the original error.
func (e *CallError) Unwrap() error { return e.Cause }

// call is the dispatcher body of registerGitHandlers() in git.ts, for one call.
//
//   - repoScoped: the first TS argument was a string repository path. False
//     for app-level methods and for methods with no arguments.
//   - The lock key is the repo path for every repo-scoped method (not just
//     logged ones); app-level ones run before (or without) a local repo, so
//     they take no lock. A method absent from readMethods takes the
//     exclusive lock.
//   - fn runs under withLockRetry; a panic inside it becomes an error.
//   - On success the activity event (if any) is counted and logged; on
//     failure the sanitized message is logged.
func (s *GitService) call(ctx context.Context, method string, repoScoped bool, repoPath string, fn func(context.Context) error, args []any) error {
	event := eventForCall(method, args)
	if IsAppLevelMethod(method) {
		repoScoped = false
	}
	logPath := ""
	if repoScoped {
		logPath = repoPath
	}

	if repoScoped {
		release, err := s.locks.lockFor(repoPath).Acquire(ctx, !IsReadMethod(method))
		if err != nil {
			return err // cancelled while queued: nothing ran, nothing to log
		}
		defer release()
	}

	err := gitx.WithLockRetry(ctx, func(ctx context.Context) error { return recoverCall(method, ctx, fn) })
	if err == nil {
		s.recordSuccess(event, logPath)
		return nil
	}
	// Sanitize at the boundary as well as in RunGit: Raw and any other path
	// can surface a URL a token was transiently injected into, and this is the
	// last point before the message reaches the log and the renderer — which
	// is also where hook output loses its colour codes.
	message := gitx.StripAnsi(gitx.RedactCredentials(err.Error()))
	s.recordFailure(event, logPath, message)
	return &CallError{Message: message, Cause: err}
}

// recoverCall runs fn, turning a panic into an error so nothing panics across
// the bridge.
func recoverCall(method string, ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{method: method, value: r, stack: debug.Stack()}
		}
	}()
	return fn(ctx)
}

// panicError is a recovered panic. Its message names the method and the panic
// value; the stack is kept for logs, never sent to the renderer.
type panicError struct {
	method string
	value  any
	stack  []byte
}

func (e *panicError) Error() string {
	return fmt.Sprintf("internal error in git method %s: %v", e.method, e.value)
}

// IsPanic reports whether err came from a recovered panic in a git method.
func IsPanic(err error) bool {
	var p *panicError
	return errors.As(err, &p)
}

// locked runs fn as the git method `method` on repoPath: lock, retry on git
// lock files, op log, panic recovery. args are the TS positional arguments,
// repoPath first; they are only consulted to pick the activity event
// (`commit`'s amend flag, `previewRef`'s mode) and may be omitted otherwise.
func locked[T any](s *GitService, ctx context.Context, method, repoPath string, fn func(context.Context) (T, error), args ...any) (T, error) {
	var out T
	err := s.call(ctx, method, true, repoPath, func(ctx context.Context) error {
		v, err := fn(ctx)
		if err == nil {
			out = v
		}
		return err
	}, withRepoArg(repoPath, args))
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// lockedErr is locked for methods that return nothing.
func lockedErr(s *GitService, ctx context.Context, method, repoPath string, fn func(context.Context) error, args ...any) error {
	return s.call(ctx, method, true, repoPath, fn, withRepoArg(repoPath, args))
}

// lockedNoRepo runs a method that takes no repository argument (`version`):
// no lock, and an empty repo path in the log.
func lockedNoRepo[T any](s *GitService, ctx context.Context, method string, fn func(context.Context) (T, error)) (T, error) {
	var out T
	err := s.call(ctx, method, false, "", func(ctx context.Context) error {
		v, err := fn(ctx)
		if err == nil {
			out = v
		}
		return err
	}, nil)
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// withRepoArg makes sure args[0] is the repo path, as it was in TS.
func withRepoArg(repoPath string, args []any) []any {
	if len(args) == 0 {
		return []any{repoPath}
	}
	return args
}
