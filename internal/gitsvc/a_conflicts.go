package gitsvc

// Slice A — an in-progress merge/rebase/cherry-pick/revert: what is going on,
// the three versions of a file, and resolving, continuing or aborting.
// Renderer channel: the `'git'` dispatcher, methods mergeState, mergeMessage,
// conflictContext, conflictVersions, resolveConflict, conflictTakeSide,
// conflictOpContinue, conflictOpAbort.

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: mergeState
//
// The operation left in progress — 'rebase' | 'merge' | 'cherry-pick' |
// 'revert' — or nil.
func (s *GitService) MergeState(ctx context.Context, repoPath string) (*string, error) {
	return locked(s, ctx, "mergeState", repoPath, func(ctx context.Context) (*string, error) {
		return aMergeState(ctx, repoPath)
	})
}

func aMergeState(ctx context.Context, repoPath string) (*string, error) {
	gitDir, err := aAbsoluteGitDir(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	has := func(name string) bool { return aExists(filepath.Join(gitDir, name)) }
	switch {
	case has("rebase-merge") || has("rebase-apply"):
		return aStrPtr("rebase"), nil
	case has("MERGE_HEAD"):
		return aStrPtr("merge"), nil
	case has("CHERRY_PICK_HEAD"):
		return aStrPtr("cherry-pick"), nil
	case has("REVERT_HEAD"):
		return aStrPtr("revert"), nil
	}
	return nil, nil
}

// aGitPath is `(await git.raw(['rev-parse', '--git-path', name])).trim()`,
// made absolute against the work tree.
func aGitPath(ctx context.Context, repoPath, name string) (string, error) {
	out, err := gitx.Raw(ctx, repoPath, []string{"rev-parse", "--git-path", name}, gitx.RunOpts{})
	if err != nil {
		return "", err
	}
	return aAbsUnder(repoPath, gitx.TrimJS(out)), nil
}

// gitsvc: mergeMessage
//
// The message git prepared for an in-progress merge/cherry-pick/revert
// (e.g. "Merge branch 'main' into feat/ui"). Empty if none is pending. Comment
// lines (starting with '#') are stripped so it can prefill the commit composer.
func (s *GitService) MergeMessage(ctx context.Context, repoPath string) (string, error) {
	return locked(s, ctx, "mergeMessage", repoPath, func(ctx context.Context) (string, error) {
		msgPath, err := aGitPath(ctx, repoPath, "MERGE_MSG")
		if err != nil {
			return "", err
		}
		if !aExists(msgPath) {
			return "", nil
		}
		raw, err := aReadFile(msgPath)
		if err != nil {
			return "", err
		}
		kept := []string{}
		for _, line := range strings.Split(raw, "\n") {
			if !strings.HasPrefix(line, "#") {
				kept = append(kept, line)
			}
		}
		return gitx.TrimJS(strings.Join(kept, "\n")), nil
	})
}

var (
	aBranchSuffixRe = regexp.MustCompile(`[~^]` + aJSDot + `*$`)
	aMergeBranchRe  = regexp.MustCompile(`(?m)^Merge (?:remote-tracking )?branch(?:es)? '([^']+)'`)
	aMergeTagRe     = regexp.MustCompile(`(?m)^Merge tag '([^']+)'`)
)

// gitsvc: conflictContext
//
// Who is being merged into whom. Drives the "Merging X into Y" header and the
// "commit abc123 on branch" labels above each side of the conflict editor.
// Returns nil when no merge/rebase/cherry-pick/revert is in progress.
func (s *GitService) ConflictContext(ctx context.Context, repoPath string) (*ConflictContext, error) {
	return locked(s, ctx, "conflictContext", repoPath, func(ctx context.Context) (*ConflictContext, error) {
		kind, err := aMergeState(ctx, repoPath)
		if err != nil || kind == nil {
			return nil, err
		}
		raw := func(args ...string) (string, error) { return gitx.Raw(ctx, repoPath, args, gitx.RunOpts{}) }
		readIfPresent := func(name string) (string, error) {
			p, err := aGitPath(ctx, repoPath, name)
			if err != nil {
				return "", err
			}
			if !aExists(p) {
				return "", nil
			}
			text, err := aReadFile(p)
			if err != nil {
				return "", nil
			}
			return gitx.TrimJS(text), nil
		}
		// `main~2` / `feature^0` still identify a branch for display purposes.
		branchOf := func(ref string) string {
			out, err := raw("name-rev", "--name-only", "--refs=refs/heads/*", ref)
			if err != nil {
				return ""
			}
			out = gitx.TrimJS(out)
			if out == "" || out == "undefined" {
				return ""
			}
			return aBranchSuffixRe.ReplaceAllString(out, "")
		}
		info := func(ref string) *ConflictRefInfo {
			sep := gitx.SEP
			out, err := raw("log", "-1", "--format=%h"+sep+"%s"+sep+"%an"+sep+"%aI", ref)
			if err != nil {
				return nil
			}
			f := strings.Split(gitx.TrimJS(out), sep)
			if f[0] == "" {
				return nil
			}
			return &ConflictRefInfo{Sha: f[0], Subject: aField(f, 1), Branch: branchOf(ref), Author: aField(f, 2),
				Date: aField(f, 3)}
		}
		currentBranch := func() string {
			out, err := raw("symbolic-ref", "--quiet", "--short", "HEAD")
			if err != nil {
				return ""
			}
			return gitx.TrimJS(out)
		}

		theirsRef := map[string]string{"merge": "MERGE_HEAD", "cherry-pick": "CHERRY_PICK_HEAD", "revert": "REVERT_HEAD"}[*kind]
		if theirsRef == "" {
			theirsRef = "REBASE_HEAD"
		}
		ours, theirs := info("HEAD"), info(theirsRef)

		source, target := "", ""
		if *kind == "rebase" {
			// During a rebase HEAD sits on the upstream side, so `ours` is the
			// target branch and the replayed commits (`theirs`) belong to the
			// rebased branch.
			headName, err := aFirstPresent(readIfPresent, "rebase-merge/head-name", "rebase-apply/head-name")
			if err != nil {
				return nil, err
			}
			onto, err := aFirstPresent(readIfPresent, "rebase-merge/onto", "rebase-apply/onto")
			if err != nil {
				return nil, err
			}
			source = strings.TrimPrefix(headName, "refs/heads/")
			if onto != "" {
				target = branchOf(onto)
			}
			if target == "" {
				if ours != nil && ours.Branch != "" {
					target = ours.Branch
				} else if onto != "" {
					target = gitx.SliceUTF16(onto, 7)
				}
			}
		} else {
			target = currentBranch()
			if target == "" && ours != nil {
				target = aOr(ours.Branch, ours.Sha)
			}
			if *kind == "merge" {
				// MERGE_MSG carries the ref the user actually typed ("Merge branch
				// 'x'"); name-rev only knows where the sha happens to live.
				msg, err := readIfPresent("MERGE_MSG")
				if err != nil {
					return nil, err
				}
				if m := aMergeBranchRe.FindStringSubmatch(msg); m != nil {
					source = m[1]
				} else if m := aMergeTagRe.FindStringSubmatch(msg); m != nil {
					source = m[1]
				}
			}
			if source == "" && theirs != nil {
				source = aOr(theirs.Branch, theirs.Sha)
			}
		}
		return &ConflictContext{Kind: *kind, Source: source, Target: target, Ours: ours, Theirs: theirs}, nil
	})
}

// aFirstPresent is `(await read(a)) || (await read(b))`.
func aFirstPresent(read func(string) (string, error), names ...string) (string, error) {
	for _, n := range names {
		v, err := read(n)
		if err != nil || v != "" {
			return v, err
		}
	}
	return "", nil
}

// gitsvc: conflictVersions
func (s *GitService) ConflictVersions(ctx context.Context, repoPath, file string) (ConflictVersions, error) {
	return locked(s, ctx, "conflictVersions", repoPath, func(ctx context.Context) (ConflictVersions, error) {
		show := func(stage string) *string {
			out, err := gitx.Raw(ctx, repoPath, []string{"show", ":" + stage + ":" + file}, gitx.RunOpts{})
			if err != nil {
				return nil
			}
			return &out
		}
		content, err := aReadFile(filepath.Join(repoPath, file))
		if err != nil {
			content = "" // deleted on disk
		}
		return ConflictVersions{Content: content, Base: show("1"), Ours: show("2"), Theirs: show("3")}, nil
	})
}

// gitsvc: resolveConflict
func (s *GitService) ResolveConflict(ctx context.Context, repoPath, file, content string) error {
	return lockedErr(s, ctx, "resolveConflict", repoPath, func(ctx context.Context) error {
		if err := aWriteFile(filepath.Join(repoPath, file), content); err != nil {
			return err
		}
		_, err := gitx.Raw(ctx, repoPath, []string{"add", file}, gitx.RunOpts{})
		return err
	})
}

// gitsvc: conflictTakeSide
//
// side is 'ours' | 'theirs' | 'delete'.
func (s *GitService) ConflictTakeSide(ctx context.Context, repoPath, file, side string) error {
	return lockedErr(s, ctx, "conflictTakeSide", repoPath, func(ctx context.Context) error {
		if side == "delete" {
			_, err := gitx.Raw(ctx, repoPath, []string{"rm", "--", file}, gitx.RunOpts{})
			return err
		}
		flag := "--theirs"
		if side == "ours" {
			flag = "--ours"
		}
		if _, err := gitx.Raw(ctx, repoPath, []string{"checkout", flag, "--", file}, gitx.RunOpts{}); err != nil {
			return err
		}
		_, err := gitx.Raw(ctx, repoPath, []string{"add", file}, gitx.RunOpts{})
		return err
	})
}

// aConflictVerb maps a ConflictOpKind to its git command; anything unknown is
// a revert, as the TS if/else chain made it.
func aConflictVerb(kind string) string {
	switch kind {
	case "merge", "cherry-pick", "rebase":
		return kind
	}
	return "revert"
}

// gitsvc: conflictOpContinue
func (s *GitService) ConflictOpContinue(ctx context.Context, repoPath, kind string) error {
	return lockedErr(s, ctx, "conflictOpContinue", repoPath, func(ctx context.Context) error {
		// Suppress the commit-message editor on --continue with a `-c
		// core.editor=true` arg (allowUnsafeEditor) rather than an env var, so
		// the child still inherits the parent env untouched.
		_, err := gitx.Raw(ctx, repoPath, []string{"-c", "core.editor=true", aConflictVerb(kind), "--continue"},
			gitx.RunOpts{AllowUnsafe: []string{"allowUnsafeEditor"}})
		return err
	})
}

// gitsvc: conflictOpAbort
func (s *GitService) ConflictOpAbort(ctx context.Context, repoPath, kind string) error {
	return lockedErr(s, ctx, "conflictOpAbort", repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, []string{aConflictVerb(kind), "--abort"}, gitx.RunOpts{})
		return err
	})
}
