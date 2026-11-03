package difftool

// Service serves the renderer's external diff/merge tool channels:
//   difftool:config, difftool:set, difftool:diff, difftool:merge
// It mirrors registerDiffToolHandlers in src/main/difftool.ts. Every exported
// method is callable from the page, so nothing else is exported here.
//
// None of these take the per-repository lock (see the package comment).

import (
	"context"
	"fmt"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Service is the bound facade.
type Service struct{}

// NewService builds the facade. It needs nothing: git is found on PATH.
func NewService() *Service {
	return &Service{}
}

func recoverErr(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("difftool: internal error: %v", p)
	}
}

func recoverMsg(msg *string) {
	if p := recover(); p != nil {
		*msg = fmt.Sprintf("difftool: internal error: %v", p)
	}
}

// Config reads diff.tool, merge.tool, mergetool.keepBackup and the tools git offers.
// channel: difftool:config
func (s *Service) Config(ctx context.Context, repoPath string) (out shared.DiffToolConfig, err error) {
	defer recoverErr(&err)
	return Config(ctx, repoPath), nil
}

// Set records the chosen tools; scope is "global" (also when empty) or "repo".
// channel: difftool:set
func (s *Service) Set(ctx context.Context, repoPath string, values Values, scope string) (err error) {
	defer recoverErr(&err)
	return Set(ctx, repoPath, values, scope)
}

// Diff opens one file in the diff tool and waits for it; ” on success, else the reason.
// channel: difftool:diff
func (s *Service) Diff(ctx context.Context, repoPath, file, rev, tool string) (msg string) {
	defer recoverMsg(&msg)
	return Diff(ctx, repoPath, file, rev, tool)
}

// Merge opens one conflicted file in the merge tool and waits for it; ” on success.
// channel: difftool:merge
func (s *Service) Merge(ctx context.Context, repoPath, file, tool string) (msg string) {
	defer recoverMsg(&msg)
	return Merge(ctx, repoPath, file, tool)
}
