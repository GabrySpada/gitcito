package gitsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/testutil"
)

// Ports of the vitest cases (test/gitOps.test.ts, test/newFeatures.test.ts,
// test/repoChat.test.ts) that exercise slice C methods. Assertions the TS
// made through other slices' methods (status, open, branches, log, …) are
// made here with plain git, so these tests stand on slice C alone.

// ─── helpers ───────────────────────────────────────────────────────────────

func cNewSvc(t *testing.T) (*GitService, context.Context) {
	t.Helper()
	cIsolate(t)
	return New(nil, nil), context.Background()
}

// cIsolate is testutil.IsolateGitEnv plus XDG_DATA_HOME, which the Linux
// trash honours: fsDelete and a "replace" drop must never reach the
// developer's real trash can.
func cIsolate(t *testing.T) testutil.GitEnv {
	t.Helper()
	env := testutil.IsolateGitEnv(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(env.Home, ".local", "share"))
	return env
}

// cGit runs git in dir, failing the test on error; output trimmed.
func cGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return testutil.Git(t, dir, args...)
}

// cGitTry runs git in dir and ignores a failure.
func cGitTry(dir string, args ...string) string {
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

// cCommitAll stages everything and commits as a fixed test identity.
func cCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	cGit(t, dir, "add", "-A")
	cGit(t, dir, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-q", "-m", msg)
}

func cWrite(t *testing.T, dir, rel, content string) { testutil.WriteFile(t, dir, rel, content) }

func cRead(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cExistsAt(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

func cCurrent(t *testing.T, dir string) string {
	return cGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

func cLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func cLocals(t *testing.T, dir string) []string {
	return cLines(cGit(t, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads"))
}

func cStaged(t *testing.T, dir string) []string {
	return cLines(cGit(t, dir, "diff", "--cached", "--name-only"))
}

func cConflicted(t *testing.T, dir string) []string {
	return cLines(cGit(t, dir, "diff", "--name-only", "--diff-filter=U"))
}

// cChanged is every path `git status` reports (staged, unstaged, untracked).
func cChanged(t *testing.T, dir string) []string {
	var out []string
	for _, l := range cLines(cGit(t, dir, "status", "--porcelain", "-uall")) {
		out = append(out, strings.TrimSpace(l[2:]))
	}
	return out
}

func cSha(t *testing.T, dir, ref string) string { return cGit(t, dir, "rev-parse", ref) }

func cInGitDir(t *testing.T, dir, name string) bool {
	gitDir := cGit(t, dir, "rev-parse", "--absolute-git-dir")
	_, err := os.Stat(filepath.Join(gitDir, name))
	return err == nil
}

func cMustErr(t *testing.T, err error, re string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an error matching %q, got none", re)
	}
	if re != "" && !regexp.MustCompile(re).MatchString(err.Error()) {
		t.Fatalf("error %q does not match %q", err.Error(), re)
	}
}

func cNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func cEq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func cSliceEq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") || len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func cBoolPtr(b bool) *bool { return &b }

func cStackNames(info StackInfo) []string {
	out := []string{}
	for _, b := range info.Branches {
		out = append(out, b.Name)
	}
	return out
}

// ─── gitOps.test.ts ────────────────────────────────────────────────────────

func TestCRepoChatAppliesPreparedEdit(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	before := cRead(t, r, "README.md")
	sum := sha256.Sum256([]byte(before))
	hash := hex.EncodeToString(sum[:])
	one := 1
	action := PreparedRepoChatFileAction{
		Type: "edit_file", Path: "README.md", OldText: "# BugShop", NewText: "# BugShop updated",
		Description: "Update heading", ExpectedHash: &hash, ExpectedOccurrences: &one,
	}
	if IsReadMethod("applyRepoFileActions") {
		t.Fatal("applyRepoFileActions must take the write lock")
	}
	res, err := svc.ApplyRepoFileActions(ctx, r, []PreparedRepoChatFileAction{action})
	cNoErr(t, err)
	if !res.Ok || res.Applied == nil || *res.Applied != 1 {
		t.Fatalf("got %+v", res)
	}
	if !strings.Contains(cRead(t, r, "README.md"), "# BugShop updated") {
		t.Fatal("edit not applied")
	}
	if !cContains(cChanged(t, r), "README.md") {
		t.Fatal("README.md not reported as changed")
	}
}

func TestCMergeConflictThenAbort(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	cMustErr(t, svc.Merge(ctx, r, "feature", MergeOptionsArg{}), "")
	if !cInGitDir(t, r, "MERGE_HEAD") {
		t.Fatal("no merge in progress")
	}
	if len(cConflicted(t, r)) == 0 {
		t.Fatal("no conflicted files")
	}
	cGit(t, r, "merge", "--abort")
	if cInGitDir(t, r, "MERGE_HEAD") || len(cConflicted(t, r)) != 0 {
		t.Fatal("abort left the merge behind")
	}
	cEq(t, cCurrent(t, r), "main")
}

func TestCRebaseConflictThenAbort(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "rebase-conflict") // checked out on feature
	cEq(t, cCurrent(t, r), "feature")
	cMustErr(t, svc.Rebase(ctx, r, "main"), "")
	if !cInGitDir(t, r, "rebase-merge") && !cInGitDir(t, r, "rebase-apply") {
		t.Fatal("no rebase in progress")
	}
	cNoErr(t, svc.RebaseAbort(ctx, r))
	if cInGitDir(t, r, "rebase-merge") || cInGitDir(t, r, "rebase-apply") {
		t.Fatal("rebase still in progress")
	}
	cEq(t, cCurrent(t, r), "feature")
}

func TestCCreateBranchStageCommit(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	cNoErr(t, svc.CreateBranch(ctx, r, "test/scratch", "", cBoolPtr(true)))
	cEq(t, cCurrent(t, r), "test/scratch")
	cWrite(t, r, "scratch.txt", "hello from a test\n")
	cGit(t, r, "add", "scratch.txt")
	if !cContains(cStaged(t, r), "scratch.txt") {
		t.Fatal("not staged")
	}
	cNoErr(t, svc.Commit(ctx, r, "test: add scratch file", false, false))
	cEq(t, cGit(t, r, "log", "-1", "--format=%s"), "test: add scratch file")
	cEq(t, len(cStaged(t, r)), 0)
}

func TestCAmendKeepsColleagueAuthorship(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	cGit(t, r, "config", "user.name", "Me")
	cGit(t, r, "config", "user.email", "me@test")
	cWrite(t, r, "theirs.txt", "their work\n")
	cGit(t, r, "add", "theirs.txt")
	cGit(t, r, "-c", "user.name=Colleague", "-c", "user.email=colleague@test", "commit", "-q", "-m", "feat: theirs")
	theirs, parent := cSha(t, r, "HEAD"), cSha(t, r, "HEAD~1")
	author, err := svc.CommitAuthor(ctx, r, "HEAD")
	cNoErr(t, err)
	if author == nil || author.Name != "Colleague" || author.Email != "colleague@test" {
		t.Fatalf("author %+v", author)
	}

	// A plain amend is what credited a colleague's policy fix to someone else.
	cWrite(t, r, "mine.txt", "my work\n")
	cGit(t, r, "add", "mine.txt")
	cNoErr(t, svc.Commit(ctx, r, "feat: mine", true, false))
	author, _ = svc.CommitAuthor(ctx, r, "HEAD")
	cEq(t, author.Email, "colleague@test")
	cEq(t, cSha(t, r, "HEAD~1"), parent) // replaced, not stacked

	// Undo resets to the amended commit, not HEAD~1: their commit is back and
	// only the amend's own change is left staged.
	cGit(t, r, "reset", "-q", "--soft", theirs)
	cEq(t, cSha(t, r, "HEAD"), theirs)
	cSliceEq(t, cStaged(t, r), []string{"mine.txt"})

	// Redo with --reset-author takes the authorship.
	cNoErr(t, svc.Commit(ctx, r, "feat: mine", true, true))
	author, _ = svc.CommitAuthor(ctx, r, "HEAD")
	if author.Name != "Me" || author.Email != "me@test" {
		t.Fatalf("author %+v", author)
	}
	cEq(t, cSha(t, r, "HEAD~1"), parent)
}

func TestCCommitAuthorUnborn(t *testing.T) {
	svc, ctx := cNewSvc(t)
	author, err := svc.CommitAuthor(ctx, testutil.CloneFixture(t, "empty-repo"), "HEAD")
	cNoErr(t, err)
	if author != nil {
		t.Fatalf("want nil, got %+v", author)
	}
}

func TestCCommitAuthorIsARead(t *testing.T) {
	if !IsReadMethod("commitAuthor") || IsReadMethod("commit") {
		t.Fatal("commitAuthor must be a read and commit a write")
	}
}

func TestCRenameCheckedOutBranch(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	cNoErr(t, svc.CreateBranch(ctx, r, "fix", "", cBoolPtr(true)))
	cNoErr(t, svc.RenameBranch(ctx, r, "fix", "fix/login-redirect"))
	cEq(t, cCurrent(t, r), "fix/login-redirect")
	if cContains(cLocals(t, r), "fix") {
		t.Fatal("old name still there")
	}
}

func TestCRenameOtherBranch(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	before := cCurrent(t, r)
	cNoErr(t, svc.CreateBranch(ctx, r, "wip", "HEAD", cBoolPtr(false)))
	cNoErr(t, svc.RenameBranch(ctx, r, "wip", "feature/checkout"))
	cEq(t, cCurrent(t, r), before)
	if !cContains(cLocals(t, r), "feature/checkout") {
		t.Fatal("renamed branch missing")
	}
}

func TestCRenameBack(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	cNoErr(t, svc.CreateBranch(ctx, r, "typoo", "HEAD", cBoolPtr(false)))
	cNoErr(t, svc.RenameBranch(ctx, r, "typoo", "typo"))
	cNoErr(t, svc.RenameBranch(ctx, r, "typo", "typoo"))
	if !cContains(cLocals(t, r), "typoo") {
		t.Fatal("rename back failed")
	}
}

func TestCRenameRefusesTakenName(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	cNoErr(t, svc.CreateBranch(ctx, r, "one", "HEAD", cBoolPtr(false)))
	cNoErr(t, svc.CreateBranch(ctx, r, "two", "HEAD", cBoolPtr(false)))
	cMustErr(t, svc.RenameBranch(ctx, r, "one", "two"), "")
	if !cContains(cLocals(t, r), "one") {
		t.Fatal("branch lost")
	}
}

func TestCTreeDndMovesTrackedFile(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	cNoErr(t, svc.FsMove(ctx, r, []string{"src/app.ts"}, "lib", ""))
	if cExistsAt(r, "src/app.ts") || !cExistsAt(r, "lib/app.ts") {
		t.Fatal("not moved")
	}
	// `git mv` stages the rename, so history follows the file.
	if !cContains(cStaged(t, r), "lib/app.ts") {
		t.Fatal("rename not staged")
	}
}

func TestCTreeDndMovesUntrackedFile(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	cNoErr(t, svc.FsMove(ctx, r, []string{"loose.txt"}, "docs", ""))
	if !cExistsAt(r, "docs/loose.txt") {
		t.Fatal("not moved into docs")
	}
	cNoErr(t, svc.FsMove(ctx, r, []string{"docs/loose.txt"}, "", ""))
	if !cExistsAt(r, "loose.txt") || cExistsAt(r, "docs/loose.txt") {
		t.Fatal("not moved back to the root")
	}
}

func TestCTreeDndRefusesMoveIntoItself(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	cMustErr(t, svc.FsMove(ctx, r, []string{"src"}, "src/deep", ""), "into itself")
	if !cExistsAt(r, "src/deep/nested/keep.ts") {
		t.Fatal("tree damaged")
	}
}

func TestCTreeDndRefusesOverwrite(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	cWrite(t, r, "helpers.ts", "root copy")
	cMustErr(t, svc.FsMove(ctx, r, []string{"helpers.ts"}, "lib", ""), "Already exists")
}

func TestCTreeDndImportCopiesOutsideMovesInside(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	outside := testutil.CloneFixture(t, "empty-repo")
	cWrite(t, outside, "dropped.txt", "from Finder")
	cNoErr(t, svc.FsImport(ctx, r, []string{filepath.Join(outside, "dropped.txt")}, "docs", ""))
	cEq(t, cRead(t, r, "docs/dropped.txt"), "from Finder")
	if !cExistsAt(outside, "dropped.txt") {
		t.Fatal("copied, not moved: the source must stay")
	}
	// A path already inside the repo is a move — no duplicate left behind.
	cNoErr(t, svc.FsImport(ctx, r, []string{filepath.Join(r, "draft.md")}, "lib", ""))
	if !cExistsAt(r, "lib/draft.md") || cExistsAt(r, "draft.md") {
		t.Fatal("inside path not moved")
	}
}

func TestCTreeDndCopiesFolder(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	outside := testutil.CloneFixture(t, "empty-repo")
	cNoErr(t, svc.FsImport(ctx, r, []string{filepath.Join(outside, ".git")}, "vendor", ""))
	if !cExistsAt(r, "vendor/.git/HEAD") {
		t.Fatal("folder not copied recursively")
	}
}

func TestCDropConflictsFsExisting(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	got, err := svc.FsExisting(ctx, r, "docs", []string{"readme.md", "loose.txt"})
	cNoErr(t, err)
	cSliceEq(t, got, []string{"readme.md"})
	got, _ = svc.FsExisting(ctx, r, "", []string{"loose.txt"})
	cSliceEq(t, got, []string{"loose.txt"})
}

func TestCDropConflictsReplace(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	cWrite(t, r, "readme.md", "incoming")
	cNoErr(t, svc.FsMove(ctx, r, []string{"readme.md"}, "docs", "replace"))
	cEq(t, cRead(t, r, "docs/readme.md"), "incoming")
	if cExistsAt(r, "readme.md") {
		t.Fatal("source still there")
	}
}

func TestCDropConflictsKeepBoth(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	cWrite(t, r, "readme.md", "incoming")
	cNoErr(t, svc.FsMove(ctx, r, []string{"readme.md"}, "docs", "keepBoth"))
	cEq(t, cRead(t, r, "docs/readme.md"), "# Docs\n")
	cEq(t, cRead(t, r, "docs/readme 2.md"), "incoming")
	// A third copy keeps counting up rather than clobbering "readme 2.md".
	cWrite(t, r, "readme.md", "third")
	cNoErr(t, svc.FsMove(ctx, r, []string{"readme.md"}, "docs", "keepBoth"))
	cEq(t, cRead(t, r, "docs/readme 3.md"), "third")
}

func TestCDropConflictsImportModes(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	outside := testutil.CloneFixture(t, "empty-repo")
	cWrite(t, outside, "readme.md", "from Finder")
	src := filepath.Join(outside, "readme.md")
	cNoErr(t, svc.FsImport(ctx, r, []string{src}, "docs", "keepBoth"))
	cEq(t, cRead(t, r, "docs/readme 2.md"), "from Finder")
	cNoErr(t, svc.FsImport(ctx, r, []string{src}, "docs", "replace"))
	cEq(t, cRead(t, r, "docs/readme.md"), "from Finder")
}

func TestCGitflowReadsLayout(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "gitflow")
	// Pin the branch rather than trusting the fixture's: a screenshot run's
	// `prepare` step checks this repo out elsewhere.
	cNoErr(t, svc.Checkout(ctx, r, "develop"))
	st, err := svc.GitflowStatus(ctx, r)
	cNoErr(t, err)
	if !st.Initialized || st.Config.Main != "main" || st.Config.Develop != "develop" ||
		st.Config.FeaturePrefix != "feature/" || st.Config.VersionTagPrefix != "v" || !st.HasMain || !st.HasDevelop {
		t.Fatalf("status %+v", st)
	}
	if st.CurrentFlow != nil {
		t.Fatalf("develop is not a flow branch: %+v", st.CurrentFlow)
	}
	cNoErr(t, svc.Checkout(ctx, r, "feature/search"))
	st, _ = svc.GitflowStatus(ctx, r)
	if st.CurrentFlow == nil || *st.CurrentFlow != (GitflowFlow{Kind: "feature", Name: "search"}) {
		t.Fatalf("flow %+v", st.CurrentFlow)
	}
}

func TestCGitflowProposesLayoutAndCreatesDevelop(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	before, err := svc.GitflowStatus(ctx, r)
	cNoErr(t, err)
	if before.Initialized || before.Config.Main != "main" || before.HasDevelop {
		t.Fatalf("before %+v", before)
	}
	cNoErr(t, svc.GitflowInit(ctx, r, before.Config))
	after, _ := svc.GitflowStatus(ctx, r)
	if !after.Initialized || !after.HasDevelop {
		t.Fatalf("after %+v", after)
	}
}

func TestCGitflowStartsFeatureAndHotfix(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "gitflow")
	feature, err := svc.GitflowStart(ctx, r, "feature", "login")
	cNoErr(t, err)
	cEq(t, feature, "feature/login")
	cEq(t, cCurrent(t, r), "feature/login")
	cEq(t, cSha(t, r, "feature/login"), cSha(t, r, "develop"))

	cNoErr(t, svc.Checkout(ctx, r, "main"))
	hotfix, err := svc.GitflowStart(ctx, r, "hotfix", "1.0.1")
	cNoErr(t, err)
	cEq(t, hotfix, "hotfix/1.0.1")
	cEq(t, cSha(t, r, "hotfix/1.0.1"), cSha(t, r, "main"))
}

func TestCGitflowStartRefusals(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "gitflow")
	_, err := svc.GitflowStart(ctx, r, "feature", "search")
	cMustErr(t, err, "already exists")
	_, err = svc.GitflowStart(ctx, r, "feature", "  ")
	cMustErr(t, err, "")
}

func TestCGitflowFinishFeature(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "gitflow")
	mainBefore := cSha(t, r, "main")
	snap, err := svc.GitflowFinish(ctx, r, "feature", "search", GitflowFinishOptions{})
	cNoErr(t, err)
	cEq(t, cCurrent(t, r), "develop")
	cEq(t, cSha(t, r, "main"), mainBefore) // main is untouched by a feature
	if snap.Tag != nil {
		t.Fatalf("tag %v", *snap.Tag)
	}
	// --no-ff, so the merge is visible: develop's tip has two parents.
	cEq(t, len(strings.Fields(cGit(t, r, "rev-list", "--parents", "-n", "1", "develop"))), 3)
	if cContains(cLocals(t, r), "feature/search") {
		t.Fatal("branch not deleted")
	}
}

func TestCGitflowFinishRelease(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "gitflow")
	snap, err := svc.GitflowFinish(ctx, r, "release", "1.1.0", GitflowFinishOptions{})
	cNoErr(t, err)
	if snap.Tag == nil || *snap.Tag != "v1.1.0" {
		t.Fatalf("tag %v", snap.Tag)
	}
	if !cContains(cLines(cGit(t, r, "tag", "--points-at", "main")), "v1.1.0") {
		t.Fatal("main not tagged")
	}
	for _, b := range []string{"main", "develop"} {
		if !strings.Contains(cGit(t, r, "branch", "--contains", snap.Branch.Sha, b), b) {
			t.Fatalf("%s lacks the release commit", b)
		}
	}
}

func TestCGitflowUndoPutsRefsBack(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "gitflow")
	mainBefore, developBefore := cSha(t, r, "main"), cSha(t, r, "develop")
	snap, err := svc.GitflowFinish(ctx, r, "release", "1.1.0", GitflowFinishOptions{})
	cNoErr(t, err)
	cNoErr(t, svc.GitflowUndo(ctx, r, snap))
	cEq(t, cSha(t, r, "main"), mainBefore)
	cEq(t, cSha(t, r, "develop"), developBefore)
	if !cContains(cLocals(t, r), "release/1.1.0") {
		t.Fatal("release branch not restored")
	}
	if cContains(cLines(cGit(t, r, "tag", "--list")), "v1.1.0") {
		t.Fatal("tag not removed")
	}
}

func TestCGitflowFinishRefusesDirtyTree(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "gitflow")
	cWrite(t, r, "app.js", "dirty\n")
	before := cSha(t, r, "develop")
	_, err := svc.GitflowFinish(ctx, r, "feature", "search", GitflowFinishOptions{})
	cMustErr(t, err, "stash")
	cEq(t, cSha(t, r, "develop"), before)
}

// cResolveAll settles every conflicted file with content and stages it —
// resolveConflict(), which belongs to slice A, done by hand.
func cResolveAll(t *testing.T, r, content string) {
	for _, f := range cConflicted(t, r) {
		cWrite(t, r, f, content)
		cGit(t, r, "add", "--", f)
	}
	// A modify/delete conflict whose file is gone is settled by removing it.
	for _, l := range cLines(cGit(t, r, "status", "--porcelain")) {
		if strings.HasPrefix(l, "DU") || strings.HasPrefix(l, "UD") {
			cGit(t, r, "add", "-A", "--", strings.TrimSpace(l[2:]))
		}
	}
}

func TestCRerereOffByDefault(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	st, err := svc.RerereStatus(ctx, r)
	cNoErr(t, err)
	if st.Enabled || st.Recorded != 0 || len(st.Replayed) != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestCRerereTurnsOnPerRepo(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	cNoErr(t, svc.SetRerere(ctx, r, RerereSettings{Enabled: cBoolPtr(true), AutoUpdate: cBoolPtr(true)}, "repo"))
	st, _ := svc.RerereStatus(ctx, r)
	if !st.Enabled || !st.AutoUpdate || !st.PerRepo {
		t.Fatalf("status %+v", st)
	}
	cEq(t, cGit(t, r, "config", "--local", "--get", "rerere.enabled"), "true")
}

func TestCRerereReplaysResolution(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	cNoErr(t, svc.SetRerere(ctx, r, RerereSettings{Enabled: cBoolPtr(true)}, "repo"))
	// First encounter: conflict, resolve by hand, commit.
	cMustErr(t, svc.Merge(ctx, r, "feature", MergeOptionsArg{}), "")
	if len(cConflicted(t, r)) == 0 {
		t.Fatal("no conflict")
	}
	cResolveAll(t, r, "resolved by hand\n")
	cGit(t, r, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-q", "--no-edit")
	st, _ := svc.RerereStatus(ctx, r)
	if st.Recorded == 0 {
		t.Fatal("nothing recorded")
	}
	// Rewind and hit the very same conflict again.
	cGit(t, r, "reset", "-q", "--hard", "HEAD~1")
	cMustErr(t, svc.Merge(ctx, r, "feature", MergeOptionsArg{}), "")
	// git replayed what it had memorised, and says so.
	st, _ = svc.RerereStatus(ctx, r)
	if len(st.Replayed) == 0 {
		t.Fatal("nothing replayed")
	}
	cEq(t, cRead(t, r, st.Replayed[0]), "resolved by hand\n")
}

func TestCRerereForgetBringsConflictBack(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	cNoErr(t, svc.SetRerere(ctx, r, RerereSettings{Enabled: cBoolPtr(true)}, "repo"))
	cMustErr(t, svc.Merge(ctx, r, "feature", MergeOptionsArg{}), "")
	// Only files rerere took a preimage of can be forgotten later.
	tracked := cLines(cGit(t, r, "rerere", "status"))
	if len(tracked) == 0 {
		t.Fatal("rerere tracked nothing")
	}
	file := tracked[0]
	cResolveAll(t, r, "first answer\n")
	cGit(t, r, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-q", "--no-edit")
	cGit(t, r, "reset", "-q", "--hard", "HEAD~1")
	cMustErr(t, svc.Merge(ctx, r, "feature", MergeOptionsArg{}), "")
	cNoErr(t, svc.RerereForget(ctx, r, file))
	// Forgetting restores the conflict markers for that file.
	if !strings.Contains(cRead(t, r, file), "<<<<<<<") {
		t.Fatal("markers not restored")
	}
}

func TestCRerereClear(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	cNoErr(t, svc.SetRerere(ctx, r, RerereSettings{Enabled: cBoolPtr(true)}, "repo"))
	cMustErr(t, svc.Merge(ctx, r, "feature", MergeOptionsArg{}), "")
	file := cConflicted(t, r)[0]
	cResolveAll(t, r, "answer\n")
	cGit(t, r, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-q", "--no-edit")
	st, _ := svc.RerereStatus(ctx, r)
	if st.Recorded == 0 {
		t.Fatal("nothing recorded")
	}
	cNoErr(t, svc.RerereClear(ctx, r))
	st, _ = svc.RerereStatus(ctx, r)
	cEq(t, st.Recorded, 0)
	if !cExistsAt(r, file) {
		t.Fatal("working tree touched")
	}
}

// cContentConflict builds two branches editing one line of shared.txt.
func cContentConflict(t *testing.T) (repo, file string) {
	repo = testutil.CloneFixture(t, "bisect-bug")
	file = "shared.txt"
	cWrite(t, repo, file, "alpha\nbeta\ngamma\n")
	cCommitAll(t, repo, "base")
	base := cSha(t, repo, "HEAD")
	cGit(t, repo, "checkout", "-q", "-b", "incoming")
	cWrite(t, repo, file, "alpha\ntheir beta\ngamma\n")
	cCommitAll(t, repo, "their edit")
	cGit(t, repo, "checkout", "-q", "-b", "mine", base)
	cWrite(t, repo, file, "alpha\nour beta\ngamma\n")
	cCommitAll(t, repo, "our edit")
	return repo, file
}

// cCleanBranch builds a side branch that merges cleanly into a HEAD that
// moved on too (so the merge is a real merge, not a fast-forward).
func cCleanBranch(t *testing.T) string {
	repo := testutil.CloneFixture(t, "bisect-bug")
	cGit(t, repo, "checkout", "-q", "-b", "addition")
	cWrite(t, repo, "added.txt", "a file nothing else touches\n")
	cCommitAll(t, repo, "add a file")
	cGit(t, repo, "checkout", "-q", "-")
	cWrite(t, repo, "here.txt", "moved on over here\n")
	cCommitAll(t, repo, "move on")
	return repo
}

func cMergeOpts(f func(o *MergeOptionsArg)) MergeOptionsArg {
	var o MergeOptionsArg
	f(&o)
	return o
}

func TestCMergeFavourOurs(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r, file := cContentConflict(t)
	// Without it, the same merge stops on the clash.
	cMustErr(t, svc.Merge(ctx, r, "incoming", MergeOptionsArg{}), "")
	cGit(t, r, "merge", "--abort")
	cNoErr(t, svc.Merge(ctx, r, "incoming", cMergeOpts(func(o *MergeOptionsArg) { o.Favour = "ours" })))
	cEq(t, len(cConflicted(t, r)), 0)
	if !strings.Contains(cRead(t, r, file), "our beta") {
		t.Fatal("ours not kept")
	}
}

func TestCMergeFavourTheirs(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r, file := cContentConflict(t)
	cNoErr(t, svc.Merge(ctx, r, "incoming", cMergeOpts(func(o *MergeOptionsArg) { o.Favour = "theirs" })))
	cEq(t, len(cConflicted(t, r)), 0)
	if !strings.Contains(cRead(t, r, file), "their beta") {
		t.Fatal("theirs not taken")
	}
}

func TestCMergeFavourCannotDecideModifyDelete(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	// The fixture deletes a file on one side and edits it on the other; -X
	// only ever decides between two versions of a line.
	cMustErr(t, svc.Merge(ctx, r, "feature", cMergeOpts(func(o *MergeOptionsArg) { o.Favour = "ours" })), `(?i)modify/delete|CONFLICT`)
}

func TestCMergeFfOnlyRefuses(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	head := cSha(t, r, "HEAD")
	cMustErr(t, svc.Merge(ctx, r, "feature", cMergeOpts(func(o *MergeOptionsArg) { o.FfOnly = true })), "")
	cEq(t, cSha(t, r, "HEAD"), head)
}

func TestCMergeSquash(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cCleanBranch(t)
	head := cSha(t, r, "HEAD")
	cNoErr(t, svc.Merge(ctx, r, "addition", cMergeOpts(func(o *MergeOptionsArg) { o.Squash = true })))
	// HEAD has not moved, and there is no second parent waiting either.
	cEq(t, cSha(t, r, "HEAD"), head)
	if cExistsAt(r, ".git/MERGE_HEAD") || len(cStaged(t, r)) == 0 {
		t.Fatal("squash should stage without recording a merge")
	}
}

func TestCMergeNoCommit(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cCleanBranch(t)
	head := cSha(t, r, "HEAD")
	cNoErr(t, svc.Merge(ctx, r, "addition", cMergeOpts(func(o *MergeOptionsArg) { o.NoCommit = true })))
	cEq(t, cSha(t, r, "HEAD"), head)
	// Unlike squash, the merge is still in progress — the parent is remembered.
	if !cExistsAt(r, ".git/MERGE_HEAD") {
		t.Fatal("no MERGE_HEAD")
	}
}

func TestCMergeIgnoreSpaceChange(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	file := "spaced.txt"
	cWrite(t, r, file, "first line\n  indented line\nlast line\n")
	cCommitAll(t, r, "base")
	base := cSha(t, r, "HEAD")
	// One side re-indents the line; the other rewrites its text.
	cGit(t, r, "checkout", "-q", "-b", "reindent")
	cWrite(t, r, file, "first line\n\tindented line\nlast line\n")
	cCommitAll(t, r, "reindent")
	cGit(t, r, "checkout", "-q", "-b", "edit", base)
	cWrite(t, r, file, "first line\n  indented line, reworded\nlast line\n")
	cCommitAll(t, r, "reword")
	// Plain merge: git sees two edits to one line and stops.
	cMustErr(t, svc.Merge(ctx, r, "reindent", MergeOptionsArg{}), "")
	cGit(t, r, "merge", "--abort")
	cNoErr(t, svc.Merge(ctx, r, "reindent", cMergeOpts(func(o *MergeOptionsArg) { o.IgnoreSpace = "change" })))
	cEq(t, len(cConflicted(t, r)), 0)
	if !strings.Contains(cRead(t, r, file), "reworded") {
		t.Fatal("edit lost")
	}
}

func TestCConflictCommitsNamesBothSides(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "merge-conflict")
	cMustErr(t, svc.Merge(ctx, r, "feature", MergeOptionsArg{}), "")
	file := cConflicted(t, r)[0]
	commits, err := svc.ConflictCommits(ctx, r, file)
	cNoErr(t, err)
	ours, theirs := false, false
	for _, c := range commits {
		ours = ours || c.Side == "ours"
		theirs = theirs || c.Side == "theirs"
	}
	if len(commits) == 0 || !ours || !theirs || commits[0].Subject == "" || commits[0].Author == "" {
		t.Fatalf("commits %+v", commits)
	}
}

const cBisectTestCmd = `node -e "const {discount}=require('./discount'); process.exit(discount(100,20)===80?0:1)"`

func cBisectSeeded(t *testing.T, svc *GitService, ctx context.Context) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; the TS case's test command needs it")
	}
	r := testutil.CloneFixture(t, "bisect-bug")
	_, err := svc.BisectStart(ctx, r)
	cNoErr(t, err)
	_, err = svc.BisectMark(ctx, r, "bad", "")
	cNoErr(t, err)
	_, err = svc.BisectMark(ctx, r, "good", cGit(t, r, "log", "--format=%H", "-1", "--grep=feat: discount function"))
	cNoErr(t, err)
	return r
}

func TestCBisectRunFindsFirstBad(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cBisectSeeded(t, svc, ctx)
	culprit := cGit(t, r, "log", "--format=%H", "-1", "--grep=refactor: simplify discount calculation")
	// Exit 0 = good, non-zero = bad. That contract is the whole feature.
	st, err := svc.BisectRunScript(ctx, r, cBisectTestCmd)
	cNoErr(t, err)
	if !st.Finished || st.FirstBadSha != culprit || !strings.Contains(st.FirstBadSubject, "simplify discount") {
		t.Fatalf("status %+v", st)
	}
	cNoErr(t, svc.BisectReset(ctx, r))
	st, _ = svc.BisectStatus(ctx, r)
	if st.InProgress {
		t.Fatal("still bisecting")
	}
}

func TestCBisectRunStreamsOutput(t *testing.T) {
	cIsolate(t)
	em := &cgEmitter{}
	svc, ctx := New(em, nil), context.Background()
	r := cBisectSeeded(t, svc, ctx)
	_, err := svc.BisectRunScript(ctx, r, cBisectTestCmd)
	cNoErr(t, err)
	// git narrates each step; without that the UI cannot show progress.
	if !regexp.MustCompile(`Bisecting|first bad commit`).MatchString(strings.Join(em.events, "")) {
		t.Fatalf("events %q", em.events)
	}
	cNoErr(t, svc.BisectReset(ctx, r))
}

func TestCBisectRunRefusesUnseeded(t *testing.T) {
	svc, ctx := cNewSvc(t)
	_, err := svc.BisectRunScript(ctx, testutil.CloneFixture(t, "bisect-bug"), "true")
	cMustErr(t, err, `(?i)bisect`)
}

func TestCBisectRunRefusesEmptyCommand(t *testing.T) {
	svc, ctx := cNewSvc(t)
	_, err := svc.BisectRunScript(ctx, testutil.CloneFixture(t, "bisect-bug"), "   ")
	cMustErr(t, err, "")
}

func TestCBisectCancelNothingInFlight(t *testing.T) {
	svc, ctx := cNewSvc(t)
	ok, err := svc.BisectCancel(ctx, testutil.CloneFixture(t, "bisect-bug"))
	cNoErr(t, err)
	if ok {
		t.Fatal("nothing should have been cancelled")
	}
}

func TestCMergeUnionAttribute(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	file := "CHANGELOG.md"
	cWrite(t, r, file, "# Changelog\n")
	cWrite(t, r, ".gitattributes", "CHANGELOG.md merge=union\n")
	cCommitAll(t, r, "changelog")
	base := cSha(t, r, "HEAD")
	cGit(t, r, "checkout", "-q", "-b", "theirs")
	cWrite(t, r, file, "# Changelog\n- their entry\n")
	cCommitAll(t, r, "their entry")
	cGit(t, r, "checkout", "-q", "-b", "ours", base)
	cWrite(t, r, file, "# Changelog\n- our entry\n")
	cCommitAll(t, r, "our entry")
	// Both sides appended to the same region: a plain merge conflicts, union
	// keeps both. That is the entire point of the attribute.
	cNoErr(t, svc.Merge(ctx, r, "theirs", MergeOptionsArg{}))
	cEq(t, len(cConflicted(t, r)), 0)
	merged := cRead(t, r, file)
	if !strings.Contains(merged, "our entry") || !strings.Contains(merged, "their entry") {
		t.Fatalf("merged %q", merged)
	}
}

// cAheadFixture puts diverged-checkout's local feature strictly ahead of
// origin/feature.
func cAheadFixture(t *testing.T) string {
	r := testutil.CloneFixture(t, "diverged-checkout")
	cGit(t, r, "checkout", "-q", "feature")
	cGit(t, r, "reset", "-q", "--hard", "origin/feature")
	cWrite(t, r, "local-only.ts", "export const localOnly = 1\n")
	cGit(t, r, "add", "-A")
	cGit(t, r, "commit", "-qm", "feat: unpushed work")
	cGit(t, r, "checkout", "-q", "main")
	return r
}

func TestCCheckoutRemoteAheadOnly(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cAheadFixture(t)
	res, err := svc.CheckoutRemote(ctx, r, "origin/feature", "feature", "origin")
	cNoErr(t, err)
	cEq(t, res, CheckoutRemoteResult{Diverged: false, AheadOnly: true, Ahead: 1, Behind: 0})
	// Nothing moved: the user still has to say which side they meant.
	cEq(t, cCurrent(t, r), "main")
}

func TestCResolveDivergedResetMixed(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cAheadFixture(t)
	before := cSha(t, r, "feature")
	res, err := svc.ResolveDivergedCheckout(ctx, r, "origin/feature", "feature", "reset-mixed", true)
	cNoErr(t, err)
	cEq(t, res.PreviousRef, before)
	cEq(t, cSha(t, r, "feature"), cSha(t, r, "origin/feature"))
	// The backup branch still points at the commits the reset walked away from.
	if !strings.HasPrefix(res.BackupRef, "backup/feature-") {
		t.Fatalf("backup %q", res.BackupRef)
	}
	cEq(t, cSha(t, r, res.BackupRef), before)
	cEq(t, cCurrent(t, r), "feature")
	cEq(t, len(cStaged(t, r)), 0)
	if !cContains(cChanged(t, r), "local-only.ts") {
		t.Fatal("changes not kept in the working tree")
	}
}

func TestCResolveDivergedResetSoft(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cAheadFixture(t)
	_, err := svc.ResolveDivergedCheckout(ctx, r, "origin/feature", "feature", "reset-soft", false)
	cNoErr(t, err)
	cEq(t, cSha(t, r, "feature"), cSha(t, r, "origin/feature"))
	if !cContains(cStaged(t, r), "local-only.ts") {
		t.Fatal("changes not kept staged")
	}
}

func TestCResolveDivergedResetHard(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cAheadFixture(t)
	_, err := svc.ResolveDivergedCheckout(ctx, r, "origin/feature", "feature", "reset-hard", false)
	cNoErr(t, err)
	cEq(t, cSha(t, r, "feature"), cSha(t, r, "origin/feature"))
	if cExistsAt(r, "local-only.ts") || len(cChanged(t, r)) != 0 {
		t.Fatal("hard reset left changes behind")
	}
}

func TestCCheckoutRemoteStillFastForwards(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "diverged-checkout")
	cGit(t, r, "checkout", "-q", "feature")
	cGit(t, r, "reset", "-q", "--hard", "origin/feature~1")
	cGit(t, r, "checkout", "-q", "main")
	res, err := svc.CheckoutRemote(ctx, r, "origin/feature", "feature", "origin")
	cNoErr(t, err)
	if res.Diverged || res.AheadOnly || res.Behind != 1 {
		t.Fatalf("result %+v", res)
	}
	cEq(t, cSha(t, r, "feature"), cSha(t, r, "origin/feature"))
}

// The Xcode cases are about the pbxproj merger (src/shared/pbxproj.ts); the
// slice C part of them is the merge that has to stop on project.pbxproj.
func TestCXcodeMergeStopsOnProjectFile(t *testing.T) {
	const pbx = "Demo.xcodeproj/project.pbxproj"
	for _, branch := range []string{"feature", "bump"} {
		t.Run(branch, func(t *testing.T) {
			svc, ctx := cNewSvc(t)
			r := testutil.CloneFixture(t, "xcode-project")
			cMustErr(t, svc.Merge(ctx, r, branch, MergeOptionsArg{}), "")
			if !cContains(cConflicted(t, r), pbx) {
				t.Fatalf("conflicted %v", cConflicted(t, r))
			}
			cGit(t, r, "merge", "--abort")
		})
	}
}

// ─── newFeatures.test.ts ───────────────────────────────────────────────────

func TestCStackInfoReportsChain(t *testing.T) {
	svc, ctx := cNewSvc(t)
	info, err := svc.StackInfo(ctx, testutil.PlaygroundRepo(t, "stacked-branches"), "")
	cNoErr(t, err)
	cEq(t, info.Trunk, "main")
	cSliceEq(t, cStackNames(info), []string{"feature/api", "feature/ui"})
	for _, b := range info.Branches {
		if !b.NeedsRestack {
			t.Fatalf("%s should need a restack", b.Name)
		}
	}
}

func TestCStackRestackCascades(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cNoErr(t, svc.StackRestack(ctx, r, "feature/ui"))
	info, _ := svc.StackInfo(ctx, r, "feature/ui")
	for _, b := range info.Branches {
		if b.NeedsRestack {
			t.Fatalf("%s still needs a restack", b.Name)
		}
	}
	cEq(t, cCurrent(t, r), "feature/ui") // left on the leaf
}

func cMergeAPIIntoMain(t *testing.T, r string) {
	cGit(t, r, "checkout", "-q", "main")
	cGit(t, r, "merge", "-q", "--no-ff", "-m", "merge feature/api", "feature/api")
}

func TestCStackPruneMergedBottom(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches") // main ← feature/api ← feature/ui
	cMergeAPIIntoMain(t, r)
	cGit(t, r, "checkout", "-q", "feature/ui")
	pruned, err := svc.StackPruneMerged(ctx, r, nil, "")
	cNoErr(t, err)
	cSliceEq(t, pruned, []string{"feature/api"})
	info, _ := svc.StackInfo(ctx, r, "")
	cEq(t, info.Trunk, "main")
	cSliceEq(t, cStackNames(info), []string{"feature/ui"})
	cEq(t, *info.Branches[0].Parent, "main")
	cEq(t, cGit(t, r, "branch", "--list", "feature/api"), "")
}

func TestCStackPruneSquashMergedWithHostProof(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	// No local ancestry at all — the host-side proof arrives via alsoMerged.
	pruned, err := svc.StackPruneMerged(ctx, r, []string{"feature/api"}, "")
	cNoErr(t, err)
	cSliceEq(t, pruned, []string{"feature/api"})
	info, _ := svc.StackInfo(ctx, r, "")
	cSliceEq(t, cStackNames(info), []string{"feature/ui"})
	cEq(t, *info.Branches[0].Parent, "main")
}

func TestCStackPruneNoOp(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	pruned, err := svc.StackPruneMerged(ctx, r, nil, "")
	cNoErr(t, err)
	cEq(t, len(pruned), 0)
	info, _ := svc.StackInfo(ctx, r, "")
	cEq(t, len(info.Branches), 2)
}

func TestCRebaseOnto(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cNoErr(t, svc.RebaseOnto(ctx, r, "feature/api", "main"))
	cEq(t, cCurrent(t, r), "feature/api")
	if !strings.Contains(cGit(t, r, "log", "--format=%s"), "hotfix") {
		t.Fatal("main's hotfix not under feature/api")
	}
}

// cCommitSeries commits one new file per name with gitService.commit.
func cCommitSeries(t *testing.T, svc *GitService, ctx context.Context, r, prefix string, names ...string) {
	for _, n := range names {
		cWrite(t, r, prefix+n+".txt", n+"\n")
		cGit(t, r, "add", "-A")
		cNoErr(t, svc.Commit(ctx, r, "add "+n, false, false))
	}
}

func TestCSquashFoldsNewestRun(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "changelog")
	cCommitSeries(t, svc, ctx, r, "sq-", "a", "b", "c")
	before := cLines(cGit(t, r, "log", "--format=%H %s"))
	oldest := strings.Fields(before[1])[0] // squash the top two: c (HEAD) + b
	_, err := svc.SquashCommits(ctx, r, oldest, "squash b and c")
	cNoErr(t, err)
	after := cLines(cGit(t, r, "log", "--format=%s"))
	cEq(t, after[0], "squash b and c")
	cEq(t, len(after), len(before)-1)
	if !cContains(after, "add a") || !cExistsAt(r, "sq-b.txt") || !cExistsAt(r, "sq-c.txt") {
		t.Fatal("squash lost something")
	}
}

func TestCSquashIgnoresRejectingHook(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "changelog")
	cCommitSeries(t, svc, ctx, r, "hk-", "a", "b")
	headBefore, oldest := cSha(t, r, "HEAD"), cSha(t, r, "HEAD~1")
	if err := os.WriteFile(filepath.Join(r, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := svc.SquashCommits(ctx, r, oldest, "squash a and b")
	cNoErr(t, err)
	cEq(t, res.Before, headBefore)
	cEq(t, cGit(t, r, "log", "-1", "--format=%s"), "squash a and b")
	cEq(t, cSha(t, r, "HEAD"), res.After)
	cEq(t, cSha(t, r, "HEAD~1"), cSha(t, r, oldest+"^"))
}

func TestCSquashLeavesStagedChangesStaged(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "changelog")
	cCommitSeries(t, svc, ctx, r, "st-", "a", "b")
	oldest := cSha(t, r, "HEAD~1")
	cWrite(t, r, "st-pending.txt", "pending\n")
	cGit(t, r, "add", "st-pending.txt")
	_, err := svc.SquashCommits(ctx, r, oldest, "squash a and b")
	cNoErr(t, err)
	inCommit := cGit(t, r, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(inCommit, "st-a.txt") || strings.Contains(inCommit, "st-pending.txt") {
		t.Fatalf("commit holds %q", inCommit)
	}
	cEq(t, cGit(t, r, "diff", "--cached", "--name-only"), "st-pending.txt")
}

func TestCDeleteRemoteBranchPrunesStaleRef(t *testing.T) {
	svc, ctx := cNewSvc(t)
	remote, work := t.TempDir(), t.TempDir()
	cGit(t, remote, "init", "-q", "--bare")
	cGit(t, work, "init", "-q", "-b", "main")
	cGit(t, work, "config", "user.email", "test@example.com")
	cGit(t, work, "config", "user.name", "Test")
	cGit(t, work, "remote", "add", "origin", remote)
	cWrite(t, work, "a.txt", "a\n")
	cGit(t, work, "add", "-A")
	cGit(t, work, "commit", "-q", "-m", "init")
	cGit(t, work, "branch", "gone-branch")
	cGit(t, work, "push", "-q", "-u", "origin", "main", "gone-branch")
	if cGitTry(work, "rev-parse", "--verify", "refs/remotes/origin/gone-branch") == "" {
		t.Fatal("no tracking ref")
	}
	// Delete it directly on the remote, leaving our tracking ref stale.
	cGit(t, remote, "update-ref", "-d", "refs/heads/gone-branch")
	cNoErr(t, svc.DeleteRemoteBranch(ctx, work, "origin", "gone-branch"))
	if cGitTry(work, "rev-parse", "--verify", "refs/remotes/origin/gone-branch") != "" {
		t.Fatal("stale tracking ref survived")
	}
}

func TestCTreeStatusCollapsesIgnoredDirs(t *testing.T) {
	svc, ctx := cNewSvc(t)
	st, err := svc.TreeStatus(ctx, testutil.PlaygroundRepo(t, "untracked-mess"))
	cNoErr(t, err)
	// The whole point of --ignored=matching: node_modules is one entry.
	cEq(t, st["node_modules"], "ignored")
	cEq(t, st["dist"], "ignored")
	for _, p := range []string{"node_modules/left-pad/index.js", "dist/assets/app.css"} {
		if _, ok := st[p]; ok {
			t.Fatalf("%s should be collapsed", p)
		}
	}
	// Ignored files not inside an ignored directory still appear by name.
	cEq(t, st[".env"], "ignored")
	cEq(t, st["app.log"], "ignored")
}

func TestCTreeStatusNamesUntrackedFiles(t *testing.T) {
	svc, ctx := cNewSvc(t)
	st, err := svc.TreeStatus(ctx, testutil.PlaygroundRepo(t, "untracked-mess"))
	cNoErr(t, err)
	cEq(t, st["notes.md"], "untracked")
	cEq(t, st["tmp/cache/blob.txt"], "untracked")
	// Untracked propagates up to its folders; ignored deliberately does not.
	cEq(t, st["tmp"], "untracked")
	if _, ok := st["node_modules/left-pad"]; ok {
		t.Fatal("ignored propagated")
	}
}

func TestCStackInsertInTheMiddle(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cNoErr(t, svc.StackInsert(ctx, r, "feature/mid", "feature/api"))
	info, _ := svc.StackInfo(ctx, r, "feature/ui")
	cSliceEq(t, cStackNames(info), []string{"feature/api", "feature/mid", "feature/ui"})
	cEq(t, info.Trunk, "main")
	// The new level sits at its parent's tip, so it is already in sync.
	cEq(t, info.Branches[1].NeedsRestack, false)
	cEq(t, cCurrent(t, r), "feature/mid") // `gs add` leaves you on the new level
}

func TestCStackReorderSwaps(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cNoErr(t, svc.StackReorder(ctx, r, "main", []string{"feature/ui", "feature/api"}))
	info, _ := svc.StackInfo(ctx, r, "feature/api")
	cSliceEq(t, cStackNames(info), []string{"feature/ui", "feature/api"})
	cEq(t, *info.Branches[0].Parent, "main")
	cEq(t, *info.Branches[1].Parent, "feature/ui")
	for _, b := range info.Branches {
		// Each level keeps its own commits — a swap moves bases, not authorship.
		if b.NeedsRestack || b.Ahead == 0 {
			t.Fatalf("level %+v", b)
		}
	}
}

func TestCStackReorderNewTrunk(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cGit(t, r, "branch", "release", "main")
	cNoErr(t, svc.StackReorder(ctx, r, "release", []string{"feature/api", "feature/ui"}))
	info, _ := svc.StackInfo(ctx, r, "feature/ui")
	cEq(t, info.Trunk, "release")
	cEq(t, *info.Branches[0].Parent, "release")
}

func TestCStackRouteDropsStop(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cNoErr(t, svc.StackSetRoute(ctx, r, "main", []string{"feature/ui"}))
	info, _ := svc.StackInfo(ctx, r, "feature/ui")
	cSliceEq(t, cStackNames(info), []string{"feature/ui"})
	cEq(t, *info.Branches[0].Parent, "main")
	// Taken off the route, not deleted.
	if !strings.Contains(cGit(t, r, "branch", "--list", "feature/api"), "feature/api") {
		t.Fatal("dropped stop was deleted")
	}
}

func TestCStackRouteSwapsBranch(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cGit(t, r, "branch", "feature/docs", "main")
	cNoErr(t, svc.StackSetRoute(ctx, r, "main", []string{"feature/docs", "feature/ui"}))
	info, _ := svc.StackInfo(ctx, r, "feature/ui")
	cSliceEq(t, cStackNames(info), []string{"feature/docs", "feature/ui"})
	if strings.Contains(cGit(t, r, "config", "--get-regexp", "gitcitoparent"), "branch.feature/api.") {
		t.Fatal("the stop that left is still tracked")
	}
}

func TestCStackRouteRefusesTrunkAsStop(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cMustErr(t, svc.StackSetRoute(ctx, r, "main", []string{"main", "feature/ui"}), `(?i)where the stack lands`)
}

func TestCStackRouteRefusesProtected(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cGit(t, r, "branch", "release", "main")
	cGit(t, r, "config", "gitcito.protectedbranches", "main,release")
	cMustErr(t, svc.StackSetRoute(ctx, r, "feature/api", []string{"release"}), `(?i)protected`)
	cEq(t, cSha(t, r, "release"), cSha(t, r, "main")) // nothing moved
}

// cConflictingStack is two stops appending to one file: swapping them
// cannot replay.
func cConflictingStack(t *testing.T) string {
	r := testutil.CloneFixture(t, "stacked-branches")
	cGit(t, r, "checkout", "-q", "main")
	cGit(t, r, "checkout", "-q", "-b", "note-1")
	cWrite(t, r, "NOTES.md", "one\n")
	cGit(t, r, "add", "NOTES.md")
	cGit(t, r, "commit", "-q", "-m", "note one")
	cGit(t, r, "checkout", "-q", "-b", "note-2")
	cWrite(t, r, "NOTES.md", "one\ntwo\n")
	cGit(t, r, "add", "NOTES.md")
	cGit(t, r, "commit", "-q", "-m", "note two")
	cGit(t, r, "config", "branch.note-1.gitcitoparent", "main")
	cGit(t, r, "config", "branch.note-2.gitcitoparent", "note-1")
	return r
}

func TestCStackRouteConflictRollsBack(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cConflictingStack(t)
	one, two, head := cSha(t, r, "note-1"), cSha(t, r, "note-2"), cSha(t, r, "HEAD")
	cMustErr(t, svc.StackSetRoute(ctx, r, "main", []string{"note-2", "note-1"}), "ROUTE_CONFLICT")
	cEq(t, cSha(t, r, "note-1"), one)
	cEq(t, cSha(t, r, "note-2"), two)
	cEq(t, cSha(t, r, "HEAD"), head)
	// No half-finished rebase left behind, and the links still describe the old route.
	if cExistsAt(r, ".git/rebase-merge") || cExistsAt(r, ".git/rebase-apply") {
		t.Fatal("rebase left behind")
	}
	info, _ := svc.StackInfo(ctx, r, "note-2")
	cSliceEq(t, cStackNames(info), []string{"note-1", "note-2"})
	cEq(t, info.Trunk, "main")
}

func TestCStackRouteConflictNamesBothBranches(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := cConflictingStack(t)
	cMustErr(t, svc.StackSetRoute(ctx, r, "main", []string{"note-2", "note-1"}), `GITCITO_ROUTE_CONFLICT:note-1:note-2`)
}

func TestCStackRestackRefusesProtected(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	// A stack whose bottom level is main itself — the shape that rewrote main.
	cGit(t, r, "config", "branch.main.gitcitoparent", "feature/api")
	before := cSha(t, r, "main")
	cMustErr(t, svc.StackRestack(ctx, r, "main"), `(?i)protected`)
	cEq(t, cSha(t, r, "main"), before)
}

func TestCStackInfoNoStack(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cGit(t, r, "checkout", "-q", "main")
	info, err := svc.StackInfo(ctx, r, "")
	cNoErr(t, err)
	if len(info.Branches) != 0 || info.Trunk != "" || info.Branches == nil {
		t.Fatalf("info %+v", info)
	}
}

func TestCStackInfoByLeafFromAnywhere(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cGit(t, r, "checkout", "-q", "main")
	info, _ := svc.StackInfo(ctx, r, "feature/ui")
	cSliceEq(t, cStackNames(info), []string{"feature/api", "feature/ui"})
	cEq(t, info.Trunk, "main")
}

func TestCStackLeavesListsEachLeaf(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cGit(t, r, "branch", "docs-1", "main")
	cGit(t, r, "config", "branch.docs-1.gitcitoparent", "main")
	leaves, err := svc.StackLeaves(ctx, r)
	cNoErr(t, err)
	cSliceEq(t, leaves, []string{"docs-1", "feature/ui"})
}

func TestCStackLeavesNone(t *testing.T) {
	svc, ctx := cNewSvc(t)
	leaves, err := svc.StackLeaves(ctx, testutil.CloneFixture(t, "merge-conflict"))
	cNoErr(t, err)
	if leaves == nil || len(leaves) != 0 {
		t.Fatalf("leaves %v", leaves)
	}
}

func TestCStackPruneByLeafFromAnywhere(t *testing.T) {
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "stacked-branches")
	cMergeAPIIntoMain(t, r)
	// Standing on main, which is on no stack: without the leaf there is
	// nothing to prune, with it the merged bottom goes.
	pruned, _ := svc.StackPruneMerged(ctx, r, nil, "")
	cEq(t, len(pruned), 0)
	pruned, err := svc.StackPruneMerged(ctx, r, []string{}, "feature/ui")
	cNoErr(t, err)
	cSliceEq(t, pruned, []string{"feature/api"})
	info, _ := svc.StackInfo(ctx, r, "feature/ui")
	cSliceEq(t, cStackNames(info), []string{"feature/ui"})
	cEq(t, *info.Branches[0].Parent, "main")
}

// ─── repoChat.test.ts ──────────────────────────────────────────────────────

// The TS case asks the checkout itself. A worktree whose node_modules is a
// symlink makes git refuse the pathspec ("beyond a symbolic link") in both
// languages, so this runs against a fresh repository carrying the project's
// .gitignore instead — same rule, same answer.
func TestCIgnoredTrackedFilesHonoursIgnoreRules(t *testing.T) {
	svc, ctx := cNewSvc(t)
	repo := testutil.InitRepo(t)
	gitignore, err := os.ReadFile(filepath.Join(testutil.MustRepoRoot(t), ".gitignore"))
	cNoErr(t, err)
	cWrite(t, repo, ".gitignore", string(gitignore))
	cWrite(t, repo, "src/main/index.ts", "export {}\n")
	cGit(t, repo, "add", "-A")
	ignored, err := svc.IgnoredTrackedFiles(ctx, repo, []string{"node_modules/example.js", "src/main/index.ts"})
	cNoErr(t, err)
	cSliceEq(t, ignored, []string{"node_modules/example.js"})
}

// ─── Go-only checks ────────────────────────────────────────────────────────

func TestCMergeOptionsArgDecodesBooleanOrObject(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`true`, []string{"--no-ff"}},
		{`false`, []string{}},
		{`null`, []string{}},
		{`{"ffOnly":true,"noFf":true,"favour":"theirs","ignoreSpace":"eol"}`, []string{"--ff-only", "-X", "theirs", "-X", "ignore-space-at-eol"}},
	} {
		var o MergeOptionsArg
		cNoErr(t, json.Unmarshal([]byte(tc.in), &o))
		got := gitx.MergeArgs(o.MergeOptions)
		cSliceEq(t, got, tc.want)
	}
}

func TestCLocaleCompareOrdersLikeICU(t *testing.T) {
	in := []string{"feature/ui", "Zeta", "alpha", "docs-1", "Alpha", "a_b", "a-b", "a1"}
	want := []string{"a_b", "a-b", "a1", "alpha", "Alpha", "docs-1", "feature/ui", "Zeta"}
	got := append([]string{}, in...)
	sort.SliceStable(got, func(i, j int) bool { return cLocaleCompare(got[i], got[j]) < 0 })
	cSliceEq(t, got, want)
}

func TestCJSReplaceFirstExpandsDollarPatterns(t *testing.T) {
	cEq(t, cJSReplaceFirst("x # BugShop y", "# BugShop", "[$&|$$|$`|$'|$1]"), "x [# BugShop|$|x | y|$1] y")
	cEq(t, cJSReplaceFirst("aaa", "a", "b"), "baa")
	cEq(t, cJSReplaceFirst("abc", "z", "b"), "abc")
}

func TestCBisectCancelStopsARun(t *testing.T) {
	// The cancel path itself, without the lock: TS queued bisectCancel behind
	// the run it meant to stop (both are writes), and so does the port.
	cIsolate(t)
	r := testutil.CloneFixture(t, "bisect-bug")
	svc, ctx := New(nil, nil), context.Background()
	_, _ = svc.BisectStart(ctx, r)
	_, _ = svc.BisectMark(ctx, r, "bad", "")
	_, _ = svc.BisectMark(ctx, r, "good", ":/feat: discount function")
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := svc.cBisectRun(ctx, r, "sleep 30", func(string) {
			select {
			case <-started:
			default:
				close(started)
			}
		})
		done <- err
	}()
	<-started
	if !cBisectCancel(r) {
		t.Fatal("no run to cancel")
	}
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if cBisectCancel(r) {
		t.Fatal("cancelled twice")
	}
}

func TestCFsDeleteGoesToTheTrash(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checks the freedesktop.org trash layout")
	}
	svc, ctx := cNewSvc(t)
	r := testutil.CloneFixture(t, "tree-dnd")
	cNoErr(t, svc.FsDelete(ctx, r, []string{"loose.txt", "docs"}))
	if cExistsAt(r, "loose.txt") || cExistsAt(r, "docs") {
		t.Fatal("not removed from the repository")
	}
	trash := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Trash")
	// Recoverable: the file is in the trash, with the .trashinfo a desktop
	// trash can reads to restore it where it was.
	cEq(t, cRead(t, trash, "files/docs/readme.md"), "# Docs\n")
	info := cRead(t, trash, "info/loose.txt.trashinfo")
	if !strings.Contains(info, "Path="+filepath.Join(r, "loose.txt")) || !strings.Contains(info, "DeletionDate=") {
		t.Fatalf("trashinfo %q", info)
	}
	// A second item of the same name does not overwrite the first.
	cWrite(t, r, "loose.txt", "again\n")
	cNoErr(t, svc.FsDelete(ctx, r, []string{"loose.txt"}))
	cEq(t, cRead(t, trash, "files/loose.txt.2"), "again\n")
	cMustErr(t, svc.FsDelete(ctx, r, []string{"missing.txt"}), "ENOENT")
}
