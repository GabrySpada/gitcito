package gitsvc

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/logbook"
)

type recordingEmitter struct {
	mu     sync.Mutex
	events []emitted
}

type emitted struct {
	name string
	data any
}

func (e *recordingEmitter) Emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, emitted{name, data})
}

type countingRecorder struct {
	mu     sync.Mutex
	events []logbook.ActivityEvent
}

func (r *countingRecorder) RecordEvent(ev logbook.ActivityEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func newTestService(t *testing.T) (*GitService, *logbook.Logbook, *countingRecorder, *recordingEmitter) {
	t.Helper()
	lb := logbook.New(t.TempDir())
	rec := &countingRecorder{}
	em := &recordingEmitter{}
	tokens := func(context.Context, string, string) string { return "" }
	return New(em, lb, WithActivityRecorder(rec), WithTokenFunc(tokens)), lb, rec, em
}

func TestEventForCall(t *testing.T) {
	tests := []struct {
		method string
		args   []any
		want   logbook.ActivityEvent
	}{
		{"commit", []any{"/r", "msg"}, logbook.EventCommit},
		{"commit", []any{"/r", "msg", false}, logbook.EventCommit},
		{"commit", []any{"/r", "msg", true}, logbook.EventAmend},
		{"commit", []any{"/r", "msg", "true"}, logbook.EventCommit}, // strictly === true
		{"previewRef", []any{"/r", "ref", "branch", "merge"}, logbook.EventMerge},
		{"previewRef", []any{"/r", "ref", "branch", "branch"}, logbook.EventFetch},
		{"previewRef", nil, logbook.EventFetch},
		{"push", nil, logbook.EventPush},
		{"pushAllTags", nil, logbook.EventPush},
		{"gitflowFinish", nil, logbook.EventMerge},
		{"conflictTakeSide", nil, logbook.EventConflictResolved},
		{"initHere", nil, logbook.EventInit},
		{"open", nil, logbook.EventRepoOpen},
		{"status", nil, ""},
		{"log", nil, ""},
	}
	for _, tt := range tests {
		if got := eventForCall(tt.method, tt.args); got != tt.want {
			t.Errorf("eventForCall(%s, %v) = %q, want %q", tt.method, tt.args, got, tt.want)
		}
	}
	if len(eventForMethod) != 27 {
		t.Errorf("eventForMethod has %d entries; EVENT_FOR_METHOD has 27", len(eventForMethod))
	}
}

func TestCallLogsAndRecords(t *testing.T) {
	ctx := context.Background()
	s, lb, rec, _ := newTestService(t)

	// A logged write succeeds: activity counted, ok entry with repo name.
	if err := lockedErr(s, ctx, "push", "/repos/widget", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// A failure: credentials redacted and colours stripped, in the error and the log.
	raw := errors.New("\x1b[31mfatal: unable to access 'https://oauth2:ghp_SECRET@github.com/a/b/'\x1b[0m")
	err := lockedErr(s, ctx, "pull", "/repos/widget", func(context.Context) error { return raw })
	wantMsg := "fatal: unable to access 'https://***@github.com/a/b/'"
	if err == nil || err.Error() != wantMsg || !errors.Is(err, raw) {
		t.Fatalf("err = %v", err)
	}
	var ce *CallError
	if !errors.As(err, &ce) || ce.Cause != raw {
		t.Fatalf("CallError = %+v", ce)
	}
	// An unlogged read records nothing.
	if _, err := locked(s, ctx, "status", "/repos/widget", func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	// commit's amend flag picks the event from the TS positional args.
	if err := lockedErr(s, ctx, "commit", "/repos/widget", func(context.Context) error { return nil }, "/repos/widget", "m", true); err != nil {
		t.Fatal(err)
	}
	// App-level: no repo path in the log even though one was passed.
	if _, err := locked(s, ctx, "clone", "/parent", func(context.Context) (string, error) { return "/parent/x", nil }); err != nil {
		t.Fatal(err)
	}

	entries := lb.Entries()
	type row struct {
		ev       logbook.ActivityEvent
		repo     string
		name     string
		ok       bool
		errorMsg string
	}
	var got []row
	for _, e := range entries {
		got = append(got, row{e.Event, e.RepoPath, e.RepoName, e.Ok, e.Error})
	}
	want := []row{
		{logbook.EventPush, "/repos/widget", "widget", true, ""},
		{logbook.EventPull, "/repos/widget", "widget", false, wantMsg},
		{logbook.EventAmend, "/repos/widget", "widget", true, ""},
		{logbook.EventClone, "", "", true, ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("log = %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(rec.events, []logbook.ActivityEvent{logbook.EventPush, logbook.EventAmend, logbook.EventClone}) {
		t.Fatalf("activity = %v (failures must not count)", rec.events)
	}
}

func TestCallRecoversPanics(t *testing.T) {
	s, lb, _, _ := newTestService(t)
	_, err := locked(s, context.Background(), "merge", "/r/x", func(context.Context) (int, error) {
		panic("boom")
	})
	if err == nil || !IsPanic(err) || err.Error() != "internal error in git method merge: boom" {
		t.Fatalf("err = %v", err)
	}
	if e := lb.Entries(); len(e) != 1 || e[0].Ok || e[0].Event != logbook.EventMerge {
		t.Fatalf("log = %+v", e)
	}
	// The lock was released despite the panic.
	if err := lockedErr(s, context.Background(), "merge", "/r/x", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestCallRetriesGitLockErrors(t *testing.T) {
	s, _, _, _ := newTestService(t)
	calls := 0
	got, err := locked(s, context.Background(), "checkout", "/r", func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("fatal: Unable to create '/r/.git/index.lock': File exists.")
		}
		return "done", nil
	})
	if err != nil || got != "done" || calls != 3 {
		t.Fatalf("got=%q err=%v calls=%d", got, err, calls)
	}
}

// Reads on one repo overlap; a write excludes everything on that repo but not
// on another repo; app-level and no-repo methods never wait.
func TestCallLocking(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx := context.Background()

	// Two reads overlap.
	var inside, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = lockedErr(s, ctx, "log", "/r", func(context.Context) error {
				n := inside.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	if peak.Load() < 2 {
		t.Fatalf("reads did not overlap (peak %d)", peak.Load())
	}

	// While a write holds /r: a read on /r waits, a read on /other and
	// app-level/no-repo calls do not.
	hold := make(chan struct{})
	entered := make(chan struct{})
	go func() {
		_ = lockedErr(s, ctx, "commit", "/r", func(context.Context) error {
			close(entered)
			<-hold
			return nil
		})
	}()
	<-entered

	readDone := make(chan struct{})
	go func() {
		_ = lockedErr(s, ctx, "status", "/r", func(context.Context) error { return nil })
		close(readDone)
	}()
	select {
	case <-readDone:
		t.Fatal("a read ran during a write on the same repo")
	case <-time.After(50 * time.Millisecond):
	}
	quick := func(name string, f func() error) {
		done := make(chan error, 1)
		go func() { done <- f() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s waited on another repo's write", name)
		}
	}
	quick("other repo", func() error {
		return lockedErr(s, ctx, "commit", "/other", func(context.Context) error { return nil })
	})
	quick("app-level with the busy path", func() error {
		_, err := locked(s, ctx, "remoteBranches", "/r", func(context.Context) ([]string, error) { return nil, nil })
		return err
	})
	quick("no-repo method", func() error {
		_, err := lockedNoRepo(s, ctx, "version", func(context.Context) (string, error) { return "2.43.0", nil })
		return err
	})
	// Unknown method = write: it must wait too.
	unknownDone := make(chan struct{})
	go func() {
		_ = lockedErr(s, ctx, "brandNewMethod", "/r", func(context.Context) error { return nil })
		close(unknownDone)
	}()
	select {
	case <-unknownDone:
		t.Fatal("an unclassified method ran during a write")
	case <-time.After(50 * time.Millisecond):
	}
	close(hold)
	<-readDone
	<-unknownDone
}

func TestCallCancelledWhileQueued(t *testing.T) {
	s, lb, _, _ := newTestService(t)
	hold := make(chan struct{})
	entered := make(chan struct{})
	go func() {
		_ = lockedErr(s, context.Background(), "commit", "/r", func(context.Context) error {
			close(entered)
			<-hold
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ran := false
	err := lockedErr(s, ctx, "push", "/r", func(context.Context) error { ran = true; return nil })
	close(hold)
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
	time.Sleep(20 * time.Millisecond)
	for _, e := range lb.Entries() {
		if e.Event == logbook.EventPush {
			t.Fatal("a call that never ran was logged")
		}
	}
}

func TestEventHooks(t *testing.T) {
	s, _, _, em := newTestService(t)
	s.cloneProgress()(CloneProgress{Stage: "receiving", Progress: 40, Processed: 4, Total: 10})
	s.bisectOutput()("Bisecting: 3 revisions left\n")
	want := []emitted{
		{EventCloneProgress, CloneProgress{Stage: "receiving", Progress: 40, Processed: 4, Total: 10}},
		{EventBisectOutput, "Bisecting: 3 revisions left\n"},
	}
	if !reflect.DeepEqual(em.events, want) {
		t.Fatalf("events = %+v", em.events)
	}
	// A service without an emitter or a logbook works (and drops both).
	bare := New(nil, nil)
	bare.cloneProgress()(CloneProgress{})
	if err := lockedErr(bare, context.Background(), "push", "/r", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if s.token == nil || bare.token != nil {
		t.Fatal("WithTokenFunc not applied")
	}
	var _ gitx.TokenFunc = s.token
}

func TestLockedReturnsZeroOnError(t *testing.T) {
	s, _, _, _ := newTestService(t)
	got, err := locked(s, context.Background(), "log", "/r", func(context.Context) ([]string, error) {
		return []string{"partial"}, errors.New("nope")
	})
	if err == nil || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
	gotV, err := lockedNoRepo(s, context.Background(), "version", func(context.Context) (string, error) {
		return "x", errors.New("no git")
	})
	if err == nil || gotV != "" {
		t.Fatalf("got=%v err=%v", gotV, err)
	}
}
