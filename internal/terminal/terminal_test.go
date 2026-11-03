//go:build !windows

package terminal

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder collects emitted events in order.
type recorder struct {
	mu     sync.Mutex
	events []event
}

type event struct {
	name string
	data any
}

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, event{name, data})
	r.mu.Unlock()
}

// output concatenates every data event for name.
func (r *recorder) output(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, e := range r.events {
		if e.name == name {
			b.WriteString(e.data.(string))
		}
	}
	return b.String()
}

func (r *recorder) has(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.name == name {
			return true
		}
	}
	return false
}

// waitFor polls until cond holds or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func shManager(rec *recorder, pipe bool) *Manager {
	return NewManager(rec, Options{
		Shell:     func() (string, []string) { return "/bin/sh", nil },
		ForcePipe: pipe,
	})
}

func TestPTYEchoResizeProcNameAndKill(t *testing.T) {
	rec := &recorder{}
	m := shManager(rec, false)
	dir := t.TempDir()
	id, err := m.Create(dir, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.KillAll)
	data, exit := "term:data:1", "term:exit:1"
	if id != 1 {
		t.Fatalf("first id = %d", id)
	}
	m.Input(id, "echo hello-$((40+2)); echo TERM=$TERM; pwd\r")
	waitFor(t, "echo output", func() bool { return strings.Contains(rec.output(data), "hello-42") })
	waitFor(t, "TERM", func() bool { return strings.Contains(rec.output(data), "TERM=xterm-256color") })
	waitFor(t, "cwd", func() bool { return strings.Contains(rec.output(data), dir) })

	m.Resize(id, 0, 10) // ignored, must not break the session
	m.Resize(id, 91, 27)
	m.Input(id, "stty size\r")
	waitFor(t, "resized", func() bool { return strings.Contains(rec.output(data), "27 91") })

	if name := m.ProcName(id); !strings.HasSuffix(name, "sh") {
		t.Errorf("ProcName = %q, want the shell", name)
	}
	m.Input(id, "sleep 30\r")
	waitFor(t, "foreground sleep", func() bool { return m.ProcName(id) == "sleep" })

	m.Kill(id)
	waitFor(t, "exit event", func() bool { return rec.has(exit) })
	if m.ProcName(id) != "" {
		t.Error("killed session still answers ProcName")
	}
	// Calls on a dead session are no-ops.
	m.Input(id, "x")
	m.Resize(id, 80, 24)
	m.Kill(id)
}

func TestPTYNaturalExitEmitsExitAfterAllData(t *testing.T) {
	rec := &recorder{}
	m := shManager(rec, false)
	id, err := m.Create(t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	m.Input(id, "printf 'last-words\\n'; exit 3\r")
	waitFor(t, "exit", func() bool { return rec.has("term:exit:1") })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	last := rec.events[len(rec.events)-1]
	if last.name != "term:exit:1" || last.data != nil {
		t.Fatalf("last event = %+v, want term:exit:1 with no payload", last)
	}
	var out strings.Builder
	for _, e := range rec.events {
		if e.name == "term:data:1" {
			out.WriteString(e.data.(string))
		}
	}
	if !strings.Contains(out.String(), "last-words") {
		t.Errorf("output lost before exit: %q", out.String())
	}
	if m.get(id) != nil {
		t.Error("exited session not forgotten")
	}
}

func TestPTYKillTakesTheProcessGroup(t *testing.T) {
	rec := &recorder{}
	m := shManager(rec, false)
	id, err := m.Create(t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir() + "/alive"
	// A background job that would keep writing if it survived the kill.
	m.Input(id, "(while true; do date > "+marker+"; sleep 0.1; done) & echo started\r")
	waitFor(t, "started", func() bool { return strings.Contains(rec.output("term:data:1"), "started") })
	waitFor(t, "job writing", func() bool { return !statOrZero(marker).IsZero() })
	m.Kill(id)
	waitFor(t, "exit", func() bool { return rec.has("term:exit:1") })
	time.Sleep(300 * time.Millisecond)
	info1 := statOrZero(marker)
	time.Sleep(500 * time.Millisecond)
	if info2 := statOrZero(marker); !info2.Equal(info1) {
		t.Error("background job outlived the terminal")
	}
}

func TestFallbackLineEditor(t *testing.T) {
	rec := &recorder{}
	m := shManager(rec, true)
	dir := t.TempDir()
	id, err := m.Create(dir, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.KillAll)
	out := rec.output("term:data:1")
	if !strings.Contains(out, "basic shell mode") || !strings.Contains(out, "\x1b[36m"+dir+"\x1b[0m $ ") {
		t.Fatalf("banner = %q", out)
	}
	// Typed characters echo locally; backspace erases; ^C clears the line.
	m.Input(id, "echo nope\x03")
	m.Input(id, "echo fallbacX\x7fk-ok\r")
	waitFor(t, "line ran", func() bool { return strings.Contains(rec.output("term:data:1"), "fallback-ok\r\n") })
	got := rec.output("term:data:1")
	for _, want := range []string{"^C\r\n$ ", "\b \b", "X"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if name := m.ProcName(id); name != "sh" {
		t.Errorf("fallback ProcName = %q", name)
	}
	m.Input(id, "exit 0\r")
	waitFor(t, "exit", func() bool { return rec.has("term:exit:1") })
}

func TestCRLF(t *testing.T) {
	tests := map[string]string{"a\nb": "a\r\nb", "a\r\nb": "a\r\nb", "\n": "\r\n", "x": "x", "\r\n\n": "\r\n\r\n"}
	for in, want := range tests {
		if got := toCRLF(in); got != want {
			t.Errorf("toCRLF(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateFailsForMissingShell(t *testing.T) {
	m := NewManager(&recorder{}, Options{Shell: func() (string, []string) { return "/nonexistent/shell", nil }})
	if _, err := m.Create(t.TempDir(), 80, 24); err == nil {
		t.Fatal("expected an error")
	}
	if len(m.sessions) != 0 {
		t.Error("failed session kept")
	}
}

func statOrZero(p string) time.Time {
	info, err := osStat(p)
	if err != nil {
		return time.Time{}
	}
	return info
}
