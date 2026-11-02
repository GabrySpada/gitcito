package gitsvc

// Ports of the slice A cases in test/gitOps.test.ts, test/newFeatures.test.ts
// and test/commitEditMerges.test.ts. Every mutating test works on a
// CloneFixture copy or a fresh temp repo. Steps that belong to other slices'
// methods (merge, rebase, cherryPick, fileContent…) are plain git here.

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

func aRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func aFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// aGitTry runs git and ignores a failure (a merge that conflicts on purpose).
func aGitTry(dir string, args ...string) {
	_ = exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
}

func aPaths(files []FileEntry) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// ─── test/gitOps.test.ts ────────────────────────────────────────────────────

func TestSliceAOpsMergeConflictAbort(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "merge-conflict")
	aGitTry(R, "merge", "feature")

	if op := aMust[*string](t)(s.MergeState(ctx, R)); op == nil || *op != "merge" {
		t.Fatalf("mergeState %v", op)
	}
	if st := aMust[RepoStatus](t)(s.Status(ctx, R)); len(st.Conflicted) == 0 {
		t.Fatal("no conflicts")
	}
	// A content conflict exposes both sides.
	v := aMust[ConflictVersions](t)(s.ConflictVersions(ctx, R, "greeting.txt"))
	if v.Ours == nil || v.Theirs == nil {
		t.Fatalf("versions %+v", v)
	}
	// "Merging feature into main" + a commit behind each side of the editor.
	c := aMust[*ConflictContext](t)(s.ConflictContext(ctx, R))
	shortSha := regexp.MustCompile(`^[0-9a-f]{7,}$`)
	if c == nil || c.Kind != "merge" || c.Source != "feature" || c.Target != "main" ||
		c.Ours == nil || c.Ours.Branch != "main" || !shortSha.MatchString(c.Ours.Sha) ||
		c.Theirs == nil || c.Theirs.Branch != "feature" || !strings.Contains(c.Theirs.Subject, "feature") {
		t.Fatalf("context %+v", c)
	}

	aNoErr(t, s.ConflictOpAbort(ctx, R, "merge"))
	if op := aMust[*string](t)(s.MergeState(ctx, R)); op != nil {
		t.Fatalf("still %s", *op)
	}
	if st := aMust[RepoStatus](t)(s.Status(ctx, R)); len(st.Conflicted) != 0 {
		t.Fatal("conflicts left")
	}
	if cur := aMust[RepoSummary](t)(s.Open(ctx, R)).Current; cur != "main" {
		t.Fatalf("current %q", cur)
	}
}

func TestSliceAOpsCherryPickResolve(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "cherry-pick")
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, nil, 0))
	var clean, conflicting string
	for _, c := range log {
		if strings.Contains(c.Subject, "clean-addition") {
			clean = c.Hash
		}
		if strings.Contains(c.Subject, "will CONFLICT") {
			conflicting = c.Hash
		}
	}
	if clean == "" || conflicting == "" {
		t.Fatal("fixture commits missing")
	}
	// Clean cherry-pick lands without conflict.
	testutil.Git(t, R, "cherry-pick", clean)
	if !aFileExists(filepath.Join(R, "clean-addition.txt")) {
		t.Fatal("clean pick did not land")
	}
	// Conflicting cherry-pick (config.json) leaves a cherry-pick in progress.
	aGitTry(R, "cherry-pick", conflicting)
	if op := aMust[*string](t)(s.MergeState(ctx, R)); op == nil || *op != "cherry-pick" {
		t.Fatalf("mergeState %v", op)
	}
	st := aMust[RepoStatus](t)(s.Status(ctx, R))
	if !aContains(aPaths(st.Conflicted), "config.json") {
		t.Fatalf("conflicted %v", aPaths(st.Conflicted))
	}
	// Take "theirs" for each conflict, then continue.
	for _, f := range st.Conflicted {
		aNoErr(t, s.ConflictTakeSide(ctx, R, f.Path, "theirs"))
	}
	aNoErr(t, s.ConflictOpContinue(ctx, R, "cherry-pick"))
	if op := aMust[*string](t)(s.MergeState(ctx, R)); op != nil {
		t.Fatalf("still %s", *op)
	}
	if st := aMust[RepoStatus](t)(s.Status(ctx, R)); len(st.Conflicted) != 0 {
		t.Fatal("conflicts left")
	}
}

func TestSliceAOpsRebaseConflictAbort(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "rebase-conflict") // checked out on feature
	if cur := aMust[RepoSummary](t)(s.Open(ctx, R)).Current; cur != "feature" {
		t.Fatalf("current %q", cur)
	}
	aGitTry(R, "rebase", "main")
	if op := aMust[*string](t)(s.MergeState(ctx, R)); op == nil || *op != "rebase" {
		t.Fatalf("mergeState %v", op)
	}
	// During a rebase HEAD sits on the target, so ours=main / theirs=replayed commit.
	c := aMust[*ConflictContext](t)(s.ConflictContext(ctx, R))
	if c == nil || c.Kind != "rebase" || c.Source != "feature" || c.Target != "main" || c.Theirs == nil ||
		!regexp.MustCompile(`^[0-9a-f]{7,}$`).MatchString(c.Theirs.Sha) {
		t.Fatalf("context %+v", c)
	}
	aNoErr(t, s.ConflictOpAbort(ctx, R, "rebase")) // rebaseAbort in TS: `rebase --abort` all the same
	if op := aMust[*string](t)(s.MergeState(ctx, R)); op != nil {
		t.Fatalf("still %s", *op)
	}
	if cur := aMust[RepoSummary](t)(s.Open(ctx, R)).Current; cur != "feature" {
		t.Fatalf("current %q", cur)
	}
}

func TestSliceAOpsPartialStashApply(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "stash-picking")
	st := aMust[[]StashInfo](t)(s.Stashes(ctx, R))
	if len(st) == 0 {
		t.Fatal("no stash")
	}
	aNoErr(t, s.StashApplyFiles(ctx, R, st[0].Sha, []string{"alpha.txt"}, []string{"delta-untracked.txt"}))
	// alpha.txt restored to its stashed v2; beta.txt left at committed v1.
	if !strings.Contains(aRead(t, filepath.Join(R, "alpha.txt")), "alpha v2") ||
		!strings.Contains(aRead(t, filepath.Join(R, "beta.txt")), "beta v1") {
		t.Fatal("wrong files restored")
	}
	// the untracked file from the stash was materialised.
	if !aFileExists(filepath.Join(R, "delta-untracked.txt")) {
		t.Fatal("untracked file not restored")
	}
	// the stash itself is untouched by a partial apply.
	if n := len(aMust[[]StashInfo](t)(s.Stashes(ctx, R))); n != 1 {
		t.Fatalf("%d stashes", n)
	}
}

func TestSliceAOpsRestoreFromCommit(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "stash-picking")
	head := testutil.Git(t, R, "rev-parse", "HEAD")
	testutil.WriteFile(t, R, "alpha.txt", "local edit\n")
	aNoErr(t, s.RestoreFromCommit(ctx, R, head, []string{"alpha.txt"}))
	if !strings.Contains(aRead(t, filepath.Join(R, "alpha.txt")), "alpha v1") {
		t.Fatal("not restored")
	}
	if testutil.Git(t, R, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved")
	}
}

func TestSliceAOpsRestoreFromCommitEmpty(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "stash-picking")
	aNoErr(t, s.RestoreFromCommit(ctx, R, testutil.Git(t, R, "rev-parse", "HEAD"), nil))
}

func TestSliceAOpsStashApplyCollidingUntracked(t *testing.T) {
	s, ctx := aSvc(t)
	// A pre-existing untracked file blocks a plain apply of an -u stash.
	collide := testutil.CloneFixture(t, "stash-picking")
	testutil.WriteFile(t, collide, "delta-untracked.txt", "local, in the way\n")
	err := s.StashApply(ctx, collide, 0)
	if err == nil || !regexp.MustCompile(`(?i)could not restore untracked files`).MatchString(err.Error()) {
		t.Fatalf("plain apply: %v", err)
	}
	// On a fresh copy, the overwrite variant deletes the collider and applies.
	R := testutil.CloneFixture(t, "stash-picking")
	testutil.WriteFile(t, R, "delta-untracked.txt", "local, in the way\n")
	aNoErr(t, s.StashApplyOverwrite(ctx, R, 0, false))
	if !strings.Contains(aRead(t, filepath.Join(R, "alpha.txt")), "alpha v2") ||
		!strings.Contains(aRead(t, filepath.Join(R, "delta-untracked.txt")), "untracked file captured by the stash") {
		t.Fatal("stash not applied over the collider")
	}
	// apply (keep) leaves the stash in place.
	if n := len(aMust[[]StashInfo](t)(s.Stashes(ctx, R))); n != 1 {
		t.Fatalf("%d stashes", n)
	}
}

func TestSliceAOpsStashApplyOverwritePop(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "stash-picking")
	testutil.WriteFile(t, R, "delta-untracked.txt", "local, in the way\n")
	aNoErr(t, s.StashApplyOverwrite(ctx, R, 0, true))
	if !aFileExists(filepath.Join(R, "delta-untracked.txt")) {
		t.Fatal("untracked file missing")
	}
	// pop removes the stash once it applies cleanly.
	if n := len(aMust[[]StashInfo](t)(s.Stashes(ctx, R))); n != 0 {
		t.Fatalf("%d stashes", n)
	}
}

func TestSliceAOpsInteractiveRebaseSquash(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "interactive-rebase") // on messy-feature, 6 commits over main
	steps := aMust[[]InteractiveRebaseStep](t)(s.InteractiveRebaseSteps(ctx, R, "main"))
	if len(steps) != 6 {
		t.Fatalf("%d steps", len(steps))
	}
	// Keep the first commit, fold the rest into it.
	plan := make([]RebaseStep, len(steps))
	for i, st := range steps {
		action := "fixup"
		if i == 0 {
			action = "pick"
		}
		plan[i] = RebaseStep{Action: action, Hash: st.Hash, Subject: st.Subject}
	}
	aNoErr(t, s.RunInteractiveRebase(ctx, R, "main", plan))
	if n := len(aMust[[]GraphCommit](t)(s.Log(ctx, R, nil, 0))); n != 2 {
		t.Fatalf("%d commits", n) // initial (on main) + the single squashed commit
	}
	if op := aMust[*string](t)(s.MergeState(ctx, R)); op != nil {
		t.Fatalf("still %s", *op)
	}
}

// ─── test/newFeatures.test.ts ───────────────────────────────────────────────

func TestSliceAFeaturesBranchesSkipSymbolicRemoteHead(t *testing.T) {
	s, ctx := aSvc(t)
	repo := testutil.InitRepo(t)
	testutil.WriteFile(t, repo, "state.txt", "main\n")
	testutil.Git(t, repo, "add", "state.txt")
	testutil.Git(t, repo, "commit", "-qm", "main")
	testutil.Git(t, repo, "update-ref", "refs/remotes/origin/base", "HEAD")
	testutil.Git(t, repo, "switch", "-qc", "remote-source")
	testutil.WriteFile(t, repo, "state.txt", "remote\n")
	testutil.Git(t, repo, "commit", "-qam", "remote")
	testutil.Git(t, repo, "update-ref", "refs/remotes/origin/feature/topic", "HEAD")
	testutil.Git(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/feature/topic")
	testutil.Git(t, repo, "switch", "-q", "main")
	testutil.Git(t, repo, "branch", "-D", "remote-source")

	b := aMust[BranchesPayload](t)(s.Branches(ctx, repo))
	var full []string
	merged := map[string]bool{}
	for _, r := range b.Remotes {
		full = append(full, r.FullName)
		merged[r.FullName] = r.MergedIntoCurrent
	}
	if !reflect.DeepEqual(full, []string{"origin/base", "origin/feature/topic"}) {
		t.Fatalf("remotes %v", full)
	}
	if !merged["origin/base"] || merged["origin/feature/topic"] {
		t.Fatalf("merged %v", merged)
	}
}

func TestSliceAFeaturesSnapshotsListAndCreate(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "snapshots")
	before := aMust[[]SnapshotInfo](t)(s.ListSnapshots(ctx, R))
	var kinds []string
	for _, sn := range before {
		kinds = append(kinds, sn.Kind)
		if sn.Sha == "" || sn.Time <= 0 {
			t.Fatalf("snapshot %+v", sn)
		}
	}
	sort.Strings(kinds)
	if !reflect.DeepEqual(kinds, []string{"auto", "guard", "manual"}) {
		t.Fatalf("kinds %v", kinds)
	}
	snap := aMust[*SnapshotInfo](t)(s.CreateSnapshot(ctx, R, nil, nil)) // working tree is dirty in this fixture
	if snap == nil || snap.Kind != "manual" {
		t.Fatalf("snapshot %+v", snap)
	}
	if n := len(aMust[[]SnapshotInfo](t)(s.ListSnapshots(ctx, R))); n != 4 {
		t.Fatalf("%d snapshots", n)
	}
}

func TestSliceAFeaturesSnapshotCapturesUntracked(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "snapshots")
	// scratch.txt is untracked in the fixture — the legacy `git stash create`
	// mechanism could not capture it; the temp-index one must.
	snap := aMust[*SnapshotInfo](t)(s.CreateSnapshot(ctx, R, nil, nil))
	if snap == nil {
		t.Fatal("no snapshot")
	}
	aNoErr(t, os.Remove(filepath.Join(R, "scratch.txt")))
	aNoErr(t, s.RestoreSnapshot(ctx, R, snap.Sha, []string{"scratch.txt"}))
	if !strings.Contains(aRead(t, filepath.Join(R, "scratch.txt")), "todo: not yet added") {
		t.Fatal("not restored")
	}
}

func TestSliceAFeaturesSnapshotDedupe(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "snapshots")
	auto, manual := "auto", "manual"
	if aMust[*SnapshotInfo](t)(s.CreateSnapshot(ctx, R, &auto, nil)) == nil {
		t.Fatal("first tick skipped")
	}
	if aMust[*SnapshotInfo](t)(s.CreateSnapshot(ctx, R, &auto, nil)) != nil {
		t.Fatal("unchanged tree snapshotted twice")
	}
	if aMust[*SnapshotInfo](t)(s.CreateSnapshot(ctx, R, &manual, nil)) == nil {
		t.Fatal("manual request ignored")
	}
}

func TestSliceAFeaturesSnapshotRestoreLegacy(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "snapshots")
	var manualSeed *SnapshotInfo
	for _, sn := range aMust[[]SnapshotInfo](t)(s.ListSnapshots(ctx, R)) {
		if sn.Kind == "manual" {
			sn := sn
			manualSeed = &sn
		}
	}
	if manualSeed == nil {
		t.Fatal("no manual seed")
	}
	aNoErr(t, s.RestoreSnapshot(ctx, R, manualSeed.Sha, []string{"draft.md"}))
	content := aRead(t, filepath.Join(R, "draft.md"))
	if !strings.Contains(content, "section one") || strings.Contains(content, "section two") {
		t.Fatalf("draft.md %q", content)
	}
}

func TestSliceAFeaturesCommitEditCleanCascade(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "bisect-bug")
	// README.md is written once and never touched again → clean cascade.
	target := testutil.Git(t, R, "log", "--format=%H", "--", "README.md")
	before := testutil.Git(t, R, "log", "--format=%s")
	countBefore := testutil.Git(t, R, "rev-list", "--count", "HEAD")

	info := aMust[CommitEditInfo](t)(s.CommitEditInfo(ctx, R, target))
	if !info.Linear || info.Pushed {
		t.Fatalf("info %+v", info)
	}
	blob := aMust[BlobAtCommit](t)(s.BlobAtCommit(ctx, R, target, "README.md"))
	if blob.Binary || blob.Content == nil {
		t.Fatalf("blob %+v", blob)
	}
	edits := map[string]string{"README.md": *blob.Content + "\nEdited three weeks later.\n"}
	preview := aMust[CommitEditPreview](t)(s.CommitEditPreview(ctx, R, target, edits, info.Message))
	if preview.NewTip == nil || len(preview.Steps) != info.Descendants {
		t.Fatalf("preview %+v (descendants %d)", preview, info.Descendants)
	}
	for _, st := range preview.Steps {
		if st.Status != "clean" {
			t.Fatalf("step %+v", st)
		}
	}
	res := aMust[CommitEditResult](t)(s.CommitEditApply(ctx, R, target, edits, info.Message))
	if res.NewTip != testutil.Git(t, R, "rev-parse", "HEAD") ||
		testutil.Git(t, R, "rev-list", "--count", "HEAD") != countBefore ||
		testutil.Git(t, R, "log", "--format=%s") != before { // subjects preserved
		t.Fatalf("result %+v", res)
	}
	newTarget := testutil.Git(t, R, "log", "--format=%H", "--", "README.md")
	if !strings.Contains(testutil.Git(t, R, "show", newTarget+":README.md"), "Edited three weeks later.") {
		t.Fatal("edit missing")
	}
}

func TestSliceAFeaturesCommitEditForecastsConflict(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "bisect-bug")
	// discount.js is rewritten again later in history → editing its first
	// version collides with that rewrite.
	target := strings.Split(testutil.Git(t, R, "log", "--reverse", "--format=%H", "--", "discount.js"), "\n")[0]
	edits := map[string]string{"discount.js": "module.exports = { discount: () => 0 }\n"}
	preview := aMust[CommitEditPreview](t)(s.CommitEditPreview(ctx, R, target, edits, "sabotage"))
	if preview.NewTip != nil {
		t.Fatal("expected a conflict")
	}
	found := false
	for _, st := range preview.Steps {
		if st.Status == "conflict" && aContains(st.Files, "discount.js") {
			found = true
		}
	}
	if !found {
		t.Fatalf("steps %+v", preview.Steps)
	}
	_, err := s.CommitEditApply(ctx, R, target, edits, "sabotage")
	if err == nil || !regexp.MustCompile(`(?i)conflict`).MatchString(err.Error()) {
		t.Fatalf("apply: %v", err)
	}
}

func TestSliceAFeaturesCommitEditRewordKeepsTree(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "bisect-bug")
	target := testutil.Git(t, R, "log", "--format=%H", "--", "tax.js")
	oldTree := testutil.Git(t, R, "rev-parse", target+"^{tree}")
	aMust[CommitEditResult](t)(s.CommitEditApply(ctx, R, target, nil, "chore: reworded from the future"))
	newTarget := testutil.Git(t, R, "log", "--format=%H", "--grep=reworded from the future")
	if newTarget == "" || testutil.Git(t, R, "rev-parse", newTarget+"^{tree}") != oldTree {
		t.Fatal("reword changed the tree")
	}
}

func TestSliceAFeaturesCommitEditMergesAndNonAncestors(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "collaborators") // history contains merge commits
	root := strings.Split(testutil.Git(t, R, "rev-list", "--max-parents=0", "HEAD"), "\n")[0]
	info := aMust[CommitEditInfo](t)(s.CommitEditInfo(ctx, R, root))
	if info.Linear || !info.Ancestor || info.Merges == 0 {
		t.Fatalf("info %+v", info)
	}
	// A commit with no path to HEAD is the one thing still off the table.
	testutil.Git(t, R, "branch", "stray", root)
	testutil.Git(t, R, "checkout", "-q", "stray")
	testutil.WriteFile(t, R, "stray.txt", "x\n")
	testutil.Git(t, R, "add", "stray.txt")
	testutil.Git(t, R, "commit", "-qm", "stray")
	straySha := testutil.Git(t, R, "rev-parse", "HEAD")
	testutil.Git(t, R, "checkout", "-q", "main")
	_, err := s.CommitEditPreview(ctx, R, straySha, nil, "x")
	if err == nil || !regexp.MustCompile(`(?i)ancestor`).MatchString(err.Error()) {
		t.Fatalf("preview: %v", err)
	}
}

func TestSliceAFeaturesTeammateRadar(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "teammate-radar")
	// The default 45-day window, widened: the fixture's commits age with the playground.
	days := 100000.0
	r := aMust[TeammateRadarResult](t)(s.TeammateRadar(ctx, R, &TeammateRadarOpts{MaxDays: &days}))
	if r.DirtyCount == 0 {
		t.Fatal("no dirty files")
	}
	find := func(ref string) *TeammateRadarEntry {
		for i := range r.Entries {
			if r.Entries[i].Ref == ref {
				return &r.Entries[i]
			}
		}
		return nil
	}
	api := find("origin/feature/api-tokens")
	if api == nil || !aContains(api.Overlap, "api.ts") || api.Author != "María García" || api.Ahead != 1 || api.Risk != "clean" {
		t.Fatalf("api %+v", api)
	}
	ui := find("origin/feature/ui-polish")
	if ui == nil || ui.Risk != "conflict" || !aContains(ui.ConflictFiles, "ui.css") || len(ui.Overlap) != 0 {
		t.Fatalf("ui %+v", ui)
	}
	if m := find("origin/main"); m == nil || m.Risk != "clean" {
		t.Fatalf("main %+v", m)
	}
	// Collision-prone first: the branch touching a dirty file leads.
	if r.Entries[0].Ref != "origin/feature/api-tokens" {
		t.Fatalf("order %+v", r.Entries)
	}
}

func TestSliceAFeaturesTeammateRadarQuiet(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "stash-picking")
	if r := aMust[TeammateRadarResult](t)(s.TeammateRadar(ctx, R, nil)); len(r.Entries) != 0 {
		t.Fatalf("entries %+v", r.Entries)
	}
}

func TestSliceAFeaturesStashToBranch(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "changelog")
	testutil.WriteFile(t, R, "wip.txt", "wip\n")
	aNoErr(t, s.Stash(ctx, R, "wip work"))
	if n := len(aMust[[]StashInfo](t)(s.Stashes(ctx, R))); n != 1 {
		t.Fatalf("%d stashes", n)
	}
	aNoErr(t, s.StashToBranch(ctx, R, "wip-branch", 0))
	if cur := aMust[RepoSummary](t)(s.Open(ctx, R)).Current; cur != "wip-branch" {
		t.Fatalf("current %q", cur) // checked out
	}
	if !aFileExists(filepath.Join(R, "wip.txt")) {
		t.Fatal("stash not applied")
	}
	if n := len(aMust[[]StashInfo](t)(s.Stashes(ctx, R))); n != 0 {
		t.Fatalf("%d stashes", n) // dropped
	}
}

func TestSliceAFeaturesStashPushPartial(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "snapshots")
	testutil.WriteFile(t, R, "partial-a.txt", "a\n")
	testutil.WriteFile(t, R, "partial-b.txt", "b\n")
	aNoErr(t, s.StashPush(ctx, R, "only a", []string{"partial-a.txt"}, false))
	if aFileExists(filepath.Join(R, "partial-a.txt")) || !aFileExists(filepath.Join(R, "partial-b.txt")) {
		t.Fatal("wrong files stashed")
	}
	st := aMust[[]StashInfo](t)(s.Stashes(ctx, R))
	if len(st) == 0 || !strings.Contains(st[0].Message, "only a") {
		t.Fatalf("stashes %+v", st)
	}
}

func TestSliceAFeaturesStashPushKeepIndex(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "snapshots")
	testutil.WriteFile(t, R, "keep.txt", "keep\n")
	aNoErr(t, s.StageAll(ctx, R))
	aNoErr(t, s.StashPush(ctx, R, "keep test", []string{"keep.txt"}, true))
	if !aFileExists(filepath.Join(R, "keep.txt")) {
		t.Fatal("--keep-index did not leave it") // --keep-index leaves it staged
	}
	if n := len(aMust[[]StashInfo](t)(s.Stashes(ctx, R))); n != 1 {
		t.Fatalf("%d stashes", n)
	}
}

var aRadarRefs = []string{"chore/merged", "feature/big-refactor", "feature/config-bump", "feature/docs",
	"feature/rename-api", "legacy/import"}

func aRadar(t *testing.T) (MergePreviewResult, map[string]MergePreviewEntry) {
	t.Helper()
	s, ctx := aSvc(t)
	res := aMust[MergePreviewResult](t)(s.MergePreview(ctx, testutil.PlaygroundRepo(t, "conflict-radar"), "main", aRadarRefs))
	by := map[string]MergePreviewEntry{}
	for _, e := range res.Entries {
		by[e.Ref] = e
	}
	return res, by
}

func TestSliceAFeaturesMergePreviewOrder(t *testing.T) {
	res, _ := aRadar(t)
	var refs []string
	for _, e := range res.Entries {
		refs = append(refs, e.Ref)
	}
	if !reflect.DeepEqual(refs, aRadarRefs) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(res.BaseSha) {
		t.Fatalf("refs %v base %q", refs, res.BaseSha)
	}
}

func TestSliceAFeaturesMergePreviewConflicts(t *testing.T) {
	_, by := aRadar(t)
	big := by["feature/big-refactor"]
	files := append([]string{}, big.Files...)
	sort.Strings(files)
	if big.Status != "conflict" || !reflect.DeepEqual(files, []string{"README.md", "src/app.js", "src/config.js"}) {
		t.Fatalf("big-refactor %+v", big)
	}
	if !reflect.DeepEqual(by["feature/rename-api"].Files, []string{"src/app.js"}) ||
		!reflect.DeepEqual(by["feature/config-bump"].Files, []string{"src/config.js"}) {
		t.Fatalf("entries %+v", by)
	}
}

func TestSliceAFeaturesMergePreviewCleanVsMerged(t *testing.T) {
	_, by := aRadar(t)
	if d := by["feature/docs"]; d.Status != "clean" || len(d.Files) != 0 {
		t.Fatalf("docs %+v", d)
	}
	if by["chore/merged"].Status != "merged" {
		t.Fatalf("merged %+v", by["chore/merged"])
	}
}

func TestSliceAFeaturesMergePreviewKeepsScanning(t *testing.T) {
	// The orphan branch aborts the batched run mid-stream; the remaining refs
	// are retried one at a time, so no verdict goes missing.
	res, by := aRadar(t)
	legacy := by["legacy/import"]
	if legacy.Status != "error" || !strings.Contains(legacy.Message, "unrelated histories") {
		t.Fatalf("legacy %+v", legacy)
	}
	n := 0
	for _, e := range res.Entries {
		if e.Status == "conflict" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d conflicts", n)
	}
}

func TestSliceAFeaturesMergePreviewTouchesNothing(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.PlaygroundRepo(t, "conflict-radar")
	before := aMust[RepoStatus](t)(s.Status(ctx, R))
	head := aMust[BranchesPayload](t)(s.Branches(ctx, R)).Current
	aMust[MergePreviewResult](t)(s.MergePreview(ctx, R, "main", aRadarRefs))
	if after := aMust[RepoStatus](t)(s.Status(ctx, R)); !reflect.DeepEqual(after, before) {
		t.Fatalf("status changed: %+v → %+v", before, after)
	}
	if aMust[BranchesPayload](t)(s.Branches(ctx, R)).Current != head {
		t.Fatal("HEAD moved")
	}
}

// aForcePush is the rangeDiff + refTips fixture: the copy fetches for real
// (its origin is the playground's bare repo), and the forced update's old
// side is read back from the tracking ref's reflog. (TS took it from
// fetchAll's report, which slice C ports.)
func aForcePush(t *testing.T) (R, oldSha, newSha string) {
	t.Helper()
	R = testutil.CloneFixture(t, "force-push")
	before := testutil.Git(t, R, "rev-parse", "origin/feature/login")
	testutil.Git(t, R, "fetch", "-q", "origin")
	return R, before, testutil.Git(t, R, "rev-parse", "origin/feature/login")
}

func TestSliceAFeaturesRefTipsForcedUpdate(t *testing.T) {
	s, ctx := aSvc(t)
	R, _, newSha := aForcePush(t)
	tips := aMust[[]RefTip](t)(s.RefTips(ctx, R, "origin/feature/login", nil))
	if len(tips) < 2 || tips[0].Selector != "origin/feature/login@{0}" || tips[0].Sha == tips[1].Sha || tips[0].Sha != newSha {
		t.Fatalf("tips %+v", tips)
	}
	// The rewrite is recorded as a forced update, which is what makes it
	// findable without any bookkeeping of our own.
	forced := false
	for _, tp := range tips {
		if strings.Contains(tp.Reason, "forced-update") {
			forced = true
		}
	}
	if !forced {
		t.Fatalf("no forced-update in %+v", tips)
	}
}

func aKinds(entries []RangeDiffEntry, kind string) []string {
	out := []string{}
	for _, e := range entries {
		if e.Kind == kind {
			out = append(out, e.Subject)
		}
	}
	return out
}

func TestSliceAFeaturesRangeDiffPairs(t *testing.T) {
	s, ctx := aSvc(t)
	R, oldSha, _ := aForcePush(t)
	entries := aMust[[]RangeDiffEntry](t)(s.RangeDiff(ctx, R, oldSha, "origin/feature/login", ""))
	if !reflect.DeepEqual(aKinds(entries, "modified"), []string{"validate password"}) ||
		!reflect.DeepEqual(aKinds(entries, "removed"), []string{"add debug logging"}) ||
		!reflect.DeepEqual(aKinds(entries, "added"), []string{"add rate limiting"}) {
		t.Fatalf("entries %+v", entries)
	}
}

func TestSliceAFeaturesRangeDiffReflogSelector(t *testing.T) {
	s, ctx := aSvc(t)
	R, _, _ := aForcePush(t)
	entries := aMust[[]RangeDiffEntry](t)(s.RangeDiff(ctx, R, "origin/feature/login@{1}", "origin/feature/login", ""))
	if len(aKinds(entries, "modified")) == 0 {
		t.Fatalf("entries %+v", entries)
	}
}

func TestSliceAFeaturesRangeDiffInterdiff(t *testing.T) {
	s, ctx := aSvc(t)
	R, _, _ := aForcePush(t)
	entries := aMust[[]RangeDiffEntry](t)(s.RangeDiff(ctx, R, "origin/feature/login@{1}", "origin/feature/login", ""))
	var rw *RangeDiffEntry
	for i := range entries {
		if entries[i].Kind == "modified" {
			rw = &entries[i]
			break
		}
	}
	if rw == nil || !strings.Contains(rw.Body, "validate password (min length)") || !strings.Contains(rw.Body, "pw.length < 12") ||
		rw.OldSha == nil || rw.NewSha == nil || *rw.OldSha == *rw.NewSha {
		t.Fatalf("rewritten %+v", rw)
	}
}

func TestSliceAFeaturesRangeDiffSelf(t *testing.T) {
	s, ctx := aSvc(t)
	R, _, _ := aForcePush(t)
	if e := aMust[[]RangeDiffEntry](t)(s.RangeDiff(ctx, R, "main", "main", "")); len(e) != 0 {
		t.Fatalf("entries %+v", e)
	}
}

func TestSliceAFeaturesRefTipsNoReflog(t *testing.T) {
	s, ctx := aSvc(t)
	R, _, _ := aForcePush(t)
	if tips := aMust[[]RefTip](t)(s.RefTips(ctx, R, "refs/heads/does-not-exist", nil)); len(tips) != 0 {
		t.Fatalf("tips %+v", tips)
	}
}

func TestSliceAFeaturesAbsorbRoutesHunks(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "absorb")
	aNoErr(t, s.StageAll(ctx, R))
	plan := aMust[AbsorbPlan](t)(s.AbsorbPlan(ctx, R))
	var subjects []string
	for _, tg := range plan.Targets {
		subjects = append(subjects, tg.Subject)
	}
	if !reflect.DeepEqual(subjects, []string{"feat: add parser", "feat: add auth"}) {
		t.Fatalf("targets %v", subjects)
	}
	files := func(h []AbsorbHunk) []string {
		out := []string{}
		for _, x := range h {
			out = append(out, x.File)
		}
		return out
	}
	if !reflect.DeepEqual(files(plan.Targets[0].Hunks), []string{"src/parser.ts"}) ||
		!reflect.DeepEqual(files(plan.Targets[1].Hunks), []string{"src/auth.ts"}) {
		t.Fatalf("hunks %+v", plan.Targets)
	}
	// A brand-new file has no history to be absorbed into.
	if !reflect.DeepEqual(files(plan.Unmatched), []string{"src/cache.ts"}) || plan.Base != "origin/main" {
		t.Fatalf("plan %+v", plan)
	}
}

func TestSliceAFeaturesAbsorbNeverRewritesPushed(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "absorb")
	// Publish everything to a throwaway bare repo — the fixture's own origin
	// is shared with the playground, and writing to it would corrupt the next
	// test run.
	bare := t.TempDir()
	testutil.Git(t, bare, "init", "-q", "--bare")
	testutil.Git(t, R, "remote", "set-url", "origin", bare)
	testutil.Git(t, R, "push", "-q", "origin", "HEAD:main")
	testutil.Git(t, R, "fetch", "-q", "origin")
	aNoErr(t, s.StageAll(ctx, R))
	if plan := aMust[AbsorbPlan](t)(s.AbsorbPlan(ctx, R)); len(plan.Targets) != 0 {
		t.Fatalf("targets %+v", plan.Targets)
	}
}

func TestSliceAFeaturesAbsorbCreatesFixups(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "absorb")
	aNoErr(t, s.StageAll(ctx, R))
	res := aMust[AbsorbApplyResult](t)(s.AbsorbApply(ctx, R, nil))
	if res != (AbsorbApplyResult{Created: 2, Rebased: false}) {
		t.Fatalf("result %+v", res)
	}
	ten := 10
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, &ten, 0))
	if !reflect.DeepEqual(aSubjects(log)[:2], []string{"fixup! feat: add parser", "fixup! feat: add auth"}) {
		t.Fatalf("log %v", aSubjects(log))
	}
	// The unattributed file is still staged, ready to be committed normally.
	st := aMust[RepoStatus](t)(s.Status(ctx, R))
	if !reflect.DeepEqual(aPaths(st.Staged), []string{"src/cache.ts"}) || len(st.Unstaged) != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestSliceAFeaturesAbsorbRebase(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "absorb")
	aNoErr(t, s.StageAll(ctx, R))
	res := aMust[AbsorbApplyResult](t)(s.AbsorbApply(ctx, R, &AbsorbApplyOpts{Rebase: true}))
	if res != (AbsorbApplyResult{Created: 2, Rebased: true}) {
		t.Fatalf("result %+v", res)
	}
	ten := 10
	log := aMust[[]GraphCommit](t)(s.Log(ctx, R, &ten, 0))
	// Back to the original three commits — no fixup left behind.
	if !reflect.DeepEqual(aSubjects(log), []string{"feat: add logger", "feat: add parser", "feat: add auth", "init: readme"}) {
		t.Fatalf("log %v", aSubjects(log))
	}
	// The fix really landed inside the commit that needed it.
	if !strings.Contains(testutil.Git(t, R, "show", log[2].Hash+":src/auth.ts"), "if (!password) return null") {
		t.Fatal("fix not folded in")
	}
	// …and the unrelated change survived the rebase, still staged.
	if st := aMust[RepoStatus](t)(s.Status(ctx, R)); !reflect.DeepEqual(aPaths(st.Staged), []string{"src/cache.ts"}) {
		t.Fatalf("staged %v", aPaths(st.Staged))
	}
}

func TestSliceAFeaturesAbsorbNothingToDo(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "absorb")
	if r := aMust[AbsorbPlan](t)(s.AbsorbPlan(ctx, R)).Reason; r != "no-staged" {
		t.Fatalf("reason %q", r)
	}
	if res := aMust[AbsorbApplyResult](t)(s.AbsorbApply(ctx, R, nil)); res != (AbsorbApplyResult{}) {
		t.Fatalf("result %+v", res)
	}
}

func TestSliceAFeaturesRepoPulseBranch(t *testing.T) {
	s, ctx := aSvc(t)
	p := aMust[RepoPulse](t)(s.RepoPulse(ctx, testutil.PlaygroundRepo(t, "absorb")))
	// Three unpushed commits is exactly what makes that fixture absorbable.
	if p.Name != "absorb" || p.Branch != "main" || p.Upstream == nil || *p.Upstream != "origin/main" ||
		p.Ahead != 3 || p.Behind != 0 || p.LastCommitAt <= 0 || p.Error != nil {
		t.Fatalf("pulse %+v", p)
	}
}

func TestSliceAFeaturesRepoPulseCounts(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "absorb")
	// The fixture leaves two edits and one new file in the working tree.
	p := aMust[RepoPulse](t)(s.RepoPulse(ctx, R))
	if p.Unstaged != 2 || p.Untracked != 1 || p.Staged != 0 {
		t.Fatalf("pulse %+v", p)
	}
	aNoErr(t, s.StageAll(ctx, R))
	p = aMust[RepoPulse](t)(s.RepoPulse(ctx, R))
	if p.Staged != 3 || p.Unstaged != 0 || p.Untracked != 0 {
		t.Fatalf("pulse %+v", p)
	}
}

func TestSliceAFeaturesRepoPulseMerging(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "merge-conflict")
	aGitTry(R, "merge", "feature") // conflicts on purpose
	p := aMust[RepoPulse](t)(s.RepoPulse(ctx, R))
	if p.Operation == nil || *p.Operation != "merge" || p.Conflicted == 0 {
		t.Fatalf("pulse %+v", p)
	}
}

func TestSliceAFeaturesRepoPulseStashes(t *testing.T) {
	s, ctx := aSvc(t)
	if p := aMust[RepoPulse](t)(s.RepoPulse(ctx, testutil.CloneFixture(t, "stash-picking"))); p.Stashes == 0 {
		t.Fatalf("pulse %+v", p)
	}
}

func TestSliceAFeaturesRepoPulseUnreadable(t *testing.T) {
	s, ctx := aSvc(t)
	p := aMust[RepoPulse](t)(s.RepoPulse(ctx, "/definitely/not/a/repo"))
	if p.Error == nil || *p.Error == "" || p.Branch != "" {
		t.Fatalf("pulse %+v", p)
	}
}

func TestSliceAFeaturesRepoPulseActivity(t *testing.T) {
	s, ctx := aSvc(t)
	// Its own repo with backdated commits: the playground is only regenerated
	// when missing, so its commits can be any age and would land in a
	// different bucket every day.
	R := testutil.InitRepo(t)
	now := time.Now().Unix()
	commitAt := func(at int64, msg string) {
		cmd := exec.Command("git", "-C", R, "commit", "--allow-empty", "-m", msg)
		date := "@" + strconv.FormatInt(at, 10) + " +0000"
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
	}
	commitAt(now-20*86400, "outside the window")
	commitAt(now-3*86400-3600, "three days ago")
	commitAt(now-120, "just now")
	commitAt(now-60, "just now, again")

	p := aMust[RepoPulse](t)(s.RepoPulse(ctx, R))
	sum := 0
	for _, n := range p.Activity {
		sum += n
	}
	// Oldest bucket first, so today is the last one.
	if len(p.Activity) != 14 || p.Activity[13] != 2 || p.Activity[10] != 1 || sum != 3 {
		t.Fatalf("activity %v", p.Activity)
	}
}

func TestSliceAFeaturesRepoDetail(t *testing.T) {
	s, ctx := aSvc(t)
	R := testutil.CloneFixture(t, "absorb")
	d := aMust[RepoDetail](t)(s.RepoDetail(ctx, R, nil))
	var subjects, rows []string
	for _, c := range d.Commits {
		subjects = append(subjects, c.Subject)
		if !regexp.MustCompile(`^[0-9a-f]{7,}$`).MatchString(c.Hash) {
			t.Fatalf("hash %q", c.Hash)
		}
	}
	// Three unpushed commits, newest first.
	if !reflect.DeepEqual(subjects, []string{"feat: add logger", "feat: add parser", "feat: add auth"}) {
		t.Fatalf("commits %v", subjects)
	}
	// Two edits plus the untracked file the fixture leaves behind.
	for _, f := range d.Files {
		rows = append(rows, f.Status+" "+f.Path)
	}
	sort.Strings(rows)
	if !reflect.DeepEqual(rows, []string{"? src/cache.ts", "M src/auth.ts", "M src/parser.ts"}) {
		t.Fatalf("files %v", rows)
	}
}

func TestSliceAFeaturesRepoDetailCapped(t *testing.T) {
	s, ctx := aSvc(t)
	three := 3
	d := aMust[RepoDetail](t)(s.RepoDetail(ctx, testutil.PlaygroundRepo(t, "deep-history-monorepo"), &three))
	if len(d.Commits) > 3 || len(d.Files) > 3 {
		t.Fatalf("detail %+v", d)
	}
}

// ─── test/commitEditMerges.test.ts ──────────────────────────────────────────

// aMergeRepo builds:
//
//	A: adds config.txt + feature.txt("base"), the edit target.
//	side (from A): changes feature.txt to "side-change".
//	B (main): changes feature.txt to "main-change".
//	M: merge side into main — CONFLICT, resolved by hand as "resolved-by-human".
//	C: touches an unrelated file on top.
func aMergeRepo(t *testing.T) (repo, A, B, side, M string) {
	t.Helper()
	repo = testutil.InitRepo(t)
	g := func(args ...string) string { return testutil.Git(t, repo, args...) }
	testutil.WriteFile(t, repo, "config.txt", "setting=1\n")
	testutil.WriteFile(t, repo, "feature.txt", "base\n")
	g("add", ".")
	g("commit", "-qm", "A: base")
	A = g("rev-parse", "HEAD")
	g("checkout", "-q", "-b", "side")
	testutil.WriteFile(t, repo, "feature.txt", "side-change\n")
	g("commit", "-qam", "side: change feature")
	side = g("rev-parse", "HEAD")
	g("checkout", "-q", "main")
	testutil.WriteFile(t, repo, "feature.txt", "main-change\n")
	g("commit", "-qam", "B: change feature on main")
	B = g("rev-parse", "HEAD")
	aGitTry(repo, "merge", "--no-ff", "side") // expected conflict
	testutil.WriteFile(t, repo, "feature.txt", "resolved-by-human\n")
	g("add", "feature.txt")
	g("commit", "-qm", "M: merge side")
	M = g("rev-parse", "HEAD")
	testutil.WriteFile(t, repo, "other.txt", "later\n")
	g("add", "other.txt")
	g("commit", "-qm", "C: later work")
	return repo, A, B, side, M
}

func TestSliceACommitEditMergesReplaysResolution(t *testing.T) {
	s, ctx := aSvc(t)
	repo, A, _, side, _ := aMergeRepo(t)
	info := aMust[CommitEditInfo](t)(s.CommitEditInfo(ctx, repo, A))
	if !info.Ancestor || info.Merges != 1 {
		t.Fatalf("info %+v", info)
	}
	edits := map[string]string{"config.txt": "setting=2\n"}
	preview := aMust[CommitEditPreview](t)(s.CommitEditPreview(ctx, repo, A, edits, ""))
	if preview.NewTip == nil {
		t.Fatal("no tip")
	}
	for _, st := range preview.Steps {
		if st.Merge && st.Status != "clean" {
			t.Fatalf("merge step %+v", st)
		}
	}
	res := aMust[CommitEditResult](t)(s.CommitEditApply(ctx, repo, A, edits, ""))
	// The hand-made resolution survived the replay verbatim.
	if aRead(t, filepath.Join(repo, "feature.txt")) != "resolved-by-human\n" ||
		aRead(t, filepath.Join(repo, "config.txt")) != "setting=2\n" {
		t.Fatal("worktree wrong after apply")
	}
	// The replayed merge still has two parents, and the side branch — which
	// also descends from A — was itself rewritten and re-pointed.
	newMerge := testutil.Git(t, repo, "rev-parse", "HEAD^")
	parents := strings.Split(testutil.Git(t, repo, "rev-list", "--parents", "-n", "1", newMerge), " ")
	if len(parents) != 3 || parents[2] == side || testutil.Git(t, repo, "show", parents[2]+":config.txt") != "setting=2" {
		t.Fatalf("parents %v", parents)
	}
	// History depth is unchanged.
	if testutil.Git(t, repo, "rev-list", "--count", "HEAD") != "5" || res.NewTip != testutil.Git(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("result %+v", res)
	}
}

func TestSliceACommitEditMergesLeavesSideBranch(t *testing.T) {
	s, ctx := aSvc(t)
	repo, _, B, side, _ := aMergeRepo(t)
	// Editing B (after the branch point): side does not contain B.
	if p := aMust[CommitEditPreview](t)(s.CommitEditPreview(ctx, repo, B, nil, "B: reworded on main")); p.NewTip == nil {
		t.Fatal("no tip")
	}
	aMust[CommitEditResult](t)(s.CommitEditApply(ctx, repo, B, nil, "B: reworded on main"))
	newMerge := testutil.Git(t, repo, "rev-parse", "HEAD^")
	parents := strings.Split(testutil.Git(t, repo, "rev-list", "--parents", "-n", "1", newMerge), " ")
	// Second parent is the ORIGINAL side commit — untouched identity.
	if parents[2] != side || aRead(t, filepath.Join(repo, "feature.txt")) != "resolved-by-human\n" {
		t.Fatalf("parents %v", parents)
	}
}

func TestSliceACommitEditMergesForecastsConflict(t *testing.T) {
	s, ctx := aSvc(t)
	repo, A, _, _, _ := aMergeRepo(t)
	// Editing the very line both branches rewrote cannot replay cleanly.
	edits := map[string]string{"feature.txt": "edited-base\n"}
	p := aMust[CommitEditPreview](t)(s.CommitEditPreview(ctx, repo, A, edits, ""))
	conflict := false
	for _, st := range p.Steps {
		if st.Status == "conflict" {
			conflict = true
		}
	}
	if p.NewTip != nil || !conflict {
		t.Fatalf("preview %+v", p)
	}
	if _, err := s.CommitEditApply(ctx, repo, A, edits, ""); err == nil || !regexp.MustCompile(`(?i)conflict`).MatchString(err.Error()) {
		t.Fatalf("apply: %v", err)
	}
}

func TestSliceACommitEditMergesRewordsMerge(t *testing.T) {
	s, ctx := aSvc(t)
	repo, _, _, _, M := aMergeRepo(t)
	if !aMust[CommitEditInfo](t)(s.CommitEditInfo(ctx, repo, M)).Ancestor {
		t.Fatal("merge not an ancestor")
	}
	oldTree := testutil.Git(t, repo, "rev-parse", M+"^{tree}")
	aMust[CommitEditResult](t)(s.CommitEditApply(ctx, repo, M, nil, "M: reworded merge"))
	newMerge := testutil.Git(t, repo, "rev-parse", "HEAD^")
	if testutil.Git(t, repo, "log", "-1", "--format=%s", newMerge) != "M: reworded merge" ||
		testutil.Git(t, repo, "rev-parse", newMerge+"^{tree}") != oldTree ||
		len(strings.Split(testutil.Git(t, repo, "rev-list", "--parents", "-n", "1", newMerge), " ")) != 3 {
		t.Fatal("merge reword went wrong")
	}
}
