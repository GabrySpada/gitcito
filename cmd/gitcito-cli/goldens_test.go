package main

// Replays cmd/gitcito-cli/testdata/goldens.json — stdout, stderr, exit code,
// the argv the app was launched with, and the files/config left behind — of
// every case test/port/cli.test.ts ran against the Electron-era CLI, now
// against this binary built with CGO_ENABLED=0.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
	"github.com/GabrySpada/gitcito/internal/version"
)

type goldenOp struct {
	Write   []string `json:"write"`
	Git     []string `json:"git"`
	MayFail bool     `json:"mayFail"`
	Mkdir   string   `json:"mkdir"`
}

type goldenResult struct {
	Stdout    string             `json:"stdout"`
	Stderr    string             `json:"stderr"`
	Code      int                `json:"code"`
	AppArgs   []string           `json:"appArgs"`
	Files     map[string]*string `json:"files,omitempty"`
	Config    map[string]*string `json:"config,omitempty"`
	Sentinels *int               `json:"sentinels,omitempty"`
}

type goldenCase struct {
	Name      string          `json:"name"`
	Bin       string          `json:"bin"`
	Repo      json.RawMessage `json:"repo"`
	Ops       []goldenOp      `json:"ops"`
	Settings  json.RawMessage `json:"settings"`
	Cwd       string          `json:"cwd"`
	Args      []string        `json:"args"`
	App       *bool           `json:"app"`
	Normalize []string        `json:"normalize"`
	Check     struct {
		Files     []string `json:"files"`
		Config    []string `json:"config"`
		Sentinels bool     `json:"sentinels"`
	} `json:"check"`
	Expect goldenResult `json:"expect"`
}

type goldenFile struct {
	FakeApp string       `json:"fakeApp"`
	Cases   []goldenCase `json:"cases"`
}

var builtCLI string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gitcito-cli-build-")
	if err != nil {
		panic(err)
	}
	builtCLI = filepath.Join(dir, "gitcito-cli")
	// CGO off is part of the contract: the companion binary must build without
	// a C toolchain or GTK headers.
	cmd := exec.Command("go", "build", "-o", builtCLI, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		panic("build gitcito-cli: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func loadGoldens(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "goldens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Cases) == 0 {
		t.Fatal("no goldens")
	}
	return g
}

func caseEnv(tmp string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(tmp, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(tmp, "xdg"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(tmp, "home", ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"TMPDIR=" + filepath.Join(tmp, "tmp"),
		"LC_ALL=C",
		"LANG=C",
		"GITCITO_DATA_DIR=" + filepath.Join(tmp, "data"),
		"FAKE_APP_LOG=" + filepath.Join(tmp, "app.log"),
		"GIT_AUTHOR_NAME=Gitcito Test",
		"GIT_AUTHOR_EMAIL=test@gitcito.invalid",
		"GIT_COMMITTER_NAME=Gitcito Test",
		"GIT_COMMITTER_EMAIL=test@gitcito.invalid",
		"GIT_AUTHOR_DATE=2024-01-02T03:04:05Z",
		"GIT_COMMITTER_DATE=2024-01-02T03:04:05Z",
	}
}

func subst(s, tmp string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "<REPO>", filepath.Join(tmp, "repo")), "<TMP>", tmp)
}

func at(p, tmp string) string {
	if strings.HasPrefix(p, "<TMP>") {
		return subst(p, tmp)
	}
	return filepath.Join(tmp, "repo", p)
}

func mustWrite(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, env []string, dir string, mayFail bool, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	if out, err := cmd.CombinedOutput(); err != nil && !mayFail {
		t.Fatalf("setup git %v: %v\n%s", args, err, out)
	}
}

func setupCase(t *testing.T, c goldenCase, tmp string, env []string) {
	for _, d := range []string{"home", "xdg", "tmp", "data"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(tmp, "home", ".gitconfig"), "[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n", 0o644)
	repo := filepath.Join(tmp, "repo")
	var repoSpec any
	_ = json.Unmarshal(c.Repo, &repoSpec)
	switch r := repoSpec.(type) {
	case string: // "init"
		runGit(t, env, tmp, false, "init", "-q", "-b", "main", repo)
		mustWrite(t, filepath.Join(repo, "README.md"), "# demo\n", 0o644)
		runGit(t, env, repo, false, "add", "-A")
		runGit(t, env, repo, false, "commit", "-q", "-m", "chore: initial commit")
	case map[string]any:
		clone := testutil.CloneFixture(t, r["fixture"].(string))
		if err := os.Rename(clone, repo); err != nil {
			t.Fatal(err)
		}
	}
	for _, op := range c.Ops {
		switch {
		case op.Write != nil:
			mustWrite(t, at(op.Write[0], tmp), op.Write[1], 0o644)
		case op.Mkdir != "":
			if err := os.MkdirAll(at(op.Mkdir, tmp), 0o755); err != nil {
				t.Fatal(err)
			}
		case op.Git != nil:
			args := make([]string, len(op.Git))
			for i, a := range op.Git {
				args[i] = subst(a, tmp)
			}
			dir := tmp
			if isDir(repo) {
				dir = repo
			}
			runGit(t, env, dir, op.MayFail, args...)
		}
	}
	if len(c.Settings) > 0 {
		var text string
		if json.Unmarshal(c.Settings, &text) != nil {
			// Re-indent verbatim: key order and unescaped `<REPO>` survive.
			var buf bytes.Buffer
			if err := json.Indent(&buf, c.Settings, "", "  "); err != nil {
				t.Fatal(err)
			}
			text = buf.String()
		}
		mustWrite(t, filepath.Join(tmp, "xdg", "Gitcito", settingsFileName), subst(text, tmp), 0o644)
		mustWrite(t, filepath.Join(tmp, "data", settingsFileName), subst(text, tmp), 0o644)
	}
}

var (
	waitIDRe = regexp.MustCompile(`gitcito-wait\.[A-Za-z0-9]+`)
	agoRe    = regexp.MustCompile(`\b\d+ (seconds?|minutes?|hours?|days?|weeks?|months?|years?)(, \d+ months?)? ago`)
	shaRe    = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
)

func normalize(s string, c goldenCase, tmp string) string {
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		s = strings.ReplaceAll(s, real, "<TMP>")
	}
	s = strings.ReplaceAll(s, tmp, "<TMP>")
	s = waitIDRe.ReplaceAllString(s, "gitcito-wait.<ID>")
	for _, n := range c.Normalize {
		switch n {
		case "ago":
			s = agoRe.ReplaceAllString(s, "<AGO>")
		case "sha":
			s = shaRe.ReplaceAllString(s, "<SHA>")
		case "version":
			s = strings.ReplaceAll(s, version.Version, "<VERSION>")
		}
	}
	return s
}

// installCLI links the built binary into dir (a copy when hard links are not
// possible), so it finds the fake app next to itself the way a build lays it out.
func installCLI(t *testing.T, dir, name string) string {
	t.Helper()
	dst := filepath.Join(dir, name)
	if err := os.Link(builtCLI, dst); err == nil {
		return dst
	}
	src, err := os.Open(builtCLI)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, src); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	return dst
}

func isWindowVerb(c goldenCase) bool {
	return c.Bin != "textconv" && (len(c.Args) == 0 || !headlessVerbs[c.Args[0]])
}

func runCase(t *testing.T, g goldenFile, c goldenCase, viaTextconvLink bool) goldenResult {
	tmp := t.TempDir()
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	env := caseEnv(tmp)
	setupCase(t, c, tmp, env)
	binDir := filepath.Join(tmp, "go")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := installCLI(t, binDir, "gitcito-cli")
	if c.App == nil || *c.App {
		mustWrite(t, filepath.Join(binDir, "Gitcito"), g.FakeApp, 0o755)
	}
	args := make([]string, 0, len(c.Args)+1)
	if c.Bin == "textconv" {
		if viaTextconvLink {
			link := filepath.Join(binDir, "gitcito-textconv")
			if err := os.Symlink(cli, link); err != nil {
				t.Fatal(err)
			}
			cli = link
		} else {
			args = append(args, "textconv")
		}
	}
	for _, a := range c.Args {
		args = append(args, subst(a, tmp))
	}
	cwdDir := "<TMP>"
	if c.Cwd != "" {
		cwdDir = c.Cwd
	} else if len(c.Repo) > 0 {
		cwdDir = "<REPO>"
	}
	wd := subst(cwdDir, tmp)
	cmd := exec.Command(cli, args...)
	cmd.Dir = wd
	cmd.Env = append(env, "PWD="+wd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	res := goldenResult{Stdout: normalize(stdout.String(), c, tmp), Stderr: normalize(stderr.String(), c, tmp), Code: code}
	log := filepath.Join(tmp, "app.log")
	if isWindowVerb(c) && code == 0 && (c.App == nil || *c.App) {
		for i := 0; i < 100 && !exists(log); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if b, err := os.ReadFile(log); err == nil {
		lines := strings.Split(string(b), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		for i := range lines {
			lines[i] = normalize(lines[i], c, tmp)
		}
		res.AppArgs = lines
	}
	if c.Check.Files != nil {
		res.Files = map[string]*string{}
		for _, f := range c.Check.Files {
			if b, err := os.ReadFile(at(f, tmp)); err == nil {
				s := string(b)
				res.Files[f] = &s
			} else {
				res.Files[f] = nil
			}
		}
	}
	if c.Check.Config != nil {
		res.Config = map[string]*string{}
		for _, key := range c.Check.Config {
			scope, name, _ := strings.Cut(key, ":")
			dir := tmp
			if isDir(filepath.Join(tmp, "repo")) {
				dir = filepath.Join(tmp, "repo")
			}
			g := exec.Command("git", "config", "--"+scope, "--get", name)
			g.Dir, g.Env = dir, env
			if out, err := g.Output(); err == nil {
				s := strings.TrimSuffix(string(out), "\n")
				res.Config[key] = &s
			} else {
				res.Config[key] = nil
			}
		}
	}
	if c.Check.Sentinels {
		n := 0
		entries, _ := os.ReadDir(filepath.Join(tmp, "tmp"))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "gitcito-wait.") {
				n++
			}
		}
		res.Sentinels = &n
	}
	return res
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestGoldens(t *testing.T) {
	testutil.Playground(t)
	g := loadGoldens(t)
	for _, c := range g.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := runCase(t, g, c, false)
			if !reflect.DeepEqual(got, c.Expect) {
				gj, _ := json.MarshalIndent(got, "", "  ")
				wj, _ := json.MarshalIndent(c.Expect, "", "  ")
				t.Errorf("got:\n%s\nwant:\n%s", gj, wj)
			}
		})
	}
}

// git runs the textconv filter as `gitcito-textconv <file>`; a link of that
// name must behave exactly like the `textconv` verb.
func TestGoldensTextconvLink(t *testing.T) {
	g := loadGoldens(t)
	for _, c := range g.Cases {
		if c.Bin != "textconv" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			got := runCase(t, g, c, true)
			if !reflect.DeepEqual(got, c.Expect) {
				t.Errorf("got %+v, want %+v", got, c.Expect)
			}
		})
	}
}
