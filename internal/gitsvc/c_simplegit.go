package gitsvc

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// The porcelain wrappers of simple-git that slice C's TS called — checkout,
// branch, branchLocal, revparse, merge, status — reproduced on top of
// gitx.Raw so their argument shapes and their error rules stay what the
// renderer saw. Each says which simple-git task it mirrors.

// cRaw is `gitFor(repoPath).raw(args)`.
func cRaw(ctx context.Context, repoPath string, args ...string) (string, error) {
	return gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
}

// cRawOr is `gitFor(repoPath).raw(args).catch(() => fallback)`.
func cRawOr(ctx context.Context, repoPath, fallback string, args ...string) string {
	out, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
	if err != nil {
		return fallback
	}
	return out
}

// cRunGitOr is `runGit(repoPath, args).catch(() => fallback)`.
func cRunGitOr(ctx context.Context, repoPath, fallback string, args ...string) string {
	out, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
	if err != nil {
		return fallback
	}
	return out
}

// cRevparse is `git.revparse(args)`: `rev-parse …`, trimmed.
func cRevparse(ctx context.Context, repoPath string, args ...string) (string, error) {
	out, err := gitx.Raw(ctx, repoPath, append([]string{"rev-parse"}, args...), gitx.RunOpts{})
	if err != nil {
		return "", err
	}
	return gitx.TrimJS(out), nil
}

// cRevparseOr is `git.revparse(args).catch(() => ”)`, trimmed.
func cRevparseOr(ctx context.Context, repoPath string, args ...string) string {
	out, err := cRevparse(ctx, repoPath, args...)
	if err != nil {
		return ""
	}
	return out
}

// cCheckout is `git.checkout(args)` — simple-git's checkoutTask.
func cCheckout(ctx context.Context, repoPath string, args ...string) error {
	_, err := gitx.Raw(ctx, repoPath, append([]string{"checkout"}, args...), gitx.RunOpts{})
	return err
}

// cBranch is `git.branch(args)` — simple-git's branchTask, which always adds
// `-v` (second) and, with no arguments at all, `-a`. A failed delete has no
// special handling there, so its error is the usual stdout+stderr.
func cBranch(ctx context.Context, repoPath string, args ...string) error {
	cmd := append([]string{"branch"}, args...)
	if len(cmd) == 1 {
		cmd = append(cmd, "-a")
	}
	hasV := false
	for _, a := range cmd {
		if a == "-v" {
			hasV = true
		}
	}
	if !hasV {
		cmd = append(cmd[:1], append([]string{"-v"}, cmd[1:]...)...)
	}
	_, err := gitx.Raw(ctx, repoPath, cmd, gitx.RunOpts{})
	return err
}

var (
	cBranchDetachedRe = regexp.MustCompile(`^([*+]\s)?\((?:HEAD )?detached (?:from|at) (\S+)\)\s+([a-z0-9]+)\s(.*)$`)
	cBranchLineRe     = regexp.MustCompile(`(?s)^([*+]\s)?(\S+)\s+([a-z0-9]+)\s?(.*)$`)
)

// cBranchLocal is `(await git.branchLocal()).all`: the names simple-git's
// branch-summary parser reads out of `git branch -v`. A detached HEAD shows
// up under the ref it is detached at, as it did there.
func cBranchLocal(ctx context.Context, repoPath string) ([]string, error) {
	out, err := gitx.Raw(ctx, repoPath, []string{"branch", "-v"}, gitx.RunOpts{})
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, line := range strings.Split(out, "\n") {
		line = gitx.TrimJS(line)
		if line == "" {
			continue
		}
		if m := cBranchDetachedRe.FindStringSubmatch(line); m != nil {
			names = append(names, m[2])
		} else if m := cBranchLineRe.FindStringSubmatch(line); m != nil {
			names = append(names, m[2])
		}
	}
	return names, nil
}

// simple-git's merge-summary line parsers, in order; the first that matches
// a line wins.
var (
	cMergeAutoRe       = regexp.MustCompile(`^Auto-merging\s+(.+)$`)
	cMergeConflictRe   = regexp.MustCompile(`^CONFLICT\s+\((.+)\): Merge conflict in (.+)$`)
	cMergeDeleteRe     = regexp.MustCompile(`^CONFLICT\s+\((.+/delete)\): (.+) deleted in (.+) and`)
	cMergeConflictAnyR = regexp.MustCompile(`^CONFLICT\s+\((.+)\):`)
)

// cMergeTask is `git.merge(args)` — simple-git's mergeTask. A merge that
// stops on conflicts exits 1 with a silent stderr, which simple-git does not
// count as a failure; its parser then finds the CONFLICT lines on stdout and
// rejects with "CONFLICTS: <file>:<reason>, …". With stderr output (a refused
// merge, rerere chatter) the usual stdout+stderr error comes first.
func cMergeTask(ctx context.Context, repoPath string, args []string) error {
	out, err := gitx.Raw(ctx, repoPath, append([]string{"merge"}, args...), gitx.RunOpts{})
	if err != nil {
		return err
	}
	var conflicts []string
	for _, line := range strings.Split(out, "\n") {
		line = gitx.TrimJS(line)
		if line == "" {
			continue
		}
		switch {
		case cMergeAutoRe.MatchString(line):
		case cMergeConflictRe.MatchString(line):
			m := cMergeConflictRe.FindStringSubmatch(line)
			conflicts = append(conflicts, m[2]+":"+m[1])
		case cMergeDeleteRe.MatchString(line):
			m := cMergeDeleteRe.FindStringSubmatch(line)
			conflicts = append(conflicts, m[2]+":"+m[1])
		case cMergeConflictAnyR.MatchString(line):
			m := cMergeConflictAnyR.FindStringSubmatch(line)
			conflicts = append(conflicts, "null:"+m[1])
		}
	}
	if len(conflicts) > 0 {
		return errors.New("CONFLICTS: " + strings.Join(conflicts, ", "))
	}
	return nil
}

// cStatusFile is one FileStatusSummary of simple-git's status parser.
type cStatusFile struct {
	path, index, workingDir string
}

// cStatusSummary is the part of simple-git's StatusResult slice C reads.
type cStatusSummary struct {
	files   []cStatusFile
	ignored []string
}

var cRenamedPathRe = regexp.MustCompile(`^(.+)\x00(.+)$`)

// cStatusScan is statusScan(): `git.status(['--ignored=matching'])`, parsed
// the way simple-git's parseStatusSummary parses it. (TS also shared one
// in-flight scan between concurrent callers — a performance detail.)
func cStatusScan(ctx context.Context, repoPath string) (cStatusSummary, error) {
	out, err := gitx.Raw(ctx, repoPath,
		[]string{"status", "--porcelain", "-b", "-u", "--null", "--ignored=matching"}, gitx.RunOpts{})
	if err != nil {
		return cStatusSummary{}, err
	}
	var st cStatusSummary
	lines := strings.Split(out, "\x00")
	for i := 0; i < len(lines); {
		line := gitx.TrimJS(lines[i])
		i++
		if line == "" {
			continue
		}
		if line[0] == 'R' {
			next := ""
			if i < len(lines) {
				next = lines[i]
			}
			i++
			line += "\x00" + next
		}
		cSplitStatusLine(&st, line)
	}
	return st, nil
}

// cSplitStatusLine is simple-git's splitLine: the two status letters, then
// the path; a line that is trimmed down to one letter shifts left.
func cSplitStatusLine(st *cStatusSummary, lineStr string) {
	t := gitx.TrimJS(lineStr)
	at := func(i int) byte {
		if i < len(t) {
			return t[i]
		}
		return 0
	}
	var index, wd, path string
	switch {
	case at(2) == ' ':
		index, wd, path = string(t[0]), string(t[1]), t[3:]
	case at(1) == ' ':
		index, wd, path = " ", string(t[0]), t[2:]
	default:
		return
	}
	raw := index + wd
	if raw == "!!" {
		st.ignored = append(st.ignored, path)
	}
	if raw == "##" || raw == "!!" {
		return
	}
	f := cStatusFile{path: path, index: index, workingDir: wd}
	if index == "R" || wd == "R" {
		if m := cRenamedPathRe.FindStringSubmatch(path); m != nil {
			f.path = m[1]
		}
	}
	st.files = append(st.files, f)
}
