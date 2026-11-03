package todoscan

// Service serves the renderer's TODO-scan channels:
//   todoscan:run, todoscan:cancel
// It mirrors registerTodoScanHandlers in src/main/todoScan.ts. Every exported
// method is callable from the page, so nothing else is exported here.

import (
	"context"
	"fmt"
)

// Service is the bound facade over the Scanner.
type Service struct {
	s *Scanner
}

// NewService builds the facade.
func NewService() *Service { return &Service{s: NewScanner()} }

// Run scans the repository for TODO-style markers.
// channel: todoscan:run
func (s *Service) Run(ctx context.Context, repoPath string) (res TodoScanResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("todoscan: internal error: %v", r)
		}
	}()
	return s.s.Scan(ctx, repoPath), nil
}

// Cancel stops the repo's running scan (fire-and-forget).
// channel: todoscan:cancel
func (s *Service) Cancel(repoPath string) { s.s.Cancel(repoPath) }

// ServiceShutdown is the Wails lifecycle hook (not bound).
func (s *Service) ServiceShutdown() error {
	s.s.CancelAll()
	return nil
}
