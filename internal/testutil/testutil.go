// Package testutil is the Go side of test/helpers.ts and test/fixtures.ts:
// the generated playground repos, throwaway copies of them, git helpers, an
// isolated git environment, and the serialization checks of brief §5.2.
//
// Real git, real repositories, no mocks. Read-only assertions may point at a
// shared playground repo (PlaygroundRepo); anything that MUTATES must work on
// a copy (CloneFixture) — a test that mutates a shared playground repo
// corrupts every later test in the run.
package testutil

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var root struct {
	once sync.Once
	dir  string
	err  error
}

// RepoRoot is the repository root: the nearest directory at or above the
// working directory (a test runs in its package dir) that holds go.mod, or,
// failing that, the one above this source file.
func RepoRoot() (string, error) {
	root.once.Do(func() {
		var starts []string
		if wd, err := os.Getwd(); err == nil {
			starts = append(starts, wd)
		}
		if _, file, _, ok := runtime.Caller(0); ok {
			starts = append(starts, filepath.Dir(file))
		}
		for _, start := range starts {
			for dir := start; ; dir = filepath.Dir(dir) {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					root.dir = dir
					return
				}
				if filepath.Dir(dir) == dir {
					break
				}
			}
		}
		root.err = errors.New("testutil: no go.mod above the working directory")
	})
	return root.dir, root.err
}

// MustRepoRoot is RepoRoot, failing the test on error.
func MustRepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// PlaygroundDir is examples/playground under the repo root, where
// setup-playground.sh writes the fixture repos.
func PlaygroundDir(t testing.TB) string {
	return filepath.Join(MustRepoRoot(t), "examples", "playground")
}

var playground struct {
	once sync.Once
	err  error
}

// Playground makes sure the playground fixtures exist and returns their
// directory. Like test/setup/global-setup.ts:
//   - fresh checkout / CI: MANIFEST.tsv is absent → generate it;
//   - local: regenerate only if missing (PLAYGROUND_FORCE=1 forces it — run
//     with `go test -p 1` then, since a rebuild wipes repos other test
//     binaries may be reading).
//
// It runs once per test binary (sync.Once) and, because `go test ./...` runs
// package binaries in parallel, under a file lock so two processes never
// generate at the same time.
func Playground(t testing.TB) string {
	t.Helper()
	dir := PlaygroundDir(t)
	playground.once.Do(func() { playground.err = ensurePlayground(MustRepoRoot(t), dir) })
	if playground.err != nil {
		t.Fatalf("playground: %v", playground.err)
	}
	return dir
}

func ensurePlayground(rootDir, dir string) error {
	manifest := filepath.Join(dir, "MANIFEST.tsv")
	force := os.Getenv("PLAYGROUND_FORCE") == "1"
	if !force && exists(manifest) {
		return nil
	}
	// The lock lives in the temp dir, keyed by checkout, so it never shows up
	// as an untracked file in the repository.
	sum := sha1.Sum([]byte(rootDir))
	unlock, err := lockFile(filepath.Join(os.TempDir(), "gitcito-playground-"+hex.EncodeToString(sum[:6])+".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	// Another process may have generated it while we waited for the lock.
	if !force && exists(manifest) {
		return nil
	}
	cmd := exec.Command("bash", filepath.Join(rootDir, "examples", "setup-playground.sh"))
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New("setup-playground.sh failed: " + err.Error() + "\n" + string(out))
	}
	if !exists(manifest) {
		return errors.New("setup-playground.sh wrote no MANIFEST.tsv")
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// PlaygroundRepo is the absolute path of a playground repo by scenario name,
// for READ-ONLY use. It fails the test if the scenario does not exist.
func PlaygroundRepo(t testing.TB, name string) string {
	t.Helper()
	p := filepath.Join(Playground(t), name)
	if !exists(p) {
		t.Fatalf("no playground repo %q (is it in examples/scenarios?)", name)
	}
	return p
}

// ManifestEntry is one MANIFEST.tsv line.
type ManifestEntry struct {
	Name        string
	Description string
}

// ReadManifest parses MANIFEST.tsv (one `name<TAB>description` line per
// scenario), generating the playground first if needed.
func ReadManifest(t testing.TB) []ManifestEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(Playground(t), "MANIFEST.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []ManifestEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" {
			continue
		}
		name, desc, _ := strings.Cut(line, "\t")
		out = append(out, ManifestEntry{Name: name, Description: desc})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// uncopyable are scenarios whose linked worktrees / submodule gitlinks hold
// absolute paths that do not survive a copy.
var uncopyable = map[string]bool{"submodules-worktrees": true}

// CloneFixture copies a playground repo (including its .git) into a fresh
// directory under t.TempDir() and returns it; the test mutates the copy, and
// the testing package removes it afterwards (cleanupFixtures in TS).
//
// It is a copy, not `git clone`: a clone would drop what the scenario set up
// outside the object store — the index, stashes, an in-progress merge, local
// config, hooks. The copy is named `gitcito-<name>-<random>` like the TS one,
// so a repo's basename looks the same in both suites.
//
// The developer's global git config must not decide what these tests see.
// `rerere.enabled` in particular changes what a conflicting merge leaves
// behind, so a machine with it on would fail tests that have nothing to do
// with it: the copy pins it off and drops any rr-cache. Tests that *are* about
// rerere turn it on themselves.
//
// Only for self-contained repos — not submodules-worktrees (the test fails).
func CloneFixture(t testing.TB, name string) string {
	t.Helper()
	if uncopyable[name] {
		t.Fatalf("CloneFixture(%q): its worktrees and submodule gitlinks hold absolute paths; use it read-only or build a fresh repo", name)
	}
	src := PlaygroundRepo(t, name)
	dir, err := os.MkdirTemp(t.TempDir(), "gitcito-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dir); err != nil {
		t.Fatalf("copy %s: %v", name, err)
	}
	neutralize(dir)
	return dir
}

// neutralize pins the settings that would otherwise leak in from the
// machine's config. A bare fixture (a .git directory used as a remote) has
// neither, so failures are ignored.
func neutralize(dir string) {
	cmd := exec.Command("git", "-C", dir, "config", "--local", "rerere.enabled", "false")
	if cmd.Run() == nil {
		_ = os.RemoveAll(filepath.Join(dir, ".git", "rr-cache"))
	}
}

// copyTree copies src's contents into the existing directory dst, keeping
// file modes (hooks stay executable) and copying symlinks as symlinks with
// their targets verbatim. (Node's cpSync rewrote relative link targets to
// absolute ones pointing back into the source; verbatim keeps a mutating
// test from reaching through a link into the shared playground.)
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return nil // sockets, fifos: nothing a fixture needs
		}
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm|0o200)
}

// Git runs `git -C dir args...` and returns its stdout with surrounding
// whitespace trimmed, failing the test (with stderr) on a non-zero exit.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// GitEnv describes the isolated environment IsolateGitEnv installed.
type GitEnv struct {
	Home         string // HOME (and USERPROFILE on Windows)
	ConfigHome   string // XDG_CONFIG_HOME
	GlobalConfig string // GIT_CONFIG_GLOBAL: a file with a fixed identity
}

// IsolateGitEnv points HOME, XDG_CONFIG_HOME and GIT_CONFIG_GLOBAL at fresh
// temp locations and turns off the system config, so a test never reads or
// writes the developer's real global git config, ~/.ssh, or credentials. The
// global config it writes holds only a fixed identity (so commits work),
// `init.defaultBranch=main`, and signing off. Restored when the test ends.
//
// It uses t.Setenv, so the test (and its parents) must not be parallel.
func IsolateGitEnv(t testing.TB) GitEnv {
	t.Helper()
	base := t.TempDir()
	env := GitEnv{
		Home:         filepath.Join(base, "home"),
		ConfigHome:   filepath.Join(base, "xdg"),
		GlobalConfig: filepath.Join(base, "home", ".gitconfig"),
	}
	for _, d := range []string{env.Home, env.ConfigHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := "[user]\n\tname = Gitcito Test\n\temail = test@gitcito.invalid\n" +
		"[init]\n\tdefaultBranch = main\n" +
		"[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n"
	if err := os.WriteFile(env.GlobalConfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", env.Home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", env.Home)
	}
	t.Setenv("XDG_CONFIG_HOME", env.ConfigHome)
	t.Setenv("GIT_CONFIG_GLOBAL", env.GlobalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	return env
}

// InitRepo makes an empty repository in a fresh temp dir (with
// IsolateGitEnv's identity if the caller installed it) and returns its path.
func InitRepo(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Git(t, dir, "init", "-q", "-b", "main")
	Git(t, dir, "config", "user.name", "Gitcito Test")
	Git(t, dir, "config", "user.email", "test@gitcito.invalid")
	Git(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// WriteFile writes content to dir/rel, creating parent directories.
func WriteFile(t testing.TB, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
