package gitsvc

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Branch and navigation operations: checkout (local and remote), create,
// delete, rename, and the remote-side checks around them.

// Checkout switches to ref.
// gitsvc: checkout
func (s *GitService) Checkout(ctx context.Context, repoPath, ref string) error {
	return lockedErr(s, ctx, "checkout", repoPath, func(ctx context.Context) error {
		return cCheckout(ctx, repoPath, ref)
	})
}

// CheckoutRemote checks out remote branch fullName as the local localName.
// remote, when given, is fetched first.
// gitsvc: checkoutRemote
func (s *GitService) CheckoutRemote(ctx context.Context, repoPath, fullName, localName, remote string) (CheckoutRemoteResult, error) {
	return locked(s, ctx, "checkoutRemote", repoPath, func(ctx context.Context) (CheckoutRemoteResult, error) {
		if remote != "" {
			// Refresh the remote-tracking ref first, so "checkout as local" always
			// picks up the actual remote tip instead of whatever was last fetched.
			// It runs inside this call, under the repo's write lock, so it cannot
			// collide with an in-flight status/checkout/stash on `.git/index.lock`.
			// Best-effort: offline/auth hiccups shouldn't block checking out the
			// locally-known ref.
			_ = gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
				// simple-git's fetchTask: `fetch <remote> <branch>`.
				_, err := cRaw(ctx, repoPath, "fetch", remote, localName)
				return err
			})
		}
		// If a local branch with that name already exists, just switch to it
		// instead of trying to recreate a tracking branch (which would fail with
		// "a branch named '<x>' already exists").
		branches, err := cBranchLocal(ctx, repoPath)
		if err != nil {
			return CheckoutRemoteResult{}, err
		}
		if !cContains(branches, localName) {
			if err := cCheckout(ctx, repoPath, "-b", localName, "--track", fullName); err != nil {
				return CheckoutRemoteResult{}, err
			}
			return CheckoutRemoteResult{}, nil
		}
		// Compare the existing local branch with the remote tip before touching
		// anything. If they've diverged (both have unique commits) a fast-forward
		// is impossible — report it so the renderer can ask the user how to
		// reconcile instead of dumping git's raw "Not possible to fast-forward".
		d, err := gitx.Divergence(ctx, repoPath, localName, fullName)
		if err != nil {
			return CheckoutRemoteResult{}, err
		}
		if d.Ahead > 0 && d.Behind > 0 {
			return CheckoutRemoteResult{Diverged: true, Ahead: d.Ahead, Behind: d.Behind}, nil
		}
		// The local branch is ahead and the remote has nothing new. Silently
		// checking it out answers a request for the remote branch with local
		// work — so report it and let the renderer ask what the user meant.
		if d.Ahead > 0 {
			return CheckoutRemoteResult{AheadOnly: true, Ahead: d.Ahead, Behind: d.Behind}, nil
		}
		// Fast-forward the existing local branch to the remote tip so the
		// checkout actually brings in the remote changes. WithAutoStash shelves
		// a dirty working tree under a named stash before the FF and restores it
		// after, so local edits don't abort the update.
		err = gitx.WithAutoStash(ctx, repoPath, "checkout "+localName, func(ctx context.Context) error {
			if err := cCheckout(ctx, repoPath, localName); err != nil {
				return err
			}
			if d.Behind > 0 {
				return cMergeTask(ctx, repoPath, []string{"--ff-only", fullName})
			}
			return nil
		})
		if err != nil {
			return CheckoutRemoteResult{}, err
		}
		return CheckoutRemoteResult{Ahead: d.Ahead, Behind: d.Behind}, nil
	})
}

// ResolveDivergedCheckout reconciles a diverged local branch with its remote
// after the user picks a strategy in the divergence dialog. With backup, a
// `backup/<localName>-<timestamp>` branch is created at the current local tip
// first, so even a reset can be undone by checking that branch out.
//   - rebase:      replay local commits on top of the remote tip (linear history)
//   - merge:       --no-ff merge, keeping both histories
//   - reset-soft:  move the branch to the remote tip, keep the commits' changes staged
//   - reset-mixed: same, but leave the changes unstaged in the working tree
//   - reset-hard:  discard the local commits and their changes entirely
//
// gitsvc: resolveDivergedCheckout
func (s *GitService) ResolveDivergedCheckout(ctx context.Context, repoPath, fullName, localName, strategy string, backup bool) (DivergedCheckoutResult, error) {
	return locked(s, ctx, "resolveDivergedCheckout", repoPath, func(ctx context.Context) (DivergedCheckoutResult, error) {
		// The tip we are about to move away from — the renderer keeps it as the
		// undo target, so a reset is recoverable even without a backup branch.
		previous, err := cRevparse(ctx, repoPath, localName)
		if err != nil {
			return DivergedCheckoutResult{}, err
		}
		res := DivergedCheckoutResult{PreviousRef: previous}
		err = gitx.WithAutoStash(ctx, repoPath, "checkout "+localName, func(ctx context.Context) error {
			if err := cCheckout(ctx, repoPath, localName); err != nil {
				return err
			}
			if backup {
				res.BackupRef = "backup/" + localName + "-" + gitx.BackupStamp(time.Now())
				if err := cBranch(ctx, repoPath, res.BackupRef); err != nil {
					return err
				}
			}
			switch strategy {
			case "rebase":
				_, err := cRaw(ctx, repoPath, "rebase", fullName)
				return err
			case "merge":
				return cMergeTask(ctx, repoPath, []string{"--no-ff", fullName})
			}
			// reset-soft / reset-mixed / reset-hard map straight onto git's modes:
			// how much of the discarded commits survives in the index and the tree.
			mode := ""
			if len(strategy) > len("reset-") {
				mode = strategy[len("reset-"):]
			}
			_, err := cRaw(ctx, repoPath, "reset", "--"+mode, fullName)
			return err
		})
		if err != nil {
			return DivergedCheckoutResult{}, err
		}
		return res, nil
	})
}

// CreateBranch creates name at at (HEAD when empty) and, unless checkout is
// false, checks it out. A nil checkout means true.
// gitsvc: createBranch
func (s *GitService) CreateBranch(ctx context.Context, repoPath, name, at string, checkout *bool) error {
	return lockedErr(s, ctx, "createBranch", repoPath, func(ctx context.Context) error {
		if checkout == nil || *checkout {
			if at != "" {
				return cCheckout(ctx, repoPath, "-b", name, at)
			}
			return cCheckout(ctx, repoPath, "-b", name)
		}
		if at != "" {
			return cBranch(ctx, repoPath, name, at)
		}
		return cBranch(ctx, repoPath, name)
	})
}

// DeleteBranch deletes a local branch (`-d`, or `-D` with force).
// gitsvc: deleteBranch
func (s *GitService) DeleteBranch(ctx context.Context, repoPath, name string, force bool) error {
	return lockedErr(s, ctx, "deleteBranch", repoPath, func(ctx context.Context) error {
		flag := "-d"
		if force {
			flag = "-D"
		}
		return cBranch(ctx, repoPath, flag, name)
	})
}

var cRemoteRefGoneRe = regexp.MustCompile(`(?i)remote ref does not exist`)

// DeleteRemoteBranch deletes name on remote and drops the local
// remote-tracking ref.
// gitsvc: deleteRemoteBranch
func (s *GitService) DeleteRemoteBranch(ctx context.Context, repoPath, remote, name string) error {
	return lockedErr(s, ctx, "deleteRemoteBranch", repoPath, func(ctx context.Context) error {
		err := gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
			_, err := gitx.RunGit(ctx, repoPath, []string{"push", remote, "--delete", name}, gitx.RunOpts{})
			return err
		})
		// The branch may already be gone on the remote (e.g. Dependabot deleted
		// it after merging its PR) while our local remote-tracking ref lingers
		// because no pruning fetch has run. Git reports this as "remote ref does
		// not exist". In that case there is nothing to push — just prune the
		// stale tracking ref so it disappears from the branch list, which is
		// what the user expects.
		if err != nil && !cRemoteRefGoneRe.MatchString(err.Error()) {
			return err
		}
		// Whether the push succeeded or the ref was already gone, drop the local
		// remote-tracking copy so the UI no longer shows the deleted branch.
		_ = cRunGitOr(ctx, repoPath, "", "update-ref", "-d", "refs/remotes/"+remote+"/"+name)
		return nil
	})
}

// RenameBranch renames a local branch.
// gitsvc: renameBranch
func (s *GitService) RenameBranch(ctx context.Context, repoPath, oldName, newName string) error {
	return lockedErr(s, ctx, "renameBranch", repoPath, func(ctx context.Context) error {
		return cBranch(ctx, repoPath, "-m", oldName, newName)
	})
}

// RenameBranchRemote renames a branch and moves it on the remote too: local
// `-m`, delete the old upstream branch, then push the new name and set it as
// upstream. The remote delete is best-effort (a protected branch may refuse).
// gitsvc: renameBranchRemote
func (s *GitService) RenameBranchRemote(ctx context.Context, repoPath, oldName, newName, remote string) error {
	return lockedErr(s, ctx, "renameBranchRemote", repoPath, func(ctx context.Context) error {
		if err := cBranch(ctx, repoPath, "-m", oldName, newName); err != nil {
			return err
		}
		return gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
			_ = cRunGitOr(ctx, repoPath, "", "push", remote, "--delete", oldName)
			_, err := gitx.RunGit(ctx, repoPath, []string{"push", "-u", remote, newName}, gitx.RunOpts{})
			return err
		})
	})
}

// RemoteHasBranches is which of branches the remote actually carries, in one
// `ls-remote`.
//
// A push that "succeeded" into the wrong remote, or a branch nobody pushed,
// both end as the same GitHub 422 — a validation failure whose entry carries
// no message. Asking the remote first turns that into a sentence naming the
// branch.
// gitsvc: remoteHasBranches
func (s *GitService) RemoteHasBranches(ctx context.Context, repoPath, remote string, branches []string) ([]string, error) {
	return locked(s, ctx, "remoteHasBranches", repoPath, func(ctx context.Context) ([]string, error) {
		if len(branches) == 0 {
			return []string{}, nil
		}
		out := ""
		_ = gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
			args := []string{"ls-remote", "--heads", remote}
			for _, b := range branches {
				args = append(args, "refs/heads/"+b)
			}
			res, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
			out = res
			return err
		})
		present := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) < 2 {
				continue
			}
			ref := gitx.TrimJS(fields[1])
			if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
				present[name] = true
			}
		}
		has := []string{}
		for _, b := range branches {
			if present[b] {
				has = append(has, b)
			}
		}
		return has, nil
	})
}

// FilesToPush is the files this push would actually publish: the ones changed
// in the commits ahead of the upstream. The secret-push guard uses it so it
// only warns about credentials introduced by the unpushed commits, not every
// secret already tracked (and long since pushed) in the repo.
//
// With no upstream (branch never pushed), the whole history publishes, so it
// falls back to every tracked file.
// gitsvc: filesToPush
func (s *GitService) FilesToPush(ctx context.Context, repoPath, branch string) ([]string, error) {
	return locked(s, ctx, "filesToPush", repoPath, func(ctx context.Context) ([]string, error) {
		upstream := gitx.TrimJS(cRawOr(ctx, repoPath, "", "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}"))
		if upstream == "" {
			return cListTrackedFiles(ctx, repoPath), nil
		}
		raw := cRawOr(ctx, repoPath, "", "diff", "--name-only", "-z", upstream+".."+branch)
		return cSplitNul(raw), nil
	})
}

// cContains is `list.includes(v)`.
func cContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// cSplitNul is `s.split('\0').filter(Boolean)`.
func cSplitNul(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
