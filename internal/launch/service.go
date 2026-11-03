package launch

// Service serves the renderer's launch channels:
//   launch:discover, launch:run, launch:runTasks,
//   launch:attach, launch:input, launch:resize, launch:signal, launch:stop
// and emits launch:data:<id> (string), launch:exit:<id> (number) and
// launch:devtools:<id> ({ url, name }). It mirrors registerLaunchHandlers in
// src/main/launch.ts. Every exported method is callable from the page, so
// nothing else is exported here.

import (
	"context"
	"fmt"
)

// Service is the bound facade over the launch manager.
type Service struct {
	m *Manager
}

// NewService builds the facade. main.go passes app.Emitter{} and an OpenURL
// that opens http(s) URLs in the system browser (serverReadyAction).
func NewService(e Emitter, openURL func(url string) error) *Service {
	return &Service{m: NewManager(e, Options{OpenURL: openURL})}
}

func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("launch: internal error: %v", r)
	}
}

// Discover lists the `.vscode/launch.json` groups under repoPath.
// channel: launch:discover
func (s *Service) Discover(ctx context.Context, repoPath string) (groups []LaunchGroup, err error) {
	defer recoverErr(&err)
	return s.m.Discover(repoPath), nil
}

// Run starts a launch config in a new session: `{ id }`, or `{ error }` when
// the config cannot run here. The session outlives the call, so it is not tied
// to ctx.
// channel: launch:run
// channel: launch:data:${id}
// channel: launch:devtools:${id}
// channel: launch:exit:${id}
func (s *Service) Run(ctx context.Context, payload RunPayload) (res RunResult, err error) {
	defer recoverErr(&err)
	return s.m.Run(payload), nil
}

// RunTasks runs a plain task chain in a new session.
// channel: launch:runTasks
func (s *Service) RunTasks(ctx context.Context, payload RunTasksPayload) (res RunResult, err error) {
	defer recoverErr(&err)
	return s.m.RunTasks(payload), nil
}

// Attach replays what the session printed before its terminal mounted
// (fire-and-forget).
// channel: launch:attach
func (s *Service) Attach(id int) { s.m.Attach(id) }

// Input writes to the session (fire-and-forget).
// channel: launch:input
func (s *Service) Input(id int, data string) { s.m.Input(id, data) }

// Resize resizes the session's terminal (fire-and-forget).
// channel: launch:resize
func (s *Service) Resize(id, cols, rows int) { s.m.Resize(id, cols, rows) }

// Signal pauses ("pause") or resumes ("resume") the session's process group
// (fire-and-forget; no-op on Windows).
// channel: launch:signal
func (s *Service) Signal(id int, action string) { s.m.Signal(id, action) }

// Stop kills the session's process group and forgets it (fire-and-forget).
// channel: launch:stop
func (s *Service) Stop(id int) { s.m.Stop(id) }

// ServiceShutdown is the Wails lifecycle hook (not bound): no launched program
// outlives the app.
func (s *Service) ServiceShutdown() error {
	s.m.StopAll()
	return nil
}
