package gitsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// ─── Bisect ────────────────────────────────────────────────────────────────

// cBisectRuns are the in-flight `git bisect run` processes, by repository, so
// one can be stopped (bisectCancel).
var cBisectRuns = struct {
	sync.Mutex
	m map[string]context.CancelFunc
}{m: map[string]context.CancelFunc{}}

var (
	cBisectFirstBadRe = regexp.MustCompile(`([0-9a-f]{40}) is the first bad commit`)
	cBisectStepsRe    = regexp.MustCompile(`roughly (\d+) step`)
	cBisectNeedGoodRe = regexp.MustCompile(`waiting for good|waiting for both`)
	cBisectNeedBadRe  = regexp.MustCompile(`waiting for both|bad commit`)
	cBisectBadKnownRe = regexp.MustCompile(`bad commit known`)
)

// cBuildBisectStatus reads the bisect state from the repository plus the
// stdout of the bisect command that just ran (lastOut): git prints progress
// like "Bisecting: N revisions left … (roughly M steps)" and, on completion,
// "<sha> is the first bad commit".
func cBuildBisectStatus(ctx context.Context, repoPath, lastOut string) (BisectStatus, error) {
	startPath, err := cRaw(ctx, repoPath, "rev-parse", "--git-path", "BISECT_START")
	if err != nil {
		return BisectStatus{}, err
	}
	p := gitx.TrimJS(startPath)
	if !strings.HasPrefix(p, "/") {
		p = filepath.Join(repoPath, p)
	}
	if _, err := os.Stat(p); err != nil {
		return BisectStatus{RemainingSteps: -1}, nil
	}

	finished := cBisectFirstBadRe.FindStringSubmatch(lastOut)
	steps := cBisectStepsRe.FindStringSubmatch(lastOut)
	st := BisectStatus{
		InProgress:     true,
		NeedGood:       cBisectNeedGoodRe.MatchString(lastOut),
		NeedBad:        cBisectNeedBadRe.MatchString(lastOut) && !cBisectBadKnownRe.MatchString(lastOut),
		RemainingSteps: -1,
		Finished:       finished != nil,
	}
	if steps != nil {
		st.RemainingSteps, _ = strconv.Atoi(steps[1])
	}
	if finished != nil {
		st.FirstBadSha = finished[1]
		st.FirstBadSubject = gitx.TrimJS(cRawOr(ctx, repoPath, "", "log", "-1", "--pretty=%s", st.FirstBadSha))
	}
	if finished == nil && !st.NeedGood && !st.NeedBad {
		st.CurrentSha = gitx.TrimJS(cRawOr(ctx, repoPath, "", "rev-parse", "HEAD"))
		st.CurrentSubject = gitx.TrimJS(cRawOr(ctx, repoPath, "", "log", "-1", "--pretty=%s", "HEAD"))
	}
	return st, nil
}

// BisectStatus is the current bisect state (used when (re)opening the UI
// mid-session).
// gitsvc: bisectStatus
func (s *GitService) BisectStatus(ctx context.Context, repoPath string) (BisectStatus, error) {
	return locked(s, ctx, "bisectStatus", repoPath, func(ctx context.Context) (BisectStatus, error) {
		return cBuildBisectStatus(ctx, repoPath, "")
	})
}

// BisectStart begins a bisect session. The caller then marks HEAD good/bad to
// seed the range.
// gitsvc: bisectStart
func (s *GitService) BisectStart(ctx context.Context, repoPath string) (BisectStatus, error) {
	return locked(s, ctx, "bisectStart", repoPath, func(ctx context.Context) (BisectStatus, error) {
		out := cRawOr(ctx, repoPath, "", "bisect", "start")
		return cBuildBisectStatus(ctx, repoPath, out)
	})
}

// BisectMark marks a commit during bisect. term is good/bad/skip; rev
// defaults to the current candidate (HEAD). Git narrows the range and checks
// out the next commit to test, or reports the first bad commit when done.
// gitsvc: bisectMark
func (s *GitService) BisectMark(ctx context.Context, repoPath, term, rev string) (BisectStatus, error) {
	return locked(s, ctx, "bisectMark", repoPath, func(ctx context.Context) (BisectStatus, error) {
		args := []string{"bisect", term}
		if rev != "" {
			args = append(args, rev)
		}
		out, err := cRaw(ctx, repoPath, args...)
		if err != nil {
			return BisectStatus{}, err
		}
		return cBuildBisectStatus(ctx, repoPath, out)
	})
}

// BisectRunScript hands the whole search to a command: `git bisect run`.
//
// git checks out each candidate, runs the command, and reads its **exit
// code**: 0 means good, 125 means "cannot test this one, skip it", anything
// else (bar 128+) means bad. That contract is the entire feature — a test
// suite already speaks it, which is why `git bisect run npm test` finds a
// regression while you make coffee.
//
// Output is streamed as the `bisect:output` event as it arrives, in order,
// because a bisect that takes ten minutes with a silent window is
// indistinguishable from a hung one.
// gitsvc: bisectRunScript
func (s *GitService) BisectRunScript(ctx context.Context, repoPath, command string) (BisectStatus, error) {
	return locked(s, ctx, "bisectRunScript", repoPath, func(ctx context.Context) (BisectStatus, error) {
		return s.cBisectRun(ctx, repoPath, command, s.bisectOutput())
	})
}

func (s *GitService) cBisectRun(ctx context.Context, repoPath, command string, onOutput func(string)) (BisectStatus, error) {
	script := gitx.TrimJS(command)
	if script == "" {
		//lint:ignore ST1005 the message is shown to the user verbatim, as in TS
		return BisectStatus{}, errors.New("Give a command to run at each step.")
	}
	status, err := cBuildBisectStatus(ctx, repoPath, "")
	if err != nil {
		return BisectStatus{}, err
	}
	if !status.InProgress {
		//lint:ignore ST1005 the message is shown to the user verbatim, as in TS
		return BisectStatus{}, errors.New("Start a bisect and mark a good and a bad commit first.")
	}

	shell, shellArgs := "sh", []string{"-c", script}
	if runtime.GOOS == "windows" {
		shell, shellArgs = "cmd", []string{"/c", script}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cBisectRuns.Lock()
	cBisectRuns.m[repoPath] = cancel
	cBisectRuns.Unlock()
	defer func() {
		cBisectRuns.Lock()
		delete(cBisectRuns.m, repoPath)
		cBisectRuns.Unlock()
	}()

	// Both streams feed one transcript in arrival order; gitx serializes the
	// chunk callbacks, so the event stream keeps that order too.
	var out strings.Builder
	collect := func(chunk []byte) {
		out.Write(chunk)
		if onOutput != nil {
			onOutput(string(chunk))
		}
	}
	args := append([]string{"-C", repoPath, "bisect", "run", shell}, shellArgs...)
	// A non-zero exit is not a failure to report: `bisect run` exits non-zero
	// when the script never returned a usable verdict, and the status built
	// from the repository afterwards says so better than an error would.
	_, _ = gitx.Exec(runCtx, "", "git", args, gitx.RunOpts{MaxBuffer: -1, OnStdout: collect, OnStderr: collect})
	return cBuildBisectStatus(ctx, repoPath, out.String())
}

// BisectCancel stops a running `bisect run`; the session itself stays open.
// It reports whether there was a run to stop.
//
// Like every write here it takes the repository's exclusive lock — the one a
// running bisectRunScript holds — so, exactly as in TS, it is served only
// after the run it would cancel has ended.
// gitsvc: bisectCancel
func (s *GitService) BisectCancel(ctx context.Context, repoPath string) (bool, error) {
	return locked(s, ctx, "bisectCancel", repoPath, func(ctx context.Context) (bool, error) {
		return cBisectCancel(repoPath), nil
	})
}

func cBisectCancel(repoPath string) bool {
	cBisectRuns.Lock()
	defer cBisectRuns.Unlock()
	cancel, ok := cBisectRuns.m[repoPath]
	if !ok {
		return false
	}
	cancel()
	delete(cBisectRuns.m, repoPath)
	return true
}

// BisectReset ends the bisect session and returns to the original
// branch/HEAD.
// gitsvc: bisectReset
func (s *GitService) BisectReset(ctx context.Context, repoPath string) error {
	return lockedErr(s, ctx, "bisectReset", repoPath, func(ctx context.Context) error {
		_, err := cRaw(ctx, repoPath, "bisect", "reset")
		return err
	})
}
