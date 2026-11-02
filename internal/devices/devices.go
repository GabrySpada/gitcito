// Package devices ports src/main/devices.ts: run targets — the phones,
// simulators, emulators and desktops a launch config can be pointed at,
// gathered from whichever SDK CLIs the machine has.
//
// Four tools know about devices and none of them agrees with the others:
// `flutter devices` (everything, already normalised, but only for Flutter
// installs), `xcrun simctl` (iOS simulators, macOS only), `adb` (attached
// Android devices and *booted* emulators) and `emulator -list-avds` (Android
// emulators that are still cold). We ask all of them, in parallel, and merge.
//
// Everything here is best-effort: a missing SDK is a normal state, not an
// error. What we cannot find is reported in `missing` so the UI can say why the
// list is short instead of showing an empty menu with no explanation.
package devices

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// flutterTimeout is long enough for a cold `flutter devices`, short enough to
// not wedge the UI.
const (
	flutterTimeout = 25 * time.Second
	toolTimeout    = 10 * time.Second
	toolMaxBuffer  = 4 * 1024 * 1024
)

// runFunc runs a tool to completion and returns its stdout; any failure
// (missing binary, non-zero exit, timeout) is an error.
type runFunc func(ctx context.Context, cmd string, args []string, timeout time.Duration, maxBuffer int) (string, error)

// spawnFunc starts a process that outlives the app.
type spawnFunc func(cmd string, args []string) error

// Options configure a Lister. The zero value is production behaviour.
type Options struct {
	// Shot is the --shot flag: GITCITO_DEVICE_BIN is honoured only then.
	Shot bool

	run    runFunc
	spawn  spawnFunc
	goos   string
	getenv func(string) string
	home   string
}

// Lister gathers and boots run targets.
type Lister struct {
	o Options
}

// New builds a Lister.
func New(o Options) *Lister {
	if o.run == nil {
		o.run = execRun
	}
	if o.spawn == nil {
		o.spawn = spawnDetached
	}
	if o.goos == "" {
		o.goos = runtime.GOOS
	}
	if o.getenv == nil {
		o.getenv = os.Getenv
	}
	if o.home == "" {
		o.home, _ = os.UserHomeDir()
	}
	return &Lister{o: o}
}

func execRun(ctx context.Context, cmd string, args []string, timeout time.Duration, maxBuffer int) (string, error) {
	res, err := gitx.Exec(ctx, "", cmd, args, gitx.RunOpts{PlainEnv: true, Timeout: timeout, MaxBuffer: maxBuffer})
	return res.Stdout, err
}

// spawnDetached starts the process in its own session with no stdio: the
// emulator outlives us by design, or quitting Gitcito would take the running
// Android device down with it.
func spawnDetached(cmd string, args []string) error {
	c := exec.Command(cmd, args...)
	detach(c)
	if err := c.Start(); err != nil {
		if strings.Contains(err.Error(), "executable file not found") || os.IsNotExist(err) {
			return &spawnError{"spawn " + cmd + " ENOENT"}
		}
		return err
	}
	go func() { _ = c.Wait() }() // reap it, never kill it
	return nil
}

type spawnError struct{ msg string }

func (e *spawnError) Error() string { return e.msg }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// shotTool points every SDK lookup at a generated fake tree in screenshot
// mode: a documentation shot must show a deterministic device list, not
// whichever phone happened to be plugged into the machine that captured it.
// Honoured **only** under `--shot`, the same shape as GITCITO_SSH_DIR.
func (l *Lister) shotTool(cmd string) string {
	if !l.o.Shot {
		return ""
	}
	dir := l.o.getenv("GITCITO_DEVICE_BIN")
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, filepath.Base(cmd))
	if exists(p) {
		return p
	}
	return ""
}

// run asks one tool; ok is false when it had nothing to tell us. Missing
// binary, non-zero exit, timeout — none of them surface as a failure.
func (l *Lister) run(ctx context.Context, cmd string, args []string, timeout time.Duration) (string, bool) {
	if s := l.shotTool(cmd); s != "" {
		cmd = s
	}
	out, err := l.o.run(ctx, cmd, args, timeout, toolMaxBuffer)
	return out, err == nil
}

// androidTool is the first existing path among the usual SDK locations, else
// the bare name (PATH).
func (l *Lister) androidTool(name string) string {
	exe := name
	if l.o.goos == "windows" {
		exe += ".exe"
	}
	sub := "emulator"
	if name == "adb" {
		sub = "platform-tools"
	}
	var platformRoot string
	switch l.o.goos {
	case "darwin":
		platformRoot = filepath.Join(l.o.home, "Library", "Android", "sdk")
	case "windows":
		base := l.o.getenv("LOCALAPPDATA")
		if base == "" {
			base = l.o.home
		}
		platformRoot = filepath.Join(base, "Android", "Sdk")
	default:
		platformRoot = filepath.Join(l.o.home, "Android", "Sdk")
	}
	for _, root := range []string{l.o.getenv("ANDROID_HOME"), l.o.getenv("ANDROID_SDK_ROOT"), platformRoot} {
		if root == "" {
			continue
		}
		if p := filepath.Join(root, sub, exe); exists(p) {
			return p
		}
	}
	return exe
}

// emulatorAvdName is `adb -s <serial> emu avd name`'s first line: the real
// AVD id of a running emulator (see RenameRunningEmulators).
func (l *Lister) emulatorAvdName(ctx context.Context, adb, serial string) string {
	out, ok := l.run(ctx, adb, []string{"-s", serial, "emu", "avd", "name"}, toolTimeout)
	if !ok {
		return ""
	}
	first, _, _ := strings.Cut(out, "\n")
	return gitx.TrimJS(first)
}

// List is every run target this machine can offer. `flutter` is only asked
// when the folder is actually a Flutter/Dart project — outside one it is a slow
// way to learn nothing, and the other tools already cover simulators and
// handsets.
func (l *Lister) List(ctx context.Context, repoPath string) RunDeviceSnapshot {
	isDart := exists(filepath.Join(repoPath, "pubspec.yaml"))
	isMac := l.o.goos == "darwin"
	adb := l.androidTool("adb")
	emulator := l.androidTool("emulator")

	type result struct {
		out string
		ok  bool
	}
	var flutterDevices, flutterEmulators, simctl, adbOut, avds result
	var wg sync.WaitGroup
	ask := func(dst *result, cmd string, args []string, timeout time.Duration) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst.out, dst.ok = l.run(ctx, cmd, args, timeout)
		}()
	}
	if isDart {
		ask(&flutterDevices, "flutter", []string{"devices", "--machine"}, flutterTimeout)
		ask(&flutterEmulators, "flutter", []string{"emulators"}, flutterTimeout)
	}
	if isMac {
		ask(&simctl, "xcrun", []string{"simctl", "list", "devices", "available", "--json"}, toolTimeout)
	}
	ask(&adbOut, adb, []string{"devices", "-l"}, toolTimeout)
	ask(&avds, emulator, []string{"-list-avds"}, toolTimeout)
	wg.Wait()

	missing := []string{}
	if isDart && !flutterDevices.ok {
		missing = append(missing, "flutter")
	}
	if isMac && !simctl.ok {
		missing = append(missing, "xcrun simctl")
	}
	if !adbOut.ok {
		missing = append(missing, "adb")
	}
	if !avds.ok {
		missing = append(missing, "emulator")
	}

	adbDevices := []RunDevice{}
	if adbOut.ok && adbOut.out != "" {
		adbDevices = ParseAdbDevices(adbOut.out)
	}
	var serials []string
	for _, d := range adbDevices {
		if d.Platform == "android" && strings.HasPrefix(d.ID, "emulator-") {
			serials = append(serials, d.ID)
		}
	}
	avdNames := map[string]string{}
	if len(serials) > 0 {
		names := make([]string, len(serials))
		for i, s := range serials {
			wg.Add(1)
			go func() {
				defer wg.Done()
				names[i] = l.emulatorAvdName(ctx, adb, s)
			}()
		}
		wg.Wait()
		for i, s := range serials {
			if names[i] != "" {
				avdNames[s] = names[i]
			}
		}
	}

	parse := func(r result, f func(string) []RunDevice) []RunDevice {
		if !r.ok || r.out == "" {
			return []RunDevice{}
		}
		return f(r.out)
	}
	devices := MergeDevices([][]RunDevice{
		RenameRunningEmulators(parse(flutterDevices, ParseFlutterDevices), avdNames),
		parse(flutterEmulators, ParseFlutterEmulators),
		parse(simctl, ParseSimctl),
		RenameRunningEmulators(adbDevices, avdNames),
		parse(avds, ParseAvds),
	})
	return RunDeviceSnapshot{Devices: devices, Missing: missing}
}

// BootResult is `{ ok: true } | { error }`.
type BootResult struct {
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// Boot starts a cold simulator/emulator. It returns once the boot command has
// been accepted — the device takes another while to appear, so the caller
// refreshes rather than waiting here.
func (l *Lister) Boot(ctx context.Context, device RunDevice) BootResult {
	fail := func(err error) BootResult { return BootResult{Error: err.Error()} }
	switch device.Source {
	case "flutter-emulator":
		if _, err := l.o.run(ctx, "flutter", []string{"emulators", "--launch", device.ID}, flutterTimeout, gitx.DefaultMaxBuffer); err != nil {
			return fail(err)
		}
	case "simctl":
		if _, err := l.o.run(ctx, "xcrun", []string{"simctl", "boot", device.ID}, toolTimeout, gitx.DefaultMaxBuffer); err != nil {
			return fail(err)
		}
		// Booting only starts the runtime; the Simulator window is separate.
		_, _ = l.o.run(ctx, "open", []string{"-a", "Simulator"}, toolTimeout, gitx.DefaultMaxBuffer)
	case "avd":
		// The emulator process outlives us by design — detach it, or quitting
		// Gitcito would take the running Android device down with it.
		if err := l.o.spawn(l.androidTool("emulator"), []string{"-avd", device.ID}); err != nil {
			return fail(err)
		}
	}
	return BootResult{OK: true}
}
