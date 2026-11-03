package terminal

// Service serves the renderer's integrated-terminal channels:
//   term:create, term:process, term:input, term:resize, term:kill
// and emits term:data:<id> (string) and term:exit:<id> (no payload).
// It mirrors registerTerminalHandlers in src/main/terminal.ts. Every exported
// method is callable from the page, so nothing else is exported here.

import (
	"context"
	"fmt"
)

// Service is the bound facade over the session manager.
type Service struct {
	m *Manager
}

// NewService builds the facade; main.go passes app.Emitter{}.
func NewService(e Emitter) *Service {
	return &Service{m: NewManager(e, Options{})}
}

// Create starts a login shell in cwd on a cols×rows terminal and returns the
// session id that names its events. The session outlives the call, so it is
// deliberately not tied to ctx.
// channel: term:create
// channel: term:data:${id}
// channel: term:exit:${id}
func (s *Service) Create(ctx context.Context, cwd string, cols, rows int) (id int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("terminal: internal error: %v", r)
		}
	}()
	return s.m.Create(cwd, cols, rows)
}

// ProcName is the name of the session's foreground process ("" when gone).
// channel: term:process
func (s *Service) ProcName(id int) string { return s.m.ProcName(id) }

// Input sends keystrokes (fire-and-forget).
// channel: term:input
func (s *Service) Input(id int, data string) { s.m.Input(id, data) }

// Resize resizes the session's terminal (fire-and-forget).
// channel: term:resize
func (s *Service) Resize(id, cols, rows int) { s.m.Resize(id, cols, rows) }

// Kill ends the session and its process group (fire-and-forget).
// channel: term:kill
func (s *Service) Kill(id int) { s.m.Kill(id) }

// ServiceShutdown is the Wails lifecycle hook (not bound): no shell outlives
// the app.
func (s *Service) ServiceShutdown() error {
	s.m.KillAll()
	return nil
}
