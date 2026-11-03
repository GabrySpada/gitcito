package devices

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Vectors are written from src/main/devices.ts by test/port/processes.test.ts:
// testdata/vectors/devices.json (parsers, merge) and
// internal/devices/testdata/io.json (devices:list / devices:boot against
// canned SDK output). No real device is ever asked or booted: every tool runs
// through a fake runner.

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

func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func sameJSON(t *testing.T, label string, got, want any) {
	t.Helper()
	if g, w := canon(t, got), canon(t, want); !reflect.DeepEqual(g, w) {
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		t.Errorf("%s:\n got %s\nwant %s", label, gb, wb)
	}
}

type strCase struct {
	Input string          `json:"input"`
	Out   json.RawMessage `json:"out"`
}

func TestParserVectors(t *testing.T) {
	var v struct {
		ParseFlutterDevices   []strCase `json:"parseFlutterDevices"`
		ParseFlutterEmulators []strCase `json:"parseFlutterEmulators"`
		ParseSimctl           []strCase `json:"parseSimctl"`
		ParseAdbDevices       []strCase `json:"parseAdbDevices"`
		ParseAvds             []strCase `json:"parseAvds"`
		MergeDevices          []struct {
			Lists [][]RunDevice   `json:"lists"`
			Out   json.RawMessage `json:"out"`
		} `json:"mergeDevices"`
		RenameRunningEmulators []struct {
			Devices []RunDevice       `json:"devices"`
			Names   map[string]string `json:"names"`
			Out     json.RawMessage   `json:"out"`
		} `json:"renameRunningEmulators"`
	}
	readJSON(t, filepath.Join("..", "..", "testdata", "vectors", "devices.json"), &v)
	check := func(name string, cases []strCase, f func(string) []RunDevice) {
		if len(cases) == 0 {
			t.Fatalf("%s: no vectors", name)
		}
		for _, c := range cases {
			sameJSON(t, name+"("+c.Input+")", f(c.Input), c.Out)
		}
	}
	check("parseFlutterDevices", v.ParseFlutterDevices, ParseFlutterDevices)
	check("parseFlutterEmulators", v.ParseFlutterEmulators, ParseFlutterEmulators)
	check("parseSimctl", v.ParseSimctl, ParseSimctl)
	check("parseAdbDevices", v.ParseAdbDevices, ParseAdbDevices)
	check("parseAvds", v.ParseAvds, ParseAvds)
	for i, c := range v.MergeDevices {
		sameJSON(t, "mergeDevices #"+string(rune('0'+i)), MergeDevices(c.Lists), c.Out)
	}
	for i, c := range v.RenameRunningEmulators {
		sameJSON(t, "renameRunningEmulators #"+string(rune('0'+i)), RenameRunningEmulators(c.Devices, c.Names), c.Out)
	}
}

type call struct {
	Cmd     string   `json:"cmd"`
	Args    []string `json:"args"`
	Timeout int64    `json:"timeout"`
}

type spawnCall struct {
	Cmd      string   `json:"cmd"`
	Args     []string `json:"args"`
	Detached bool     `json:"detached"`
}

// fakeTools answers like the vitest's fake execFile: a canned stdout, an
// error message, or "spawn <cmd> ENOENT" for anything it does not know.
type fakeTools struct {
	mu      sync.Mutex
	replies map[string]json.RawMessage
	calls   []call
	spawns  []spawnCall
}

func (f *fakeTools) run(_ context.Context, cmd string, args []string, timeout time.Duration, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{cmd, args, timeout.Milliseconds()})
	raw, ok := f.replies[strings.Join(append([]string{cmd}, args...), " ")]
	if !ok || string(raw) == "null" {
		return "", errors.New("spawn " + cmd + " ENOENT")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return "", errors.New(e.Error)
}

func (f *fakeTools) spawn(cmd string, args []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawns = append(f.spawns, spawnCall{cmd, args, true})
	return nil
}

func TestListAndBootVectors(t *testing.T) {
	var v struct {
		List []struct {
			Name     string                     `json:"name"`
			Platform string                     `json:"platform"`
			Dart     bool                       `json:"dart"`
			SDK      bool                       `json:"sdk"`
			Shot     []string                   `json:"shot"`
			Replies  map[string]json.RawMessage `json:"replies"`
			Snapshot json.RawMessage            `json:"snapshot"`
			Calls    []call                     `json:"calls"`
		} `json:"list"`
		Boot []struct {
			Device     RunDevice                  `json:"device"`
			Replies    map[string]json.RawMessage `json:"replies"`
			Result     json.RawMessage            `json:"result"`
			ExecCalls  []call                     `json:"execCalls"`
			SpawnCalls []spawnCall                `json:"spawnCalls"`
		} `json:"boot"`
	}
	readJSON(t, filepath.Join("testdata", "io.json"), &v)
	if len(v.List) == 0 || len(v.Boot) == 0 {
		t.Fatal("no vectors")
	}
	mkdir := func(files ...string) string {
		root := t.TempDir()
		for _, f := range files {
			p := filepath.Join(root, f)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, nil, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	for _, c := range v.List {
		t.Run(c.Name, func(t *testing.T) {
			repo := mkdir("README.md")
			if c.Dart {
				repo = mkdir("pubspec.yaml")
			}
			sdk := mkdir()
			if c.SDK {
				sdk = mkdir("platform-tools/adb", "emulator/emulator")
			}
			bin := mkdir(c.Shot...)
			env := map[string]string{}
			if c.SDK {
				env["ANDROID_HOME"] = sdk
			}
			if c.Shot != nil {
				env["GITCITO_DEVICE_BIN"] = bin
			}
			replies := map[string]json.RawMessage{}
			for k, r := range c.Replies {
				replies[strings.ReplaceAll(strings.ReplaceAll(k, "<SDK>", sdk), "<BIN>", bin)] = r
			}
			f := &fakeTools{replies: replies}
			l := New(Options{
				Shot: c.Shot != nil, run: f.run, spawn: f.spawn, goos: c.Platform,
				getenv: func(k string) string { return env[k] },
				home:   t.TempDir(),
			})
			sameJSON(t, "snapshot", l.List(context.Background(), repo), c.Snapshot)
			got := make([]call, len(f.calls))
			for i, x := range f.calls {
				x.Cmd = strings.ReplaceAll(strings.ReplaceAll(x.Cmd, sdk, "<SDK>"), bin, "<BIN>")
				got[i] = x
			}
			key := func(x call) string { return strings.Join(append([]string{x.Cmd}, x.Args...), " ") }
			sort.Slice(got, func(i, j int) bool { return key(got[i]) < key(got[j]) })
			want := append([]call(nil), c.Calls...)
			sort.Slice(want, func(i, j int) bool { return key(want[i]) < key(want[j]) })
			sameJSON(t, "calls", got, want)
		})
	}
	for i, c := range v.Boot {
		t.Run("boot/"+c.Device.Source+string(rune('0'+i)), func(t *testing.T) {
			f := &fakeTools{replies: c.Replies}
			l := New(Options{run: f.run, spawn: f.spawn, goos: "linux", getenv: func(string) string { return "" }, home: t.TempDir()})
			sameJSON(t, "result", l.Boot(context.Background(), c.Device), c.Result)
			if f.calls == nil {
				f.calls = []call{}
			}
			if f.spawns == nil {
				f.spawns = []spawnCall{}
			}
			sameJSON(t, "exec calls", f.calls, c.ExecCalls)
			sameJSON(t, "spawn calls", f.spawns, c.SpawnCalls)
		})
	}
}

// ─── test/devices.test.ts ───────────────────────────────────────────────────

func TestFlutterDevicesMachine(t *testing.T) {
	stdout := `Warning: dart is not on your PATH.
[
  { "name": "iPhone 16 Pro", "id": "3B7F1A22-0000-4444-9999-AAAABBBBCCCC", "isSupported": true,
    "targetPlatform": "ios", "emulator": true, "sdk": "iOS 18.0" },
  { "name": "Pixel 8", "id": "39121FDJG0018Z", "targetPlatform": "android-arm64", "emulator": false,
    "sdk": "Android 14 (API 34)" },
  { "name": "macOS", "id": "macos", "targetPlatform": "darwin", "emulator": false },
  { "name": "Chrome", "id": "chrome", "targetPlatform": "web-javascript", "emulator": false }
]`
	t.Run("reads the JSON out of a noisy stdout and classifies each target", func(t *testing.T) {
		d := ParseFlutterDevices(stdout)
		var got [][3]string
		for _, x := range d {
			got = append(got, [3]string{x.ID, x.Platform, x.Kind})
			// Anything flutter lists is up right now — that is what makes it runnable.
			if !x.Running {
				t.Error("not running")
			}
		}
		want := [][3]string{
			{"3B7F1A22-0000-4444-9999-AAAABBBBCCCC", "ios", "simulator"},
			{"39121FDJG0018Z", "android", "device"},
			{"macos", "macos", "desktop"},
			{"chrome", "web", "web"},
		}
		if !reflect.DeepEqual(got, want) || d[0].Detail != "iOS 18.0" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("survives output that is not JSON at all", func(t *testing.T) {
		if len(ParseFlutterDevices("No devices detected.")) != 0 || len(ParseFlutterDevices("[not json]")) != 0 {
			t.Error("expected empty")
		}
	})
}

func TestOtherTools(t *testing.T) {
	t.Run("parses flutter emulators bullet lines as cold targets", func(t *testing.T) {
		out := "2 available emulators:\n\napple_ios_simulator • iOS Simulator • Apple • ios\nPixel_7_API_34      • Pixel 7 API 34 • Google • android\n"
		d := ParseFlutterEmulators(out)
		if len(d) != 2 || d[0].ID != "apple_ios_simulator" || d[0].Platform != "ios" || d[1].ID != "Pixel_7_API_34" || d[1].Platform != "android" {
			t.Errorf("got %+v", d)
		}
		for _, x := range d {
			if x.Running {
				t.Error("running")
			}
		}
	})
	t.Run("parses simctl, keeping only available iOS runtimes", func(t *testing.T) {
		js := `{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-18-0":[
			{"udid":"UDID-1","name":"iPhone 16","state":"Booted","isAvailable":true},
			{"udid":"UDID-2","name":"iPad Air","state":"Shutdown","isAvailable":true},
			{"udid":"UDID-3","name":"Broken","state":"Shutdown","isAvailable":false}],
			"com.apple.CoreSimulator.SimRuntime.watchOS-11-0":[{"udid":"UDID-4","name":"Apple Watch","state":"Shutdown","isAvailable":true}]}}`
		d := ParseSimctl(js)
		// The watch runtime is dropped: nothing Gitcito launches can target it.
		if len(d) != 2 || d[0].ID != "UDID-1" || d[1].ID != "UDID-2" || !d[0].Running || d[0].Detail != "iOS 18.0" {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("parses adb devices -l, skipping anything that cannot take a run", func(t *testing.T) {
		out := "List of devices attached\nemulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a\n39121FDJG0018Z         device usb:1-2 product:shiba model:Pixel_8 device:shiba\nR5CT10ABCDE            unauthorized\n1234567890             offline\n"
		d := ParseAdbDevices(out)
		if len(d) != 2 || d[0].Name != "sdk gphone64 arm64" || d[0].Kind != "simulator" || d[1].Name != "Pixel 8" || d[1].Kind != "device" {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("parses AVD names and ignores the tool's warning lines", func(t *testing.T) {
		d := ParseAvds("INFO    | Storing crashdata\nPixel_7_API_34\nNexus_5X_API_28\n")
		if len(d) != 2 || d[0].ID != "Pixel_7_API_34" || d[1].ID != "Nexus_5X_API_28" {
			t.Errorf("got %+v", d)
		}
	})
}

func TestMergingTheSources(t *testing.T) {
	t.Run("keeps one entry per device, preferring the running, flutter-issued one", func(t *testing.T) {
		merged := MergeDevices([][]RunDevice{
			{{ID: "UDID-1", Name: "iPhone 16", Platform: "ios", Kind: "simulator", Running: true, Source: "flutter"}},
			{
				{ID: "UDID-1", Name: "iPhone 16", Platform: "ios", Kind: "simulator", Running: true, Source: "simctl"},
				{ID: "UDID-2", Name: "iPad Air", Platform: "ios", Kind: "simulator", Running: false, Source: "simctl"},
			},
		})
		if len(merged) != 2 || merged[0].Source != "flutter" || !merged[0].Running || merged[1].Running {
			t.Errorf("got %+v", merged)
		}
	})
	t.Run("renames a running emulator to its real AVD name so it collides with the cold AVD entry", func(t *testing.T) {
		adb := "List of devices attached\nemulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a\n"
		running := RenameRunningEmulators(ParseAdbDevices(adb), map[string]string{"emulator-5554": "Pixel_9a"})
		merged := MergeDevices([][]RunDevice{running, ParseAvds("Pixel_9a\n")})
		if len(merged) != 1 || merged[0].ID != "emulator-5554" || merged[0].Name != "Pixel 9a" || !merged[0].Running {
			t.Errorf("got %+v", merged)
		}
	})
	t.Run("sorts by platform once running-ness ties", func(t *testing.T) {
		merged := MergeDevices([][]RunDevice{{
			{ID: "chrome", Name: "Chrome", Platform: "web", Kind: "web", Running: true, Source: "flutter"},
			{ID: "a", Name: "Pixel 8", Platform: "android", Kind: "device", Running: true, Source: "adb"},
			{ID: "u", Name: "iPhone 16", Platform: "ios", Kind: "simulator", Running: true, Source: "simctl"},
		}})
		var got []string
		for _, d := range merged {
			got = append(got, d.Platform)
		}
		if !reflect.DeepEqual(got, []string{"ios", "android", "web"}) {
			t.Errorf("got %v", got)
		}
	})
}

func TestShotModeIgnoredWithoutFlag(t *testing.T) {
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "adb"), nil, 0o755)
	f := &fakeTools{replies: map[string]json.RawMessage{}}
	l := New(Options{Shot: false, run: f.run, spawn: f.spawn, goos: "linux",
		getenv: func(k string) string {
			if k == "GITCITO_DEVICE_BIN" {
				return bin
			}
			return ""
		}, home: t.TempDir()})
	l.List(context.Background(), t.TempDir())
	for _, c := range f.calls {
		if strings.HasPrefix(c.Cmd, bin) {
			t.Fatalf("GITCITO_DEVICE_BIN honoured outside --shot: %v", c)
		}
	}
}
