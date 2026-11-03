package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testInstaller(t *testing.T) (*Installer, string) {
	t.Helper()
	base := t.TempDir()
	target := filepath.Join(base, "app", BinaryName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Installer{
		Target: target,
		Dirs:   []string{filepath.Join(base, "local-bin"), filepath.Join(base, "usr-local-bin")},
		GOOS:   "linux",
	}, base
}

func TestCandidateDirs(t *testing.T) {
	if got := candidateDirs("darwin", "/Users/u"); strings.Join(got, ",") != "/usr/local/bin,/opt/homebrew/bin" {
		t.Fatalf("darwin: %v", got)
	}
	if got := candidateDirs("linux", "/home/u"); strings.Join(got, ",") != "/home/u/.local/bin,/usr/local/bin" {
		t.Fatalf("linux: %v", got)
	}
}

func TestInstallLinksTheBinaryAndReportsIt(t *testing.T) {
	inst, base := testInstaller(t)
	if inst.IsInstalled() {
		t.Fatal("installed before install")
	}
	// No candidate exists: the first is created on demand.
	if r := inst.Install(context.Background()); !r.OK || r.Error != "" {
		t.Fatalf("install: %+v", r)
	}
	link := filepath.Join(base, "local-bin", LinkName)
	dest, err := os.Readlink(link)
	if err != nil || dest != inst.Target {
		t.Fatalf("link %q -> %q (%v)", link, dest, err)
	}
	if !inst.IsInstalled() {
		t.Fatal("not reported installed")
	}
	// Installing again replaces the link instead of failing on it.
	if r := inst.Install(context.Background()); !r.OK {
		t.Fatalf("reinstall: %+v", r)
	}
	if r := inst.Uninstall(context.Background()); !r.OK {
		t.Fatalf("uninstall: %+v", r)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link survived uninstall: %v", err)
	}
}

func TestInstallPrefersAnExistingCandidate(t *testing.T) {
	inst, base := testInstaller(t)
	if err := os.MkdirAll(inst.Dirs[1], 0o755); err != nil {
		t.Fatal(err)
	}
	if r := inst.Install(context.Background()); !r.OK {
		t.Fatalf("install: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(base, "usr-local-bin", LinkName)); err != nil {
		t.Fatalf("not in the existing dir: %v", err)
	}
	if _, err := os.Stat(inst.Dirs[0]); !os.IsNotExist(err) {
		t.Fatal("created a directory nobody asked for")
	}
}

// A same-named file installed by something else is neither "installed" nor
// removed by uninstall.
func TestForeignGitcitoIsLeftAlone(t *testing.T) {
	inst, _ := testInstaller(t)
	if err := os.MkdirAll(inst.Dirs[0], 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(inst.Dirs[0], LinkName)
	if err := os.WriteFile(foreign, []byte("someone else"), 0o755); err != nil {
		t.Fatal(err)
	}
	if inst.IsInstalled() {
		t.Fatal("a plain file reported as installed")
	}
	if r := inst.Uninstall(context.Background()); !r.OK {
		t.Fatalf("uninstall: %+v", r)
	}
	if b, err := os.ReadFile(foreign); err != nil || string(b) != "someone else" {
		t.Fatalf("foreign file touched: %q %v", b, err)
	}
	// A link pointing elsewhere is not ours either.
	_ = os.Remove(foreign)
	if err := os.Symlink("/elsewhere/gitcito", foreign); err != nil {
		t.Fatal(err)
	}
	if inst.IsInstalled() {
		t.Fatal("a foreign link reported as installed")
	}
	_ = inst.Uninstall(context.Background())
	if _, err := os.Lstat(foreign); err != nil {
		t.Fatal("foreign link removed")
	}
}

func TestInstallRefusedOnWindows(t *testing.T) {
	inst, _ := testInstaller(t)
	inst.GOOS = "windows"
	if r := inst.Install(context.Background()); r.OK || r.Error != "Not supported on Windows yet" {
		t.Fatalf("got %+v", r)
	}
}

// When the directory cannot be written, macOS retries through the
// authorization dialog with every path quoted; elsewhere the original error
// is reported the way Node worded it.
func TestInstallEscalatesOnlyOnMac(t *testing.T) {
	inst, base := testInstaller(t)
	// A regular file where the directory should be makes MkdirAll fail even
	// for root, which ignores permission bits.
	blocker := filepath.Join(base, "blocked dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	inst.Dirs = []string{filepath.Join(blocker, "bin")}

	if r := inst.Install(context.Background()); r.OK || !strings.HasPrefix(r.Error, "ENOTDIR: not a directory, mkdir '"+blocker) {
		t.Fatalf("linux: %+v", r)
	}

	var ran string
	inst.GOOS = "darwin"
	inst.Privileged = func(_ context.Context, command string) error { ran = command; return nil }
	if r := inst.Install(context.Background()); !r.OK {
		t.Fatalf("darwin: %+v", r)
	}
	dir := filepath.Join(blocker, "bin")
	want := "mkdir -p '" + dir + "' && ln -sf '" + inst.Target + "' '" + filepath.Join(dir, LinkName) + "'"
	if ran != want {
		t.Fatalf("privileged command:\n got %s\nwant %s", ran, want)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/Applications/My App's.app"); got != `'/Applications/My App'\''s.app'` {
		t.Fatalf("got %s", got)
	}
	if got := appleScriptString(`rm -f '/a "b"\c'`); got != `"rm -f '/a \"b\"\\c'"` {
		t.Fatalf("got %s", got)
	}
}

func TestServiceDelegates(t *testing.T) {
	inst, _ := testInstaller(t)
	s := NewService(inst)
	if s.IsInstalled() {
		t.Fatal("installed")
	}
	if r := s.Install(context.Background()); !r.OK || !s.IsInstalled() {
		t.Fatalf("install: %+v", r)
	}
	if r := s.Uninstall(context.Background()); !r.OK || s.IsInstalled() {
		t.Fatalf("uninstall: %+v", r)
	}
}
