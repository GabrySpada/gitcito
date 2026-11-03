package analyze

// Service serves the renderer's Problems channels:
//   analyze:run, analyze:detect, analyze:cancel
// It mirrors registerAnalyzeHandlers in src/main/analyze.ts. Every exported
// method is callable from the page, so nothing else is exported here.

import (
	"context"
	"fmt"
)

// Service is the bound facade over the Runner.
type Service struct {
	r *Runner
}

// NewService builds the facade.
func NewService() *Service { return &Service{r: NewRunner()} }

func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("analyze: internal error: %v", r)
	}
}

// Run runs the project's analyzers and returns their merged problems.
// channel: analyze:run
func (s *Service) Run(ctx context.Context, repoPath string) (res AnalyzeResult, err error) {
	defer recoverErr(&err)
	return s.r.Analyze(ctx, repoPath), nil
}

// Detect names the analyzers the repo asks for, without running them.
// channel: analyze:detect
func (s *Service) Detect(ctx context.Context, repoPath string) (ids []string, err error) {
	defer recoverErr(&err)
	return DetectAnalyzers(repoPath), nil
}

// Cancel stops the repo's running sweep (fire-and-forget).
// channel: analyze:cancel
func (s *Service) Cancel(repoPath string) { s.r.Cancel(repoPath) }

// ServiceShutdown is the Wails lifecycle hook (not bound).
func (s *Service) ServiceShutdown() error {
	s.r.CancelAll()
	return nil
}
