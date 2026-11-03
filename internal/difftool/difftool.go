// Package difftool hands a file to an external diff or merge tool — the port
// of src/main/difftool.ts.
//
// These get their own bound service rather than riding the GitService on
// purpose: that service serialises calls per repository, and a merge tool stays
// open for as long as the user needs it. Holding the repo's write lock for ten
// minutes would freeze every other action in that tab.
package difftool

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// maxBuffer is the 10 MiB execFile limit the TS helper set.
const maxBuffer = 10 * 1024 * 1024

// Values are the choices difftool:set records; a nil field is left alone.
type Values struct {
	DiffTool   *string `json:"diffTool,omitempty"`
	MergeTool  *string `json:"mergeTool,omitempty"`
	KeepBackup *bool   `json:"keepBackup,omitempty"`
}

// git is `execFile('git', ['-C', repoPath, ...args])` with
// GIT_TERMINAL_PROMPT=0 and a 10 MiB buffer. A failure is the *gitx.ExecError,
// whose message is Node's "Command failed: …" text.
func git(ctx context.Context, repoPath string, args []string) (string, error) {
	full := append([]string{"-C", repoPath}, args...)
	res, err := gitx.Exec(ctx, "", "git", full, gitx.RunOpts{MaxBuffer: maxBuffer})
	return res.Stdout, err
}

func config(ctx context.Context, repoPath, key string) string {
	out, err := git(ctx, repoPath, []string{"config", "--get", key})
	if err != nil {
		return ""
	}
	return gitx.TrimJS(out)
}

// execFields is what the TS catch blocks read off a rejection: `e.stdout`,
// `e.stderr` and `e.message`.
func execFields(err error) (stdout, stderr, message string) {
	var ee *gitx.ExecError
	if errors.As(err, &ee) {
		return ee.Stdout, ee.Stderr, ee.Error()
	}
	return "", "", err.Error()
}

// Config reads the configured tools and the lists git offers.
func Config(ctx context.Context, repoPath string) shared.DiffToolConfig {
	// `--tool-help` exits non-zero on some builds even when it printed the list.
	help := func(cmd string) string {
		out, err := git(ctx, repoPath, []string{cmd, "--tool-help"})
		if err != nil {
			out, _, _ = execFields(err)
		}
		return out
	}
	var diffHelp, mergeHelp, diffTool, mergeTool, keepBackup string
	var wg sync.WaitGroup
	for _, job := range []struct {
		out *string
		run func() string
	}{
		{&diffHelp, func() string { return help("difftool") }},
		{&mergeHelp, func() string { return help("mergetool") }},
		{&diffTool, func() string { return config(ctx, repoPath, "diff.tool") }},
		{&mergeTool, func() string { return config(ctx, repoPath, "merge.tool") }},
		{&keepBackup, func() string { return config(ctx, repoPath, "mergetool.keepBackup") }},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			*job.out = job.run()
		}()
	}
	wg.Wait()
	return shared.DiffToolConfig{
		DiffTool:   diffTool,
		MergeTool:  mergeTool,
		DiffTools:  shared.SortTools(shared.ParseToolHelp(diffHelp)),
		MergeTools: shared.SortTools(shared.ParseToolHelp(mergeHelp)),
		// Unset means git's default, which is to keep the .orig file.
		KeepBackup: keepBackup != "false",
	}
}

// Set records the chosen tools. Written globally by default (scope "" or
// "global"): which comparison app someone likes is a property of the person,
// not of one repository — and it is the same `diff.tool` their command line
// already reads. Any other scope writes to the repository.
func Set(ctx context.Context, repoPath string, values Values, scope string) error {
	var where []string
	if scope == "" || scope == "global" {
		where = []string{"--global"}
	}
	write := func(key, value string) error {
		if value != "" {
			_, err := git(ctx, repoPath, append(append([]string{"config"}, where...), key, value))
			return err
		}
		// An empty choice means "no tool", which is an unset key, not an empty one.
		_, _ = git(ctx, repoPath, append(append(append([]string{"config"}, where...), "--unset"), key))
		return nil
	}
	if values.DiffTool != nil {
		if err := write("diff.tool", *values.DiffTool); err != nil {
			return err
		}
	}
	if values.MergeTool != nil {
		if err := write("merge.tool", *values.MergeTool); err != nil {
			return err
		}
	}
	if values.KeepBackup != nil {
		if err := write("mergetool.keepBackup", strconv.FormatBool(*values.KeepBackup)); err != nil {
			return err
		}
	}
	return nil
}

// firstNonEmpty is the `a || b || c` chain of the TS catch blocks.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Diff runs `git difftool` on one file. rev compares the working tree against
// that commit; without it, against the index/HEAD as git normally would.
//
// It returns when the tool exits. That can be minutes — the renderer keeps its
// button disabled meanwhile rather than the app blocking. ” means success.
func Diff(ctx context.Context, repoPath, file, rev, tool string) string {
	args := []string{"difftool", "--no-prompt"}
	if tool != "" {
		args = append(args, "--tool="+tool)
	}
	if rev != "" {
		args = append(args, rev)
	}
	args = append(args, "--", file)
	if _, err := git(ctx, repoPath, args); err != nil {
		_, stderr, message := execFields(err)
		return gitx.TrimJS(firstNonEmpty(stderr, message, "git difftool failed"))
	}
	return ""
}

// Merge runs `git mergetool` on one conflicted file. On a clean exit git
// stages the result itself, so the conflict is resolved by the time this
// returns. ” means success.
func Merge(ctx context.Context, repoPath, file, tool string) string {
	args := []string{"mergetool", "--no-prompt"}
	if tool != "" {
		args = append(args, "--tool="+tool)
	}
	args = append(args, "--", file)
	if _, err := git(ctx, repoPath, args); err != nil {
		stdout, stderr, message := execFields(err)
		// mergetool reports "file was not modified" on stdout and still exits 1
		// when the user closed the tool without saving — worth showing verbatim.
		return gitx.TrimJS(firstNonEmpty(stderr, stdout, message, "git mergetool failed"))
	}
	return ""
}
