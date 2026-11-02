package gitsvc

// Ported vitest cases for slice D's subtree, clean, bundle/archive,
// maintenance, attributes, filters, credentials and replace methods
// (test/gitOps.test.ts, test/filters.test.ts, test/textconv.test.ts).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

var dCtx = context.Background()

func dCommitAll(t *testing.T, repo, msg string) {
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-m", msg)
}

func dFindSubtree(list []SubtreeInfo, prefix string) *SubtreeInfo {
	for i := range list {
		if list[i].Prefix == prefix {
			return &list[i]
		}
	}
	return nil
}

const dParserPrefix = "vendor/parser"

// gitOps: subtrees > finds a subtree from the trailer git leaves in history
func TestDSubtreesFromTrailer(t *testing.T) {
	repo := testutil.CloneFixture(t, "subtree")
	found, err := New(nil, nil).Subtrees(dCtx, repo)
	if err != nil {
		t.Fatal(err)
	}
	p := dFindSubtree(found, dParserPrefix)
	if p == nil || !p.Present || len(p.LastSplit) < 7 || p.URL != "" {
		t.Fatalf("subtrees = %+v", found)
	}
}

// gitOps: subtrees > remembers the source after an add, since git does not
func TestDSubtreeAddRemembers(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "subtree")
	lib := testutil.CloneFixture(t, "subtree-lib.git")
	s := New(nil, nil)
	if err := s.SubtreeAdd(dCtx, repo, "vendor/second", lib, "main", nil); err != nil {
		t.Fatal(err)
	}
	found, _ := s.Subtrees(dCtx, repo)
	added := dFindSubtree(found, "vendor/second")
	if added == nil || added.URL != lib || added.Ref != "main" || !dExists(filepath.Join(repo, "vendor/second/parser.js")) {
		t.Fatalf("subtrees = %+v", found)
	}
}

// gitOps: subtrees > refuses to add onto a path that already exists
func TestDSubtreeAddRefusesExisting(t *testing.T) {
	repo := testutil.CloneFixture(t, "subtree")
	lib := testutil.CloneFixture(t, "subtree-lib.git")
	dMustMatch(t, New(nil, nil).SubtreeAdd(dCtx, repo, dParserPrefix, lib, "main", nil), `(?i)already exists`)
}

// gitOps: subtrees > refuses an add missing any of prefix, repository or ref
func TestDSubtreeAddRequiresAll(t *testing.T) {
	repo := testutil.CloneFixture(t, "subtree")
	s := New(nil, nil)
	dMustMatch(t, s.SubtreeAdd(dCtx, repo, "", "x", "main", nil), `(?i)required`)
	dMustMatch(t, s.SubtreeAdd(dCtx, repo, "vendor/x", "", "main", nil), `(?i)required`)
	dMustMatch(t, s.SubtreeAdd(dCtx, repo, "vendor/x", "x", "", nil), `(?i)required`)
}

// gitOps: subtrees > pulls upstream changes into the vendored directory
func TestDSubtreePull(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "subtree")
	lib := testutil.CloneFixture(t, "subtree-lib.git")
	// Move the library on: a clone of the bare repo, a commit, a push back.
	work := t.TempDir()
	testutil.Git(t, work, "clone", lib, "w")
	w := filepath.Join(work, "w")
	testutil.WriteFile(t, w, "parser.js", "exports.parse = () => 42\n")
	testutil.Git(t, w, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-am", "feat: upstream change")
	testutil.Git(t, w, "push", "origin", "main")
	if err := New(nil, nil).SubtreePull(dCtx, repo, dParserPrefix, lib, "main", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dReadFile(t, filepath.Join(repo, dParserPrefix, "parser.js")), "42") {
		t.Fatal("subtree was not updated")
	}
}

// gitOps: subtrees > splits the vendored directory back into a branch of its own
func TestDSubtreeSplit(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "subtree")
	if _, err := New(nil, nil).SubtreeSplit(dCtx, repo, dParserPrefix, "parser-only"); err != nil {
		t.Fatal(err)
	}
	// The split branch holds the library's files at its root, not under the prefix.
	names := testutil.Git(t, repo, "ls-tree", "--name-only", "parser-only")
	if !strings.Contains(names, "parser.js") || strings.Contains(names, "app.js") {
		t.Fatalf("ls-tree = %q", names)
	}
}

// gitOps: subtrees > refuses a pull or push with no repository to talk to
func TestDSubtreeNoRepository(t *testing.T) {
	repo := testutil.CloneFixture(t, "subtree")
	s := New(nil, nil)
	dMustMatch(t, s.SubtreePull(dCtx, repo, dParserPrefix, "", "main", nil), `(?i)no remembered repository`)
	dMustMatch(t, s.SubtreePush(dCtx, repo, dParserPrefix, "", "main"), `(?i)no remembered repository`)
}

// gitOps: subtrees > forgets the remembered source without touching the files
func TestDSubtreeForget(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "subtree")
	lib := testutil.CloneFixture(t, "subtree-lib.git")
	s := New(nil, nil)
	if err := s.SubtreeAdd(dCtx, repo, "vendor/third", lib, "main", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SubtreeForget(dCtx, repo, "vendor/third"); err != nil {
		t.Fatal(err)
	}
	found, _ := s.Subtrees(dCtx, repo)
	// Still discovered from history, just no longer remembered.
	e := dFindSubtree(found, "vendor/third")
	if e == nil || e.URL != "" || !dExists(filepath.Join(repo, "vendor/third/parser.js")) {
		t.Fatalf("subtrees = %+v", found)
	}
}

func dEntry(entries []CleanEntry, path string) *CleanEntry {
	for i := range entries {
		if entries[i].Path == path {
			return &entries[i]
		}
	}
	return nil
}

// gitOps: remove untracked files > previews untracked and ignored paths apart, collapsing whole directories
func TestDCleanPreview(t *testing.T) {
	repo := testutil.CloneFixture(t, "untracked-mess")
	p, err := New(nil, nil).CleanPreview(dCtx, repo)
	if err != nil || p.Truncated {
		t.Fatalf("preview = %+v, err = %v", p, err)
	}
	set := func(ignored bool) map[string]bool {
		m := map[string]bool{}
		for _, e := range p.Entries {
			if e.Ignored == ignored {
				m[e.Path] = true
			}
		}
		return m
	}
	untracked, ignored := set(false), set(true)
	if !untracked["notes.md"] || !untracked["tmp/"] || untracked["tmp/scratch.txt"] || untracked[".env"] {
		t.Fatalf("untracked = %v", untracked)
	}
	if !ignored[".env"] || !ignored["dist/"] {
		t.Fatalf("ignored = %v", ignored)
	}
	// A directory is sized by what it holds, and the nested repo is flagged.
	if dEntry(p.Entries, "tmp/").Bytes <= 0 || !dEntry(p.Entries, "experiment/").Nested || dEntry(p.Entries, "notes.md").Kind != "file" {
		t.Fatalf("entries = %+v", p.Entries)
	}
}

// gitOps: remove untracked files > removes only the chosen paths and leaves ignored ones alone
func TestDCleanChosen(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "untracked-mess")
	r, err := New(nil, nil).Clean(dCtx, repo, []string{"notes.md", "tmp/"}, false)
	if err != nil || r.Removed != 2 || r.Bytes <= 0 || r.Trashed {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
	for p, want := range map[string]bool{"notes.md": false, "tmp": false, ".env": true, "dist": true, "src/app.js": true} {
		if dExists(filepath.Join(repo, p)) != want {
			t.Fatalf("%s exists = %v", p, !want)
		}
	}
}

// gitOps: remove untracked files > removes an ignored path when it is explicitly chosen
func TestDCleanIgnoredWhenChosen(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "untracked-mess")
	if _, err := New(nil, nil).Clean(dCtx, repo, []string{"dist/"}, false); err != nil {
		t.Fatal(err)
	}
	if dExists(filepath.Join(repo, "dist")) || !dExists(filepath.Join(repo, "node_modules")) {
		t.Fatal("wrong paths removed")
	}
}

// gitOps: remove untracked files > refuses a tracked path and anything outside the repository
func TestDCleanRefusesTrackedAndEscape(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "untracked-mess")
	s := New(nil, nil)
	_, err := s.Clean(dCtx, repo, []string{"src/app.js"}, true)
	dMustMatch(t, err, `(?i)untracked`)
	_, err = s.Clean(dCtx, repo, []string{"../escape.txt"}, true)
	dMustMatch(t, err, `(?i)untracked`)
	if !dExists(filepath.Join(repo, "src/app.js")) {
		t.Fatal("tracked file removed")
	}
}

// gitOps: remove untracked files > reports the nested repository git skips, and takes it via the trash
func TestDCleanNestedRepoViaTrash(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "untracked-mess")
	s := New(nil, nil)
	_, err := s.Clean(dCtx, repo, []string{"experiment/"}, false)
	dMustMatch(t, err, `(?i)nested repository`)
	if !dExists(filepath.Join(repo, "experiment")) {
		t.Fatal("experiment/ should still be there")
	}
	// The trash route does not go through git, so it has no such limit.
	r, err := s.Clean(dCtx, repo, []string{"experiment/"}, true)
	if err != nil || !r.Trashed || dExists(filepath.Join(repo, "experiment")) {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
	// It went to the (isolated) trash, recoverable, rather than nowhere.
	if entries, _ := os.ReadDir(filepath.Join(os.Getenv("XDG_DATA_HOME"), "Trash", "files")); len(entries) != 1 || entries[0].Name() != "experiment" {
		t.Fatalf("trash = %v", entries)
	}
}

func dTarEntries(t *testing.T, file string) []string {
	out, err := exec.Command("tar", "-tf", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(out))
}

// gitOps: bundles and archives > bundles the whole repository, and another repo can fetch from the file
func TestDBundleAllAndFetch(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	out := filepath.Join(t.TempDir(), "all.bundle")
	s := New(nil, nil)
	r, err := s.BundleCreate(dCtx, repo, out, BundleScope{Kind: "all"})
	// main, a hotfix branch and five tags all travel in one file.
	if err != nil || !dExists(out) || r.Bytes <= 0 || r.Refs <= 5 {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
	info, err := s.BundleInspect(dCtx, repo, out)
	if err != nil || !info.Usable || len(info.Prerequisites) != 0 {
		t.Fatalf("info = %+v", info)
	}
	hasMain := false
	for _, ref := range info.Refs {
		hasMain = hasMain || ref.Name == "refs/heads/main"
	}
	if !hasMain {
		t.Fatalf("refs = %+v", info.Refs)
	}
	// The receiving side: refs land under bundle/, local branches untouched.
	target := testutil.CloneFixture(t, "tags-and-releases")
	testutil.Git(t, target, "reset", "--hard", "HEAD~2")
	before := testutil.Git(t, target, "rev-parse", "main")
	created, err := s.BundleFetch(dCtx, target, out, []string{"refs/heads/main"})
	if err != nil || len(created) != 1 || created[0] != "bundle/main" {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if testutil.Git(t, target, "rev-parse", "refs/remotes/bundle/main") != testutil.Git(t, repo, "rev-parse", "main") ||
		testutil.Git(t, target, "rev-parse", "main") != before {
		t.Fatal("bundle fetch landed in the wrong place")
	}
}

// gitOps: bundles and archives > bundles a range, and says so when the other side lacks its starting point
func TestDBundleRangePrerequisites(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	out := filepath.Join(t.TempDir(), "range.bundle")
	s := New(nil, nil)
	if _, err := s.BundleCreate(dCtx, repo, out, BundleScope{Kind: "range", From: "HEAD~2", To: "main"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := s.BundleInspect(dCtx, repo, out); !info.Usable {
		t.Fatalf("info = %+v", info)
	}
	stranger := testutil.CloneFixture(t, "bisect-bug")
	info, _ := s.BundleInspect(dCtx, stranger, out)
	if info.Usable || len(info.Prerequisites) == 0 || !strings.Contains(strings.ToLower(info.Message), "prerequisite") {
		t.Fatalf("info = %+v", info)
	}
}

// gitOps: bundles and archives > refuses to create an empty bundle rather than writing a useless file
func TestDBundleRefusesEmpty(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	_, err := New(nil, nil).BundleCreate(dCtx, repo, filepath.Join(t.TempDir(), "empty.bundle"), BundleScope{Kind: "range", From: "main", To: "main"})
	if err == nil {
		t.Fatal("expected an error")
	}
}

// gitOps: bundles and archives > archives one tree, wrapped in a prefix, honouring export-ignore
func TestDArchivePrefixExportIgnore(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	testutil.WriteFile(t, repo, ".gitattributes", "plugins.py export-ignore\n")
	dCommitAll(t, repo, "add export-ignore")
	out := filepath.Join(t.TempDir(), "src.tar")
	r, err := New(nil, nil).ArchiveCreate(dCtx, repo, out, "main", "tar", "demo-1.0", "")
	if err != nil || r.Bytes <= 0 {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
	entries := strings.Join(dTarEntries(t, out), "\n")
	// export-ignore is the whole point: the path is in the commit, not in the file.
	if !strings.Contains(entries, "demo-1.0/app.py") || strings.Contains(entries, "plugins.py") {
		t.Fatalf("entries = %s", entries)
	}
}

// gitOps: bundles and archives > archives a single subdirectory when asked for one
func TestDArchiveSubdirectory(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	dir := strings.Fields(testutil.Git(t, repo, "ls-tree", "--name-only", "-d", "HEAD"))[0]
	out := filepath.Join(t.TempDir(), "part.tar")
	if _, err := New(nil, nil).ArchiveCreate(dCtx, repo, out, "HEAD", "tar", "", dir); err != nil {
		t.Fatal(err)
	}
	entries := dTarEntries(t, out)
	if len(entries) == 0 {
		t.Fatal("empty archive")
	}
	for _, e := range entries {
		if !strings.HasPrefix(e, dir) {
			t.Fatalf("entry %q outside %s", e, dir)
		}
	}
}

// gitOps: repository maintenance > reports where the disk went, and packs the loose objects away
func TestDMaintenanceGc(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	testutil.WriteFile(t, repo, "loose.txt", "written after the clone\n")
	dCommitAll(t, repo, "loose object")
	s := New(nil, nil)
	before, err := s.MaintenanceStats(dCtx, repo)
	if err != nil || before.GitBytes <= 0 || before.LooseObjects <= 0 || before.Scheduled {
		t.Fatalf("before = %+v, err = %v", before, err)
	}
	r, err := s.MaintenanceRun(dCtx, repo, "gc")
	if err != nil || r.Before <= 0 {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
	after, _ := s.MaintenanceStats(dCtx, repo)
	if after.LooseObjects >= before.LooseObjects || after.PackedObjects <= 0 || after.LastPack == nil {
		t.Fatalf("after = %+v", after)
	}
}

// gitOps: repository maintenance > counts unreachable objects, and prune is what actually drops them
func TestDMaintenancePrune(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.WriteFile(t, repo, "doomed.txt", "this commit is about to be abandoned\n")
	dCommitAll(t, repo, "doomed")
	doomed := testutil.Git(t, repo, "rev-parse", "HEAD")
	testutil.Git(t, repo, "reset", "--hard", "HEAD~1")
	s := New(nil, nil)
	// The reflog still points at it, so it is not unreachable yet — which is
	// exactly the two-week safety net gc keeps.
	if st, _ := s.MaintenanceStats(dCtx, repo); st.Prunable != 0 {
		t.Fatalf("prunable = %d", st.Prunable)
	}
	testutil.Git(t, repo, "reflog", "expire", "--expire=now", "--all")
	st, _ := s.MaintenanceStats(dCtx, repo)
	if st.Prunable <= 0 || st.PrunableBytes <= 0 {
		t.Fatalf("stats = %+v", st)
	}
	if _, err := s.MaintenanceRun(dCtx, repo, "prune"); err != nil {
		t.Fatal(err)
	}
	// The object is gone for good; a plain gc would have kept it two weeks.
	if dGitErr(repo, "cat-file", "-e", doomed) == nil {
		t.Fatal("doomed object survived the prune")
	}
	if st, _ := s.MaintenanceStats(dCtx, repo); st.Prunable != 0 {
		t.Fatalf("prunable = %d", st.Prunable)
	}
}

// gitOps: repository maintenance > fsck passes on a healthy repository and counts dangling objects as normal
func TestDFsckDangling(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.WriteFile(t, repo, "orphan.txt", "dropped\n")
	dCommitAll(t, repo, "orphan")
	testutil.Git(t, repo, "reset", "--hard", "HEAD~1")
	testutil.Git(t, repo, "reflog", "expire", "--expire=now", "--all")
	r, err := New(nil, nil).Fsck(dCtx, repo)
	if err != nil || !r.OK || r.Missing != 0 || r.Dangling <= 0 {
		t.Fatalf("report = %+v, err = %v", r, err)
	}
}

// gitOps: repository maintenance > rebuilds the commit graph without changing what git reports
func TestDMaintenanceCommitGraph(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	head := testutil.Git(t, repo, "rev-parse", "HEAD")
	if _, err := New(nil, nil).MaintenanceRun(dCtx, repo, "commitGraph"); err != nil {
		t.Fatal(err)
	}
	if !dExists(filepath.Join(repo, ".git/objects/info/commit-graph")) || testutil.Git(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("commit graph not written, or HEAD moved")
	}
}

// gitOps: gitattributes > finds every attributes file, including the private local one
func TestDAttributeFiles(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.WriteFile(t, repo, ".gitattributes", "* text=auto\n")
	testutil.WriteFile(t, repo, "docs/.gitattributes", "*.md merge=union\n")
	dCommitAll(t, repo, "attributes")
	testutil.WriteFile(t, repo, ".git/info/attributes", "*.secret -diff\n")
	files, err := New(nil, nil).AttributeFiles(dCtx, repo)
	if err != nil || len(files) != 3 {
		t.Fatalf("files = %+v, err = %v", files, err)
	}
	if files[0].Path != ".gitattributes" || files[1].Path != "docs/.gitattributes" || files[2].Path != ".git/info/attributes" {
		t.Fatalf("files = %+v", files)
	}
	// The local one is flagged: it never travels with the clone.
	if files[0].Content != "* text=auto\n" || !files[2].Local || files[0].Local {
		t.Fatalf("files = %+v", files)
	}
}

// gitOps: gitattributes > asks git what applies to a path, not the file
func TestDCheckAttributes(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.WriteFile(t, repo, ".gitattributes", "*.js text\nmath.js merge=union\n")
	s := New(nil, nil)
	checked, _ := s.CheckAttributes(dCtx, repo, []string{"math.js"})
	// The later, more specific rule adds to what the earlier one set.
	if checked[0].Path != "math.js" || checked[0].Attrs["text"] != "set" || checked[0].Attrs["merge"] != "union" {
		t.Fatalf("checked = %+v", checked)
	}
	other, _ := s.CheckAttributes(dCtx, repo, []string{"currency.js"})
	if other[0].Attrs["text"] != "set" {
		t.Fatalf("other = %+v", other)
	}
	if m, ok := other[0].Attrs["merge"]; ok && m != "unspecified" {
		t.Fatalf("merge = %q", m)
	}
}

// gitOps: gitattributes > writes an attributes file and refuses anything that is not one
func TestDAttributeWrite(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	s := New(nil, nil)
	if err := s.AttributeWrite(dCtx, repo, ".gitattributes", "*.png binary\n"); err != nil {
		t.Fatal(err)
	}
	if dReadFile(t, filepath.Join(repo, ".gitattributes")) != "*.png binary\n" {
		t.Fatal("root attributes not written")
	}
	if err := s.AttributeWrite(dCtx, repo, ".git/info/attributes", "*.key -diff\n"); err != nil {
		t.Fatal(err)
	}
	if dReadFile(t, filepath.Join(repo, ".git/info/attributes")) != "*.key -diff\n" {
		t.Fatal("info attributes not written")
	}
	dMustMatch(t, s.AttributeWrite(dCtx, repo, "math.js", "nope"), `(?i)attributes file`)
	if s.AttributeWrite(dCtx, repo, "../escape/.gitattributes", "nope") == nil {
		t.Fatal("escape was allowed")
	}
}

// gitOps: gitattributes > keeps export-ignore paths out of an archive
func TestDArchiveExportIgnore(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.WriteFile(t, repo, ".gitattributes", "currency.js export-ignore\n")
	dCommitAll(t, repo, "export-ignore")
	out := filepath.Join(t.TempDir(), "src.tar")
	if _, err := New(nil, nil).ArchiveCreate(dCtx, repo, out, "HEAD", "tar", "", ""); err != nil {
		t.Fatal(err)
	}
	entries := strings.Join(dTarEntries(t, out), "\n")
	if !strings.Contains(entries, "math.js") || strings.Contains(entries, "currency.js") {
		t.Fatalf("entries = %s", entries)
	}
}

// gitOps: gitattributes > lists configured diff drivers and says which converters are missing
func TestDDiffDrivers(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	s := New(nil, nil)
	if err := s.SetDiffDriver(dCtx, repo, "nonesuch", "definitely-not-installed --to-text", false); err != nil {
		t.Fatal(err)
	}
	drivers, _ := s.DiffDrivers(dCtx, repo)
	var mine *DiffDriverInfo
	for i := range drivers {
		if drivers[i].Name == "nonesuch" {
			mine = &drivers[i]
		}
	}
	if mine == nil || mine.Textconv != "definitely-not-installed --to-text" || mine.Available || mine.Scope != "repo" {
		t.Fatalf("drivers = %+v", drivers)
	}
	if err := s.SetDiffDriver(dCtx, repo, "nonesuch", "", false); err != nil {
		t.Fatal(err)
	}
	drivers, _ = s.DiffDrivers(dCtx, repo)
	for _, d := range drivers {
		if d.Name == "nonesuch" {
			t.Fatal("driver not cleared")
		}
	}
}

func dSuggestion(t *testing.T, list []DiffDriverSuggestion, name string) DiffDriverSuggestion {
	for _, s := range list {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %q suggestion in %+v", name, list)
	return DiffDriverSuggestion{}
}

// gitOps: gitattributes > offers converter suggestions with honest availability
func TestDDiffDriverSuggestions(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	suggestions, err := New(nil, nil).DiffDriverSuggestions(dCtx, repo)
	if err != nil {
		t.Fatal(err)
	}
	dSuggestion(t, suggestions, "pdf")
	for _, s := range suggestions {
		if len(s.Patterns) == 0 {
			t.Fatalf("%s has no patterns", s.Name)
		}
	}
	// word/excel/json ride the converter Gitcito ships — always available.
	for _, name := range []string{"word", "excel", "json"} {
		s := dSuggestion(t, suggestions, name)
		if !s.Bundled || !s.Available || !strings.Contains(s.Textconv, "gitcito-textconv") {
			t.Fatalf("%s = %+v", name, s)
		}
	}
}

// gitOps: UTF-16 .strings localization files > suggests a driver that needs nothing installed, and writes it to git config
func TestDStringsDriverSuggestion(t *testing.T) {
	repo := testutil.CloneFixture(t, "xcode-project")
	s := New(nil, nil)
	suggestions, _ := s.DiffDriverSuggestions(dCtx, repo)
	sg := dSuggestion(t, suggestions, "strings")
	if !sg.Bundled {
		t.Fatalf("strings = %+v", sg)
	}
	if err := s.SetDiffDriver(dCtx, repo, sg.Name, sg.Textconv, false); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo, "config", "--local", "diff.strings.textconv"); got != strings.TrimSpace(sg.Textconv) {
		t.Fatalf("config = %q", got)
	}
}

// textconv: Apple localization files end to end > offers both as bundled drivers, so nothing has to be installed
func TestDBundledAppleDrivers(t *testing.T) {
	suggestions, _ := New(nil, nil).DiffDriverSuggestions(dCtx, ".")
	sg := dSuggestion(t, suggestions, "strings")
	if len(sg.Patterns) != 1 || sg.Patterns[0] != "*.strings" || !sg.Bundled || sg.Binary != "" {
		t.Fatalf("strings = %+v", sg)
	}
	json := dSuggestion(t, suggestions, "json")
	if !strings.Contains(strings.Join(json.Patterns, " "), "*.xcstrings") {
		t.Fatalf("json = %+v", json)
	}
}

func dTrack(t *testing.T, repo, file, content string) {
	testutil.WriteFile(t, repo, file, content)
	testutil.Git(t, repo, "add", file)
}

func dFilter(list []FilterDriverInfo, name string) *FilterDriverInfo {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// filters: clean/smudge filter drivers > writes a filter, lists it, and returns what it overwrote
func TestDFilterDriverWriteListOverwrite(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	s := New(nil, nil)
	before, err := s.SetFilterDriver(dCtx, repo, "vault", FilterDriverInput{Clean: "cat", Smudge: "cat", Required: true}, false)
	if err != nil || before != (FilterDriverPrevious{}) {
		t.Fatalf("before = %+v, err = %v", before, err)
	}
	list, _ := s.FilterDrivers(dCtx, repo)
	l := dFilter(list, "vault")
	if l == nil || l.Clean != "cat" || l.Smudge != "cat" || !l.Required || l.Scope != "repo" || !l.CleanAvailable {
		t.Fatalf("listed = %+v", l)
	}
	// Overwriting hands back the old values — the undo payload.
	previous, _ := s.SetFilterDriver(dCtx, repo, "vault", FilterDriverInput{Clean: "tac"}, false)
	if previous != (FilterDriverPrevious{Clean: "cat", Smudge: "cat", Required: true}) {
		t.Fatalf("previous = %+v", previous)
	}
	// Clearing removes every key.
	if _, err := s.SetFilterDriver(dCtx, repo, "vault", FilterDriverInput{}, false); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.FilterDrivers(dCtx, repo); dFilter(list, "vault") != nil {
		t.Fatal("filter not cleared")
	}
}

// filters: clean/smudge filter drivers > flags a filter whose command does not exist
func TestDFilterDriverMissingCommand(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	s := New(nil, nil)
	if _, err := s.SetFilterDriver(dCtx, repo, "ghost", FilterDriverInput{Clean: "definitely-not-installed --x"}, false); err != nil {
		t.Fatal(err)
	}
	list, _ := s.FilterDrivers(dCtx, repo)
	if l := dFilter(list, "ghost"); l == nil || l.CleanAvailable {
		t.Fatalf("listed = %+v", l)
	}
}

// filters: clean/smudge filter drivers > dry run: a lossless pair roundtrips, and nothing is configured
func TestDFilterDryRunLossless(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	dTrack(t, repo, "creds.secret", "token=hunter2\n")
	s := New(nil, nil)
	r, _ := s.FilterDryRun(dCtx, repo, "*.secret", "cat", "cat")
	if r.Matched != 1 || len(r.Tested) != 1 || !r.Tested[0].OK || r.Tested[0].Roundtrip != "ok" || !strings.Contains(r.Tested[0].Preview, "token=hunter2") {
		t.Fatalf("result = %+v", r)
	}
	// The dry run must not have written any config.
	list, _ := s.FilterDrivers(dCtx, repo)
	for _, f := range list {
		if f.Scope == "repo" {
			t.Fatalf("configured: %+v", f)
		}
	}
}

// filters: clean/smudge filter drivers > dry run: a lossy clean is reported as a differing roundtrip
func TestDFilterDryRunLossy(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	dTrack(t, repo, "creds.secret", "token=hunter2\n")
	r, _ := New(nil, nil).FilterDryRun(dCtx, repo, "*.secret", "tr 'a-z' 'A-Z'", "cat")
	if !r.Tested[0].OK || r.Tested[0].Roundtrip != "different" {
		t.Fatalf("result = %+v", r)
	}
}

// filters: clean/smudge filter drivers > dry run: no smudge command means the roundtrip is skipped, not failed
func TestDFilterDryRunNoSmudge(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	dTrack(t, repo, "creds.secret", "token=hunter2\n")
	r, _ := New(nil, nil).FilterDryRun(dCtx, repo, "*.secret", "tr 'a-z' 'A-Z'", "")
	if !r.Tested[0].OK || r.Tested[0].Roundtrip != "skipped" || !strings.Contains(r.Tested[0].Preview, "TOKEN=HUNTER2") {
		t.Fatalf("result = %+v", r)
	}
}

// filters: clean/smudge filter drivers > dry run: a failing command reports its stderr instead of pretending
func TestDFilterDryRunFailing(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	dTrack(t, repo, "creds.secret", "token=hunter2\n")
	r, _ := New(nil, nil).FilterDryRun(dCtx, repo, "*.secret", `sh -c "echo broken >&2; exit 3"`, "")
	if r.Tested[0].OK || !strings.Contains(r.Tested[0].Error, "broken") {
		t.Fatalf("result = %+v", r)
	}
}

// filters: clean/smudge filter drivers > dry run: an unmatched pattern says so
func TestDFilterDryRunUnmatched(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	r, _ := New(nil, nil).FilterDryRun(dCtx, repo, "*.nothing-matches-this", "cat", "cat")
	if r.Matched != 0 || len(r.Tested) != 0 {
		t.Fatalf("result = %+v", r)
	}
}

// filters: clean/smudge filter drivers > substitutes %f with the repo-relative path, like git does
func TestDFilterDryRunPercentF(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	dTrack(t, repo, "creds.secret", "token=hunter2\n")
	r, _ := New(nil, nil).FilterDryRun(dCtx, repo, "*.secret", "echo %f", "")
	if strings.TrimSpace(r.Tested[0].Preview) != "creds.secret" {
		t.Fatalf("result = %+v", r)
	}
}

// filters: clean/smudge filter drivers > classifies the new methods correctly for the repo lock
func TestDFilterMethodsClassified(t *testing.T) {
	if !IsReadMethod("filterDrivers") || !IsReadMethod("filterDryRun") || IsReadMethod("setFilterDriver") {
		t.Fatal("filter methods misclassified")
	}
}

func dRepoHelpers(st CredentialStatus) []CredentialHelperInfo {
	var out []CredentialHelperInfo
	for _, h := range st.Helpers {
		if h.Scope == "repo" {
			out = append(out, h)
		}
	}
	return out
}

// gitOps: credential helpers > reports a repository-scoped helper, and flags the plaintext one
func TestDCredentialRepoHelper(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	s := New(nil, nil)
	if err := s.SetCredentialHelper(dCtx, repo, "store", "repo"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.CredentialStatus(dCtx, repo)
	mine := dRepoHelpers(st)
	// `store` writes passwords to a plain file — the whole reason to say so.
	if len(mine) != 1 || mine[0].Value != "store" || !mine[0].Plaintext || !mine[0].Available {
		t.Fatalf("helpers = %+v", mine)
	}
}

// gitOps: credential helpers > clears every stacked helper rather than leaving one behind
func TestDCredentialClearsStacked(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.Git(t, repo, "config", "--local", "--add", "credential.helper", "cache")
	testutil.Git(t, repo, "config", "--local", "--add", "credential.helper", "store")
	s := New(nil, nil)
	if st, _ := s.CredentialStatus(dCtx, repo); len(dRepoHelpers(st)) != 2 {
		t.Fatalf("helpers = %+v", st.Helpers)
	}
	if err := s.SetCredentialHelper(dCtx, repo, "", "repo"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.CredentialStatus(dCtx, repo); len(dRepoHelpers(st)) != 0 {
		t.Fatalf("helpers = %+v", st.Helpers)
	}
}

// gitOps: credential helpers > marks a helper that is not installed
func TestDCredentialMissingHelper(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	s := New(nil, nil)
	if err := s.SetCredentialHelper(dCtx, repo, "definitely-not-a-helper", "repo"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.CredentialStatus(dCtx, repo)
	if h := dRepoHelpers(st); len(h) != 1 || h[0].Available {
		t.Fatalf("helpers = %+v", h)
	}
}

// gitOps: credential helpers > surfaces per-URL rules, which beat the plain setting
func TestDCredentialURLRules(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.Git(t, repo, "config", "--local", "credential.https://example.com.helper", "cache")
	testutil.Git(t, repo, "config", "--local", "credential.https://example.com.username", "octocat")
	st, _ := New(nil, nil).CredentialStatus(dCtx, repo)
	for _, r := range st.URLRules {
		if r.URL == "https://example.com" {
			if r.Helper != "cache" || r.Username != "octocat" || r.Scope != "repo" {
				t.Fatalf("rule = %+v", r)
			}
			return
		}
	}
	t.Fatalf("rules = %+v", st.URLRules)
}

// gitOps: credential helpers > lists the https hosts this repository would ask about
func TestDCredentialHTTPSHosts(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.Git(t, repo, "remote", "add", "origin", "https://git.example.com/example/demo.git")
	testutil.Git(t, repo, "remote", "add", "ssh", "git@ssh.example.com:example/demo.git")
	st, _ := New(nil, nil).CredentialStatus(dCtx, repo)
	// An ssh remote never reaches a credential helper, so it is not listed.
	if len(st.HTTPSHosts) != 1 || st.HTTPSHosts[0] != "git.example.com" {
		t.Fatalf("hosts = %v", st.HTTPSHosts)
	}
}

// gitOps: credential helpers > offers exactly one recommended helper for this platform
func TestDCredentialCandidates(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	st, _ := New(nil, nil).CredentialStatus(dCtx, repo)
	recommended, cache := 0, false
	for _, c := range st.Candidates {
		if c.Recommended {
			recommended++
		}
		if c.Name == "cache" {
			cache = c.Available
		}
	}
	// git's own built-ins are always there.
	if len(st.Candidates) <= 1 || recommended != 1 || !cache {
		t.Fatalf("candidates = %+v", st.Candidates)
	}
}

// gitOps: credential helpers > counts the plaintext credentials file without reading it out
func TestDCredentialPlaintextFileShape(t *testing.T) {
	env := testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.WriteFile(t, env.Home, ".git-credentials", "https://u:p@a.example\n\nhttps://u:p@b.example\n")
	st, _ := New(nil, nil).CredentialStatus(dCtx, repo)
	if !strings.HasSuffix(st.PlaintextFile.Path, ".git-credentials") || !st.PlaintextFile.Exists || st.PlaintextFile.Entries != 2 {
		t.Fatalf("plaintext = %+v", st.PlaintextFile)
	}
	// The shape carries a count and nothing else — no field can leak a secret.
	b, _ := json.Marshal(st.PlaintextFile)
	var shape map[string]any
	_ = json.Unmarshal(b, &shape)
	keys := make([]string, 0, len(shape))
	for k := range shape {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "entries,exists,path" {
		t.Fatalf("plaintextFile keys = %v", keys)
	}
}

// gitOps: credential helpers > refuses to forget nothing
func TestDForgetCredentialNeedsHost(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	dMustMatch(t, New(nil, nil).ForgetCredential(dCtx, repo, "  ", nil), `(?i)host`)
}

func dCount(t *testing.T, repo string, extra ...string) int {
	n, err := strconv.Atoi(testutil.Git(t, repo, append(extra, "rev-list", "--count", "HEAD")...))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// gitOps: object replacement > grafts a commit to the start of history without rewriting anything
func TestDReplaceGraftRoot(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	full := dCount(t, repo)
	head := testutil.Git(t, repo, "rev-parse", "HEAD")
	cut := testutil.Git(t, repo, "rev-parse", "HEAD~5")
	s := New(nil, nil)
	if _, err := s.ReplaceGraft(dCtx, repo, cut, nil); err != nil {
		t.Fatal(err)
	}
	// The log is shorter, and HEAD is still the very same commit.
	if dCount(t, repo) != 6 || testutil.Git(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("graft did not cut history, or HEAD changed")
	}
	// The real history is still there, one flag away.
	if dCount(t, repo, "--no-replace-objects") != full {
		t.Fatal("stored history changed")
	}
	st, _ := s.Replacements(dCtx, repo)
	if len(st.Refs) != 1 || st.Refs[0].Original != cut || len(st.Refs[0].ReplacementParents) != 0 || !st.Enabled {
		t.Fatalf("status = %+v", st)
	}
}

// gitOps: object replacement > puts the history back when the replacement is deleted
func TestDReplaceDeleteRestores(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	full := dCount(t, repo)
	cut := testutil.Git(t, repo, "rev-parse", "HEAD~4")
	s := New(nil, nil)
	if _, err := s.ReplaceGraft(dCtx, repo, cut, nil); err != nil {
		t.Fatal(err)
	}
	if dCount(t, repo) >= full {
		t.Fatal("graft did not shorten history")
	}
	if err := s.ReplaceDelete(dCtx, repo, cut); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Replacements(dCtx, repo); dCount(t, repo) != full || len(st.Refs) != 0 {
		t.Fatalf("status = %+v", st)
	}
}

// gitOps: object replacement > grafts onto chosen parents, which is how an archive is reattached
func TestDReplaceGraftParents(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	cut := testutil.Git(t, repo, "rev-parse", "HEAD~5")
	elsewhere := testutil.Git(t, repo, "rev-parse", "HEAD~8")
	full := dCount(t, repo)
	s := New(nil, nil)
	if _, err := s.ReplaceGraft(dCtx, repo, cut, []string{elsewhere}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Replacements(dCtx, repo)
	if len(st.Refs) != 1 || len(st.Refs[0].ReplacementParents) != 1 || st.Refs[0].ReplacementParents[0] != elsewhere {
		t.Fatalf("status = %+v", st)
	}
	// Only the two commits between the graft and its new parent leave the walk.
	if dCount(t, repo) != full-2 {
		t.Fatalf("count = %d, full = %d", dCount(t, repo), full)
	}
}

// gitOps: object replacement > reports and honours core.useReplaceRefs
func TestDReplaceUseReplaceRefs(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	full := dCount(t, repo)
	s := New(nil, nil)
	if _, err := s.ReplaceGraft(dCtx, repo, testutil.Git(t, repo, "rev-parse", "HEAD~5"), nil); err != nil {
		t.Fatal(err)
	}
	if dCount(t, repo) != 6 {
		t.Fatal("graft not honoured")
	}
	if err := s.SetUseReplaceRefs(dCtx, repo, false); err != nil {
		t.Fatal(err)
	}
	// git now reads straight through the replacement.
	if st, _ := s.Replacements(dCtx, repo); st.Enabled || dCount(t, repo) != full {
		t.Fatal("replacements still in effect")
	}
	if err := s.SetUseReplaceRefs(dCtx, repo, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Replacements(dCtx, repo); !st.Enabled || dCount(t, repo) != 6 {
		t.Fatal("replacements not back in effect")
	}
}

// gitOps: object replacement > replaces one object with another outright
func TestDReplaceObject(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	original := testutil.Git(t, repo, "rev-parse", "HEAD~2")
	other := testutil.Git(t, repo, "rev-parse", "HEAD~7")
	s := New(nil, nil)
	if err := s.ReplaceObject(dCtx, repo, original, other); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Replacements(dCtx, repo)
	r := st.Refs[0]
	// Both subjects are readable: the original is fetched with replacements off,
	// so the two sides do not collapse into the same line.
	if r.Original != original || r.Replacement != other || r.OriginalSubject == "" || r.ReplacementSubject == "" || r.OriginalSubject == r.ReplacementSubject {
		t.Fatalf("ref = %+v", r)
	}
}

// gitOps: object replacement > refuses an empty graft or delete
func TestDReplaceRefusesEmpty(t *testing.T) {
	repo := testutil.CloneFixture(t, "deep-history-monorepo")
	s := New(nil, nil)
	if _, err := s.ReplaceGraft(dCtx, repo, "  ", nil); err == nil {
		t.Fatal("empty graft accepted")
	}
	if s.ReplaceDelete(dCtx, repo, "") == nil {
		t.Fatal("empty delete accepted")
	}
}
