package gitsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Merge, rebase, the commits behind a conflict, and rerere — git's "reuse
// recorded resolution" memory.

// cRerereReplayed is, per repository, the files git said it resolved from
// memory during the merge or rebase still in progress.
//
// Git only says so once — in the output of the command that hit the
// conflict — and nothing in the repository records it afterwards. It is
// session state on purpose: if Gitcito restarts mid-conflict the hint is
// simply gone, which is better than showing a guess.
var cRerereReplayed = struct {
	sync.Mutex
	m map[string][]string
}{m: map[string][]string{}}

// cWithRerereCapture runs an operation that may conflict, noting any rerere
// replays its error output announces.
func cWithRerereCapture(repoPath string, op func() error) error {
	err := op()
	if err != nil {
		if files := gitx.ParseRerereResolved(err.Error()); len(files) > 0 {
			cRerereReplayed.Lock()
			cRerereReplayed.m[repoPath] = files
			cRerereReplayed.Unlock()
		}
	}
	return err
}

// Merge merges ref into the current branch. options is MergeOptions or the
// legacy boolean noFf.
// gitsvc: merge
func (s *GitService) Merge(ctx context.Context, repoPath, ref string, options MergeOptionsArg) error {
	return lockedErr(s, ctx, "merge", repoPath, func(ctx context.Context) error {
		args := append(gitx.MergeArgs(options.MergeOptions), ref)
		return cWithRerereCapture(repoPath, func() error {
			return gitx.WithAutoStash(ctx, repoPath, "merge "+ref, func(ctx context.Context) error {
				return cMergeTask(ctx, repoPath, args)
			})
		})
	})
}

// MergeInto checks out target and merges source into it.
// gitsvc: mergeInto
func (s *GitService) MergeInto(ctx context.Context, repoPath, source, target string, options MergeOptionsArg) error {
	return lockedErr(s, ctx, "mergeInto", repoPath, func(ctx context.Context) error {
		args := append(gitx.MergeArgs(options.MergeOptions), source)
		return cWithRerereCapture(repoPath, func() error {
			return gitx.WithAutoStash(ctx, repoPath, "merge "+source+" into "+target, func(ctx context.Context) error {
				if err := cCheckout(ctx, repoPath, target); err != nil {
					return err
				}
				return cMergeTask(ctx, repoPath, args)
			})
		})
	})
}

// ConflictCommits is the commits behind a conflict: what each side did to
// this file since they parted.
//
// This is `git log --merge`, which git has shipped forever and nobody finds.
// A conflict marker says *what* clashes; this says *who changed it and why*,
// which is usually the question that actually decides the resolution.
// gitsvc: conflictCommits
func (s *GitService) ConflictCommits(ctx context.Context, repoPath, file string) ([]ConflictCommit, error) {
	return locked(s, ctx, "conflictCommits", repoPath, func(ctx context.Context) ([]ConflictCommit, error) {
		sep := gitx.SEP
		out := cRunGitOr(ctx, repoPath, "", "log", "--merge", "--left-right",
			"--format=%m"+sep+"%H"+sep+"%s"+sep+"%an"+sep+"%aI", "--", file)
		commits := []ConflictCommit{}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(line, sep)
			get := func(i int) string {
				if i < len(f) {
					return f[i]
				}
				return ""
			}
			if get(1) == "" {
				continue
			}
			// `<` is the side already checked out, `>` the one being merged in.
			side := "theirs"
			if f[0] == "<" {
				side = "ours"
			}
			commits = append(commits, ConflictCommit{Sha: get(1), Subject: get(2), Author: get(3), Date: get(4), Side: side})
		}
		return commits, nil
	})
}

// Rebase rebases the current branch onto onto.
// gitsvc: rebase
func (s *GitService) Rebase(ctx context.Context, repoPath, onto string) error {
	return lockedErr(s, ctx, "rebase", repoPath, func(ctx context.Context) error {
		return cWithRerereCapture(repoPath, func() error {
			_, err := cRaw(ctx, repoPath, "rebase", onto)
			return err
		})
	})
}

// RebaseOnto checks out branch then rebases it onto onto (the drag-to-rebase
// gesture).
// gitsvc: rebaseOnto
func (s *GitService) RebaseOnto(ctx context.Context, repoPath, branch, onto string) error {
	return lockedErr(s, ctx, "rebaseOnto", repoPath, func(ctx context.Context) error {
		if err := cCheckout(ctx, repoPath, branch); err != nil {
			return err
		}
		return cWithRerereCapture(repoPath, func() error {
			_, err := cRaw(ctx, repoPath, "rebase", onto)
			return err
		})
	})
}

// RebaseAbort abandons the rebase in progress.
// gitsvc: rebaseAbort
func (s *GitService) RebaseAbort(ctx context.Context, repoPath string) error {
	return lockedErr(s, ctx, "rebaseAbort", repoPath, func(ctx context.Context) error {
		_, err := cRaw(ctx, repoPath, "rebase", "--abort")
		return err
	})
}

// cRrCacheDir is `resolve(repoPath, gitDir, 'rr-cache')`.
func cRrCacheDir(repoPath, gitDir string) string {
	if filepath.IsAbs(gitDir) {
		return filepath.Join(gitDir, "rr-cache")
	}
	return filepath.Join(repoPath, gitDir, "rr-cache")
}

// RerereStatus says whether git is memorising conflict resolutions, how many
// it holds, and which files of the current conflict it replayed.
// gitsvc: rerereStatus
func (s *GitService) RerereStatus(ctx context.Context, repoPath string) (RerereStatus, error) {
	return locked(s, ctx, "rerereStatus", repoPath, func(ctx context.Context) (RerereStatus, error) {
		read := func(key string, scope ...string) string {
			args := append([]string{"config"}, scope...)
			return gitx.TrimJS(cRawOr(ctx, repoPath, "", append(args, "--get", key)...))
		}
		enabled := read("rerere.enabled")
		local := read("rerere.enabled", "--local")

		// `.git/rr-cache` holds one directory per memorised conflict.
		gitDir := gitx.TrimJS(cRawOr(ctx, repoPath, ".git", "rev-parse", "--git-dir"))
		recorded := 0
		if entries, err := os.ReadDir(cRrCacheDir(repoPath, gitDir)); err == nil {
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), ".") {
					recorded++
				}
			}
		}

		// Only what git itself announced during the operation still in
		// progress. A file the *user* resolved without staging also has no
		// markers and is also still unmerged, so content alone cannot tell the
		// two apart.
		conflicted := map[string]bool{}
		for _, l := range gitx.SplitTrimmedLines(cRawOr(ctx, repoPath, "", "diff", "--name-only", "--diff-filter=U")) {
			conflicted[l] = true
		}
		replayed := []string{}
		cRerereReplayed.Lock()
		if len(conflicted) == 0 {
			delete(cRerereReplayed.m, repoPath)
		}
		for _, f := range cRerereReplayed.m[repoPath] {
			if conflicted[f] {
				replayed = append(replayed, f)
			}
		}
		cRerereReplayed.Unlock()

		return RerereStatus{
			Enabled:    enabled == "true",
			AutoUpdate: read("rerere.autoUpdate") == "true",
			PerRepo:    local != "",
			Recorded:   recorded,
			Replayed:   replayed,
		}, nil
	})
}

// SetRerere turns the memory on or off. It is written globally by default
// (scope ” or "global") — someone who wants git to remember their
// resolutions wants that everywhere, and it is the same `rerere.enabled`
// their command line reads. Any other scope writes the repository's config.
// gitsvc: setRerere
func (s *GitService) SetRerere(ctx context.Context, repoPath string, values RerereSettings, scope string) error {
	return lockedErr(s, ctx, "setRerere", repoPath, func(ctx context.Context) error {
		where := "--local"
		if scope == "" || scope == "global" {
			where = "--global"
		}
		if values.Enabled != nil {
			if _, err := cRaw(ctx, repoPath, "config", where, "rerere.enabled", cJSBool(*values.Enabled)); err != nil {
				return err
			}
		}
		if values.AutoUpdate != nil {
			if _, err := cRaw(ctx, repoPath, "config", where, "rerere.autoUpdate", cJSBool(*values.AutoUpdate)); err != nil {
				return err
			}
		}
		return nil
	})
}

// RerereForget drops the memorised resolution for one file and brings its
// conflict back.
//
// `git rerere forget` alone only stops the replay happening *next* time — it
// leaves the already-written file as it is, which is not what someone asking
// for the conflict back means. `checkout --merge` rebuilds the markers from
// the index stages, which are still there.
// gitsvc: rerereForget
func (s *GitService) RerereForget(ctx context.Context, repoPath, file string) error {
	return lockedErr(s, ctx, "rerereForget", repoPath, func(ctx context.Context) error {
		if _, err := gitx.RunGit(ctx, repoPath, []string{"rerere", "forget", "--", file}, gitx.RunOpts{}); err != nil {
			return err
		}
		_ = cRunGitOr(ctx, repoPath, "", "checkout", "--merge", "--", file)
		return nil
	})
}

// RerereClear empties the whole memory. Files and history are untouched.
// gitsvc: rerereClear
func (s *GitService) RerereClear(ctx context.Context, repoPath string) error {
	return lockedErr(s, ctx, "rerereClear", repoPath, func(ctx context.Context) error {
		gitDir, err := cRaw(ctx, repoPath, "rev-parse", "--git-dir")
		if err != nil {
			return err
		}
		return os.RemoveAll(cRrCacheDir(repoPath, gitx.TrimJS(gitDir)))
	})
}
