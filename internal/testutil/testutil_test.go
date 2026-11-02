package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRepoRoot(t *testing.T) {
	root := MustRepoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "examples", "setup-playground.sh")); err != nil {
		t.Fatal("repo root does not hold examples/setup-playground.sh")
	}
}

func TestPlaygroundAndManifest(t *testing.T) {
	dir := Playground(t)
	if _, err := os.Stat(filepath.Join(dir, "MANIFEST.tsv")); err != nil {
		t.Fatal(err)
	}
	m := ReadManifest(t)
	if len(m) < 10 {
		t.Fatalf("manifest has %d entries", len(m))
	}
	for _, e := range m {
		if e.Name == "" || strings.Contains(e.Name, "\t") {
			t.Fatalf("bad entry %+v", e)
		}
	}
	if p := PlaygroundRepo(t, m[0].Name); filepath.Base(p) != m[0].Name {
		t.Fatalf("PlaygroundRepo = %q", p)
	}
}

func TestCloneFixtureIsIndependent(t *testing.T) {
	IsolateGitEnv(t)
	src := PlaygroundRepo(t, "merge-conflict")
	srcHead := Git(t, src, "rev-parse", "HEAD")
	srcStatus := Git(t, src, "status", "--porcelain")

	dir := CloneFixture(t, "merge-conflict")
	if !strings.HasPrefix(filepath.Base(dir), "gitcito-merge-conflict-") {
		t.Fatalf("name = %q", filepath.Base(dir))
	}
	if got := Git(t, dir, "rev-parse", "HEAD"); got != srcHead {
		t.Fatalf("copy HEAD %s != %s", got, srcHead)
	}
	if got := Git(t, dir, "config", "--local", "rerere.enabled"); got != "false" {
		t.Fatalf("rerere.enabled = %q", got)
	}
	// Mutate the copy; the shared repo must not notice.
	WriteFile(t, dir, "gitcito-copy-probe.txt", "x\n")
	Git(t, dir, "add", "gitcito-copy-probe.txt")
	Git(t, dir, "commit", "-q", "-m", "probe")
	if Git(t, src, "rev-parse", "HEAD") != srcHead || Git(t, src, "status", "--porcelain") != srcStatus {
		t.Fatal("mutating the copy changed the shared playground repo")
	}
	if Git(t, dir, "rev-parse", "HEAD") == srcHead {
		t.Fatal("commit did not land in the copy")
	}
}

func TestCloneFixtureKeepsModesAndLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes and symlinks")
	}
	src := t.TempDir()
	WriteFile(t, src, "hook.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(src, "hook.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("hook.sh", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dst, "hook.sh"))
	if err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("exec bit lost: %v %v", st.Mode(), err)
	}
	if target, err := os.Readlink(filepath.Join(dst, "link")); err != nil || target != "hook.sh" {
		t.Fatalf("link = %q %v", target, err)
	}
}

// fatalTB records a Fatal instead of failing the real test.
type fatalTB struct {
	testing.TB
	mu  sync.Mutex
	msg string
}

func (f *fatalTB) Helper() {}
func (f *fatalTB) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.msg = format
	f.mu.Unlock()
	runtime.Goexit()
}

func TestCloneFixtureRefusesSubmodulesWorktrees(t *testing.T) {
	ft := &fatalTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		CloneFixture(ft, "submodules-worktrees")
	}()
	<-done
	if !strings.Contains(ft.msg, "absolute paths") {
		t.Fatalf("CloneFixture did not refuse: %q", ft.msg)
	}
}

func TestIsolateGitEnv(t *testing.T) {
	realHome, _ := os.UserHomeDir()
	env := IsolateGitEnv(t)
	if os.Getenv("HOME") != env.Home || env.Home == realHome {
		t.Fatalf("HOME = %q", os.Getenv("HOME"))
	}
	dir := InitRepo(t)
	if got := Git(t, dir, "config", "--global", "user.email"); got != "test@gitcito.invalid" {
		t.Fatalf("global identity = %q", got)
	}
	// Writes to --global land in the throwaway file.
	Git(t, dir, "config", "--global", "gitcito.probe", "1")
	b, err := os.ReadFile(env.GlobalConfig)
	if err != nil || !strings.Contains(string(b), "probe = 1") {
		t.Fatalf("global config = %q %v", b, err)
	}
	WriteFile(t, dir, "x/y.txt", "y")
	Git(t, dir, "add", ".")
	Git(t, dir, "commit", "-q", "-m", "c")
	if got := Git(t, dir, "log", "--format=%an", "-1"); got != "Gitcito Test" {
		t.Fatalf("author = %q", got)
	}
}

type inner struct {
	Tags []string `json:"tags"`
}

type payload struct {
	Files    []string          `json:"files"`
	Optional []string          `json:"optional,omitempty"`
	Tracking *string           `json:"tracking"` // string | null in TS
	Meta     map[string]int    `json:"meta"`
	Raw      json.RawMessage   `json:"raw"`
	Bytes    []byte            `json:"bytes,omitempty"`
	Nested   []inner           `json:"nested"`
	ByName   map[string]*inner `json:"byName"`
	Skip     []string          `json:"-"`
	inner
}

func TestNullPaths(t *testing.T) {
	good := payload{Files: []string{}, Meta: map[string]int{}, Nested: []inner{{Tags: []string{}}},
		ByName: map[string]*inner{"a": {Tags: []string{"x"}}}, inner: inner{Tags: []string{}}}
	if p := NullPaths(good); len(p) != 0 {
		t.Fatalf("good payload flagged: %q", p)
	}
	NoNulls(t, good)

	bad := payload{Nested: []inner{{}}, ByName: map[string]*inner{"k": {}}}
	got := NullPaths(bad)
	want := []string{
		"$.files is a nil []string",
		"$.meta is a nil map[string]int",
		"$.nested[0].tags is a nil []string",
		"$.byName[k].tags is a nil []string",
		"$.tags is a nil []string",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NullPaths =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if p := NullPaths([]string(nil)); len(p) != 1 || p[0] != "$ is a nil []string" {
		t.Fatalf("top-level nil slice: %q", p)
	}
	if p := NullPaths(&good); len(p) != 0 {
		t.Fatalf("pointer payload: %q", p)
	}
}
