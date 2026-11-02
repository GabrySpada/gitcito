package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// The vectors are written from the TypeScript implementation by
// test/port/processes.test.ts: testdata/vectors/launch.json (pure helpers) and
// internal/launch/testdata/{discover,run}.json (discovery over generated trees,
// and launch:run / launch:runTasks driven through the real handlers with a fake
// node-pty).

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// canon round-trips v through JSON so Go structs and decoded vectors compare
// as the same plain values.
func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameJSON(t *testing.T, label string, got, want any) {
	t.Helper()
	g, w := canon(t, got), canon(t, want)
	if !reflect.DeepEqual(g, w) {
		gb, _ := json.MarshalIndent(g, "", "  ")
		wb, _ := json.MarshalIndent(w, "", "  ")
		t.Errorf("%s:\n got %s\nwant %s", label, gb, wb)
	}
}

// normalize replaces machine paths with the vector placeholders.
func normalize(t *testing.T, v any, root string) any {
	t.Helper()
	b, _ := json.Marshal(v)
	s := string(b)
	esc := func(p string) string { q, _ := json.Marshal(p); return string(q[1 : len(q)-1]) }
	if root != "" {
		s = strings.ReplaceAll(s, esc(root), "<ROOT>")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, esc(home), "<HOME>")
	}
	var out any
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func expand(t *testing.T, raw json.RawMessage, root string, into any) {
	t.Helper()
	esc, _ := json.Marshal(root)
	s := strings.ReplaceAll(string(raw), "<ROOT>", string(esc[1:len(esc)-1]))
	if err := json.Unmarshal([]byte(s), into); err != nil {
		t.Fatal(err)
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPureVectors(t *testing.T) {
	var v struct {
		ExtractDevToolsURL []struct {
			Input string          `json:"input"`
			Out   json.RawMessage `json:"out"`
		} `json:"extractDevToolsUrl"`
		ResolveInputTokens []struct {
			Value  any               `json:"value"`
			Values map[string]string `json:"values"`
			Out    any               `json:"out"`
		} `json:"resolveInputTokens"`
		IsLoopbackURL []struct {
			Input string `json:"input"`
			Out   bool   `json:"out"`
		} `json:"isLoopbackUrl"`
	}
	readJSON(t, filepath.Join("..", "..", "testdata", "vectors", "launch.json"), &v)
	if len(v.ExtractDevToolsURL) == 0 || len(v.ResolveInputTokens) == 0 || len(v.IsLoopbackURL) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.ExtractDevToolsURL {
		var want any
		_ = json.Unmarshal(c.Out, &want)
		got := ExtractDevToolsURL(c.Input)
		if got == nil {
			sameJSON(t, "extractDevToolsUrl("+c.Input+")", nil, want)
		} else {
			sameJSON(t, "extractDevToolsUrl("+c.Input+")", got, want)
		}
	}
	for i, c := range v.ResolveInputTokens {
		sameJSON(t, "resolveInputTokens #"+string(rune('0'+i)), ResolveInputTokens(c.Value, c.Values), c.Out)
	}
	for _, c := range v.IsLoopbackURL {
		if got := IsLoopbackURL(c.Input); got != c.Out {
			t.Errorf("IsLoopbackURL(%q) = %v, want %v", c.Input, got, c.Out)
		}
	}
}

func TestDiscoverVectors(t *testing.T) {
	var v struct {
		Cases []struct {
			Name   string            `json:"name"`
			Files  map[string]string `json:"files"`
			Groups any               `json:"groups"`
		} `json:"cases"`
		Playground any `json:"playground"`
	}
	readJSON(t, filepath.Join("testdata", "discover.json"), &v)
	for _, c := range v.Cases {
		root := writeTree(t, c.Files)
		sameJSON(t, c.Name, normalize(t, DiscoverLaunch(root), root), c.Groups)
	}
	pg := testutil.PlaygroundRepo(t, "launch-configs")
	sameJSON(t, "playground", normalize(t, DiscoverLaunch(pg), pg), v.Playground)
}

// ─── fakes for the run vectors ─────────────────────────────────────────────

type recEvent struct {
	Ch   string `json:"ch"`
	Data any    `json:"data"`
}

type recorder struct {
	mu     sync.Mutex
	events []recEvent
}

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, recEvent{name, data})
	r.mu.Unlock()
}

func (r *recorder) take() []recEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

type fakeProc struct{ spec spawnSpec }

func (f *fakeProc) Write(string) error      { return nil }
func (f *fakeProc) Resize(int, int) error   { return nil }
func (f *fakeProc) Kill()                   {}
func (f *fakeProc) Signal(bool)             {}
func (f *fakeProc) feed(d string)           { f.spec.OnData(d) }
func (f *fakeProc) exit(code int)           { f.spec.OnExit(code) }
func (f *fakeProc) envDiff() map[string]any { return envDiff(f.spec.Env) }

func envDiff(env []string) map[string]any {
	out := map[string]any{}
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k, val := kv[:i], kv[i+1:]
		if cur, ok := os.LookupEnv(k); !ok || cur != val {
			out[k] = val
		}
	}
	return out
}

type fakeStarter struct {
	procs []*fakeProc
}

func (f *fakeStarter) start(spec spawnSpec) (proc, error) {
	p := &fakeProc{spec: spec}
	f.procs = append(f.procs, p)
	return p, nil
}

var idSuffix = regexp.MustCompile(`:\d+$`)

func normalizeEvents(evs []recEvent) []recEvent {
	out := make([]recEvent, len(evs))
	for i, e := range evs {
		out[i] = recEvent{Ch: idSuffix.ReplaceAllString(e.Ch, ":<id>"), Data: e.Data}
	}
	return out
}

func linuxHost() *host {
	h := defaultHost()
	h.goos, h.sep = "linux", "/"
	return &h
}

func TestRunVectors(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("GITCITO_VEC", "from-env")
	os.Unsetenv("GITCITO_UNSET_VAR")
	var v struct {
		Run []struct {
			Name    string            `json:"name"`
			Files   map[string]string `json:"files"`
			Payload json.RawMessage   `json:"payload"`
			Error   *string           `json:"error"`
			Spawn   any               `json:"spawn"`
			Sent    []recEvent        `json:"sent"`
		} `json:"run"`
		RunTasks []struct {
			Name    string          `json:"name"`
			Payload RunTasksPayload `json:"payload"`
			Error   *string         `json:"error"`
			Spawn   any             `json:"spawn"`
			Sent    []recEvent      `json:"sent"`
		} `json:"runTasks"`
		Stream []struct {
			Name   string       `json:"name"`
			Config LaunchConfig `json:"config"`
			Steps  []struct {
				Data   *string `json:"data"`
				Attach bool    `json:"attach"`
				Exit   *int    `json:"exit"`
			} `json:"steps"`
			Events []recEvent `json:"events"`
			Opened []string   `json:"opened"`
		} `json:"stream"`
		Backlog struct {
			Chunks     int    `json:"chunks"`
			ChunkLen   int    `json:"chunkLen"`
			ReplayLen  int    `json:"replayLen"`
			ReplayHead string `json:"replayHead"`
		} `json:"backlog"`
	}
	readJSON(t, filepath.Join("testdata", "run.json"), &v)
	if len(v.Run) == 0 || len(v.RunTasks) == 0 || len(v.Stream) == 0 {
		t.Fatal("no vectors")
	}

	spawnRecord := func(p *fakeProc) map[string]any {
		return map[string]any{
			"file": p.spec.Name, "args": p.spec.Args, "cwd": p.spec.Dir,
			"cols": p.spec.Cols, "rows": p.spec.Rows, "name": "xterm-256color", "env": p.envDiff(),
		}
	}

	for _, c := range v.Run {
		t.Run(c.Name, func(t *testing.T) {
			root := ""
			if c.Files != nil || strings.Contains(string(c.Payload), "<ROOT>") {
				root = writeTree(t, c.Files)
			}
			var payload RunPayload
			expand(t, c.Payload, root, &payload)
			rec, fs := &recorder{}, &fakeStarter{}
			m := NewManager(rec, Options{start: fs.start, h: linuxHost()})
			res := m.Run(payload)
			gotErr := (*string)(nil)
			if res.Error != "" {
				gotErr = &res.Error
			}
			sameJSON(t, "error", gotErr, c.Error)
			var spawn any
			if len(fs.procs) > 0 {
				spawn = normalize(t, spawnRecord(fs.procs[0]), root)
			}
			sameJSON(t, "spawn", spawn, c.Spawn)
			sameJSON(t, "sent", normalize(t, normalizeEvents(rec.take()), root), normalizeEvents(c.Sent))
		})
	}

	for _, c := range v.RunTasks {
		t.Run("tasks/"+c.Name, func(t *testing.T) {
			rec, fs := &recorder{}, &fakeStarter{}
			m := NewManager(rec, Options{start: fs.start, h: linuxHost()})
			res := m.RunTasks(c.Payload)
			gotErr := (*string)(nil)
			if res.Error != "" {
				gotErr = &res.Error
			}
			sameJSON(t, "error", gotErr, c.Error)
			var spawn any
			if len(fs.procs) > 0 {
				spawn = spawnRecord(fs.procs[0])
			}
			sameJSON(t, "spawn", spawn, c.Spawn)
			sameJSON(t, "sent", normalizeEvents(rec.take()), normalizeEvents(c.Sent))
		})
	}

	for _, c := range v.Stream {
		t.Run("stream/"+c.Name, func(t *testing.T) {
			rec, fs := &recorder{}, &fakeStarter{}
			var opened []string
			m := NewManager(rec, Options{
				start: fs.start, h: linuxHost(),
				OpenURL: func(u string) error { opened = append(opened, u); return nil },
			})
			res := m.Run(RunPayload{Dir: "/w", Config: c.Config, Tasks: []LaunchTask{}, Cols: 80, Rows: 24})
			p := fs.procs[0]
			for _, s := range c.Steps {
				if s.Data != nil {
					p.feed(*s.Data)
				}
				if s.Attach {
					m.Attach(res.ID)
				}
				if s.Exit != nil {
					p.exit(*s.Exit)
				}
			}
			sameJSON(t, "events", normalizeEvents(rec.take()), c.Events)
			if opened == nil {
				opened = []string{}
			}
			sameJSON(t, "opened", opened, c.Opened)
		})
	}

	t.Run("backlog keeps the tail", func(t *testing.T) {
		rec, fs := &recorder{}, &fakeStarter{}
		m := NewManager(rec, Options{start: fs.start, h: linuxHost()})
		res := m.Run(RunPayload{Dir: "/w", Config: LaunchConfig{"name": "Big", "command": "yes"}, Cols: 80, Rows: 24})
		line := strings.Repeat("x", 1023) + "\n"
		for i := 0; i < v.Backlog.Chunks; i++ {
			s := "000" + itoa(i)
			fs.procs[0].feed(s[len(s)-4:] + line)
		}
		rec.take()
		m.Attach(res.ID)
		evs := rec.take()
		replay := evs[0].Data.(string)
		if len(replay) != v.Backlog.ReplayLen || replay[:40] != v.Backlog.ReplayHead {
			t.Errorf("replay len %d head %q, want %d %q", len(replay), replay[:40], v.Backlog.ReplayLen, v.Backlog.ReplayHead)
		}
	})
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
