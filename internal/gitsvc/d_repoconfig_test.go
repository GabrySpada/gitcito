package gitsvc

// Ported from test/repoConfig.test.ts. The TS suite called src/main/repoConfig.ts
// directly; here the same cases go through the gitService methods that wrap
// it (repoConfig, setRepoConfig, repoDoctor, applyDoctorFix, suggestRepoConfig).

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

func dBoolPtr(b bool) *bool { return &b }

func dWriteConfig(t *testing.T, s *GitService, repo string, c RepoConfig) {
	t.Helper()
	if _, err := s.SetRepoConfig(dCtx, repo, c); err != nil {
		t.Fatal(err)
	}
}

func dDoctor(t *testing.T, s *GitService, repo string) []DoctorCheck {
	t.Helper()
	checks, err := s.RepoDoctor(dCtx, repo)
	if err != nil {
		t.Fatal(err)
	}
	return checks
}

// repoConfig: reading and writing .gitcito.json > reports a repository that has none, without inventing a config
func TestDRepoConfigAbsent(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	r, err := New(nil, nil).RepoConfig(dCtx, repo)
	if err != nil || r.Exists || r.Config != nil || len(r.Issues) != 0 || r.Issues == nil {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
}

// repoConfig: reading and writing .gitcito.json > writes a config and reads back exactly what it stored
func TestDRepoConfigRoundTrip(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	want := RepoConfig{Version: 1, Protect: []string{"main"}, Commit: &RepoConfigCommit{Scopes: []string{"ui"}}}
	dWriteConfig(t, s, repo, want)
	back, _ := s.RepoConfig(dCtx, repo)
	if !back.Exists || !reflect.DeepEqual(*back.Config, want) {
		t.Fatalf("back = %+v", back.Config)
	}
	// It is a plain tracked file, so it shows up as a working-tree change.
	if !strings.Contains(testutil.Git(t, repo, "status", "--porcelain"), ".gitcito.json") {
		t.Fatal(".gitcito.json not in status")
	}
}

// repoConfig: reading and writing .gitcito.json > re-validates on write rather than trusting the caller
func TestDRepoConfigRevalidatesOnWrite(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	if _, err := New(nil, nil).SetRepoConfig(dCtx, repo, RepoConfig{Version: 7}); err == nil {
		t.Fatal("version 7 accepted")
	}
}

// repoConfig: reading and writing .gitcito.json > keeps the good half of a file with a bad field
func TestDRepoConfigKeepsGoodHalf(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	testutil.WriteFile(t, repo, ".gitcito.json", `{"version":1,"protect":["main"],"requires":{"hooksPath":"../elsewhere"}}`)
	r, _ := New(nil, nil).RepoConfig(dCtx, repo)
	if !reflect.DeepEqual(r.Config.Protect, []string{"main"}) || r.Config.Requires != nil ||
		!reflect.DeepEqual(r.Issues, []RepoConfigIssue{{Field: "requires.hooksPath", Code: "unsafe"}}) {
		t.Fatalf("result = %+v %+v", r.Config, r.Issues)
	}
}

// repoConfig: the repo doctor > says nothing at all when the repository declares no requirements
func TestDDoctorNoRequirements(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Protect: []string{"main"}})
	if checks := dDoctor(t, s, repo); len(checks) != 0 {
		t.Fatalf("checks = %+v", checks)
	}
}

// repoConfig: the repo doctor > finds a missing file, offers the copy, and goes green once it is there
func TestDDoctorMissingFileCopy(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	testutil.WriteFile(t, repo, ".env.example", "API_URL=http://localhost\n")
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Requires: &RepoConfigRequires{Files: []RepoConfigFileReq{{Path: ".env.local", From: ".env.example", Why: "the dev server reads it"}}}})
	check := dDoctor(t, s, repo)[0]
	if check.Status != "fail" || check.Why != "the dev server reads it" || *check.Fix != (DoctorFix{Kind: "copyFile", From: ".env.example", To: ".env.local"}) {
		t.Fatalf("check = %+v", check)
	}
	if err := s.ApplyDoctorFix(dCtx, repo, *check.Fix); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dReadFile(t, filepath.Join(repo, ".env.local")), "API_URL") || dDoctor(t, s, repo)[0].Status != "ok" {
		t.Fatal("copy fix did not take")
	}
}

// repoConfig: the repo doctor > offers no copy when there is no template to copy from
func TestDDoctorNoTemplate(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Requires: &RepoConfigRequires{Files: []RepoConfigFileReq{{Path: ".env.local"}}}})
	if check := dDoctor(t, s, repo)[0]; check.Status != "fail" || check.Fix != nil {
		t.Fatalf("check = %+v", check)
	}
}

// repoConfig: the repo doctor > checks core.hooksPath and can set it
func TestDDoctorHooksPath(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	if err := os.MkdirAll(filepath.Join(repo, ".husky"), 0o755); err != nil {
		t.Fatal(err)
	}
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Requires: &RepoConfigRequires{HooksPath: ".husky"}})
	check := dDoctor(t, s, repo)[0]
	if check.Status != "fail" || *check.Fix != (DoctorFix{Kind: "hooksPath", Value: ".husky"}) {
		t.Fatalf("check = %+v", check)
	}
	if err := s.ApplyDoctorFix(dCtx, repo, *check.Fix); err != nil {
		t.Fatal(err)
	}
	if testutil.Git(t, repo, "config", "--get", "core.hooksPath") != ".husky" || dDoctor(t, s, repo)[0].Status != "ok" {
		t.Fatal("hooksPath fix did not take")
	}
}

// repoConfig: the repo doctor > accepts an absolute core.hooksPath that points at the declared directory
func TestDDoctorAbsoluteHooksPath(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	if err := os.MkdirAll(filepath.Join(repo, ".husky", "_"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "config", "core.hooksPath", filepath.Join(repo, ".husky", "_"))
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Requires: &RepoConfigRequires{HooksPath: ".husky/_"}})
	if check := dDoctor(t, s, repo)[0]; check.Status != "ok" {
		t.Fatalf("check = %+v", check)
	}
}

// repoConfig: the repo doctor > checks the node version against this machine
func TestDDoctorNodeVersion(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Requires: &RepoConfigRequires{Node: ">=1"}})
	if ok := dDoctor(t, s, repo)[0]; ok.Status != "ok" {
		t.Fatalf("check = %+v", ok)
	}
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Requires: &RepoConfigRequires{Node: ">=9999"}})
	if bad := dDoctor(t, s, repo)[0]; bad.Status != "warn" || bad.Expected != ">=9999" || bad.Actual == "" {
		t.Fatalf("check = %+v", bad)
	}
}

// repoConfig: the repo doctor > skips the submodule check in a repository with no submodules
func TestDDoctorNoSubmodules(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	s := New(nil, nil)
	dWriteConfig(t, s, repo, RepoConfig{Version: 1, Requires: &RepoConfigRequires{Submodules: dBoolPtr(true)}})
	if checks := dDoctor(t, s, repo); len(checks) != 0 {
		t.Fatalf("checks = %+v", checks)
	}
}

// repoConfig: doctor repairs refuse to leave the repository or destroy work > never overwrites the file it was asked to create
func TestDDoctorFixNeverOverwrites(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	testutil.WriteFile(t, repo, ".env.example", "A=1\n")
	testutil.WriteFile(t, repo, ".env.local", "A=already mine\n")
	if New(nil, nil).ApplyDoctorFix(dCtx, repo, DoctorFix{Kind: "copyFile", From: ".env.example", To: ".env.local"}) == nil {
		t.Fatal("overwrite allowed")
	}
	if dReadFile(t, filepath.Join(repo, ".env.local")) != "A=already mine\n" {
		t.Fatal("file was overwritten")
	}
}

// repoConfig: doctor repairs refuse to leave the repository or destroy work > refuses a copy that would read or write outside the repository
func TestDDoctorFixRefusesEscape(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	if New(nil, nil).ApplyDoctorFix(dCtx, repo, DoctorFix{Kind: "copyFile", From: "../../etc/hosts", To: "stolen"}) == nil {
		t.Fatal("escape allowed")
	}
	if dExists(filepath.Join(repo, "stolen")) {
		t.Fatal("file was written")
	}
}

// repoConfig: doctor repairs refuse to leave the repository or destroy work > refuses a hooksPath that escapes, even though the loader already checked
func TestDDoctorFixRefusesEscapingHooksPath(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	if New(nil, nil).ApplyDoctorFix(dCtx, repo, DoctorFix{Kind: "hooksPath", Value: "../../hooks"}) == nil {
		t.Fatal("escaping hooksPath accepted")
	}
	// `config --get` exits 1 for a key that was never set — which is the point.
	if dGitErr(repo, "config", "--get", "core.hooksPath") == nil {
		t.Fatal("core.hooksPath was set")
	}
}

func dSuggest(t *testing.T, repo string) RepoConfig {
	t.Helper()
	c, err := New(nil, nil).SuggestRepoConfig(dCtx, repo)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// repoConfig: proposing a config from the repository > infers requirements from the files that are already there
func TestDSuggestInfers(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	testutil.WriteFile(t, repo, ".nvmrc", "v20.11.1\n")
	testutil.WriteFile(t, repo, ".env.example", "A=1\n")
	if err := os.Remove(filepath.Join(repo, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".husky"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := dSuggest(t, repo)
	r := p.Requires
	if p.Version != 1 || r == nil || r.Node != "20.11.1" || r.HooksPath != ".husky" ||
		!reflect.DeepEqual(r.Files, []RepoConfigFileReq{{Path: ".env", From: ".env.example"}}) {
		t.Fatalf("proposal = %+v %+v", p, r)
	}
	// No .gitmodules and no LFS filter in this fixture — nothing invented.
	if r.Submodules != nil || r.Lfs != nil {
		t.Fatalf("requires = %+v", r)
	}
}

// repoConfig: proposing a config from the repository > proposes the hooks path the clone actually uses, not the directory name
func TestDSuggestUsesConfiguredHooksPath(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	if err := os.MkdirAll(filepath.Join(repo, ".husky", "_"), 0o755); err != nil {
		t.Fatal(err)
	}
	// What husky v9 writes: an absolute path at `.husky/_`.
	testutil.Git(t, repo, "config", "core.hooksPath", filepath.Join(repo, ".husky", "_"))
	if r := dSuggest(t, repo).Requires; r == nil || r.HooksPath != ".husky/_" {
		t.Fatalf("requires = %+v", r)
	}
}

// repoConfig: proposing a config from the repository > falls back to .husky only when nothing is configured
func TestDSuggestHuskyFallback(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	if err := os.MkdirAll(filepath.Join(repo, ".husky"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := dSuggest(t, repo).Requires; r == nil || r.HooksPath != ".husky" {
		t.Fatalf("requires = %+v", r)
	}
}

// repoConfig: proposing a config from the repository > never writes another machine's absolute path into a shared file
func TestDSuggestNoForeignAbsolutePath(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	testutil.Git(t, repo, "config", "core.hooksPath", "/opt/somebody-elses/hooks")
	if r := dSuggest(t, repo).Requires; r != nil && r.HooksPath != "" {
		t.Fatalf("requires = %+v", r)
	}
}

// repoConfig: proposing a config from the repository > leaves the .env requirement out when the file is already there
func TestDSuggestEnvAlreadyThere(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	testutil.WriteFile(t, repo, ".env.example", "A=1\n")
	// The fixture already ships a .env — nothing to propose.
	if r := dSuggest(t, repo).Requires; r != nil && r.Files != nil {
		t.Fatalf("requires = %+v", r)
	}
}

// repoConfig: proposing a config from the repository > lifts protected branches out of the per-clone git config
func TestDSuggestProtectedBranches(t *testing.T) {
	repo := testutil.CloneFixture(t, "project-tree")
	testutil.Git(t, repo, "config", "gitcito.protectedbranches", "main,release/3.x")
	if p := dSuggest(t, repo).Protect; !strings.Contains(strings.Join(p, ","), "release/3.x") {
		t.Fatalf("protect = %v", p)
	}
}

// repoConfig: proposing a config from the repository > reads the scopes the history is already using, ignoring one-offs
func TestDSuggestScopes(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "project-tree")
	for _, subject := range []string{"feat(ui): a", "fix(ui): b", "chore(build): c", "docs(build): d", "feat(once): e"} {
		testutil.Git(t, repo, "commit", "--allow-empty", "-m", subject)
	}
	if c := dSuggest(t, repo).Commit; c == nil || !reflect.DeepEqual(c.Scopes, []string{"build", "ui"}) {
		t.Fatalf("commit = %+v", c)
	}
}

// repoConfig: proposing a config from the repository > turns on ticketFromBranch only when the history is full of ticket keys
func TestDSuggestTicketFromBranch(t *testing.T) {
	testutil.IsolateGitEnv(t)
	plain := testutil.CloneFixture(t, "project-tree")
	if c := dSuggest(t, plain).Commit; c != nil && c.TicketFromBranch != nil {
		t.Fatalf("commit = %+v", c)
	}
	ticketed := testutil.CloneFixture(t, "project-tree")
	for i := 1; i <= 6; i++ {
		testutil.Git(t, ticketed, "commit", "--allow-empty", "-m", "GC-"+string(rune('0'+i))+": work")
	}
	if c := dSuggest(t, ticketed).Commit; c == nil || c.TicketFromBranch == nil || !*c.TicketFromBranch {
		t.Fatalf("commit = %+v", c)
	}
}

// Validation edge cases the goldens do not pin: JS Object.keys ordering of
// unknown top-level keys, and the regex dialect bridge.
func TestDRepoConfigValidationEdges(t *testing.T) {
	_, keys, err := dParseJSONOrdered([]byte(`{"b":1,"2":0,"a":1,"10":0,"b":2,"01":0}`))
	if err != nil || strings.Join(keys, ",") != "2,10,b,a,01" {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
	for src, want := range map[string]bool{
		`[A-Z]+-\d+`: true, `(?=x)a`: true, `(a)\1`: true, `(?<y>a)`: true,
		`(`: false, `(?i)a`: false, `(?P<n>a)`: false, ``: false,
	} {
		if got := dIsSafePattern(src); got != want {
			t.Errorf("dIsSafePattern(%q) = %v, want %v", src, got, want)
		}
	}
	for v, want := range map[string]bool{
		".env": true, "a/../b": false, "/etc": false, `\\x`: false, "~/x": false, "C:x": false, ".git/hooks": false, "a\x00b": false,
	} {
		if got := dIsSafeRepoRelPath(v); got != want {
			t.Errorf("dIsSafeRepoRelPath(%q) = %v, want %v", v, got, want)
		}
	}
	for actual, spec := range map[string]string{"20.11.1": "20", "22.1.0": ">=20", "18.0.0": "<20", "20.0.0": "lts"} {
		if !dSatisfiesNodeRange(actual, spec) {
			t.Errorf("%s should satisfy %s", actual, spec)
		}
	}
	if dSatisfiesNodeRange("18.0.0", "^20") || dSatisfiesNodeRange("x", "20") {
		t.Error("range too lenient")
	}
}
