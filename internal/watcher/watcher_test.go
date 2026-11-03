package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	events []Changed
	ch     chan Changed
}

func newRecorder() *recorder { return &recorder{ch: make(chan Changed, 64)} }

func (r *recorder) Emit(name string, data any) {
	if name != EventChanged {
		return
	}
	c := data.(Changed)
	r.mu.Lock()
	r.events = append(r.events, c)
	r.mu.Unlock()
	r.ch <- c
}

// next waits for one event, or fails.
func (r *recorder) next(t *testing.T) Changed {
	t.Helper()
	select {
	case c := <-r.ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no repo:changed event")
	}
	return Changed{}
}

// none asserts that nothing arrives within a few debounce windows.
func (r *recorder) none(t *testing.T) {
	t.Helper()
	select {
	case c := <-r.ch:
		t.Fatalf("unexpected event %+v", c)
	case <-time.After(300 * time.Millisecond):
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{".git/refs/heads", ".git/objects/ab", "src", "node_modules/pkg"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, ".git/HEAD", "ref: refs/heads/main\n")
	write(t, dir, "src/a.txt", "a\n")
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func watching(t *testing.T, dir string) (*Watcher, *recorder) {
	t.Helper()
	rec := newRecorder()
	w := New(rec, nil)
	w.debounce = 50 * time.Millisecond
	w.Set(dir)
	t.Cleanup(w.Close)
	// Let the kernel watches settle before the test writes.
	time.Sleep(50 * time.Millisecond)
	return w, rec
}

func TestClassification(t *testing.T) {
	for _, c := range []struct {
		rel         string
		noise, meta bool
	}{
		{"src/a.txt", false, false},
		{".git", false, true},
		{".git/HEAD", false, true},
		{".git/refs/heads/main", false, true},
		{".git/objects/ab/cdef", true, true},
		{".git/index.lock", true, true},
		{"node_modules/pkg/index.js", true, false},
		{"node_modules", false, false},
		{`.git\HEAD`, false, true},
		{`web\node_modules\x`, true, false},
		{"sub/.git/HEAD", false, false},
	} {
		if isNoise(c.rel) != c.noise || isGitMeta(c.rel) != c.meta {
			t.Errorf("%s: noise=%v meta=%v", c.rel, isNoise(c.rel), isGitMeta(c.rel))
		}
	}
}

func TestWorkingTreeChangeIsLight(t *testing.T) {
	dir := repo(t)
	_, rec := watching(t, dir)
	write(t, dir, "src/a.txt", "changed\n")
	if c := rec.next(t); c.Path != dir || !c.Light {
		t.Fatalf("event = %+v", c)
	}
}

func TestGitChangeIsHeavy(t *testing.T) {
	dir := repo(t)
	_, rec := watching(t, dir)
	write(t, dir, "src/a.txt", "changed\n")
	write(t, dir, ".git/refs/heads/main", "0123\n")
	// One debounced event, heavy because .git moved inside the window.
	if c := rec.next(t); c.Light {
		t.Fatalf("event = %+v", c)
	}
	rec.none(t)
	// The flag resets after it fired.
	write(t, dir, "src/a.txt", "again\n")
	if c := rec.next(t); !c.Light {
		t.Fatalf("event = %+v", c)
	}
}

func TestNoiseIsIgnored(t *testing.T) {
	dir := repo(t)
	_, rec := watching(t, dir)
	write(t, dir, ".git/objects/ab/cdef", "blob")
	write(t, dir, ".git/index.lock", "x")
	write(t, dir, "node_modules/pkg/index.js", "x")
	rec.none(t)
}

func TestDebounceCoalesces(t *testing.T) {
	dir := repo(t)
	_, rec := watching(t, dir)
	for i := 0; i < 10; i++ {
		write(t, dir, "src/a.txt", string(rune('a'+i)))
		time.Sleep(5 * time.Millisecond)
	}
	rec.next(t)
	rec.none(t)
}

func TestMute(t *testing.T) {
	dir := repo(t)
	w, rec := watching(t, dir)
	w.Mute("/some/other/repo", 10_000) // another path: no effect
	w.Mute(dir, 10_000)
	write(t, dir, "src/a.txt", "muted\n")
	rec.none(t)
	w.Mute(dir, -5) // negative clamps to now: unmuted
	write(t, dir, "src/a.txt", "heard\n")
	rec.next(t)
}

func TestNewDirectoriesAreWatched(t *testing.T) {
	dir := repo(t)
	_, rec := watching(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "fresh", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec.next(t) // the mkdir itself
	time.Sleep(50 * time.Millisecond)
	write(t, dir, "fresh/deeper/x.txt", "x")
	if c := rec.next(t); !c.Light {
		t.Fatalf("event = %+v", c)
	}
}

func TestSwitchingAndStopping(t *testing.T) {
	a, b := repo(t), repo(t)
	w, rec := watching(t, a)
	w.Set(a) // same path: no-op, keeps the watch
	w.Set(b)
	time.Sleep(50 * time.Millisecond)
	write(t, a, "src/a.txt", "old repo\n")
	rec.none(t)
	write(t, b, "src/a.txt", "new repo\n")
	if c := rec.next(t); c.Path != b {
		t.Fatalf("event = %+v", c)
	}
	s := NewService(w)
	s.Repo(context.Background(), nil)
	write(t, b, "src/a.txt", "stopped\n")
	rec.none(t)
}

func TestMissingRepoFallsBackOrGivesUp(t *testing.T) {
	rec := newRecorder()
	w := New(rec, nil)
	w.Set(filepath.Join(t.TempDir(), "nope"))
	w.mu.Lock()
	active := w.active
	w.mu.Unlock()
	if active != nil {
		t.Fatal("watching a folder that does not exist")
	}
	w.Close()
}

// Many goroutines driving one watcher, for the race detector.
func TestConcurrentUse(t *testing.T) {
	a, b := repo(t), repo(t)
	rec := newRecorder()
	go func() {
		for range rec.ch {
		}
	}()
	w := New(rec, nil)
	w.debounce = 5 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				switch (i + j) % 4 {
				case 0:
					w.Set(a)
				case 1:
					w.Set(b)
				case 2:
					w.Mute(a, 1)
				default:
					write(t, a, "src/a.txt", "x")
				}
			}
		}(i)
	}
	wg.Wait()
	w.Close()
	_ = NewService(w).ServiceShutdown(context.Background())
}
