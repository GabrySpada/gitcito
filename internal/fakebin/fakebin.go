// Package fakebin installs fake.sh — a recording stand-in for ssh, ssh-add,
// ssh-keygen, git, open and editor CLIs — for the tools-group tests
// (internal/editor, internal/difftool, internal/ssh). The TypeScript capture
// (test/port/tools.test.ts) drives the original code against the same script,
// so a golden recorded there replays here call for call. Test-only: nothing in
// the app imports it.
package fakebin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

//go:embed fake.sh
var script []byte

// utils are what fake.sh itself runs; they are linked into the bin dir so
// PATH can hold nothing real (a missing fake is then a genuine ENOENT).
var utils = []string{"basename", "mkdir", "cat", "cmp", "cut"}

// Cleared are the variables an askpass decision reads. Every case starts with
// them unset, in the TypeScript capture as here.
var Cleared = []string{"DISPLAY", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GIT_TERMINAL_PROMPT", "GITCITO_SSH_PASSPHRASE"}

// Reply is one canned answer. A nil Argv is the default reply, for any call.
type Reply struct {
	Argv   []string `json:"argv,omitempty"`
	Stdout string   `json:"stdout,omitempty"`
	Stderr string   `json:"stderr,omitempty"`
	Code   int      `json:"code,omitempty"`
	Create []string `json:"create,omitempty"`
}

// Call is what one invocation recorded.
type Call struct {
	Argv          []string          `json:"argv"`
	Env           map[string]string `json:"env"`
	Askpass       *string           `json:"askpass,omitempty"`
	AskpassScript *string           `json:"askpassScript,omitempty"`
	Leader        string            `json:"leader,omitempty"`
}

// Fake is an installed set of fake binaries.
type Fake struct {
	// Bin is the directory to put on PATH.
	Bin string
	// Dir is $FAKE_DIR: the replies and the recorded calls.
	Dir string
}

// Install lays out root/fakebin (the fake under each name, plus its
// utilities) and root/fake (the replies). Skips the test where /bin/sh is not.
func Install(t testing.TB, root string, bins []string, replies []Reply) Fake {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake binaries are shell scripts")
	}
	f := Fake{Bin: filepath.Join(root, "fakebin"), Dir: filepath.Join(root, "fake")}
	must(t, os.MkdirAll(f.Bin, 0o755))
	must(t, os.MkdirAll(filepath.Join(f.Dir, "replies"), 0o755))
	exe := filepath.Join(f.Bin, ".fake.sh")
	must(t, os.WriteFile(exe, script, 0o755))
	for _, name := range bins {
		must(t, os.Symlink(exe, filepath.Join(f.Bin, name)))
	}
	for _, u := range utils {
		real, err := exec.LookPath(u)
		if err != nil {
			t.Skipf("%s is not on PATH", u)
		}
		must(t, os.Symlink(real, filepath.Join(f.Bin, u)))
	}
	for i, r := range replies {
		name := "default"
		if r.Argv != nil {
			name = fmt.Sprintf("r%03d", i)
		}
		dir := filepath.Join(f.Dir, "replies", name)
		must(t, os.Mkdir(dir, 0o755))
		if r.Argv != nil {
			must(t, os.WriteFile(filepath.Join(dir, "argv"), []byte(nulJoin(r.Argv)), 0o644))
		}
		must(t, os.WriteFile(filepath.Join(dir, "stdout"), []byte(r.Stdout), 0o644))
		must(t, os.WriteFile(filepath.Join(dir, "stderr"), []byte(r.Stderr), 0o644))
		must(t, os.WriteFile(filepath.Join(dir, "code"), []byte(strconv.Itoa(r.Code)), 0o644))
		if len(r.Create) > 0 {
			must(t, os.WriteFile(filepath.Join(dir, "create"), []byte(strings.Join(r.Create, "\n")+"\n"), 0o644))
		}
	}
	return f
}

// Use points PATH at the fakes alone, sets FAKE_DIR and clears Cleared, for
// the rest of the test.
func (f Fake) Use(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", f.Bin)
	t.Setenv("FAKE_DIR", f.Dir)
	Clear(t)
}

// Clear unsets Cleared for the rest of the test.
func Clear(t *testing.T) {
	t.Helper()
	for _, k := range Cleared {
		t.Setenv(k, "")
		must(t, os.Unsetenv(k))
	}
}

// Calls reads every recorded call, in the order the calls claimed their slots.
func (f Fake) Calls(t testing.TB) []Call {
	t.Helper()
	dir := filepath.Join(f.Dir, "calls")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []Call{}
	}
	must(t, err)
	nums := []int{}
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	calls := []Call{}
	for _, n := range nums {
		c := filepath.Join(dir, strconv.Itoa(n))
		call := Call{Argv: splitNul(read(t, filepath.Join(c, "argv"))), Env: map[string]string{}}
		pairs := splitNul(read(t, filepath.Join(c, "env")))
		for i := 0; i+1 < len(pairs); i += 2 {
			call.Env[pairs[i]] = pairs[i+1]
		}
		if s, ok := readOpt(filepath.Join(c, "askpass")); ok {
			call.Askpass = &s
		}
		if s, ok := readOpt(filepath.Join(c, "askpass_script")); ok {
			call.AskpassScript = &s
		}
		if s, ok := readOpt(filepath.Join(c, "leader")); ok {
			call.Leader = strings.TrimSpace(s)
		}
		calls = append(calls, call)
	}
	return calls
}

// Wait blocks until n calls have finished recording themselves — a detached
// child is still running when the code that spawned it returns.
func (f Fake) Wait(t testing.TB, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(filepath.Join(f.Dir, "calls"))
		done := 0
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(f.Dir, "calls", e.Name(), "done")); err == nil {
				done++
			}
		}
		if done >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake was called fewer than %d times", n)
}

func nulJoin(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p)
		b.WriteByte(0)
	}
	return b.String()
}

func splitNul(s string) []string {
	parts := strings.Split(s, "\x00")
	return parts[:len(parts)-1]
}

func read(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return string(b)
}

func readOpt(path string) (string, bool) {
	b, err := os.ReadFile(path)
	return string(b), err == nil
}

// TempRoot is a short temp dir for one case: t.TempDir embeds the subtest
// name, whose colons would split PATH.
func TempRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "gitcito-tools-")
	must(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// Expand swaps the <ROOT> placeholder of a golden value for root, through JSON.
func Expand[T any](t testing.TB, v T, root string) T {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	// Marshal escapes '<' as \u003c; undo that so the placeholder is found.
	s := strings.ReplaceAll(string(b), `\u003cROOT\u003e`, "<ROOT>")
	var out T
	must(t, json.Unmarshal([]byte(strings.ReplaceAll(s, "<ROOT>", jsonEsc(root))), &out))
	return out
}

// SameJSON compares got, with root put back to <ROOT>, against a golden value.
func SameJSON(t testing.TB, label string, got any, want json.RawMessage, root string) {
	t.Helper()
	b, err := json.Marshal(got)
	must(t, err)
	b = []byte(strings.ReplaceAll(string(b), jsonEsc(root), "<ROOT>"))
	var a, w any
	must(t, json.Unmarshal(b, &a))
	must(t, json.Unmarshal(want, &w))
	ga, _ := json.Marshal(a)
	gw, _ := json.Marshal(w)
	if string(ga) != string(gw) {
		t.Errorf("%s:\n got  %s\n want %s", label, ga, gw)
	}
}

func jsonEsc(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
