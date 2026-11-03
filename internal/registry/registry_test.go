package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

var ctx = context.Background()

// ─── vectors ────────────────────────────────────────────────────────────────

// Replays testdata/vectors/repoMeta.json (test/port/registry.test.ts).
func TestOwnerFromRemoteURLVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "repoMeta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		URL string  `json:"url"`
		Out *string `json:"out"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range cases {
		got := OwnerFromRemoteURL(c.URL)
		if (got == nil) != (c.Out == nil) || got != nil && *got != *c.Out {
			t.Errorf("OwnerFromRemoteURL(%q) = %v, want %v", c.URL, deref(got), deref(c.Out))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func fakeRepo(t *testing.T, dir, head string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ─── test/repoMeta.test.ts ──────────────────────────────────────────────────

// ownerFromRemoteUrl > …
func TestOwnerFromRemoteURL(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"https://github.com/acme/widget.git", "acme"},                      // reads a GitHub org
		{"git@github.com:acme/widget.git", "acme"},                          // reads a GitHub org
		{"https://gitlab.com/top-solution/sub/widget.git", "top-solution"},  // nested GitLab namespace
		{"https://git.example.com/top-solution/widget.git", "top-solution"}, // unknown host
		{"git@git.example.com:top-solution/widget.git", "top-solution"},     // unknown host, scp
		{"https://git.example.com/widget.git", ""},                          // no namespace
		{"not a url", ""}, // no namespace
		{"", ""},          // no namespace
		{"ssh://git@host.example.com:2222/group/repo.git", "group"},                   // not the port
		{"https://user@git.example.com:8443/top-solution/widget.git", "top-solution"}, // not the port
	} {
		if got := deref(OwnerFromRemoteURL(c.url)); got != c.want && !(c.want == "" && got == "<nil>") {
			t.Errorf("OwnerFromRemoteURL(%q) = %s, want %q", c.url, got, c.want)
		}
	}
}

// readHeadBranch > reads the checked-out branch without spawning git
func TestReadHeadBranchFixture(t *testing.T) {
	dir := testutil.CloneFixture(t, "file-nav")
	if got := deref(ReadHeadBranch(dir)); got != "main" {
		t.Fatalf("branch = %s", got)
	}
}

// readHeadBranch > returns null for a path that is not a repo
func TestReadHeadBranchNotARepo(t *testing.T) {
	if ReadHeadBranch("/definitely/not/here") != nil {
		t.Fatal("not nil")
	}
}

// gitDirOf > resolves a .git directory
func TestGitDirOfDirectory(t *testing.T) {
	dir := testutil.CloneFixture(t, "file-nav")
	if got := GitDirOf(dir); got != filepath.Join(dir, ".git") {
		t.Fatalf("gitDir = %s", got)
	}
}

// gitDirOf > follows a .git file to the real git directory
func TestGitDirOfFile(t *testing.T) {
	dir, real := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+real+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "HEAD"), []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GitDirOf(dir); got != real {
		t.Fatalf("gitDir = %s", got)
	}
	if got := deref(ReadHeadBranch(dir)); got != "feature" {
		t.Fatalf("branch = %s", got)
	}
	// A relative gitdir resolves against the repository.
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: ../elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GitDirOf(dir); got != filepath.Join(filepath.Dir(dir), "elsewhere") {
		t.Fatalf("relative gitDir = %s", got)
	}
}

// gitDirOf > returns null for a detached HEAD
func TestReadHeadBranchDetached(t *testing.T) {
	dir := t.TempDir()
	fakeRepo(t, dir, "9f4a1c2e8b7d6a5f4e3c2b1a0d9e8f7a6b5c4d3e\n")
	if ReadHeadBranch(dir) != nil {
		t.Fatal("not nil")
	}
}

// readOriginOwner > returns null for a repo with no remote
func TestReadOriginOwnerNoRemote(t *testing.T) {
	if ReadOriginOwner(testutil.CloneFixture(t, "file-nav")) != nil {
		t.Fatal("not nil")
	}
}

// readOriginOwner > reads the owner of origin from .git/config
// host-remotes carries several remotes; only origin decides the owner.
func TestReadOriginOwner(t *testing.T) {
	if got := deref(ReadOriginOwner(testutil.CloneFixture(t, "host-remotes"))); got != "contoso" {
		t.Fatalf("owner = %s", got)
	}
}

// ─── test/repoScan.test.ts ──────────────────────────────────────────────────

func scanTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{"alpha", "client/beta", "client/gamma", "too/deep/for/us/delta", "alpha/vendor/nested", "node_modules/evil"} {
		fakeRepo(t, filepath.Join(root, filepath.FromSlash(rel)), "ref: refs/heads/main\n")
	}
	if err := os.MkdirAll(filepath.Join(root, "plain-folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// scanForRepos > finds repos down to the given depth / does not descend past the depth limit
func TestScanDepth(t *testing.T) {
	root := scanTree(t)
	found := ScanForRepos(ctx, root, 3)
	for _, rel := range []string{"alpha", "client/beta", "client/gamma"} {
		if !contains(found, filepath.Join(root, filepath.FromSlash(rel))) {
			t.Errorf("missing %s in %v", rel, found)
		}
	}
	if contains(found, filepath.Join(root, "too", "deep", "for", "us", "delta")) {
		t.Error("descended past the depth limit")
	}
}

// scanForRepos > stops at a repository rather than walking into it / skips node_modules
func TestScanStopsAndSkips(t *testing.T) {
	root := scanTree(t)
	found := ScanForRepos(ctx, root, 5)
	if contains(found, filepath.Join(root, "alpha", "vendor", "nested")) {
		t.Error("walked into a repository")
	}
	if contains(found, filepath.Join(root, "node_modules", "evil")) {
		t.Error("entered node_modules")
	}
}

// scanForRepos > returns nothing for a root that does not exist
func TestScanMissingRoot(t *testing.T) {
	if got := ScanForRepos(ctx, "/definitely/not/here", 3); len(got) != 0 || got == nil {
		t.Fatalf("got %v", got)
	}
}

// The depth ceiling holds whatever the caller asks for.
func TestScanClampsDepth(t *testing.T) {
	root := t.TempDir()
	deep := root
	for i := 0; i < 12; i++ {
		deep = filepath.Join(deep, "d")
	}
	fakeRepo(t, deep, "ref: refs/heads/main\n")
	if got := ScanForRepos(ctx, root, 1e9); len(got) != 0 {
		t.Fatalf("found %v past the ceiling", got)
	}
	if got := ScanForRepos(ctx, root, 12.5); len(got) != 0 {
		t.Fatalf("found %v with a fractional depth", got)
	}
}

// ─── test/repoRegistry.test.ts ──────────────────────────────────────────────

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "data", FileName), nil)
}

func mustList(t *testing.T, r *Registry) []RegistryRepo {
	t.Helper()
	repos, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	return repos
}

// repoRegistry > starts empty
func TestRegistryStartsEmpty(t *testing.T) {
	repos := mustList(t, newRegistry(t))
	if repos == nil || len(repos) != 0 {
		t.Fatalf("repos = %v", repos)
	}
}

// repoRegistry > remembers a repo and survives a reload
func TestRegistryRemembers(t *testing.T) {
	r := newRegistry(t)
	dir := t.TempDir()
	if _, err := r.Remember(dir); err != nil {
		t.Fatal(err)
	}
	repos := mustList(t, New(r.Path(), nil))
	if len(repos) != 1 || repos[0].Path != dir || repos[0].Source != "opened" || repos[0].LastOpenedAt <= 0 {
		t.Fatalf("repos = %+v", repos)
	}
}

// repoRegistry > upserts rather than duplicating, and bumps lastOpenedAt
func TestRegistryUpserts(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	r := New(filepath.Join(t.TempDir(), FileName), func() time.Time { return clock })
	dir := t.TempDir()
	_, _ = r.Remember(dir)
	first := mustList(t, r)[0].LastOpenedAt
	clock = clock.Add(1100 * time.Millisecond)
	_, _ = r.Remember(dir)
	repos := mustList(t, r)
	if len(repos) != 1 || repos[0].LastOpenedAt <= first {
		t.Fatalf("repos = %+v", repos)
	}
}

// repoRegistry > marks a deleted folder missing instead of dropping it
func TestRegistryMarksMissing(t *testing.T) {
	r := newRegistry(t)
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _ = r.Remember(dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	repos := mustList(t, r)
	if len(repos) != 1 || !repos[0].Missing {
		t.Fatalf("repos = %+v", repos)
	}
}

// repoRegistry > forgets a repo without touching disk
func TestRegistryForgets(t *testing.T) {
	r := newRegistry(t)
	dir := t.TempDir()
	_, _ = r.Remember(dir)
	if _, err := r.Forget(dir); err != nil {
		t.Fatal(err)
	}
	if repos := mustList(t, r); len(repos) != 0 {
		t.Fatalf("repos = %+v", repos)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("folder touched")
	}
}

// repoRegistry > rejects a path that is not safe
func TestRegistryRejectsUnsafePath(t *testing.T) {
	r := newRegistry(t)
	for _, p := range []string{"", "   ", "relative/path", "/a\x00b", "/" + strings.Repeat("x", 4096)} {
		_, _ = r.Remember(p)
	}
	if repos := mustList(t, r); len(repos) != 0 {
		t.Fatalf("repos = %+v", repos)
	}
}

// repoRegistry > populates branch from .git when remembering a real repo
func TestRegistryPopulatesBranch(t *testing.T) {
	r := newRegistry(t)
	dir := testutil.CloneFixture(t, "file-nav")
	_, _ = r.Remember(dir)
	if got := deref(mustList(t, r)[0].Branch); got != "main" {
		t.Fatalf("branch = %s", got)
	}
}

// repoRegistry > keeps an opened repo opened when a scan finds it again
func TestRegistryScanKeepsOpened(t *testing.T) {
	r := newRegistry(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "alpha")
	fakeRepo(t, dir, "ref: refs/heads/main\n")
	_, _ = r.Remember(dir)
	if _, err := r.Scan(ctx, []RepoScanRoot{{Path: parent, Depth: 2}}); err != nil {
		t.Fatal(err)
	}
	for _, repo := range mustList(t, r) {
		if repo.Path == dir && (repo.Source != "opened" || repo.LastOpenedAt <= 0) {
			t.Fatalf("demoted: %+v", repo)
		}
	}
}

// repoRegistry > adds a repo found only by a scan as scanned, unopened
func TestRegistryScanAddsScanned(t *testing.T) {
	r := newRegistry(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "beta")
	fakeRepo(t, dir, "ref: refs/heads/develop\n")
	_, _ = r.Scan(ctx, []RepoScanRoot{{Path: parent, Depth: 2}})
	repos := mustList(t, r)
	if len(repos) != 1 || repos[0].Path != dir || repos[0].Source != "scanned" || repos[0].LastOpenedAt != 0 || deref(repos[0].Branch) != "develop" {
		t.Fatalf("repos = %+v", repos)
	}
}

// repoRegistry > counts only what a scan added, not the registry it returns
func TestRegistryScanCountsAdded(t *testing.T) {
	r := newRegistry(t)
	parent := t.TempDir()
	for _, name := range []string{"one", "two"} {
		fakeRepo(t, filepath.Join(parent, name), "ref: refs/heads/main\n")
	}
	_, _ = r.Remember(t.TempDir()) // already in the registry, found by no scan
	first, err := r.Scan(ctx, []RepoScanRoot{{Path: parent, Depth: 2}})
	if err != nil || first.Added != 2 || len(first.Repos) != 3 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	again, _ := r.Scan(ctx, []RepoScanRoot{{Path: parent, Depth: 2}})
	if again.Added != 0 {
		t.Fatalf("again = %+v", again)
	}
}

// repoRegistry > files a padded path under its trimmed spelling
func TestRegistryTrimsPaths(t *testing.T) {
	r := newRegistry(t)
	dir := t.TempDir()
	_, _ = r.Remember("  " + dir + "  ")
	_, _ = r.Remember(dir)
	repos := mustList(t, r)
	if len(repos) != 1 || repos[0].Path != dir {
		t.Fatalf("repos = %+v", repos)
	}
}

// repoRegistry > locates a moved repo, keeping its identity
func TestRegistryLocates(t *testing.T) {
	r := newRegistry(t)
	dir := t.TempDir()
	_, _ = r.Remember(dir)
	// Locate refuses to re-point at a folder that isn't a repository.
	notRepo := t.TempDir()
	_, _ = r.Locate(dir, notRepo)
	if mustList(t, r)[0].Path != dir {
		t.Fatal("re-pointed at a folder that is not a repository")
	}
	moved := t.TempDir()
	fakeRepo(t, moved, "ref: refs/heads/main\n")
	if _, err := r.Locate(dir, moved); err != nil {
		t.Fatal(err)
	}
	repos := mustList(t, r)
	if len(repos) != 1 || repos[0].Path != moved || repos[0].Missing || repos[0].Name != filepath.Base(moved) {
		t.Fatalf("repos = %+v", repos)
	}
}

// Refresh re-reads the branch, but only for listed, present repositories.
func TestRegistryRefresh(t *testing.T) {
	r := newRegistry(t)
	dir := t.TempDir()
	fakeRepo(t, dir, "ref: refs/heads/main\n")
	_, _ = r.Remember(dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if repos, _ := r.Refresh([]string{"/elsewhere"}); deref(repos[0].Branch) != "main" {
		t.Fatal("refreshed an unlisted repository")
	}
	if repos, _ := r.Refresh([]string{dir}); deref(repos[0].Branch) != "next" {
		t.Fatalf("branch = %s", deref(repos[0].Branch))
	}
}

// repoRegistry > does not lose entries when remembers race a scan (write queue)
func TestRegistryConcurrentWrites(t *testing.T) {
	r := newRegistry(t)
	parent := t.TempDir()
	scanned := filepath.Join(parent, "gamma")
	fakeRepo(t, scanned, "ref: refs/heads/main\n")
	var opened []string
	for i := 0; i < 5; i++ {
		opened = append(opened, t.TempDir())
	}
	var wg sync.WaitGroup
	wg.Add(1 + len(opened))
	go func() {
		defer wg.Done()
		_, _ = r.Scan(ctx, []RepoScanRoot{{Path: parent, Depth: 2}})
	}()
	for _, d := range opened {
		go func(d string) {
			defer wg.Done()
			_, _ = r.Remember(d)
		}(d)
	}
	wg.Wait()
	paths := map[string]bool{}
	for _, repo := range mustList(t, r) {
		paths[repo.Path] = true
	}
	for _, d := range append(opened, scanned) {
		if !paths[d] {
			t.Errorf("lost %s", d)
		}
	}
}

// A corrupt file starts fresh rather than failing the page.
func TestRegistryCorruptFile(t *testing.T) {
	r := newRegistry(t)
	if err := os.MkdirAll(filepath.Dir(r.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"{nope", `{"repos":"x"}`, `{}`} {
		if err := os.WriteFile(r.Path(), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if repos := mustList(t, r); repos == nil || len(repos) != 0 {
			t.Fatalf("%s: repos = %v", content, repos)
		}
	}
}

// The wire shape: nulls where TS had null, [] for an empty registry.
func TestServiceJSONShape(t *testing.T) {
	s := NewService(newRegistry(t))
	repos, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(repos); string(b) != "[]" {
		t.Fatalf("empty = %s", b)
	}
	dir := t.TempDir()
	repos, _ = s.Remember(ctx, dir)
	b, _ := json.Marshal(repos[0])
	for _, want := range []string{`"owner":null`, `"branch":null`, `"source":"opened"`, `"missing":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
	res, _ := s.Scan(ctx, nil)
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"added":0`) {
		t.Fatalf("scan = %s", b)
	}
}
