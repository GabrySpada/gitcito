package gitsvc

// Commit operations from the graph: cherry-pick, revert, reset, and undoing
// the last commit (and putting it back).
//
// Renderer channels served (through the 'git' dispatcher): cherryPick,
// cherryPickMany, revertCommit, reset, undoCommit, restoreUndoneCommit.

import (
	"context"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: cherryPick
func (s *GitService) CherryPick(ctx context.Context, repoPath, hash string, noCommit bool) error {
	return lockedErr(s, ctx, "cherryPick", repoPath, func(ctx context.Context) error {
		args := []string{"cherry-pick"}
		if noCommit {
			args = append(args, "-n")
		}
		args = append(args, hash)
		return bWithRerereCapture(repoPath, func() error {
			_, err := bRaw(ctx, repoPath, args...)
			return err
		})
	})
}

// gitsvc: cherryPickMany
//
// Cherry-pick several commits in one go. Hashes are applied in the given
// order, so callers pass them oldest-first to preserve history order.
func (s *GitService) CherryPickMany(ctx context.Context, repoPath string, hashes []string, noCommit bool) error {
	return lockedErr(s, ctx, "cherryPickMany", repoPath, func(ctx context.Context) error {
		if len(hashes) == 0 {
			return nil
		}
		args := []string{"cherry-pick"}
		if noCommit {
			args = append(args, "-n")
		}
		args = append(args, hashes...)
		return bWithRerereCapture(repoPath, func() error {
			_, err := bRaw(ctx, repoPath, args...)
			return err
		})
	})
}

// gitsvc: revertCommit
func (s *GitService) RevertCommit(ctx context.Context, repoPath, hash string) error {
	return lockedErr(s, ctx, "revertCommit", repoPath, func(ctx context.Context) error {
		_, err := bRaw(ctx, repoPath, "revert", "--no-edit", hash)
		return err
	})
}

// gitsvc: reset
//
// mode is 'soft' | 'mixed' | 'hard' | 'keep'.
func (s *GitService) Reset(ctx context.Context, repoPath, ref, mode string) error {
	return lockedErr(s, ctx, "reset", repoPath, func(ctx context.Context) error {
		// Only --hard destroys working-tree content; soft/mixed move
		// refs/index and --keep carries local changes over or aborts.
		if mode == "hard" {
			aGuardSnapshot(ctx, repoPath)
		}
		_, err := bRaw(ctx, repoPath, "reset", "--"+mode, ref)
		return err
	})
}

// UndoCommitResult is UndoCommitResult in src/shared/types.ts: enough for the
// composer prefill and an undo entry.
type UndoCommitResult struct {
	PreviousSha string  `json:"previousSha"`
	ParentSha   *string `json:"parentSha"`
	WasRoot     bool    `json:"wasRoot"`
	Message     string  `json:"message"`
	Branch      string  `json:"branch"`
}

// bDeletedPaths lists the paths gitService.status() would report with a 'D'
// on either side: `git status --porcelain -b -u --null --ignored=matching`,
// skipping untracked and conflicted entries.
func bDeletedPaths(ctx context.Context, repoPath string) ([]string, error) {
	out, err := bRaw(ctx, repoPath, "status", "--porcelain", "-b", "-u", "--null", "--ignored=matching")
	if err != nil {
		return nil, err
	}
	conflict := map[string]bool{"DD": true, "AU": true, "UD": true, "UA": true, "DU": true, "AA": true, "UU": true}
	var deleted []string
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if len(rec) < 4 || strings.HasPrefix(rec, "## ") {
			continue
		}
		xy, path := rec[:2], rec[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // the original path follows a rename or copy
		}
		if conflict[xy] || xy == "??" {
			continue
		}
		if xy[0] == 'D' || xy[1] == 'D' {
			deleted = append(deleted, path)
		}
	}
	return deleted, nil
}

// bUndoRootCommit undoes the initial commit: leave an unborn branch, keep the
// working tree, unstage everything. Deleted worktree files are restored first
// so they are not lost when the index is emptied.
func bUndoRootCommit(ctx context.Context, repoPath string) error {
	deleted, err := bDeletedPaths(ctx, repoPath)
	if err != nil {
		return err
	}
	if len(deleted) > 0 {
		_, _ = bGit(ctx, repoPath, append([]string{"checkout", "HEAD", "--"}, deleted...)...)
	}
	if _, err := bGit(ctx, repoPath, "update-ref", "-d", "HEAD"); err != nil {
		return err
	}
	_, _ = bGit(ctx, repoPath, "rm", "-r", "--cached", "-q", ".")
	return nil
}

// gitsvc: undoCommit
//
// Undo HEAD with mixed-reset semantics (or a safe unborn-branch path for the
// root commit). Working-tree changes — including pre-existing WIP — are kept.
func (s *GitService) UndoCommit(ctx context.Context, repoPath string) (UndoCommitResult, error) {
	return locked(s, ctx, "undoCommit", repoPath, func(ctx context.Context) (UndoCommitResult, error) {
		headOut, err := bGit(ctx, repoPath, "rev-parse", "HEAD")
		if err != nil {
			return UndoCommitResult{}, err
		}
		headSha := gitx.TrimJS(headOut)
		symbolic := gitx.TrimJS(bGitOr(ctx, repoPath, "", "symbolic-ref", "-q", "HEAD"))
		if !strings.HasPrefix(symbolic, "refs/heads/") {
			return UndoCommitResult{}, bErr("HEAD is detached — check out a branch first.")
		}
		branch := strings.TrimPrefix(symbolic, "refs/heads/")
		message, err := bRaw(ctx, repoPath, "log", "-1", "--format=%B", headSha)
		if err != nil {
			return UndoCommitResult{}, err
		}
		parentLine, err := bGit(ctx, repoPath, "rev-list", "-n", "1", "--parents", headSha)
		if err != nil {
			return UndoCommitResult{}, err
		}
		parents := bSplitWS(gitx.TrimJS(parentLine))
		if len(parents) < 2 {
			if err := bUndoRootCommit(ctx, repoPath); err != nil {
				return UndoCommitResult{}, err
			}
			return UndoCommitResult{PreviousSha: headSha, WasRoot: true, Message: message, Branch: branch}, nil
		}
		parentSha := parents[1]
		if _, err := bRaw(ctx, repoPath, "reset", "--mixed", parentSha); err != nil {
			return UndoCommitResult{}, err
		}
		return UndoCommitResult{PreviousSha: headSha, ParentSha: &parentSha, Message: message, Branch: branch}, nil
	})
}

// gitsvc: restoreUndoneCommit
//
// Put a branch tip back after undoCommit — unborn-safe.
func (s *GitService) RestoreUndoneCommit(ctx context.Context, repoPath, previousSha string) error {
	return lockedErr(s, ctx, "restoreUndoneCommit", repoPath, func(ctx context.Context) error {
		if _, err := bGit(ctx, repoPath, "update-ref", "HEAD", previousSha); err != nil {
			return err
		}
		// undoCommit left the index at the parent (or emptied it, for the root
		// commit); realign it with the restored HEAD or status shows the whole
		// commit as staged reversals. Working tree stays untouched.
		_, err := bGit(ctx, repoPath, "reset", "--mixed")
		return err
	})
}
