package logbook

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedClock(ms int64) func() time.Time {
	return func() time.Time { return time.UnixMilli(ms) }
}

func TestRecordAndFormat(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	l.now = fixedClock(1700000000123)
	l.Record(EventPush, "/repos/widget", true, "")
	l.Record(EventPull, "/repos/widget/", false, "fatal: <html> & stuff")
	l.Record(EventClone, "", true, "")
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// JSON.stringify(entries, null, 2): no trailing newline, no HTML escaping,
	// `error` absent when there is none.
	want := `[
  {
    "ts": 1700000000123,
    "repoPath": "/repos/widget",
    "repoName": "widget",
    "event": "push",
    "ok": true
  },
  {
    "ts": 1700000000123,
    "repoPath": "/repos/widget/",
    "repoName": "widget",
    "event": "pull",
    "ok": false,
    "error": "fatal: <html> & stuff"
  },
  {
    "ts": 1700000000123,
    "repoPath": "",
    "repoName": "",
    "event": "clone",
    "ok": true
  }
]`
	if string(got) != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, FileName))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", st.Mode().Perm())
		}
	}

	// A fresh Logbook reads it back.
	again := New(dir)
	if e := again.Entries(); len(e) != 3 || e[1].Error != "fatal: <html> & stuff" || e[2].Event != EventClone {
		t.Fatalf("reloaded = %+v", e)
	}
}

func TestErrorRedactedThenTruncated(t *testing.T) {
	l := New(t.TempDir())
	long := strings.Repeat("é", 498) + "https://oauth2:ghp_SECRETSECRET@github.com/x"
	l.Record(EventPush, "/r", false, long)
	e := l.Entries()[0]
	if strings.Contains(e.Error, "ghp_") || strings.Contains(e.Error, "SECRET") {
		t.Fatalf("secret leaked: %q", e.Error)
	}
	if got := len([]rune(e.Error)); got != MaxErrorLen {
		t.Fatalf("error is %d units, want %d", got, MaxErrorLen)
	}
	l.Record(EventPush, "/r", false, strings.Repeat("😀", 300)) // 600 UTF-16 units
	if e := l.Entries()[1]; len([]rune(e.Error)) != 250 {
		t.Fatalf("astral truncation = %d runes", len([]rune(e.Error)))
	}
}

func TestCapAndClear(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	for i := 0; i < MaxEntries+25; i++ {
		l.now = fixedClock(int64(i))
		l.Record(EventFetch, "/r", true, "")
	}
	e := l.Entries()
	if len(e) != MaxEntries || e[0].Ts != 25 || e[len(e)-1].Ts != MaxEntries+24 {
		t.Fatalf("len=%d first=%d last=%d", len(e), e[0].Ts, e[len(e)-1].Ts)
	}
	// Entries is a copy.
	e[0].Ts = -1
	if l.Entries()[0].Ts == -1 {
		t.Fatal("Entries exposed internal state")
	}
	if c := l.Clear(); c == nil || len(c) != 0 {
		t.Fatalf("Clear = %#v", c)
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, FileName)); string(b) != "[]" {
		t.Fatalf("file after clear = %q", b)
	}
}

func TestLoadTolerance(t *testing.T) {
	tests := []struct {
		name, content string
		want          int
	}{
		{"missing", "", 0},
		{"not json", "{oops", 0},
		{"not an array", `{"ts":1}`, 0},
		{"null", "null", 0},
		{"array", `[{"ts":1,"repoPath":"/a","repoName":"a","event":"push","ok":true},{"ts":"bad"}]`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != "" {
				if err := os.WriteFile(filepath.Join(dir, FileName), []byte(tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			e := New(dir).Entries()
			if e == nil || len(e) != tt.want {
				t.Fatalf("entries = %#v", e)
			}
		})
	}
}

func TestDebouncedSave(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	l.Record(EventCommit, "/r", true, "")
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Fatal("written before the debounce")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(filepath.Join(dir, FileName)); err == nil && strings.Contains(string(b), `"commit"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("debounced save never happened")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Flush on an untouched logbook writes nothing.
	empty := t.TempDir()
	if err := New(empty).Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, FileName)); err == nil {
		t.Fatal("Flush created a file nobody asked for")
	}
}

func TestConcurrentRecord(t *testing.T) {
	l := New(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Record(EventStash, "/r", true, "")
			_ = l.Entries()
		}()
	}
	wg.Wait()
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := len(New(filepath.Dir(l.Path())).Entries()); n != 50 {
		t.Fatalf("persisted %d entries", n)
	}
}

func TestBasename(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b": "b", "/a/b/": "b", "b": "b", "/": "", "": "", "/a/b//": "b",
	} {
		if got := basename(in); got != want {
			t.Errorf("basename(%q) = %q, want %q", in, got, want)
		}
	}
}
