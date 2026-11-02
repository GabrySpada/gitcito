package gitsvc

// Ports of test/gitService.test.ts and test/playground.smoke.test.ts: the
// slice A reads driven against the playground repos. Assertions target
// structural facts (branch/tag/stash presence, commit counts, current ref)
// that are stable across runs — never specific SHAs.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// aSvc isolates git from the machine's config and returns a fresh service.
func aSvc(t *testing.T) (*GitService, context.Context) {
	t.Helper()
	testutil.IsolateGitEnv(t)
	t.Setenv("GITCITO_DATA_DIR", t.TempDir())
	return New(nil, nil), context.Background()
}

func aMust[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func aNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func aLocalNames(b BranchesPayload) []string {
	var out []string
	for _, l := range b.Locals {
		out = append(out, l.Name)
	}
	return out
}

func aContains(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

func aSubjects(log []GraphCommit) []string {
	var out []string
	for _, c := range log {
		out = append(out, c.Subject)
	}
	return out
}

func aAnySubject(log []GraphCommit, pred func(string) bool) bool {
	for _, c := range log {
		if pred(c.Subject) {
			return true
		}
	}
	return false
}

func TestSliceASmokeMergeConflictBranches(t *testing.T) {
	s, ctx := aSvc(t)
	b := aMust[BranchesPayload](t)(s.Branches(ctx, testutil.PlaygroundRepo(t, "merge-conflict")))
	names := aLocalNames(b)
	sort.Strings(names)
	if b.Current != "main" || !reflect.DeepEqual(names, []string{"feature", "main"}) {
		t.Fatalf("current %q, locals %v", b.Current, names)
	}
}

func TestSliceASmokeCherryPickFeatureAhead(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "cherry-pick")
	if cur := aMust[RepoSummary](t)(s.Open(ctx, R)).Current; cur != "main" {
		t.Fatalf("current %q", cur)
	}
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, nil, 0))
	if !aAnySubject(log, func(s string) bool { return strings.Contains(s, "cherry-picks cleanly") }) {
		t.Fatal("clean commit missing from log")
	}
}

func TestSliceASmokeStashPickingOneStash(t *testing.T) {
	s, ctx := aSvc(t)
	st := aMust[[]StashInfo](t)(s.Stashes(ctx, testutil.PlaygroundRepo(t, "stash-picking")))
	if len(st) != 1 || !strings.Contains(st[0].Message, "WIP") {
		t.Fatalf("stashes %+v", st)
	}
}

func TestSliceASmokeRebaseConflictOnFeature(t *testing.T) {
	s, ctx := aSvc(t)
	if cur := aMust[RepoSummary](t)(s.Open(ctx, testutil.PlaygroundRepo(t, "rebase-conflict"))).Current; cur != "feature" {
		t.Fatalf("current %q", cur)
	}
}

func TestSliceASmokeInteractiveRebaseMessyFeature(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "interactive-rebase")
	if cur := aMust[RepoSummary](t)(s.Open(ctx, R)).Current; cur != "messy-feature" {
		t.Fatalf("current %q", cur)
	}
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, nil, 0))
	if len(log) != 7 || !aContains(aSubjects(log), "WIP") ||
		!aAnySubject(log, func(s string) bool { return strings.HasPrefix(s, "fixup!") }) {
		t.Fatalf("log %v", aSubjects(log))
	}
}

func TestSliceASmokeBisectBugHistory(t *testing.T) {
	s, ctx := aSvc(t)
	log := aMust[[]GraphCommit](t)(s.Log(ctx, testutil.PlaygroundRepo(t, "bisect-bug"), nil, 0))
	if len(log) != 13 || !aContains(aSubjects(log), "refactor: simplify discount calculation") {
		t.Fatalf("log %v", aSubjects(log))
	}
}

func TestSliceASmokeMultiRemote(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "multi-remote")
	var names []string
	for _, r := range aMust[[]RemoteInfo](t)(s.Remotes(ctx, R)) {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"origin", "upstream"}) {
		t.Fatalf("remotes %v", names)
	}
	if len(aMust[BranchesPayload](t)(s.Branches(ctx, R)).Remotes) == 0 {
		t.Fatal("no remote-tracking branches")
	}
}

func TestSliceASmokeOctopusMergeBranches(t *testing.T) {
	s, ctx := aSvc(t)
	b := aMust[BranchesPayload](t)(s.Branches(ctx, testutil.PlaygroundRepo(t, "octopus-merge")))
	if b.Current != "main" {
		t.Fatalf("current %q", b.Current)
	}
	for _, f := range []string{"feat/auth", "feat/api", "feat/ui"} {
		if !aContains(aLocalNames(b), f) {
			t.Errorf("missing %s", f)
		}
	}
}

func TestSliceASmokeTagsAndReleases(t *testing.T) {
	s, ctx := aSvc(t)
	b := aMust[BranchesPayload](t)(s.Branches(ctx, testutil.PlaygroundRepo(t, "tags-and-releases")))
	var tags []string
	for _, tg := range b.Tags {
		tags = append(tags, tg.Name)
	}
	sort.Strings(tags)
	if !reflect.DeepEqual(tags, []string{"v1.0.0", "v1.0.1", "v1.0.2", "v1.1.0", "v2.0.0"}) {
		t.Fatalf("tags %v", tags)
	}
	if !aContains(aLocalNames(b), "hotfix/security-patch") {
		t.Fatal("hotfix branch missing")
	}
}

func TestSliceASmokeDetachedHead(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "detached-head")
	if cur := aMust[RepoSummary](t)(s.Open(ctx, R)).Current; cur != "HEAD" {
		t.Fatalf("current %q", cur)
	}
	names := aLocalNames(aMust[BranchesPayload](t)(s.Branches(ctx, R)))
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"main", "stable"}) {
		t.Fatalf("locals %v", names)
	}
}

func TestSliceASmokeCollaborators(t *testing.T) {
	s, ctx := aSvc(t)
	log := aMust[[]GraphCommit](t)(s.Log(ctx, testutil.PlaygroundRepo(t, "collaborators"), nil, 0))
	authors := map[string]bool{}
	coAuthored := false
	for _, c := range log {
		authors[c.Author] = true
		if len(c.CoAuthors) > 0 {
			coAuthored = true
		}
	}
	for _, a := range []string{"Alice Liddell", "Bob Marley", "Carol Danvers", "Dave Grohl"} {
		if !authors[a] {
			t.Errorf("author %s missing", a)
		}
	}
	if !coAuthored {
		t.Error("no co-authored commit")
	}
}

func TestSliceASmokeReflogRecovery(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "reflog-recovery")
	b := aMust[BranchesPayload](t)(s.Branches(ctx, R))
	if b.Current != "main" || aContains(aLocalNames(b), "experiment") {
		t.Fatalf("branches %+v", b)
	}
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, nil, 0))
	if !aAnySubject(log, func(s string) bool { return strings.Contains(s, "newUI + betaSearch") }) {
		t.Error("amended commit missing")
	}
	// the hard-reset WIP commit is not reachable from any ref
	if aAnySubject(log, func(s string) bool { return strings.Contains(s, "broken refactor") }) {
		t.Error("discarded commit is reachable")
	}
}

func TestSliceASmokeBinaryImagesUnicode(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "binary-images-unicode")
	st := aMust[RepoStatus](t)(s.Status(ctx, R))
	if len(st.Staged)+len(st.Unstaged)+len(st.Conflicted) != 0 {
		t.Fatalf("status %+v", st)
	}
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, nil, 0))
	if !aAnySubject(log, func(s string) bool { return strings.Contains(s, "binary diff") }) {
		t.Error("binary-diff commit missing")
	}
}

func TestSliceASmokeDeepHistory(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "deep-history-monorepo")
	n := 500
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, &n, 0))
	authors := map[string]bool{}
	for _, c := range log {
		authors[c.Author] = true
	}
	if len(log) <= 200 || len(authors) < 5 {
		t.Fatalf("%d commits, %d authors", len(log), len(authors))
	}
	var tags []string
	for _, tg := range aMust[BranchesPayload](t)(s.Branches(ctx, R)).Tags {
		tags = append(tags, tg.Name)
	}
	sort.Strings(tags)
	if !reflect.DeepEqual(tags, []string{"v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"}) {
		t.Fatalf("tags %v", tags)
	}
}

func TestSliceASmokeDeepHistoryPaging(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "deep-history-monorepo")
	hundred, twoHundred := 100, 200
	// Concurrent reads, as the TS test ran them with Promise.all.
	type res struct {
		log []GraphCommit
		err error
	}
	ch := make([]chan res, 3)
	for i := range ch {
		ch[i] = make(chan res, 1)
	}
	go func() { l, e := s.Log(ctx, R, &hundred, 0); ch[0] <- res{l, e} }()
	go func() { l, e := s.Log(ctx, R, &hundred, 100); ch[1] <- res{l, e} }()
	go func() { l, e := s.Log(ctx, R, &twoHundred, 0); ch[2] <- res{l, e} }()
	first, second, whole := <-ch[0], <-ch[1], <-ch[2]
	for _, r := range []res{first, second, whole} {
		aNoErr(t, r.err)
	}
	if len(first.log) != 100 || len(second.log) != 100 {
		t.Fatalf("page sizes %d, %d", len(first.log), len(second.log))
	}
	seen := map[string]bool{}
	for _, c := range first.log {
		seen[c.Hash] = true
	}
	var joined, all []string
	for _, c := range append(first.log, second.log...) {
		joined = append(joined, c.Hash)
	}
	for _, c := range second.log {
		if seen[c.Hash] {
			t.Fatal("pages overlap")
		}
	}
	for _, c := range whole.log {
		all = append(all, c.Hash)
	}
	if !reflect.DeepEqual(joined, all) {
		t.Fatal("two pages differ from one big window")
	}
}

// ─── test/playground.smoke.test.ts ──────────────────────────────────────────

var aExpectedScenarios = []string{
	"merge-conflict", "cherry-pick", "stash-picking", "rebase-conflict", "interactive-rebase", "bisect-bug",
	"multi-remote", "octopus-merge", "tags-and-releases", "detached-head", "collaborators", "submodules-worktrees",
	"reflog-recovery", "binary-images-unicode", "deep-history-monorepo", "image-showcase", "submodule-states",
	"gitignore-untrack", "signed-commits", "pr-ready-branch", "lfs-assets", "patch-apply", "hooks",
	"commit-template", "line-staging", "project-tree", "command-palette", "code-search", "stacked-branches",
	"insights", "changelog", "word-diff", "snapshots", "secrets", "terminal-naming", "diverged-checkout",
	"branch-grouping", "launch-configs", "empty-repo", "terminal-per-tab", "panel-layout", "secure-share",
	"tree-badges", "workspace-share", "tree-dnd", "newer-branch-merge", "pinned-branches", "ai-review",
	"nested-folders", "file-nav", "host-remotes", "conflict-radar", "semantic-diff", "force-push", "absorb",
	"time-machine", "gitflow", "leaked-secret", "subtree", "untracked-mess", "attributes", "teammate-radar",
	"local-ci", "analyzer-problems", "xcode-project", "change-summary", "readme-showcase", "dated-history",
	"split-diff",
}

func TestSliceAPlaygroundManifestListsEveryScenario(t *testing.T) {
	var names []string
	for _, m := range testutil.ReadManifest(t) {
		names = append(names, m.Name)
	}
	for _, n := range aExpectedScenarios {
		if !aContains(names, n) {
			t.Errorf("manifest lacks %s", n)
		}
	}
	if len(names) != len(aExpectedScenarios) {
		t.Errorf("manifest has %d entries, want %d", len(names), len(aExpectedScenarios))
	}
}

func TestSliceAPlaygroundManifestDescriptions(t *testing.T) {
	for _, m := range testutil.ReadManifest(t) {
		if m.Description == "" {
			t.Errorf("%s has no description", m.Name)
		}
	}
}

func TestSliceAPlaygroundGitIsAvailable(t *testing.T) {
	s, ctx := aSvc(t)
	v := aMust[string](t)(s.Version(ctx))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+`).MatchString(v) {
		t.Fatalf("version %q", v)
	}
}

func TestSliceAPlaygroundEveryScenarioOpens(t *testing.T) {
	s, ctx := aSvc(t)
	for _, name := range aExpectedScenarios {
		t.Run(name, func(t *testing.T) {
			path := testutil.PlaygroundRepo(t, name)
			if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
				t.Fatalf("%s/.git missing", name)
			}
			sum := aMust[RepoSummary](t)(s.Open(ctx, path))
			if sum.Name != name || sum.Current == "" {
				t.Fatalf("summary %+v", sum)
			}
		})
	}
}
