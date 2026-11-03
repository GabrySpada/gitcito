package launch

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// ─── test/devtoolsUrl.test.ts ───────────────────────────────────────────────

func TestDevToolsAnnouncedOnOutput(t *testing.T) {
	t.Run("reads the Flutter DevTools line", func(t *testing.T) {
		line := "The Flutter DevTools debugger and profiler on iPhone 16 Pro is available at: " +
			"http://127.0.0.1:9100?uri=http://127.0.0.1:53412/uJ8k=/"
		got := ExtractDevToolsURL(line)
		want := &DevToolsFound{URL: "http://127.0.0.1:9100?uri=http://127.0.0.1:53412/uJ8k=/", Name: "Flutter DevTools"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("reads the other tools, naming each one", func(t *testing.T) {
		for line, name := range map[string]string{
			"Serving DevTools at http://127.0.0.1:9101/":                  "Dart DevTools",
			"Vue Devtools listening on http://localhost:8098":             "Vue DevTools",
			"Prisma Studio is up on http://localhost:5555":                "Prisma Studio",
			"Webpack Bundle Analyzer is started at http://127.0.0.1:8888": "Bundle Analyzer",
		} {
			if got := ExtractDevToolsURL(line); got == nil || got.Name != name {
				t.Errorf("%q → %+v, want %s", line, got, name)
			}
		}
	})
	t.Run("does not swallow the punctuation of a sentence-shaped log line", func(t *testing.T) {
		if got := ExtractDevToolsURL("DevTools is available at: http://127.0.0.1:9100/."); got == nil || got.URL != "http://127.0.0.1:9100/" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("only ever offers an address served by this machine", func(t *testing.T) {
		if got := ExtractDevToolsURL("DevTools is available at: https://devtools.example.com/"); got != nil {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("says nothing when the line is about something else", func(t *testing.T) {
		for _, line := range []string{
			"A Dart VM Service is available at: http://127.0.0.1:53412/",
			"Debugger listening on ws://127.0.0.1:9229/2f1e",
			"Syncing files to device…",
		} {
			if got := ExtractDevToolsURL(line); got != nil {
				t.Errorf("%q → %+v", line, got)
			}
		}
	})
}

// ─── test/pureLogic.test.ts › launch ${input:…} prompts ─────────────────────

func TestResolveInputTokensDeep(t *testing.T) {
	// resolves ${input:id} tokens deep in configs and tasks, leaving unknown ids intact
	config := map[string]any{
		"name": "Run ${input:who}",
		"args": []any{"--from", "${input:who}", "${input:who}+${input:greeting}"},
		"env":  map[string]any{"GREETING": "${input:greeting}", "KEEP": "${input:unknown}"},
		"nested": map[string]any{
			"deep": []any{"${input:who}"},
		},
	}
	out := ResolveInputTokens(config, map[string]string{"who": "carlos", "greeting": "hola"}).(map[string]any)
	if out["name"] != "Run carlos" {
		t.Errorf("name = %v", out["name"])
	}
	if !reflect.DeepEqual(out["args"], []any{"--from", "carlos", "carlos+hola"}) {
		t.Errorf("args = %v", out["args"])
	}
	if !reflect.DeepEqual(out["env"], map[string]any{"GREETING": "hola", "KEEP": "${input:unknown}"}) {
		t.Errorf("env = %v", out["env"])
	}
	if !reflect.DeepEqual(out["nested"], map[string]any{"deep": []any{"carlos"}}) {
		t.Errorf("nested = %v", out["nested"])
	}
	// The input is not mutated.
	if config["name"] != "Run ${input:who}" {
		t.Error("input mutated")
	}
}

// ─── test/newFeatures.test.ts › discoverLaunch (launch-configs playground) ──

func playgroundRoot(t *testing.T) LaunchGroup {
	t.Helper()
	for _, g := range DiscoverLaunch(testutil.PlaygroundRepo(t, "launch-configs")) {
		if g.IsRoot {
			return g
		}
	}
	t.Fatal("no root group")
	return LaunchGroup{}
}

func findConfig(g LaunchGroup, name string) LaunchConfig {
	for _, c := range g.Configs {
		if c["name"] == name {
			return c
		}
	}
	return nil
}

func TestDiscoverCompoundsAndServerReady(t *testing.T) {
	root := playgroundRoot(t)
	t.Run("surfaces a compound as a synthetic config carrying its members and stopAll", func(t *testing.T) {
		c := findConfig(root, "Run both services")
		if c == nil {
			t.Fatal("compound missing")
		}
		if c["type"] != "compound" || !reflect.DeepEqual(c["compound"], []any{"Service A", "Service B"}) || c["compoundStopAll"] != true {
			t.Errorf("compound = %v", c)
		}
		// Both members exist as plain configs, so the renderer can spawn one
		// parallel session per member.
		if findConfig(root, "Service A") == nil || findConfig(root, "Service B") == nil {
			t.Error("members missing")
		}
	})
	t.Run("parses the inputs array — promptString and pickString with options and defaults", func(t *testing.T) {
		var who, greeting LaunchInput
		for _, i := range root.Inputs {
			switch i["id"] {
			case "who":
				who = i
			case "greeting":
				greeting = i
			}
		}
		if who["type"] != "promptString" || who["default"] != "gitcito" {
			t.Errorf("who = %v", who)
		}
		if greeting["type"] != "pickString" || greeting["default"] != "hola" {
			t.Errorf("greeting = %v", greeting)
		}
		if !reflect.DeepEqual(greeting["options"], []any{"hola", "hello", "bonjour", "ciao"}) {
			t.Errorf("options = %v", greeting["options"])
		}
	})
	t.Run("preserves serverReadyAction on the config it belongs to", func(t *testing.T) {
		srv := findConfig(root, "Server (opens browser when ready)")
		sra, _ := srv["serverReadyAction"].(map[string]any)
		if p, _ := sra["pattern"].(string); !strings.Contains(p, "Local:") || sra["action"] != "openExternally" {
			t.Errorf("serverReadyAction = %v", sra)
		}
	})
}

func TestStripJsonc(t *testing.T) {
	in := "{ // c\n \"a\": \"// kept\", /* b */ \"b\": [1, 2, ], 'c': 'it\\'s', }"
	want := "{ \n \"a\": \"// kept\",  \"b\": [1, 2 ], 'c': 'it\\'s' }"
	if got := stripJsonc(in); got != want {
		t.Errorf("stripJsonc = %q\nwant %q", got, want)
	}
}

// ─── real processes ─────────────────────────────────────────────────────────

func waitEvents(t *testing.T, rec *recorder, what string, cond func([]recEvent) bool) []recEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		evs := append([]recEvent(nil), rec.events...)
		rec.mu.Unlock()
		if cond(evs) {
			return evs
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

func dataOf(evs []recEvent, ch string) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Ch == ch {
			b.WriteString(e.Data.(string))
		}
	}
	return b.String()
}

func exitOf(evs []recEvent, ch string) (int, bool) {
	for _, e := range evs {
		if e.Ch == ch {
			return e.Data.(int), true
		}
	}
	return 0, false
}

func TestRealSessionExitCodeAndLateAttach(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	rec := &recorder{}
	m := NewManager(rec, Options{})
	t.Cleanup(m.StopAll)
	res := m.Run(RunPayload{Dir: t.TempDir(), Config: LaunchConfig{"name": "Fast", "command": "sh -c 'echo fast-out; exit 3'"}, Cols: 80, Rows: 24})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	waitEvents(t, rec, "exit", func(evs []recEvent) bool { _, ok := exitOf(evs, "launch:exit:1"); return ok })
	rec.take()
	// The terminal mounts after the program died: it still learns why.
	m.Attach(res.ID)
	evs := rec.take()
	if len(evs) != 2 || !strings.Contains(evs[0].Data.(string), "fast-out") || evs[1].Ch != "launch:exit:1" || evs[1].Data != 3 {
		t.Fatalf("replay = %+v", evs)
	}
}

func TestRealSessionPauseResumeStop(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	rec := &recorder{}
	m := NewManager(rec, Options{})
	t.Cleanup(m.StopAll)
	// The last segment is exec'd, so the program is one command.
	script := `sh -c 'i=0; while true; do i=$((i+1)); echo tick-$i; sleep 0.05; done'`
	res := m.Run(RunPayload{Dir: t.TempDir(), Config: LaunchConfig{"name": "Ticker", "command": script}, Cols: 80, Rows: 24})
	m.Attach(res.ID)
	m.Resize(res.ID, 100, 30)
	waitEvents(t, rec, "ticks", func(evs []recEvent) bool { return strings.Contains(dataOf(evs, "launch:data:1"), "tick-3") })

	m.Signal(res.ID, "pause")
	time.Sleep(200 * time.Millisecond)
	before := len(dataOf(rec.take(), "launch:data:1"))
	time.Sleep(300 * time.Millisecond)
	if paused := dataOf(rec.take(), "launch:data:1"); len(paused) > 0 {
		t.Errorf("output while paused: %q (had %d before)", paused, before)
	}
	m.Signal(res.ID, "resume")
	waitEvents(t, rec, "ticks after resume", func(evs []recEvent) bool { return strings.Contains(dataOf(evs, "launch:data:1"), "tick-") })

	m.Stop(res.ID)
	waitEvents(t, rec, "exit after stop", func(evs []recEvent) bool { _, ok := exitOf(evs, "launch:exit:1"); return ok })
	if m.get(res.ID) != nil {
		t.Error("stopped session still registered")
	}
	// Calls on a gone session are no-ops.
	m.Input(res.ID, "x")
	m.Signal(res.ID, "pause")
	m.Stop(res.ID)
}

func TestRealSessionInputReachesTheProgram(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	rec := &recorder{}
	m := NewManager(rec, Options{})
	t.Cleanup(m.StopAll)
	res := m.RunTasks(RunTasksPayload{
		Dir:    t.TempDir(),
		Tasks:  []LaunchTask{{"label": "ask", "type": "shell", "command": "read x; echo got-$x"}},
		Labels: []string{"ask"},
	})
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	m.Input(res.ID, "answer\r")
	evs := waitEvents(t, rec, "exit", func(evs []recEvent) bool { _, ok := exitOf(evs, "launch:exit:1"); return ok })
	if !strings.Contains(dataOf(evs, "launch:data:1"), "got-answer") {
		t.Errorf("output = %q", dataOf(evs, "launch:data:1"))
	}
	if code, _ := exitOf(evs, "launch:exit:1"); code != 0 {
		t.Errorf("exit = %d", code)
	}
}
