package gitsvc

// Slice A — the stash: list, push (whole or partial), pop/apply/drop, to a
// branch, rename, partial apply, forced apply; plus restoring files from a
// commit. Renderer channel: the `'git'` dispatcher, methods stashes, stash,
// stashPush, stashPop, stashApply, stashDrop, stashToBranch, renameStash,
// stashApplyFiles, stashApplyOverwrite, restoreFromCommit.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: stashes
func (s *GitService) Stashes(ctx context.Context, repoPath string) ([]StashInfo, error) {
	return locked(s, ctx, "stashes", repoPath, func(ctx context.Context) ([]StashInfo, error) {
		sep := gitx.SEP
		list := []StashInfo{}
		out, err := gitx.RunGit(ctx, repoPath, []string{"stash", "list", "--pretty=format:%H" + sep + "%P" + sep + "%at" + sep + "%gs"},
			gitx.RunOpts{})
		if err != nil {
			return list, nil
		}
		for i, line := range gitx.SplitLines(out) {
			f := strings.Split(line, sep)
			var parents []string
			for _, p := range strings.Split(aField(f, 1), " ") {
				if p != "" {
					parents = append(parents, p)
				}
			}
			subj := gitx.ParseStashSubject(aField(f, 3))
			info := StashInfo{Index: i, Sha: f[0], ParentSha: aField(parents, 0), Date: aJSInt(aField(f, 2)),
				Message: subj.Message, Branch: subj.Branch}
			if len(parents) > 2 {
				info.UntrackedSha = aStrPtr(parents[2])
			}
			list = append(list, info)
		}
		return list, nil
	})
}

func aStashRaw(ctx context.Context, repoPath string, args ...string) error {
	_, err := gitx.Raw(ctx, repoPath, append([]string{"stash"}, args...), gitx.RunOpts{})
	return err
}

func aStashRef(index int) string { return "stash@{" + strconv.Itoa(index) + "}" }

// gitsvc: stash
func (s *GitService) Stash(ctx context.Context, repoPath, message string) error {
	return lockedErr(s, ctx, "stash", repoPath, func(ctx context.Context) error {
		args := []string{"push", "--include-untracked"}
		if message != "" {
			args = append(args, "-m", message)
		}
		return aStashRaw(ctx, repoPath, args...)
	})
}

// gitsvc: stashPush
//
// Stash a chosen subset of changes (partial stash). With `paths`, only those
// pathspecs are stashed; empty/omitted stashes everything. `keepIndex` leaves
// the staged index intact in the working tree (`git stash push --keep-index`).
func (s *GitService) StashPush(ctx context.Context, repoPath, message string, paths []string, keepIndex bool) error {
	return lockedErr(s, ctx, "stashPush", repoPath, func(ctx context.Context) error {
		args := []string{"push", "--include-untracked"}
		if keepIndex {
			args = append(args, "--keep-index")
		}
		if message != "" {
			args = append(args, "-m", message)
		}
		if len(paths) > 0 {
			args = append(append(args, "--"), paths...)
		}
		return aStashRaw(ctx, repoPath, args...)
	})
}

// gitsvc: stashPop
func (s *GitService) StashPop(ctx context.Context, repoPath string, index int) error {
	return lockedErr(s, ctx, "stashPop", repoPath, func(ctx context.Context) error {
		return aStashRaw(ctx, repoPath, "pop", aStashRef(index))
	})
}

// gitsvc: stashApply
func (s *GitService) StashApply(ctx context.Context, repoPath string, index int) error {
	return lockedErr(s, ctx, "stashApply", repoPath, func(ctx context.Context) error {
		return aStashRaw(ctx, repoPath, "apply", aStashRef(index))
	})
}

// gitsvc: stashDrop
func (s *GitService) StashDrop(ctx context.Context, repoPath string, index int) error {
	return lockedErr(s, ctx, "stashDrop", repoPath, func(ctx context.Context) error {
		return aStashRaw(ctx, repoPath, "drop", aStashRef(index))
	})
}

// gitsvc: stashToBranch
//
// Create a new branch from a stash and apply it there (`git stash branch`).
// Branches off the commit the stash was made on, checks it out, applies the
// stash, and drops it on success — handy when a stash won't apply cleanly onto
// the current branch.
func (s *GitService) StashToBranch(ctx context.Context, repoPath, branch string, index int) error {
	return lockedErr(s, ctx, "stashToBranch", repoPath, func(ctx context.Context) error {
		return aStashRaw(ctx, repoPath, "branch", branch, aStashRef(index))
	})
}

var aStashPrefixRe = regexp.MustCompile(`^((?:WIP on|On) [^:]*:[` + aJSSpace + `]*)`)

// gitsvc: renameStash
//
// Rename a stash by rewriting its reflog subject in place. Git has no native
// rename, but `stash@{n}` is just an entry in `logs/refs/stash` — editing the
// message after the tab keeps the stash's commit and stack position intact.
// The `WIP on <branch>:` / `On <branch>:` prefix is preserved so the branch
// label still shows; only the user-facing message is replaced.
func (s *GitService) RenameStash(ctx context.Context, repoPath string, index int, message string) error {
	return lockedErr(s, ctx, "renameStash", repoPath, func(ctx context.Context) error {
		out, err := gitx.Raw(ctx, repoPath, []string{"rev-parse", "--git-path", "logs/refs/stash"}, gitx.RunOpts{})
		if err != nil {
			return err
		}
		logPath := gitx.TrimJS(out)
		if logPath == "" {
			return &aMessageError{"No stash reflog found"}
		}
		logPath = aAbsUnder(repoPath, logPath)
		raw, err := aReadFile(logPath)
		if err != nil {
			return err
		}
		lines := strings.Split(raw, "\n")
		// The reflog is oldest-first, so stash@{0} is the last non-empty line.
		var nonEmpty []int
		for i, l := range lines {
			if l != "" {
				nonEmpty = append(nonEmpty, i)
			}
		}
		target := len(nonEmpty) - 1 - index
		if target < 0 || target >= len(nonEmpty) {
			return &aMessageError{"No stash at index " + strconv.Itoa(index)}
		}
		fileIdx := nonEmpty[target]
		tab := strings.IndexByte(lines[fileIdx], '\t')
		if tab < 0 {
			return &aMessageError{"Malformed stash reflog entry"}
		}
		meta, subject := lines[fileIdx][:tab], lines[fileIdx][tab+1:]
		prefix := ""
		if m := aStashPrefixRe.FindStringSubmatch(subject); m != nil {
			prefix = m[1]
		}
		lines[fileIdx] = meta + "\t" + prefix + gitx.TrimJS(message)
		// split/join round-trips the trailing newline (last element stays '').
		return aWriteFile(logPath, strings.Join(lines, "\n"))
	})
}

// gitsvc: stashApplyFiles
func (s *GitService) StashApplyFiles(ctx context.Context, repoPath, sha string, tracked, untracked []string) error {
	return lockedErr(s, ctx, "stashApplyFiles", repoPath, func(ctx context.Context) error {
		if len(tracked) > 0 {
			args := append([]string{"restore", "--source", sha, "--worktree", "--"}, tracked...)
			if _, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{}); err != nil {
				return err
			}
		}
		if len(untracked) > 0 {
			args := append([]string{"restore", "--source", sha + "^3", "--worktree", "--"}, untracked...)
			if _, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{}); err != nil {
				return err
			}
		}
		return nil
	})
}

// gitsvc: restoreFromCommit
//
// Overwrite the working copies of `paths` with their content at `hash` —
// "take these changes" from a commit without touching HEAD or the index.
func (s *GitService) RestoreFromCommit(ctx context.Context, repoPath, hash string, paths []string) error {
	return lockedErr(s, ctx, "restoreFromCommit", repoPath, func(ctx context.Context) error {
		if len(paths) == 0 {
			return nil
		}
		aGuardSnapshot(ctx, repoPath)
		_, err := gitx.Raw(ctx, repoPath, append([]string{"restore", "--source", hash, "--worktree", "--"}, paths...),
			gitx.RunOpts{})
		return err
	})
}

// gitsvc: stashApplyOverwrite
//
// Force-apply (or pop) a stash whose untracked files collide with files that
// already exist in the working tree. Plain `git stash apply` aborts with
// "could not restore untracked files from stash" rather than clobber them, so
// we delete the colliding untracked paths — they're about to be replaced by the
// stash's own copies — then retry the apply/pop.
func (s *GitService) StashApplyOverwrite(ctx context.Context, repoPath string, index int, pop bool) error {
	return lockedErr(s, ctx, "stashApplyOverwrite", repoPath, func(ctx context.Context) error {
		ref := aStashRef(index)
		out, err := gitx.Raw(ctx, repoPath, []string{"rev-parse", ref}, gitx.RunOpts{})
		if err != nil {
			return err
		}
		sha := gitx.TrimJS(out)
		// The untracked tree (`^3`) exists only when the stash was made with -u.
		listed, err := gitx.Raw(ctx, repoPath, []string{"ls-tree", "-r", "-z", "--name-only", sha + "^3"}, gitx.RunOpts{})
		if err != nil {
			listed = ""
		}
		for _, rel := range strings.Split(listed, "\x00") {
			if rel == "" {
				continue
			}
			abs := filepath.Join(repoPath, rel)
			// unlink(): a file or a link, never a directory (os.Remove would
			// take an empty one).
			if aExists(abs) {
				if st, err := os.Lstat(abs); err == nil && !st.IsDir() {
					_ = os.Remove(abs)
				}
			}
		}
		verb := "apply"
		if pop {
			verb = "pop"
		}
		return aStashRaw(ctx, repoPath, verb, ref)
	})
}
