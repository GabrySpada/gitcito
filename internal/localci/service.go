package localci

// Service serves the renderer's local-CI channels:
//   localci:status, localci:workflows, localci:run, localci:cancel,
//   localci:record, localci:verdicts, localci:runAt, localci:resolveRange,
//   localci:sweep
// and emits localci:data ({ repoPath, sha?, chunk }) and
// localci:sweep-progress. It mirrors registerLocalCiHandlers in
// src/main/localCi.ts. Every exported method is callable from the page, so
// nothing else is exported here.

import (
	"context"
	"fmt"
)

// Service is the bound facade over the Manager.
type Service struct {
	m *Manager
}

// NewService builds the facade; main.go passes app.Emitter{}.
func NewService(e Emitter) *Service { return &Service{m: NewManager(e)} }

func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("localci: internal error: %v", r)
	}
}

// Status reports act's version (null when missing) and whether Docker answers.
// channel: localci:status
func (s *Service) Status(ctx context.Context) (st LocalCiStatus, err error) {
	defer recoverErr(&err)
	return s.m.Status(ctx), nil
}

// Workflows lists the repo's workflow files with their names.
// channel: localci:workflows
func (s *Service) Workflows(ctx context.Context, repoPath string) (w []LocalCiWorkflow, err error) {
	defer recoverErr(&err)
	return s.m.Workflows(repoPath), nil
}

// Run runs one workflow in the working tree; the exit code, or null when
// cancelled.
// channel: localci:run
// channel: localci:data
func (s *Service) Run(ctx context.Context, repoPath, workflowFile string) (exit *int, err error) {
	defer recoverErr(&err)
	return s.m.Run(ctx, repoPath, workflowFile)
}

// Cancel stops the repo's run and sweep.
// channel: localci:cancel
func (s *Service) Cancel(repoPath string) { s.m.Cancel(repoPath) }

// Record pins a verdict to HEAD when the tree is clean.
// channel: localci:record
func (s *Service) Record(ctx context.Context, repoPath, workflowFile string, ok bool) (res RecordResult, err error) {
	defer recoverErr(&err)
	return s.m.Record(ctx, repoPath, workflowFile, ok)
}

// Verdicts lists every recorded verdict by commit sha.
// channel: localci:verdicts
func (s *Service) Verdicts(ctx context.Context, repoPath string) (v map[string]LocalCiVerdict, err error) {
	defer recoverErr(&err)
	return s.m.Verdicts(ctx, repoPath), nil
}

// RunAt runs one workflow against another commit in a throwaway worktree.
// channel: localci:runAt
func (s *Service) RunAt(ctx context.Context, repoPath, workflowFile, sha string) (res RunAtResult, err error) {
	defer recoverErr(&err)
	return s.m.RunAt(ctx, repoPath, workflowFile, sha)
}

// ResolveRange resolves a revision/range spec to the commits a sweep would run.
// channel: localci:resolveRange
func (s *Service) ResolveRange(ctx context.Context, repoPath, spec string, limit int) (res RangeResult, err error) {
	defer recoverErr(&err)
	return s.m.ResolveRange(ctx, repoPath, spec, limit)
}

// Sweep runs one workflow across several commits, sequentially.
// channel: localci:sweep
// channel: localci:sweep-progress
func (s *Service) Sweep(ctx context.Context, repoPath, workflowFile string, shas []string) (res LocalCiSweepResult, err error) {
	defer recoverErr(&err)
	return s.m.Sweep(ctx, repoPath, workflowFile, shas)
}

// ServiceShutdown is the Wails lifecycle hook (not bound).
func (s *Service) ServiceShutdown() error {
	s.m.CancelAll()
	return nil
}
