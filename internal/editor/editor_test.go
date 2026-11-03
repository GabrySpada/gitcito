package editor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GabrySpada/gitcito/internal/fakebin"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Replays testdata/editor.json, recorded from src/main/editor.ts by
// test/port/tools.test.ts against the same fake binaries.

type golden struct {
	Detect []struct {
		Name     string             `json:"name"`
		Platform string             `json:"platform"`
		Env      map[string]string  `json:"env"`
		Files    map[string]*string `json:"files"`
		Modes    map[string]int     `json:"modes"`
		Result   json.RawMessage    `json:"result"`
	} `json:"detect"`
	Open []struct {
		Name     string                `json:"name"`
		Bins     []string              `json:"bins"`
		Replies  []fakebin.Reply       `json:"replies"`
		Setting  *shared.EditorSetting `json:"setting"`
		Target   *shared.EditorTarget  `json:"target"`
		WaitFor  int                   `json:"waitFor"`
		Result   string                `json:"result"`
		Calls    json.RawMessage       `json:"calls"`
		Platform string                `json:"platform"`
	} `json:"open"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "editor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Detect) == 0 || len(g.Open) == 0 {
		t.Fatal("no cases")
	}
	return g
}

func goos(platform string) string {
	if platform == "win32" {
		return "windows"
	}
	return platform
}

func TestDetectGolden(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the vector's file layout (backslashes in names, X_OK) is a Linux capture")
	}
	for _, c := range loadGolden(t).Detect {
		t.Run(c.Name, func(t *testing.T) {
			root := fakebin.TempRoot(t)
			for rel, content := range c.Files {
				full := filepath.Join(root, rel)
				if content == nil {
					if err := os.MkdirAll(full, 0o755); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0o644)
				if m, ok := c.Modes[rel]; ok {
					mode = os.FileMode(m)
				}
				if err := os.WriteFile(full, []byte(*content), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(full, mode); err != nil {
					t.Fatal(err)
				}
			}
			for _, k := range []string{"PATH", "HOME", "ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
				t.Setenv(k, "")
				_ = os.Unsetenv(k)
			}
			for k, v := range fakebin.Expand(t, c.Env, root) {
				t.Setenv(k, v)
			}
			e := New()
			e.goos = goos(c.Platform)
			// The vector was captured where /Applications does not exist.
			sys := filepath.Join(root, "no-system-applications")
			e.appRoots = func() []string { return []string{sys, filepath.Join(homeDir(), "Applications")} }
			got, err := NewService(e).Detect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			fakebin.SameJSON(t, "detect", got, c.Result, root)
		})
	}
}

func TestOpenGolden(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("session leadership is read from /proc")
	}
	for _, c := range loadGolden(t).Open {
		t.Run(c.Name, func(t *testing.T) {
			root := fakebin.TempRoot(t)
			f := fakebin.Install(t, root, c.Bins, fakebin.Expand(t, c.Replies, root))
			f.Use(t)
			got := NewService(New()).Open(context.Background(), fakebin.Expand(t, c.Setting, root), fakebin.Expand(t, c.Target, root))
			if c.WaitFor > 0 {
				f.Wait(t, c.WaitFor)
			}
			if got != c.Result {
				t.Errorf("result %q, want %q", got, c.Result)
			}
			fakebin.SameJSON(t, "calls", f.Calls(t), c.Calls, root)
		})
	}
}

// TS registered an error listener on the spawned child but resolved ” first,
// so a missing editor binary was reported as success. The Go start is
// synchronous and says what went wrong, as the doc comment always promised.
func TestOpenReportsAMissingEditor(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-editor")
	setting := &shared.EditorSetting{ID: "custom", Name: "x", Command: missing, Source: "cli"}
	got := NewService(New()).Open(context.Background(), setting, &shared.EditorTarget{Path: "/r/a.ts"})
	if got != "spawn "+missing+" ENOENT" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("PATH", t.TempDir())
	setting.Command = "no-such-editor"
	if got := NewService(New()).Open(context.Background(), setting, &shared.EditorTarget{Path: "/r/a.ts"}); got != "spawn no-such-editor ENOENT" {
		t.Fatalf("got %q", got)
	}
}

// The facade's guard: nothing configured, or nothing to open.
func TestOpenGuard(t *testing.T) {
	s := NewService(New())
	ok := &shared.EditorSetting{ID: "vscode", Name: "VS Code", Command: "/bin/code", Source: "cli"}
	for name, c := range map[string]struct {
		setting *shared.EditorSetting
		target  *shared.EditorTarget
	}{
		"nil setting":  {nil, &shared.EditorTarget{Path: "/r"}},
		"no command":   {&shared.EditorSetting{ID: "vscode"}, &shared.EditorTarget{Path: "/r"}},
		"nil target":   {ok, nil},
		"missing path": {ok, &shared.EditorTarget{}},
	} {
		if got := s.Open(context.Background(), c.setting, c.target); got != NoEditorMessage {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

// A path holding shell syntax reaches the editor as one literal argument.
func TestOpenPassesShellSyntaxAsData(t *testing.T) {
	root := fakebin.TempRoot(t)
	f := fakebin.Install(t, root, []string{"ed"}, []fakebin.Reply{{}})
	f.Use(t)
	evil := `/r/a b;$(touch pwned)` + "`id`" + `'"|&.ts`
	setting := &shared.EditorSetting{ID: "custom", Name: "ed", Command: filepath.Join(f.Bin, "ed"), Source: "cli"}
	if got := NewService(New()).Open(context.Background(), setting, &shared.EditorTarget{Path: evil}); got != "" {
		t.Fatalf("got %q", got)
	}
	f.Wait(t, 1)
	calls := f.Calls(t)
	if len(calls) != 1 || len(calls[0].Argv) != 2 || calls[0].Argv[1] != evil {
		t.Fatalf("calls %+v", calls)
	}
	if _, err := os.Stat("pwned"); err == nil {
		t.Fatal("a shell ran")
	}
}
