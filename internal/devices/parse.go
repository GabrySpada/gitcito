package devices

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// RunDevice is where a launch config can be pointed: a handset, a simulator,
// this desktop (src/shared/types.ts RunDevice).
type RunDevice struct {
	// ID is the value the runtime wants (`flutter run -d <id>`, `--udid`, `--target`).
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	// Kind is device | simulator | desktop | web.
	Kind string `json:"kind"`
	// Running: booted and able to take a run right now.
	Running bool `json:"running"`
	// Source is the SDK tool that reported it — it also decides how it boots.
	Source string `json:"source"`
	// Detail is the picker's secondary line — an OS version, a serial.
	Detail string `json:"detail,omitempty"`
}

// RunDeviceSnapshot is the devices:list result.
type RunDeviceSnapshot struct {
	Devices []RunDevice `json:"devices"`
	// Missing are the SDK tools that answered nothing, so the UI can explain a
	// short list.
	Missing []string `json:"missing"`
}

// ─── Parsers (pure — covered by the test suite) ──────────────────────────────

// ParseFlutterDevices reads `flutter devices --machine` — JSON, possibly
// behind a line of SDK warnings.
func ParseFlutterDevices(stdout string) []RunDevice {
	out := []RunDevice{}
	start := strings.Index(stdout, "[")
	end := strings.LastIndex(stdout, "]")
	if start < 0 || end <= start {
		return out
	}
	var raw []any
	if err := json.Unmarshal([]byte(stdout[start:end+1]), &raw); err != nil {
		return out
	}
	for _, entry := range raw {
		d, ok := entry.(map[string]any)
		if !ok || d == nil {
			continue
		}
		id, _ := d["id"].(string)
		name, isStr := d["name"].(string)
		if !isStr {
			name = id
		}
		if id == "" {
			continue
		}
		target, _ := d["targetPlatform"].(string)
		platform := flutterPlatform(target)
		emulator, _ := d["emulator"].(bool)
		kind := "device"
		switch {
		case platform == "web":
			kind = "web"
		case platform == "macos" || platform == "windows" || platform == "linux":
			kind = "desktop"
		case emulator:
			kind = "simulator"
		}
		dev := RunDevice{
			ID: id, Name: name, Platform: platform, Kind: kind,
			// `flutter devices` only ever lists things that are up right now.
			Running: true,
			Source:  "flutter",
		}
		if sdk, ok := d["sdk"].(string); ok {
			dev.Detail = sdk
		}
		out = append(out, dev)
	}
	return out
}

func flutterPlatform(target string) string {
	for _, p := range []struct{ prefix, platform string }{
		{"ios", "ios"}, {"android", "android"}, {"darwin", "macos"},
		{"windows", "windows"}, {"linux", "linux"}, {"web", "web"},
	} {
		if strings.HasPrefix(target, p.prefix) {
			return p.platform
		}
	}
	return "other"
}

// ParseFlutterEmulators reads `flutter emulators` — `id • name • manufacturer
// • platformType` per line.
func ParseFlutterEmulators(stdout string) []RunDevice {
	out := []RunDevice{}
	for _, line := range shared.SplitNL(stdout) {
		if !strings.Contains(line, "•") {
			continue
		}
		parts := strings.Split(line, "•")
		for i := range parts {
			parts[i] = gitx.TrimJS(parts[i])
		}
		if len(parts) < 4 || parts[0] == "" {
			continue
		}
		platform := "android"
		if strings.HasPrefix(strings.ToLower(parts[3]), "ios") {
			platform = "ios"
		}
		name := parts[1]
		if name == "" {
			name = parts[0]
		}
		out = append(out, RunDevice{
			ID: parts[0], Name: name, Platform: platform, Kind: "simulator",
			// An emulator listed here is one that is *not* running — a booted one
			// shows up in `flutter devices` instead.
			Running: false,
			Source:  "flutter-emulator",
			Detail:  parts[2],
		})
	}
	return out
}

var (
	reIOSRuntime = regexp.MustCompile(`(?i)^iOS-`)
	reAvdBad     = shared.MustJSRegexp(`\s`, false)
	reSpaces     = shared.MustJSRegexp(`\s+`, false)
)

// ParseSimctl reads `xcrun simctl list devices available --json`.
func ParseSimctl(stdout string) []RunDevice {
	out := []RunDevice{}
	var raw struct {
		Devices json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return out
	}
	var devices map[string]any
	if err := json.Unmarshal(raw.Devices, &devices); err != nil || devices == nil {
		return out
	}
	// Object.entries order: the document's key order, which a Go map loses.
	runtimes := objectKeys(raw.Devices)
	for _, runtime := range runtimes {
		// "com.apple.CoreSimulator.SimRuntime.iOS-18-0" → "iOS 18.0". Anything
		// that is not iOS (watchOS, tvOS) has nothing here that could run it.
		parts := strings.Split(runtime, ".")
		tag := parts[len(parts)-1]
		if !reIOSRuntime.MatchString(tag) {
			continue
		}
		version := strings.ReplaceAll(reIOSRuntime.ReplaceAllLiteralString(tag, ""), "-", ".")
		list, ok := devices[runtime].([]any)
		if !ok {
			continue
		}
		for _, entry := range list {
			d, ok := entry.(map[string]any)
			if !ok || d == nil {
				continue
			}
			if b, isBool := d["isAvailable"].(bool); isBool && !b {
				continue
			}
			udid, _ := d["udid"].(string)
			name, isStr := d["name"].(string)
			if !isStr {
				name = udid
			}
			if udid == "" {
				continue
			}
			state, _ := d["state"].(string)
			out = append(out, RunDevice{
				ID: udid, Name: name, Platform: "ios", Kind: "simulator",
				Running: state == "Booted", Source: "simctl", Detail: "iOS " + version,
			})
		}
	}
	return out
}

// ParseAdbDevices reads `adb devices -l` — attached handsets and
// already-booted emulators.
func ParseAdbDevices(stdout string) []RunDevice {
	out := []RunDevice{}
	lines := shared.SplitNL(stdout)
	for _, line := range lines[1:] {
		trimmed := gitx.TrimJS(line)
		if trimmed == "" {
			continue
		}
		fields := reSpaces.Split(trimmed, -1)
		if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		id, state := fields[0], fields[1]
		// `offline` / `unauthorized` devices cannot take a run — listing them as
		// ready is how you get a launch that fails five seconds later.
		if state != "device" {
			continue
		}
		model := ""
		for _, p := range fields[2:] {
			if strings.HasPrefix(p, "model:") {
				model = strings.ReplaceAll(p[len("model:"):], "_", " ")
				break
			}
		}
		kind := "device"
		if strings.HasPrefix(id, "emulator-") {
			kind = "simulator"
		}
		name := model
		if name == "" {
			name = id
		}
		dev := RunDevice{ID: id, Name: name, Platform: "android", Kind: kind, Running: true, Source: "adb"}
		if model != "" {
			dev.Detail = id
		}
		out = append(out, dev)
	}
	return out
}

// ParseAvds reads `emulator -list-avds` — one AVD name per line, all of them
// cold.
func ParseAvds(stdout string) []RunDevice {
	out := []RunDevice{}
	for _, line := range shared.SplitNL(stdout) {
		name := gitx.TrimJS(line)
		// The tool prefixes warnings on some SDK versions; AVD names never contain
		// whitespace.
		if name == "" || reAvdBad.MatchString(name) || strings.HasPrefix(name, "INFO") || strings.Contains(name, ":") {
			continue
		}
		out = append(out, RunDevice{
			ID: name, Name: strings.ReplaceAll(name, "_", " "), Platform: "android",
			Kind: "simulator", Running: false, Source: "avd",
		})
	}
	return out
}

// objectKeys lists a JSON object's keys in JS property order, as
// Object.entries does: integer-like keys first (ascending), then the rest in
// document order.
func objectKeys(raw json.RawMessage) []string {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var ints, keys []string
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil
		}
		k, _ := t.(string)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		if isArrayIndex(k) {
			ints = append(ints, k)
		} else {
			keys = append(keys, k)
		}
	}
	sort.SliceStable(ints, func(i, j int) bool {
		return len(ints[i]) < len(ints[j]) || len(ints[i]) == len(ints[j]) && ints[i] < ints[j]
	})
	return append(ints, keys...)
}

func isArrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return false
		}
	}
	return len(k) < 10 || k < "4294967295"
}

var platformOrder = []string{"ios", "android", "macos", "windows", "linux", "web", "other"}

func platformIndex(p string) int {
	for i, x := range platformOrder {
		if x == p {
			return i
		}
	}
	return -1
}

// MergeDevices folds the sources into one list. The same simulator is reported
// by up to three tools, so entries are keyed by platform + name: a running
// entry always beats a cold one, and Flutter's version wins ties because its
// id is the one `flutter run -d` expects.
func MergeDevices(lists [][]RunDevice) []RunDevice {
	rank := func(d RunDevice) int {
		r := 0
		if d.Running {
			r += 2
		}
		if strings.HasPrefix(d.Source, "flutter") {
			r++
		}
		return r
	}
	byKey := map[string]int{}
	out := []RunDevice{}
	for _, l := range lists {
		for _, d := range l {
			key := d.Platform + ":" + strings.ToLower(d.Name)
			i, seen := byKey[key]
			if !seen {
				byKey[key] = len(out)
				out = append(out, d)
				continue
			}
			if prev := out[i]; rank(d) > rank(prev) {
				d.Running = d.Running || prev.Running
				out[i] = d
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Running != b.Running {
			return a.Running
		}
		if pa, pb := platformIndex(a.Platform), platformIndex(b.Platform); pa != pb {
			return pa < pb
		}
		return shared.LocaleCompare(a.Name, b.Name) < 0
	})
	return out
}

// RenameRunningEmulators renames running emulators to their real AVD names.
//
// `adb`/`flutter devices` name a running Android emulator after its generic
// hardware model (e.g. "sdk gphone64 arm64"), not the AVD's own name — so a
// booted "Pixel 9a" never key-matches its cold ParseAvds entry and shows up
// twice: once correctly (running, under the model name) and once as a stale
// "available" duplicate under the recognisable name, which is the one a
// picker's user actually reaches for. `adb -s <serial> emu avd name` gives the
// real AVD id back, letting us rename the running entry so it collides into
// the AVD's slot instead of sitting beside it.
func RenameRunningEmulators(devices []RunDevice, names map[string]string) []RunDevice {
	if len(names) == 0 {
		return devices
	}
	out := make([]RunDevice, len(devices))
	for i, d := range devices {
		if avd, ok := names[d.ID]; ok && avd != "" {
			d.Name = strings.ReplaceAll(avd, "_", " ")
		}
		out[i] = d
	}
	return out
}
