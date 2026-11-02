//go:build !windows

package localci

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

type recorder struct {
	mu     sync.Mutex
	events []struct {
		name string
		data any
	}
}

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, struct {
		name string
		data any
	}{name, data})
	r.mu.Unlock()
}

func (r *recorder) all(name string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []any
	for _, e := range r.events {
		if e.name == name {
			out = append(out, e.data)
		}
	}
	return out
}

// fakeAct installs a stand-in for act: succeeds only when the checked-out
// commit contains ok.flag. That makes the verdict depend on WHICH commit the
// worktree holds — exactly what RunAt must get right.
func fakeAct(t *testing.T, body string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-act")
	if body == "" {
		body = "echo \"run in $PWD\"\ntest -f ok.flag\n"
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo fake-act 1.0; exit 0; fi\n" + body
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITCITO_ACT_BIN", bin)
}

// seed adds two commits on top of the fixture: one with ok.flag, one without.
func seed(t *testing.T, repo string) (good, bad string) {
	t.Helper()
	testutil.WriteFile(t, repo, "ok.flag", "yes\n")
	testutil.Git(t, repo, "add", "ok.flag")
	testutil.Git(t, repo, "commit", "-m", "test: add ok.flag")
	good = strings.TrimSpace(testutil.Git(t, repo, "rev-parse", "HEAD"))
	testutil.Git(t, repo, "rm", "-q", "ok.flag")
	testutil.Git(t, repo, "commit", "-m", "test: drop ok.flag")
	bad = strings.TrimSpace(testutil.Git(t, repo, "rev-parse", "HEAD"))
	return good, bad
}

// ─── test/newFeatures.test.ts › local CI (local-ci playground) ──────────────

func TestLocalCIPlayground(t *testing.T) {
	testutil.IsolateGitEnv(t)
	m := NewManager(&recorder{})
	ctx := context.Background()
	t.Run("lists workflows with their names, filename as fallback", func(t *testing.T) {
		ws := m.Workflows(testutil.PlaygroundRepo(t, "local-ci"))
		if len(ws) != 2 || ws[0].File != "ci.yml" || ws[1].File != "lint.yml" || ws[0].Name != "CI" || ws[1].Name != "lint.yml" {
			t.Errorf("workflows = %+v", ws)
		}
	})
	t.Run("reports tool availability truthfully (machine-dependent, shape only)", func(t *testing.T) {
		fakeAct(t, "")
		st := m.Status(ctx)
		if st.Act == nil || *st.Act != "fake-act 1.0" {
			t.Errorf("act = %v", st.Act)
		}
		t.Setenv("GITCITO_ACT_BIN", filepath.Join(t.TempDir(), "missing-act"))
		if st := m.Status(ctx); st.Act != nil {
			t.Errorf("missing act reported as %q", *st.Act)
		}
	})
	t.Run("reads the seeded per-commit verdicts", func(t *testing.T) {
		v := m.Verdicts(ctx, testutil.PlaygroundRepo(t, "local-ci"))
		ok := 0
		for _, e := range v {
			if e.OK {
				ok++
			}
			if e.Workflow != "ci.yml" {
				t.Errorf("workflow = %q", e.Workflow)
			}
		}
		if len(v) != 2 || ok != 1 {
			t.Errorf("verdicts = %+v", v)
		}
	})
	t.Run("records a verdict only on a clean tree", func(t *testing.T) {
		r := testutil.CloneFixture(t, "local-ci")
		clean, err := m.Record(ctx, r, "ci.yml", true)
		if err != nil || !clean.Recorded {
			t.Fatalf("clean = %+v, %v", clean, err)
		}
		if !m.Verdicts(ctx, r)[clean.SHA].OK {
			t.Error("clean verdict missing")
		}
		testutil.WriteFile(t, r, "dirty.txt", "wip")
		dirty, err := m.Record(ctx, r, "ci.yml", false)
		if err != nil || dirty.Recorded {
			t.Fatalf("dirty = %+v, %v", dirty, err)
		}
		// The clean verdict is untouched.
		if !m.Verdicts(ctx, r)[clean.SHA].OK {
			t.Error("clean verdict overwritten")
		}
	})
	t.Run("refuses workflow paths that escape .github/workflows", func(t *testing.T) {
		_, err := m.Run(ctx, testutil.PlaygroundRepo(t, "local-ci"), "../../evil.yml")
		if err == nil || !regexp.MustCompile(`(?i)workflow`).MatchString(err.Error()) {
			t.Errorf("err = %v", err)
		}
		if err.Error() != "Not a workflow filename: ../../evil.yml" {
			t.Errorf("message = %q", err.Error())
		}
	})
}

// ─── test/localCiSweep.test.ts ──────────────────────────────────────────────

func TestLocalCIAgainstACommitYouAreNotOn(t *testing.T) {
	testutil.IsolateGitEnv(t)
	fakeAct(t, "")
	ctx := context.Background()
	t.Run("runs in a throwaway worktree, pins the verdict to that sha, and cleans up", func(t *testing.T) {
		rec := &recorder{}
		m := NewManager(rec)
		r := testutil.CloneFixture(t, "local-ci")
		good, bad := seed(t, r)
		// HEAD sits on `bad`; run against `good`, which we are NOT on.
		res, err := m.RunAt(ctx, r, "ci.yml", good)
		if err != nil {
			t.Fatal(err)
		}
		if res.SHA != good || res.Exit == nil || *res.Exit != 0 || !res.OK || !res.Recorded {
			t.Errorf("res = %+v", res)
		}
		if !m.Verdicts(ctx, r)[good].OK {
			t.Error("verdict not recorded")
		}
		// The main working tree never moved and no worktree is left behind.
		if head := strings.TrimSpace(testutil.Git(t, r, "rev-parse", "HEAD")); head != bad {
			t.Errorf("HEAD moved to %s", head)
		}
		if n := strings.Count(testutil.Git(t, r, "worktree", "list", "--porcelain"), "worktree "); n != 1 {
			t.Errorf("%d worktrees", n)
		}
		// Output streamed, keyed by repo and sha.
		data := rec.all("localci:data")
		if len(data) == 0 {
			t.Fatal("no localci:data")
		}
		ev := data[0].(dataEvent)
		if ev.RepoPath != r || ev.SHA != good || !strings.Contains(ev.Chunk, "run in ") {
			t.Errorf("data = %+v", ev)
		}
	})
	t.Run("a commit whose tree fails CI records a failing verdict", func(t *testing.T) {
		m := NewManager(&recorder{})
		r := testutil.CloneFixture(t, "local-ci")
		_, bad := seed(t, r)
		res, err := m.RunAt(ctx, r, "ci.yml", bad)
		if err != nil || res.OK {
			t.Fatalf("res = %+v, %v", res, err)
		}
		if v, ok := m.Verdicts(ctx, r)[bad]; !ok || v.OK {
			t.Errorf("verdict = %+v", v)
		}
	})
	t.Run("rejects a sha that is not a commit", func(t *testing.T) {
		m := NewManager(&recorder{})
		r := testutil.CloneFixture(t, "local-ci")
		_, err := m.RunAt(ctx, r, "ci.yml", "not-a-rev")
		if err == nil || !strings.HasPrefix(err.Error(), "Command failed: git -C "+r+" rev-parse --verify not-a-rev^{commit}\n") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("resolveRange caps the list, reports the true total, and refuses option-like specs", func(t *testing.T) {
		m := NewManager(&recorder{})
		r := testutil.CloneFixture(t, "local-ci")
		seed(t, r)
		capped, err := m.ResolveRange(ctx, r, "HEAD", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(capped.SHAs) != 2 || capped.Total <= 2 || capped.Subjects[capped.SHAs[0]] != "test: drop ok.flag" {
			t.Errorf("capped = %+v", capped)
		}
		if _, err := m.ResolveRange(ctx, r, "--output=/tmp/x", 5); err == nil || !strings.Contains(err.Error(), "revision spec") {
			t.Errorf("err = %v", err)
		}
		if _, err := m.ResolveRange(ctx, r, "   ", 5); err == nil || err.Error() != "Not a revision spec:    " {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("sweeps a range sequentially and records one verdict per commit", func(t *testing.T) {
		rec := &recorder{}
		m := NewManager(rec)
		r := testutil.CloneFixture(t, "local-ci")
		good, bad := seed(t, r)
		res, err := m.Sweep(ctx, r, "ci.yml", []string{bad, good})
		if err != nil || res.Aborted || len(res.Results) != 2 || res.Results[0].OK || !res.Results[1].OK {
			t.Fatalf("res = %+v, %v", res, err)
		}
		v := m.Verdicts(ctx, r)
		if !v[good].OK || v[bad].OK {
			t.Errorf("verdicts = %+v", v)
		}
		prog := rec.all("localci:sweep-progress")
		if len(prog) != 4 {
			t.Fatalf("progress = %+v", prog)
		}
		if s := prog[0].(sweepStart); s.Phase != "start" || s.SHA != bad || s.Index != 0 || s.Total != 2 {
			t.Errorf("start = %+v", s)
		}
		if d := prog[3].(sweepDone); d.Phase != "done" || !d.OK || d.Exit == nil || *d.Exit != 0 || d.Index != 1 {
			t.Errorf("done = %+v", d)
		}
	})
}

func TestCancelStopsRunAndSweep(t *testing.T) {
	testutil.IsolateGitEnv(t)
	fakeAct(t, "echo started\nsleep 30\n")
	ctx := context.Background()
	r := testutil.CloneFixture(t, "local-ci")
	good, bad := seed(t, r)

	m := NewManager(&recorder{})
	done := make(chan *int)
	go func() {
		exit, _ := m.Run(ctx, r, "ci.yml")
		done <- exit
	}()
	time.Sleep(300 * time.Millisecond)
	m.Cancel(r)
	select {
	case exit := <-done:
		if exit != nil {
			t.Errorf("cancelled run exit = %d, want null", *exit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the run")
	}

	sweepDone := make(chan LocalCiSweepResult)
	go func() {
		res, _ := m.Sweep(ctx, r, "ci.yml", []string{bad, good, bad})
		sweepDone <- res
	}()
	time.Sleep(500 * time.Millisecond)
	m.Cancel(r)
	select {
	case res := <-sweepDone:
		if !res.Aborted || len(res.Results) != 1 || res.Results[0].Exit != nil {
			t.Errorf("sweep = %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the sweep")
	}
	if n := strings.Count(testutil.Git(t, r, "worktree", "list", "--porcelain"), "worktree "); n != 1 {
		t.Errorf("%d worktrees left", n)
	}
}

func TestMissingActRejects(t *testing.T) {
	testutil.IsolateGitEnv(t)
	t.Setenv("GITCITO_ACT_BIN", "gitcito-no-such-act")
	m := NewManager(&recorder{})
	_, err := m.Run(context.Background(), testutil.CloneFixture(t, "local-ci"), "ci.yml")
	if err == nil || err.Error() != "spawn gitcito-no-such-act ENOENT" {
		t.Errorf("err = %v", err)
	}
}

func TestNoteJSONIsJSONStringify(t *testing.T) {
	if got := noteJSON(true, "a&b<c>.yml", 1700000000000); got != `{"ok":true,"workflow":"a&b<c>.yml","at":1700000000000}` {
		t.Errorf("note = %s", got)
	}
}
