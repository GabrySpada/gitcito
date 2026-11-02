package gitsvc

// Slice A — interactive rebase and absorb (staged hunks → fixup! commits).
// Renderer channel: the `'git'` dispatcher, methods interactiveRebaseSteps,
// runInteractiveRebase, absorbPlan, absorbApply.

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: interactiveRebaseSteps
func (s *GitService) InteractiveRebaseSteps(ctx context.Context, repoPath, base string) ([]InteractiveRebaseStep, error) {
	return locked(s, ctx, "interactiveRebaseSteps", repoPath, func(ctx context.Context) ([]InteractiveRebaseStep, error) {
		out, err := gitx.Raw(ctx, repoPath, []string{"log", "--reverse", base + "..HEAD", "--format=%H" + gitx.SEP + "%s"},
			gitx.RunOpts{})
		if err != nil {
			out = ""
		}
		steps := []InteractiveRebaseStep{}
		for _, line := range gitx.SplitLines(out) {
			idx := strings.Index(line, gitx.SEP)
			if idx < 0 {
				// line.slice(0, -1) / line.slice(0): the TS shape for a line
				// without the separator (git never prints one).
				steps = append(steps, InteractiveRebaseStep{Hash: gitx.SliceUTF16(line, gitx.UTF16Len(line)-1), Subject: line})
				continue
			}
			steps = append(steps, InteractiveRebaseStep{Hash: line[:idx], Subject: line[idx+len(gitx.SEP):]})
		}
		return steps, nil
	})
}

// aShort7 is `hash.slice(0, 7)`.
func aShort7(hash string) string { return gitx.SliceUTF16(hash, 7) }

// gitsvc: runInteractiveRebase
func (s *GitService) RunInteractiveRebase(ctx context.Context, repoPath, base string, steps []RebaseStep) error {
	return lockedErr(s, ctx, "runInteractiveRebase", repoPath, func(ctx context.Context) error {
		lines := []string{}
		for _, st := range steps {
			switch {
			case st.Action == "drop":
				lines = append(lines, "drop "+aShort7(st.Hash)+" "+st.Subject)
			case st.Action == "reword" && st.NewMessage != "":
				lines = append(lines, "pick "+aShort7(st.Hash)+" "+st.Subject)
				escaped := strings.ReplaceAll(strings.ReplaceAll(st.NewMessage, `\`, `\\`), "'", `'\''`)
				lines = append(lines, "exec git commit --amend -m '"+escaped+"'")
			default:
				lines = append(lines, st.Action+" "+aShort7(st.Hash)+" "+st.Subject)
			}
		}
		// TS named it gitcito-rebase-<Date.now()>.txt; CreateTemp keeps the
		// prefix without the same-millisecond collision.
		f, err := os.CreateTemp("", "gitcito-rebase-*.txt")
		if err != nil {
			return err
		}
		tmpTodo := f.Name()
		defer os.Remove(tmpTodo)
		_, werr := f.WriteString(strings.Join(lines, "\n") + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
		// Drive the rebase todo via `-c sequence.editor` (copies our generated
		// todo over git's) and silence the commit editor via `-c core.editor`.
		// `-c` args rather than env, so the child inherits the parent env as is.
		_, err = gitx.Raw(ctx, repoPath, []string{
			"-c", "sequence.editor=cp " + aJSONString(tmpTodo),
			"-c", "core.editor=true",
			"rebase", "-i", base,
		}, gitx.RunOpts{AllowUnsafe: []string{"allowUnsafeEditor"}})
		return err
	})
}

// aAutosquash is gitService.autosquash (slice C owns the bound method):
// rebase onto base, auto-ordering and folding any fixup!/squash! commits,
// accepting the generated todo as-is.
//
// Plain exec, not the simple-git path: that guard refuses both a PAGER env
// and `-c core.editor`. Real git with these *_EDITOR vars set to true accepts
// the auto-generated todo without opening an editor. `--autostash` lets the
// rebase run with a dirty tree (absorb leaves the unattributed changes in
// place), restoring them afterwards.
func aAutosquash(ctx context.Context, repoPath, base string, autostash bool) error {
	args := []string{"rebase", "-i", "--autosquash"}
	if autostash {
		args = append(args, "--autostash")
	}
	args = append(args, base)
	_, err := gitx.Run(ctx, repoPath, args, gitx.RunOpts{PlainEnv: true, Env: []string{"GIT_SEQUENCE_EDITOR=true", "GIT_EDITOR=true"}})
	return err
}

// ─── Absorb ──────────────────────────────────────────────────────────────────

// gitsvc: absorbPlan
//
// Work out which of your own recent commits each staged hunk belongs to.
//
// This is `git absorb`: instead of one lumpy "review fixes" commit, blame
// tells you the commit that introduced the lines you just touched, and each
// hunk becomes a `fixup!` for it. Only unpublished commits are candidates —
// rewriting anything already pushed is not ours to offer.
func (s *GitService) AbsorbPlan(ctx context.Context, repoPath string) (AbsorbPlan, error) {
	return locked(s, ctx, "absorbPlan", repoPath, func(ctx context.Context) (AbsorbPlan, error) {
		return aAbsorbPlan(ctx, repoPath)
	})
}

func aAbsorbPlan(ctx context.Context, repoPath string) (AbsorbPlan, error) {
	empty := func(reason string) AbsorbPlan {
		return AbsorbPlan{Targets: []AbsorbTarget{}, Unmatched: []AbsorbHunk{}, Reason: reason}
	}
	// A rebase/merge/cherry-pick in flight owns the index; stay out of it.
	op, err := aMergeState(ctx, repoPath)
	if err != nil {
		return AbsorbPlan{}, err
	}
	if op != nil {
		return empty("in-progress"), nil
	}
	diff, err := gitx.RunGit(ctx, repoPath, []string{"diff", "--cached"}, gitx.RunOpts{})
	if err != nil {
		return AbsorbPlan{}, err
	}
	if gitx.TrimJS(diff) == "" {
		return empty("no-staged"), nil
	}
	commits, base, label := aAbsorbCandidates(ctx, repoPath)
	if len(commits) == 0 {
		return empty("no-commits"), nil
	}
	targets, unmatched := aAttributeHunks(ctx, repoPath, diff, commits)
	return AbsorbPlan{Base: base, RangeLabel: label, Targets: targets, Unmatched: unmatched}, nil
}

type aCandidate struct{ sha, subject string }

// aAbsorbCandidates decides how far back absorb may reach: your unpublished
// commits, and no further.
func aAbsorbCandidates(ctx context.Context, repoPath string) (commits []aCandidate, base, label string) {
	read := func(rng string) []aCandidate {
		out, err := gitx.RunGit(ctx, repoPath, []string{"log", "--format=%H" + gitx.SEP + "%s", rng}, gitx.RunOpts{})
		if err != nil {
			return nil
		}
		var cs []aCandidate
		for _, line := range gitx.SplitLines(out) {
			f := strings.Split(line, gitx.SEP)
			cs = append(cs, aCandidate{sha: f[0], subject: aField(f, 1)})
		}
		return cs
	}

	// Preferred: everything the upstream branch doesn't have yet.
	up, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"}, gitx.RunOpts{})
	if upstream := gitx.TrimJS(up); err == nil && upstream != "" {
		if cs := read(upstream + "..HEAD"); len(cs) > 0 {
			return cs, upstream, "unpushed (" + upstream + "..HEAD)"
		}
	}

	// No upstream: fall back to the commits this branch added on top of the trunk.
	for _, trunk := range []string{"origin/main", "origin/master", "main", "master"} {
		mb, err := gitx.RunGit(ctx, repoPath, []string{"merge-base", "HEAD", trunk}, gitx.RunOpts{})
		b := gitx.TrimJS(mb)
		if err != nil || b == "" {
			continue
		}
		if cs := read(b + "..HEAD"); len(cs) > 0 {
			return cs, b, "since " + trunk
		}
	}

	// Brand-new repo with no trunk to compare against: offer the recent
	// commits. (One argument, "-25 HEAD", exactly as TS passed it.)
	if cs := read("-25 HEAD"); len(cs) > 0 {
		return cs, "HEAD~25", "last 25 commits"
	}
	return nil, "", ""
}

var aBlameLineRe = regexp.MustCompile(`^([0-9a-f]{40}) \d+ \d+`)

// aAttributeHunks blames the old side of each hunk and hands it to the newest
// candidate commit that owns any of its lines. Deleted lines are the strong
// signal; a hunk that only adds falls back to the context around it, which is
// where the new code is going.
func aAttributeHunks(ctx context.Context, repoPath, diff string, candidates []aCandidate) ([]AbsorbTarget, []AbsorbHunk) {
	// Candidates come newest-first; the index doubles as "how recent".
	rank := map[string]int{}
	for i, c := range candidates {
		rank[c.sha] = i // a repeated sha keeps its last index, as new Map(entries) does
	}
	byTarget := map[string][]AbsorbHunk{}
	unmatched := []AbsorbHunk{}
	for _, file := range aParsePatch(diff) {
		for _, hunk := range file.Hunks {
			entry := AbsorbHunk{File: file.NewPath, Header: hunk.Header}
			for _, l := range hunk.Lines {
				if strings.HasPrefix(l, "+") {
					entry.Added++
				}
				if strings.HasPrefix(l, "-") {
					entry.Removed++
				}
			}
			// A new or binary file has no history to attribute to.
			if file.Binary || hunk.OldCount == 0 {
				unmatched = append(unmatched, entry)
				continue
			}
			deleted, context := aTouchedOldLines(hunk)
			lines := context
			if len(deleted) > 0 {
				lines = deleted
			}
			best := ""
			for sha := range aBlameOwners(ctx, repoPath, file.OldPath, lines) {
				r, ok := rank[sha]
				if !ok {
					continue
				}
				if best == "" || r < rank[best] {
					best = sha
				}
			}
			if best == "" {
				unmatched = append(unmatched, entry)
				continue
			}
			byTarget[best] = append(byTarget[best], entry)
		}
	}
	// Keep the targets in history order (newest first), like the graph shows them.
	targets := []AbsorbTarget{}
	for _, c := range candidates {
		if hunks, ok := byTarget[c.sha]; ok {
			targets = append(targets, AbsorbTarget{Sha: c.sha, Subject: c.subject, Hunks: hunks})
		}
	}
	return targets, unmatched
}

// aBlameOwners lists the commits that last touched the given lines of a file
// in HEAD.
func aBlameOwners(ctx context.Context, repoPath, file string, lines []int) map[string]bool {
	owners := map[string]bool{}
	if len(lines) == 0 {
		return owners
	}
	// One blame per contiguous run keeps the number of processes down on a
	// hunk that spans many lines.
	for _, run := range gitx.ContiguousRuns(lines) {
		out, err := gitx.RunGit(ctx, repoPath, []string{"blame", "-w", "--porcelain", "-L",
			strconv.Itoa(run[0]) + "," + strconv.Itoa(run[1]), "HEAD", "--", file}, gitx.RunOpts{})
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			if m := aBlameLineRe.FindStringSubmatch(line); m != nil {
				owners[m[1]] = true
			}
		}
	}
	return owners
}

// gitsvc: absorbApply
//
// Turn the plan into real `fixup!` commits, optionally folding them in with
// an autosquash rebase.
//
// The working tree is never touched: only the index and the commits this
// creates. If any step fails, HEAD and the index are put back exactly as they
// were, so a half-absorbed state cannot survive.
func (s *GitService) AbsorbApply(ctx context.Context, repoPath string, opts *AbsorbApplyOpts) (AbsorbApplyResult, error) {
	return locked(s, ctx, "absorbApply", repoPath, func(ctx context.Context) (AbsorbApplyResult, error) {
		rebase := opts != nil && opts.Rebase
		plan, err := aAbsorbPlan(ctx, repoPath)
		if err != nil {
			return AbsorbApplyResult{}, err
		}
		if len(plan.Targets) == 0 {
			return AbsorbApplyResult{}, nil
		}
		headOut, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", "HEAD"}, gitx.RunOpts{})
		if err != nil {
			return AbsorbApplyResult{}, err
		}
		originalHead := gitx.TrimJS(headOut)
		originalDiff, err := gitx.RunGit(ctx, repoPath, []string{"diff", "--cached"}, gitx.RunOpts{})
		if err != nil {
			return AbsorbApplyResult{}, err
		}
		files := aParsePatch(originalDiff)

		// Oldest target first: each fixup shifts the lines below it, and
		// applying in history order keeps every later patch's context where
		// git expects it.
		fixups := func() error {
			// Clear the index (working tree untouched) so each fixup can be
			// staged on its own.
			if _, err := gitx.RunGit(ctx, repoPath, []string{"reset", "-q"}, gitx.RunOpts{}); err != nil {
				return err
			}
			for i := len(plan.Targets) - 1; i >= 0; i-- {
				target := plan.Targets[i]
				wanted := map[string]bool{}
				for _, h := range target.Hunks {
					wanted[h.File+" "+h.Header] = true
				}
				for _, file := range files {
					var hunks []*aPatchHunk
					for _, h := range file.Hunks {
						if wanted[file.NewPath+" "+h.Header] {
							hunks = append(hunks, h)
						}
					}
					if len(hunks) == 0 {
						continue
					}
					if err := aStagePatch(ctx, repoPath, aBuildPatch(file, hunks), false); err != nil {
						return err
					}
				}
				if _, err := gitx.Run(ctx, repoPath, []string{"commit", "--fixup=" + target.Sha}, gitx.RunOpts{}); err != nil {
					return err
				}
			}
			return nil
		}
		if err := fixups(); err != nil {
			// Undo our commits and put the original staged set back. `reset
			// --mixed` never touches the working tree, so the user's edits are
			// safe throughout.
			_, _ = gitx.RunGit(ctx, repoPath, []string{"reset", "-q", "--mixed", originalHead}, gitx.RunOpts{})
			if gitx.TrimJS(originalDiff) != "" {
				_ = aStagePatch(ctx, repoPath, originalDiff, false)
			}
			return AbsorbApplyResult{}, err
		}

		// The rebase runs with a clean index (git refuses otherwise), so
		// whatever blame could not place is re-staged only once the fixups are
		// folded in.
		if rebase {
			if err := aAutosquash(ctx, repoPath, plan.Base, true); err != nil {
				return AbsorbApplyResult{}, err
			}
		}
		for _, file := range files {
			claimed := map[string]bool{}
			for _, t := range plan.Targets {
				for _, h := range t.Hunks {
					if h.File == file.NewPath {
						claimed[h.Header] = true
					}
				}
			}
			var rest []*aPatchHunk
			for _, h := range file.Hunks {
				if !claimed[h.Header] {
					rest = append(rest, h)
				}
			}
			// Best effort: the change itself is still in the working tree, so a
			// patch that no longer applies costs the user nothing but a re-stage.
			if len(rest) > 0 {
				_ = aStagePatch(ctx, repoPath, aBuildPatch(file, rest), false)
			}
		}
		return AbsorbApplyResult{Created: len(plan.Targets), Rebased: rebase}, nil
	})
}
