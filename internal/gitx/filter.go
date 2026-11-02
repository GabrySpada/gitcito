package gitx

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// FilterTimeout bounds one dry-run clean/smudge command: enough to judge a
// filter, cheap enough to run on a click.
const FilterTimeout = 10 * time.Second

// RunFilterCommand runs one clean/smudge command the way git would: content on
// stdin, result on stdout, `%f` replaced by the single-quoted repo-relative
// path, through the shell. Used only by the filter dry run — git itself runs
// the real thing after the config is written.
//
// Errors: "filter command timed out after 10s" (the process tree is killed),
// else the command's trimmed stderr, else "filter command exited with <code>".
func RunFilterCommand(ctx context.Context, command, cwd, file string, input []byte) ([]byte, error) {
	return runFilterCommand(ctx, command, cwd, file, input, FilterTimeout)
}

func runFilterCommand(ctx context.Context, command, cwd, file string, input []byte, timeout time.Duration) ([]byte, error) {
	quoted := "'" + strings.ReplaceAll(file, "'", `'\''`) + "'"
	cmdline := strings.ReplaceAll(command, "%f", quoted)
	name, args, prepare := shellCommand(cmdline)

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if input == nil {
		input = []byte{} // stdin is always a pipe that gets closed, never /dev/null
	}
	res, err := Exec(tctx, cwd, name, args, RunOpts{PlainEnv: true, Stdin: input, MaxBuffer: -1, prepare: prepare})
	if err == nil {
		return []byte(res.Stdout), nil
	}
	if errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, errors.New("filter command timed out after " + strconv.Itoa(int(timeout/time.Second)) + "s")
	}
	var ee *ExecError
	if errors.As(err, &ee) && ee.notStarted {
		return nil, ee
	}
	if msg := TrimJS(res.Stderr); msg != "" {
		return nil, errors.New(msg)
	}
	return nil, errors.New("filter command exited with " + exitLabel(ee))
}
