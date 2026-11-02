package gitsvc

// The vitest cases that covered slice B methods, ported. Each Go test names
// the vitest case it replaces (see the slice report's TEST-MAP fragment).
// Assertions that exercise renderer logic (commitMenuCapabilities,
// githubCommitUrl) or other slices' methods stay in vitest; where a TS test
// used another slice's method only to set up or inspect state, plain git
// stands in for it here.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

func bSvc() (*GitService, context.Context) { return New(nil, nil), context.Background() }

// bGitTry runs git and reports whether it succeeded (conflicts are expected
// in some setups).
func bGitTry(dir string, args ...string) bool {
	_, code, _ := bGitRun(dir, args...)
	return code == 0
}

// ─── commitMenu.test.ts ────────────────────────────────────────────────────

func bInitMenuRepo(t *testing.T) string {
	t.Helper()
	testutil.IsolateGitEnv(t)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "init", "-q", "-b", "main")
	testutil.Git(t, repo, "config", "user.name", "Gitcito Test")
	testutil.Git(t, repo, "config", "user.email", "gitcito@example.invalid")
	testutil.Git(t, repo, "config", "commit.gpgsign", "false")
	testutil.Git(t, repo, "config", "core.autocrlf", "false")
	return repo
}

func bCommitFile(t *testing.T, repo, file, content, message string) string {
	t.Helper()
	testutil.WriteFile(t, repo, file, content)
	testutil.Git(t, repo, "add", file)
	testutil.Git(t, repo, "commit", "-qm", message)
	return testutil.Git(t, repo, "rev-parse", "HEAD")
}

func bAttachBareOrigin(t *testing.T, repo string) string {
	t.Helper()
	bare := filepath.Join(filepath.Dir(repo), "origin.git")
	testutil.Git(t, filepath.Dir(repo), "init", "-q", "--bare", bare)
	testutil.Git(t, repo, "remote", "add", "origin", bare)
	testutil.Git(t, repo, "push", "-q", "-u", "origin", "main")
	return bare
}

func bProbe(t *testing.T, repo, sha string) CommitMenuProbe {
	t.Helper()
	s, ctx := bSvc()
	p, err := s.CommitMenuProbe(ctx, repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBCommitMenuClassification(t *testing.T) {
	if !IsReadMethod("commitMenuProbe") || IsReadMethod("undoCommit") || IsReadMethod("restoreUndoneCommit") {
		t.Fatal("commitMenuProbe must be a read; undoCommit and restoreUndoneCommit writes")
	}
}

func TestBCommitMenuUnpushedHead(t *testing.T) {
	repo := bInitMenuRepo(t)
	head := bCommitFile(t, repo, "a.txt", "a\n", "first")
	p := bProbe(t, repo, head)
	if !p.IsHead || !p.IsOnLocalBranch || p.IsPublished || p.IsWithinResetBoundary {
		t.Fatalf("probe = %+v", p)
	}
}

func TestBCommitMenuPushedHead(t *testing.T) {
	repo := bInitMenuRepo(t)
	head := bCommitFile(t, repo, "a.txt", "a\n", "first")
	bAttachBareOrigin(t, repo)
	testutil.Git(t, repo, "remote", "set-url", "origin", "https://github.com/acme/app.git")
	p := bProbe(t, repo, head)
	if !p.IsPublished || !p.IsHead || p.IsWithinResetBoundary {
		t.Fatalf("probe = %+v", p)
	}
}

func TestBCommitMenuResetBoundary(t *testing.T) {
	repo := bInitMenuRepo(t)
	base := bCommitFile(t, repo, "a.txt", "base\n", "base")
	bAttachBareOrigin(t, repo)
	local1 := bCommitFile(t, repo, "a.txt", "local1\n", "local one")
	head := bCommitFile(t, repo, "a.txt", "local2\n", "local two")
	baseP, localP, headP := bProbe(t, repo, base), bProbe(t, repo, local1), bProbe(t, repo, head)
	if !headP.IsHead || headP.IsWithinResetBoundary {
		t.Errorf("head = %+v", headP)
	}
	if localP.IsPublished || !localP.IsAncestorOfHead || !localP.IsWithinResetBoundary {
		t.Errorf("local = %+v", localP)
	}
	if !baseP.IsPublished || !baseP.IsWithinResetBoundary {
		t.Errorf("base = %+v", baseP)
	}
}

func TestBCommitMenuBeyondFirstPublished(t *testing.T) {
	repo := bInitMenuRepo(t)
	ancient := bCommitFile(t, repo, "a.txt", "0\n", "ancient")
	base := bCommitFile(t, repo, "a.txt", "1\n", "base")
	bAttachBareOrigin(t, repo)
	bCommitFile(t, repo, "a.txt", "2\n", "local")
	ancientP, baseP := bProbe(t, repo, ancient), bProbe(t, repo, base)
	if !baseP.IsPublished || !baseP.IsWithinResetBoundary {
		t.Errorf("base = %+v", baseP)
	}
	if !ancientP.IsPublished || !ancientP.IsAncestorOfHead || ancientP.IsWithinResetBoundary {
		t.Errorf("ancient = %+v", ancientP)
	}
}

func TestBCommitMenuTagDoesNotMakeResettable(t *testing.T) {
	repo := bInitMenuRepo(t)
	ancient := bCommitFile(t, repo, "a.txt", "0\n", "ancient")
	bCommitFile(t, repo, "a.txt", "1\n", "base")
	bAttachBareOrigin(t, repo)
	testutil.Git(t, repo, "tag", "v1", ancient)
	if p := bProbe(t, repo, ancient); !p.IsPublished || p.IsWithinResetBoundary {
		t.Fatalf("probe = %+v", p)
	}
}

func TestBUndoRootCommitLeavesUnbornBranch(t *testing.T) {
	repo := bInitMenuRepo(t)
	bCommitFile(t, repo, "a.txt", "hello\n", "initial\n\nThe body.")
	testutil.WriteFile(t, repo, "wip.txt", "keep me\n")
	s, ctx := bSvc()
	res, err := s.UndoCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !res.WasRoot || !strings.Contains(res.Message, "initial") || res.ParentSha != nil {
		t.Fatalf("result = %+v", res)
	}
	if got := testutil.Git(t, repo, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD = %s", got)
	}
	files := testutil.Git(t, repo, "status", "--porcelain", "-uall")
	if !strings.Contains(files, "a.txt") || !strings.Contains(files, "wip.txt") {
		t.Fatalf("status = %q", files)
	}
	if bGitTry(repo, "rev-parse", "HEAD") {
		t.Fatal("HEAD still resolves after undoing the root commit")
	}
	if err := s.RestoreUndoneCommit(ctx, repo, res.PreviousSha); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo, "rev-parse", "HEAD"); got != res.PreviousSha {
		t.Fatalf("HEAD = %s, want %s", got, res.PreviousSha)
	}
	// The index is realigned with the restored HEAD: only the WIP file is
	// left over, and nothing shows as a staged reversal of the commit.
	if got := testutil.Git(t, repo, "status", "--porcelain", "-uall"); got != "?? wip.txt" {
		t.Fatalf("status = %q", got)
	}
}

func TestBUndoNonRootIsMixedReset(t *testing.T) {
	repo := bInitMenuRepo(t)
	parent := bCommitFile(t, repo, "a.txt", "one\n", "first")
	bCommitFile(t, repo, "a.txt", "two\n", "second\n\nBody line.")
	testutil.WriteFile(t, repo, "a.txt", "two\nand wip\n")
	testutil.WriteFile(t, repo, "extra.txt", "untracked\n")
	s, ctx := bSvc()
	res, err := s.UndoCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.WasRoot || !strings.Contains(res.Message, "second") || res.ParentSha == nil || *res.ParentSha != parent {
		t.Fatalf("result = %+v", res)
	}
	if got := testutil.Git(t, repo, "rev-parse", "HEAD"); got != parent {
		t.Fatalf("HEAD = %s", got)
	}
	porcelain := testutil.Git(t, repo, "status", "--porcelain", "-uall")
	if !strings.Contains(porcelain, "a.txt") || !strings.Contains(porcelain, "extra.txt") {
		t.Fatalf("status = %q", porcelain)
	}
	if got := testutil.Git(t, repo, "show", ":a.txt"); got != "one" {
		t.Fatalf("index a.txt = %q", got)
	}
}

func TestBCommitMenuDetachedHead(t *testing.T) {
	repo := bInitMenuRepo(t)
	head := bCommitFile(t, repo, "a.txt", "a\n", "first")
	testutil.Git(t, repo, "checkout", "--detach", "HEAD")
	if p := bProbe(t, repo, head); p.IsOnLocalBranch || !p.IsHead || p.Branch != "" {
		t.Fatalf("probe = %+v", p)
	}
}

func TestBCommitMenuNoRemote(t *testing.T) {
	repo := bInitMenuRepo(t)
	head := bCommitFile(t, repo, "a.txt", "a\n", "first")
	if p := bProbe(t, repo, head); p.IsPublished {
		t.Fatalf("probe = %+v", p)
	}
}

func TestBCommitMenuNonGitHubRemote(t *testing.T) {
	repo := bInitMenuRepo(t)
	head := bCommitFile(t, repo, "a.txt", "a\n", "first")
	bAttachBareOrigin(t, repo)
	testutil.Git(t, repo, "remote", "set-url", "origin", "https://gitlab.com/acme/app.git")
	if p := bProbe(t, repo, head); !p.IsPublished {
		t.Fatalf("probe = %+v", p)
	}
}

func TestBCommitMenuDirtyTreeDoesNotChangeFlags(t *testing.T) {
	repo := bInitMenuRepo(t)
	parent := bCommitFile(t, repo, "a.txt", "a\n", "first")
	head := bCommitFile(t, repo, "a.txt", "b\n", "second")
	testutil.WriteFile(t, repo, "a.txt", "dirty\n")
	testutil.WriteFile(t, repo, "staged.txt", "s\n")
	testutil.Git(t, repo, "add", "staged.txt")
	if p := bProbe(t, repo, head); !p.IsHead {
		t.Errorf("head = %+v", p)
	}
	if p := bProbe(t, repo, parent); !p.IsWithinResetBoundary {
		t.Errorf("parent = %+v", p)
	}
}

func bDivergedMenuRepo(t *testing.T) string {
	repo := bInitMenuRepo(t)
	bCommitFile(t, repo, "a.txt", "main\n", "main")
	testutil.Git(t, repo, "checkout", "-q", "-b", "feature")
	bCommitFile(t, repo, "a.txt", "feature\n", "feature")
	testutil.Git(t, repo, "checkout", "-q", "main")
	bCommitFile(t, repo, "a.txt", "other\n", "other")
	return repo
}

func TestBCommitMenuOperationInProgress(t *testing.T) {
	for _, op := range [][]string{{"merge", "--no-commit", "feature"}, {"cherry-pick", "feature"}, {"rebase", "feature"}} {
		t.Run(op[0], func(t *testing.T) {
			repo := bDivergedMenuRepo(t)
			bGitTry(repo, op...) // the conflict is expected
			if p := bProbe(t, repo, testutil.Git(t, repo, "rev-parse", "HEAD")); !p.OperationInProgress {
				t.Fatalf("probe = %+v", p)
			}
		})
	}
}

func TestBCommitMenuProbeIsQuietWithoutOperation(t *testing.T) {
	repo := bInitMenuRepo(t)
	head := bCommitFile(t, repo, "a.txt", "a\n", "first")
	if p := bProbe(t, repo, head); p.OperationInProgress {
		t.Fatalf("probe = %+v", p)
	}
}

func TestBResetMixedKeepsUncommittedWork(t *testing.T) {
	repo := bInitMenuRepo(t)
	target := bCommitFile(t, repo, "a.txt", "one\n", "one")
	bCommitFile(t, repo, "a.txt", "two\n", "two")
	testutil.WriteFile(t, repo, "a.txt", "two plus wip\n")
	s, ctx := bSvc()
	if err := s.Reset(ctx, repo, target, "mixed"); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo, "rev-parse", "HEAD"); got != target {
		t.Fatalf("HEAD = %s", got)
	}
	if testutil.Git(t, repo, "status", "--porcelain") == "" {
		t.Fatal("the working tree lost its changes")
	}
}

// ─── prPreview.test.ts ─────────────────────────────────────────────────────

func TestBPrRefCandidatesOrder(t *testing.T) {
	gh := bPrRefCandidates(7, "https://github.com/owner/repo.git")
	if len(gh) != 4 || gh[0] != (PrRefCandidate{Flavor: "github", Ref: "refs/pull/7/head"}) {
		t.Fatalf("github = %+v", gh)
	}
	gl := bPrRefCandidates(7, "git@gitlab.example.com:group/repo.git")
	if gl[0] != (PrRefCandidate{Flavor: "gitlab", Ref: "refs/merge-requests/7/head"}) {
		t.Fatalf("gitlab = %+v", gl)
	}
	found := false
	for _, c := range gl {
		found = found || c.Ref == "refs/pull/7/head"
	}
	if !found {
		t.Fatal("a vanity domain must not rule a convention out")
	}
	var flavors []string
	for _, c := range bPrRefCandidates(7, "https://git.example.net/repo.git") {
		flavors = append(flavors, c.Flavor)
	}
	if !reflect.DeepEqual(flavors, []string{"github", "gitlab", "bitbucket", "azure"}) {
		t.Fatalf("default order = %v", flavors)
	}
	if bFlavorForRemoteURL("") != "" {
		t.Fatal("no URL, no flavor")
	}
}

type bPrSetup struct{ upstream, local, featureSha, cleanSha string }

// bUpstreamWithPrRefs is upstreamWithPrRefs(): another local repository with
// PR refs written into it by hand — exactly the shape a forge serves.
func bUpstreamWithPrRefs(t *testing.T) bPrSetup {
	t.Helper()
	testutil.IsolateGitEnv(t)
	up := testutil.CloneFixture(t, "merge-conflict")
	featureSha := testutil.Git(t, up, "rev-parse", "feature")
	testutil.Git(t, up, "checkout", "-b", "tidy", "main")
	testutil.WriteFile(t, up, "NOTES.md", "from the pull request\n")
	testutil.Git(t, up, "add", "NOTES.md")
	testutil.Git(t, up, "commit", "-m", "docs: add notes")
	cleanSha := testutil.Git(t, up, "rev-parse", "HEAD")
	testutil.Git(t, up, "checkout", "main")
	testutil.Git(t, up, "update-ref", "refs/pull/7/head", featureSha)
	testutil.Git(t, up, "update-ref", "refs/merge-requests/3/head", cleanSha)
	local := testutil.CloneFixture(t, "merge-conflict")
	testutil.Git(t, local, "remote", "add", "up", up)
	return bPrSetup{up, local, featureSha, cleanSha}
}

func TestBResolvePrRefFindsFlavor(t *testing.T) {
	p := bUpstreamWithPrRefs(t)
	s, ctx := bSvc()
	gh, err := s.ResolvePrRef(ctx, p.local, "up", 7)
	if err != nil || gh == nil || *gh != (PrRefProbe{Flavor: "github", Ref: "refs/pull/7/head", Sha: p.featureSha}) {
		t.Fatalf("github = %+v, %v", gh, err)
	}
	// The same probe finds a GitLab-style ref on the same remote — the
	// convention is discovered, not assumed from the URL.
	gl, err := s.ResolvePrRef(ctx, p.local, "up", 3)
	if err != nil || gl == nil || *gl != (PrRefProbe{Flavor: "gitlab", Ref: "refs/merge-requests/3/head", Sha: p.cleanSha}) {
		t.Fatalf("gitlab = %+v, %v", gl, err)
	}
}

func TestBResolvePrRefNone(t *testing.T) {
	p := bUpstreamWithPrRefs(t)
	s, ctx := bSvc()
	if got, err := s.ResolvePrRef(ctx, p.local, "up", 999); err != nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestBPreviewRefCheckout(t *testing.T) {
	p := bUpstreamWithPrRefs(t)
	s, ctx := bSvc()
	before := testutil.Git(t, p.local, "rev-parse", "main")
	res, err := s.PreviewRef(ctx, p.local, "up", "refs/pull/7/head", "checkout", "pr/7")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "checkout" || res.LocalBranch != "pr/7" || res.Sha != p.featureSha || len(res.Conflicts) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := testutil.Git(t, p.local, "branch", "--show-current"); got != "pr/7" {
		t.Fatalf("current = %s", got)
	}
	// main is untouched and nothing was committed on top of the fetched head.
	if testutil.Git(t, p.local, "rev-parse", "main") != before || testutil.Git(t, p.local, "rev-parse", "HEAD") != p.featureSha {
		t.Fatal("checkout preview moved something it should not have")
	}
}

func TestBPreviewRefReusesBranch(t *testing.T) {
	p := bUpstreamWithPrRefs(t)
	s, ctx := bSvc()
	if _, err := s.PreviewRef(ctx, p.local, "up", "refs/merge-requests/3/head", "checkout", "pr/7"); err != nil {
		t.Fatal(err)
	}
	if testutil.Git(t, p.local, "rev-parse", "pr/7") != p.cleanSha {
		t.Fatal("first preview not on pr/7")
	}
	if _, err := s.PreviewRef(ctx, p.local, "up", "refs/pull/7/head", "checkout", "pr/7"); err != nil {
		t.Fatal(err)
	}
	if testutil.Git(t, p.local, "rev-parse", "pr/7") != p.featureSha {
		t.Fatal("second preview did not move pr/7")
	}
}

func TestBPreviewRefMergeClean(t *testing.T) {
	p := bUpstreamWithPrRefs(t)
	s, ctx := bSvc()
	head := testutil.Git(t, p.local, "rev-parse", "HEAD")
	res, err := s.PreviewRef(ctx, p.local, "up", "refs/merge-requests/3/head", "merge", "")
	if err != nil || len(res.Conflicts) != 0 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	// The tree is merged, but HEAD has not moved — that is what makes it a preview.
	if testutil.Git(t, p.local, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved")
	}
	if st, _ := bMergeState(ctx, p.local); st != "merge" {
		t.Fatalf("merge state = %q", st)
	}
	if !strings.Contains(testutil.Git(t, p.local, "diff", "--cached", "--name-only"), "NOTES.md") {
		t.Fatal("NOTES.md not staged")
	}
	testutil.Git(t, p.local, "merge", "--abort")
	if st, _ := bMergeState(ctx, p.local); st != "" {
		t.Fatalf("merge state after abort = %q", st)
	}
}

func TestBPreviewRefMergeConflicts(t *testing.T) {
	p := bUpstreamWithPrRefs(t)
	s, ctx := bSvc()
	res, err := s.PreviewRef(ctx, p.local, "up", "refs/pull/7/head", "merge", "")
	if err != nil || len(res.Conflicts) == 0 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if st, _ := bMergeState(ctx, p.local); st != "merge" {
		t.Fatalf("merge state = %q", st)
	}
	testutil.Git(t, p.local, "merge", "--abort")
	if st, _ := bMergeState(ctx, p.local); st != "" {
		t.Fatalf("merge state after abort = %q", st)
	}
}

func TestBPreviewRefCheckoutNeedsBranch(t *testing.T) {
	p := bUpstreamWithPrRefs(t)
	s, ctx := bSvc()
	if _, err := s.PreviewRef(ctx, p.local, "up", "refs/pull/7/head", "checkout", ""); err == nil {
		t.Fatal("a checkout preview with no branch must fail")
	}
}

// ─── gitOps.test.ts ────────────────────────────────────────────────────────

func TestBCherryPickCleanThenConflict(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "cherry-pick")
	s, ctx := bSvc()
	clean := testutil.Git(t, repo, "rev-parse", ":/clean-addition")
	conflicting := testutil.Git(t, repo, "rev-parse", ":/will CONFLICT")
	if err := s.CherryPick(ctx, repo, clean, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "clean-addition.txt")); err != nil {
		t.Fatal("clean pick did not land")
	}
	if err := s.CherryPick(ctx, repo, conflicting, false); err == nil {
		t.Fatal("conflicting pick must fail")
	}
	if st, _ := bMergeState(ctx, repo); st != "cherry-pick" {
		t.Fatalf("merge state = %q", st)
	}
	if !strings.Contains(testutil.Git(t, repo, "diff", "--name-only", "--diff-filter=U"), "config.json") {
		t.Fatal("config.json not conflicted")
	}
	// Resolution belongs to slice A (conflictTakeSide / conflictOpContinue);
	// plain git takes theirs and continues here.
	testutil.Git(t, repo, "checkout", "--theirs", "--", "config.json")
	testutil.Git(t, repo, "add", "config.json")
	testutil.Git(t, repo, "-c", "core.editor=true", "cherry-pick", "--continue")
	if st, _ := bMergeState(ctx, repo); st != "" {
		t.Fatalf("merge state after continue = %q", st)
	}
}

const bSecret = "config/credentials.env"

func bFreshLeakedClone(t *testing.T) string {
	t.Helper()
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "leaked-secret")
	if testutil.Git(t, repo, "log", "--branches", "--tags", "--oneline", "--", bSecret) == "" {
		t.Fatal("the shared fixture lost its secret")
	}
	if testutil.Git(t, repo, "for-each-ref", "--format=%(refname)", "refs/gitcito/pre-purge") != "" {
		t.Fatal("the shared fixture already has a purge backup")
	}
	return repo
}

func TestBHistoryPurgePreviewMeasures(t *testing.T) {
	repo := bFreshLeakedClone(t)
	s, ctx := bSvc()
	p, err := s.HistoryPurgePreview(ctx, repo, []string{bSecret})
	if err != nil {
		t.Fatal(err)
	}
	// Added, rotated, deleted — three commits touch it.
	if p.Commits != 3 || p.FirstCommit == nil || p.FirstCommit.Subject != "chore: add service credentials" {
		t.Fatalf("preview = %+v", p)
	}
	if !bContains(p.Branches, "main") || !bContains(p.Branches, "feature/reporting") || !bContains(p.Tags, "v1.0.0") {
		t.Fatalf("refs = %v %v", p.Branches, p.Tags)
	}
	if p.Bytes <= 0 || p.Blocked != "" {
		t.Fatalf("preview = %+v", p)
	}
}

func TestBHistoryPurgeRefusesDirtyTree(t *testing.T) {
	repo := bFreshLeakedClone(t)
	testutil.WriteFile(t, repo, "app.js", "dirty\n")
	s, ctx := bSvc()
	p, err := s.HistoryPurgePreview(ctx, repo, []string{bSecret})
	if err != nil || !strings.Contains(strings.ToLower(p.Blocked), "stash") {
		t.Fatalf("preview = %+v, %v", p, err)
	}
	if _, err := s.HistoryPurge(ctx, repo, []string{bSecret}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "stash") {
		t.Fatalf("purge err = %v", err)
	}
}

func TestBHistoryPurgeRefusesUntouchedPath(t *testing.T) {
	repo := bFreshLeakedClone(t)
	s, ctx := bSvc()
	if _, err := s.HistoryPurge(ctx, repo, []string{"does/not/exist.env"}); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "nothing to rewrite") {
		t.Fatalf("err = %v", err)
	}
}

func TestBHistoryPurgeRemovesBlobEverywhere(t *testing.T) {
	repo := bFreshLeakedClone(t)
	s, ctx := bSvc()
	res, err := s.HistoryPurge(ctx, repo, []string{bSecret})
	if err != nil || res.Rewritten <= 0 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	// No branch or tag mentions the path any more. (`--all` would still find
	// it through the backup refs, which is exactly what they are for.)
	if testutil.Git(t, repo, "log", "--branches", "--tags", "--oneline", "--", bSecret) != "" {
		t.Fatal("the path is still in history")
	}
	for _, ref := range []string{"main", "feature/reporting", "v1.0.0"} {
		if bGitTry(repo, "cat-file", "-e", ref+":"+bSecret) {
			t.Fatalf("%s still holds the secret", ref)
		}
	}
	if !strings.Contains(testutil.Git(t, repo, "log", "--oneline", "main"), "feat: bump app") {
		t.Fatal("the rest of the history did not survive")
	}
}

func TestBHistoryPurgeKeepsBackup(t *testing.T) {
	repo := bFreshLeakedClone(t)
	s, ctx := bSvc()
	before := testutil.Git(t, repo, "rev-parse", "main")
	res, err := s.HistoryPurge(ctx, repo, []string{bSecret})
	if err != nil {
		t.Fatal(err)
	}
	b := res.Backup
	if !reflect.DeepEqual(b.Paths, []string{bSecret}) || b.Refs <= 0 {
		t.Fatalf("backup = %+v", b)
	}
	// The backup still points at the original commits, secret and all.
	if testutil.Git(t, repo, "rev-parse", b.Prefix+"/heads/main") != before {
		t.Fatal("backup does not hold the old main")
	}
	if testutil.Git(t, repo, "log", "--oneline", b.Prefix+"/heads/main", "--", bSecret) == "" {
		t.Fatal("backup lost the secret's history")
	}
	backups, err := s.HistoryPurgeBackups(ctx, repo)
	if err != nil || len(backups) == 0 || backups[0].Prefix != b.Prefix || !reflect.DeepEqual(backups[0].Paths, []string{bSecret}) {
		t.Fatalf("backups = %+v, %v", backups, err)
	}
}

func TestBHistoryPurgeRestore(t *testing.T) {
	repo := bFreshLeakedClone(t)
	s, ctx := bSvc()
	main, feature := testutil.Git(t, repo, "rev-parse", "main"), testutil.Git(t, repo, "rev-parse", "feature/reporting")
	res, err := s.HistoryPurge(ctx, repo, []string{bSecret})
	if err != nil {
		t.Fatal(err)
	}
	if testutil.Git(t, repo, "rev-parse", "main") == main {
		t.Fatal("purge did not rewrite main")
	}
	if err := s.HistoryPurgeRestore(ctx, repo, res.Backup.Prefix); err != nil {
		t.Fatal(err)
	}
	if testutil.Git(t, repo, "rev-parse", "main") != main || testutil.Git(t, repo, "rev-parse", "feature/reporting") != feature {
		t.Fatal("restore did not put the refs back")
	}
	if testutil.Git(t, repo, "log", "--all", "--oneline", "--", bSecret) == "" {
		t.Fatal("the file is not back in history")
	}
}

func TestBHistoryPurgeDropBackupIsPermanent(t *testing.T) {
	repo := bFreshLeakedClone(t)
	s, ctx := bSvc()
	res, err := s.HistoryPurge(ctx, repo, []string{bSecret})
	if err != nil {
		t.Fatal(err)
	}
	// Until then the old history is still reachable — that is the trade the
	// backup makes, and the reason space is not reclaimed yet.
	if testutil.Git(t, repo, "log", "--all", "--oneline", "--", bSecret) == "" {
		t.Fatal("backup is not keeping the old history")
	}
	if err := s.HistoryPurgeDropBackup(ctx, repo, res.Backup.Prefix); err != nil {
		t.Fatal(err)
	}
	if backups, _ := s.HistoryPurgeBackups(ctx, repo); len(backups) != 0 {
		t.Fatalf("backups = %+v", backups)
	}
	if testutil.Git(t, repo, "for-each-ref", "--format=%(refname)", "refs/gitcito/pre-purge") != "" ||
		testutil.Git(t, repo, "log", "--all", "--oneline", "--", bSecret) != "" {
		t.Fatal("the old history survived dropping the backup")
	}
}

func TestBHistoryPurgeRejectsNonBackup(t *testing.T) {
	repo := bFreshLeakedClone(t)
	s, ctx := bSvc()
	if err := s.HistoryPurgeRestore(ctx, repo, "refs/heads/main"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "not a purge backup") {
		t.Fatalf("restore err = %v", err)
	}
	if err := s.HistoryPurgeDropBackup(ctx, repo, "refs/heads/main"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "not a purge backup") {
		t.Fatalf("drop err = %v", err)
	}
}

func bHistoryPaths(t *testing.T, max *int) []HistoryPathEntry {
	t.Helper()
	repo := testutil.CloneFixture(t, "leaked-secret")
	s, ctx := bSvc()
	entries, err := s.HistoryPaths(ctx, repo, max)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestBHistoryPathsListsDeletedPaths(t *testing.T) {
	for _, e := range bHistoryPaths(t, nil) {
		if e.Path == bSecret {
			// Added once, rotated once — two distinct blobs.
			if !e.Deleted || e.Versions != 2 || e.Bytes <= 0 {
				t.Fatalf("entry = %+v", e)
			}
			return
		}
	}
	t.Fatal("the deleted secret is not offered")
}

func TestBHistoryPathsMarksTracked(t *testing.T) {
	for _, e := range bHistoryPaths(t, nil) {
		if e.Path == "app.js" {
			if e.Deleted {
				t.Fatal("app.js is still tracked")
			}
			return
		}
	}
	t.Fatal("app.js missing")
}

func TestBHistoryPathsHeaviestFirst(t *testing.T) {
	entries := bHistoryPaths(t, nil)
	for i := 1; i < len(entries); i++ {
		if entries[i].Bytes > entries[i-1].Bytes {
			t.Fatalf("not sorted by weight: %+v", entries)
		}
	}
}

func TestBHistoryPathsHonoursCap(t *testing.T) {
	two := 2
	if n := len(bHistoryPaths(t, &two)); n != 2 {
		t.Fatalf("got %d entries", n)
	}
}

func bNotesRepo(t *testing.T) (string, string) {
	t.Helper()
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "cherry-pick")
	return repo, testutil.Git(t, repo, "rev-parse", "HEAD")
}

func TestBNoteNone(t *testing.T) {
	repo, sha := bNotesRepo(t)
	s, ctx := bSvc()
	if n, _ := s.Note(ctx, repo, sha); n != "" {
		t.Fatalf("note = %q", n)
	}
	if c, _ := s.NotedCommits(ctx, repo); len(c) != 0 {
		t.Fatalf("noted = %v", c)
	}
}

func TestBNoteRoundTrip(t *testing.T) {
	repo, sha := bNotesRepo(t)
	s, ctx := bSvc()
	text := "Reviewed by Ana.\n\nShipped in build 412 — see the incident doc."
	if err := s.SetNote(ctx, repo, sha, text); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Note(ctx, repo, sha); n != text {
		t.Fatalf("note = %q", n)
	}
	// git's own reader agrees, so the note is a real note and not our invention.
	if !strings.Contains(testutil.Git(t, repo, "log", "-1", "--format=%N", sha), "build 412") {
		t.Fatal("git log does not see the note")
	}
}

func TestBNoteOverwrites(t *testing.T) {
	repo, sha := bNotesRepo(t)
	s, ctx := bSvc()
	_ = s.SetNote(ctx, repo, sha, "first")
	_ = s.SetNote(ctx, repo, sha, "second")
	if n, _ := s.Note(ctx, repo, sha); n != "second" {
		t.Fatalf("note = %q", n)
	}
}

func TestBNotedCommitsExact(t *testing.T) {
	repo, sha := bNotesRepo(t)
	s, ctx := bSvc()
	_ = s.SetNote(ctx, repo, sha, "noted")
	if c, _ := s.NotedCommits(ctx, repo); !reflect.DeepEqual(c, []string{sha}) {
		t.Fatalf("noted = %v", c)
	}
}

func TestBNoteBlankRemoves(t *testing.T) {
	repo, sha := bNotesRepo(t)
	s, ctx := bSvc()
	_ = s.SetNote(ctx, repo, sha, "temporary")
	_ = s.SetNote(ctx, repo, sha, "   ")
	if n, _ := s.Note(ctx, repo, sha); n != "" {
		t.Fatalf("note = %q", n)
	}
	if c, _ := s.NotedCommits(ctx, repo); len(c) != 0 {
		t.Fatalf("noted = %v", c)
	}
}

func TestBRemoveNoteIdempotent(t *testing.T) {
	repo, sha := bNotesRepo(t)
	s, ctx := bSvc()
	_ = s.SetNote(ctx, repo, sha, "gone soon")
	if err := s.RemoveNote(ctx, repo, sha); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Note(ctx, repo, sha); n != "" {
		t.Fatalf("note = %q", n)
	}
	if err := s.RemoveNote(ctx, repo, sha); err != nil {
		t.Fatalf("removing a missing note: %v", err)
	}
}

func TestBNoteLeavesCommitAlone(t *testing.T) {
	repo, sha := bNotesRepo(t)
	s, ctx := bSvc()
	_ = s.SetNote(ctx, repo, sha, "annotation")
	if testutil.Git(t, repo, "rev-parse", "HEAD") != sha {
		t.Fatal("a note moved HEAD")
	}
}

func TestBObjectRefsListsHeadFirst(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	s, ctx := bSvc()
	refs, err := s.ObjectRefs(ctx, repo)
	if err != nil || len(refs) == 0 || refs[0].Name != "HEAD" {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
	hasMain, hasTag := false, false
	for _, r := range refs {
		hasMain = hasMain || r.Name == "refs/heads/main"
		hasTag = hasTag || strings.HasPrefix(r.Name, "refs/tags/")
		if len(r.Sha) != 40 {
			t.Fatalf("sha %q", r.Sha)
		}
	}
	if !hasMain || !hasTag {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestBGitObjectWalk(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	s, ctx := bSvc()
	commit, err := s.GitObject(ctx, repo, "HEAD")
	if err != nil || commit.Kind != "commit" || commit.Commit == nil || len(commit.Commit.Tree) != 40 ||
		!strings.Contains(commit.Commit.Author, "@") || strings.TrimSpace(commit.Commit.Message) == "" {
		t.Fatalf("commit = %+v, %v", commit, err)
	}
	tree, err := s.GitObject(ctx, repo, commit.Commit.Tree)
	if err != nil || tree.Kind != "tree" || tree.Tree == nil {
		t.Fatalf("tree = %+v, %v", tree, err)
	}
	var entry *TreeChild
	for i := range *tree.Tree {
		if (*tree.Tree)[i].Name == "app.py" {
			entry = &(*tree.Tree)[i]
		}
	}
	if entry == nil || entry.Mode != "100644" || entry.Kind != "blob" || entry.Size == nil || *entry.Size <= 0 {
		t.Fatalf("entry = %+v", entry)
	}
	blob, err := s.GitObject(ctx, repo, entry.Sha)
	if err != nil || blob.Kind != "blob" || blob.Blob == nil || blob.Blob.Text == nil ||
		!strings.Contains(*blob.Blob.Text, "def ") || blob.Blob.Truncated {
		t.Fatalf("blob = %+v, %v", blob, err)
	}
}

func TestBGitObjectRevExpressions(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	s, ctx := bSvc()
	if o, err := s.GitObject(ctx, repo, "HEAD:app.py"); err != nil || o.Kind != "blob" {
		t.Fatalf("path rev = %+v, %v", o, err)
	}
	peel, _ := s.GitObject(ctx, repo, "HEAD^{tree}")
	commit, _ := s.GitObject(ctx, repo, "HEAD")
	if commit.Commit == nil || peel.Sha != commit.Commit.Tree {
		t.Fatal("peeled tree differs from the commit's tree")
	}
}

func TestBGitObjectAnnotatedTag(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	s, ctx := bSvc()
	refs, _ := s.ObjectRefs(ctx, repo)
	var annotated *RefObject
	for i := range refs {
		if refs[i].Kind == "tag" {
			annotated = &refs[i]
			break
		}
	}
	if annotated == nil {
		return // the fixture may carry only lightweight tags
	}
	tag, err := s.GitObject(ctx, repo, annotated.Name)
	if err != nil || tag.Kind != "tag" || tag.Tag == nil || tag.Tag.Type != "commit" || len(tag.Tag.Object) != 40 {
		t.Fatalf("tag = %+v, %v", tag, err)
	}
	if o, _ := s.GitObject(ctx, repo, tag.Tag.Object); o.Kind != "commit" {
		t.Fatalf("tagged object kind = %s", o.Kind)
	}
}

func TestBGitObjectBinaryBlob(t *testing.T) {
	repo := testutil.CloneFixture(t, "binary-images-unicode")
	s, ctx := bSvc()
	tree, err := s.GitObject(ctx, repo, "HEAD^{tree}")
	if err != nil || tree.Tree == nil {
		t.Fatal(err)
	}
	for _, c := range *tree.Tree {
		if strings.HasSuffix(c.Name, ".png") {
			blob, err := s.GitObject(ctx, repo, c.Sha)
			if err != nil || blob.Blob == nil || blob.Blob.Text != nil || blob.Size <= 0 {
				t.Fatalf("blob = %+v, %v", blob, err)
			}
			return
		}
	}
}

func TestBGitObjectUnknownRev(t *testing.T) {
	repo := testutil.CloneFixture(t, "tags-and-releases")
	s, ctx := bSvc()
	if _, err := s.GitObject(ctx, repo, "no-such-ref"); err == nil {
		t.Fatal("an unknown revision must be an error")
	}
}

// ─── newFeatures.test.ts ───────────────────────────────────────────────────

func TestBGenerateChangelogGroups(t *testing.T) {
	s, ctx := bSvc()
	res, err := s.GenerateChangelog(ctx, testutil.PlaygroundRepo(t, "changelog"), nil)
	if err != nil || res.Count != 11 { // v1.0.0..HEAD
		t.Fatalf("result = %+v, %v", res, err)
	}
	for _, want := range []string{"BREAKING CHANGES", "Features", "Bug Fixes", "Other"} {
		if !strings.Contains(res.Markdown, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
}

func TestBGenerateChangelogVersionHeading(t *testing.T) {
	s, ctx := bSvc()
	res, _ := s.GenerateChangelog(ctx, testutil.PlaygroundRepo(t, "changelog"), &ChangelogOptions{Version: "v1.1.0"})
	if !strings.HasPrefix(res.Markdown, "## v1.1.0") {
		t.Fatalf("markdown = %q", res.Markdown)
	}
}

func TestBRepoInsightsRanks(t *testing.T) {
	s, ctx := bSvc()
	ins, err := s.RepoInsights(ctx, testutil.PlaygroundRepo(t, "insights"), 0)
	if err != nil || len(ins.Hotspots) == 0 || ins.Hotspots[0].Path != "src/core.js" {
		t.Fatalf("insights = %+v, %v", ins, err)
	}
	if ins.Authors[0].Name != "Alice" || ins.Authors[0].Commits < ins.Authors[1].Commits || len(ins.Churn) < 5 || ins.TotalCommits <= 0 {
		t.Fatalf("insights = %+v", ins)
	}
}

func TestBGrepWorkingTree(t *testing.T) {
	s, ctx := bSvc()
	hits, err := s.GrepWorkingTree(ctx, testutil.PlaygroundRepo(t, "code-search"), "TODO", nil)
	if err != nil || len(hits) < 3 {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	untracked := false
	for _, h := range hits {
		untracked = untracked || h.File == "scratch.js"
	}
	if !untracked || hits[0].Line <= 0 {
		t.Fatalf("hits = %+v", hits)
	}
}

func bFileHits(hits []CodeSearchHit, file string) int {
	n := 0
	for _, h := range hits {
		if h.File == file {
			n++
		}
	}
	return n
}

func TestBSearchFileMatchesLines(t *testing.T) {
	s, ctx := bSvc()
	hits, err := s.SearchFileMatches(ctx, testutil.PlaygroundRepo(t, "code-search"), []string{"src/util/log.js", "src/util/mail.js"}, "TODO", nil)
	if err != nil || bFileHits(hits, "src/util/log.js") != 3 || bFileHits(hits, "src/util/mail.js") != 2 || bFileHits(hits, "README.md") != 0 {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	for _, h := range hits {
		if h.Line <= 0 || !strings.Contains(h.Text, "TODO") {
			t.Fatalf("hit = %+v", h)
		}
	}
}

func TestBSearchFileMatchesToggles(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.PlaygroundRepo(t, "code-search")
	if h, _ := s.SearchFileMatches(ctx, repo, []string{"README.md"}, "todo", &SearchOptions{CaseSensitive: true}); len(h) != 0 {
		t.Errorf("case-sensitive = %+v", h)
	}
	if h, _ := s.SearchFileMatches(ctx, repo, []string{"README.md"}, "todo", nil); len(h) != 1 {
		t.Errorf("case-insensitive = %+v", h)
	}
	if h, _ := s.SearchFileMatches(ctx, repo, []string{"src/util/mail.js"}, `\w+@corp\.dev`, &SearchOptions{Regex: true}); len(h) != 1 {
		t.Errorf("regex = %+v", h)
	}
	if h, _ := s.SearchFileMatches(ctx, repo, []string{"src/util/log.js"}, "TOD", &SearchOptions{WholeWord: true}); len(h) != 0 {
		t.Errorf("whole word = %+v", h)
	}
}

func TestBSearchFileMatchesPerFileCap(t *testing.T) {
	s, ctx := bSvc()
	two := 2
	h, _ := s.SearchFileMatches(ctx, testutil.PlaygroundRepo(t, "code-search"), []string{"src/util/log.js"}, "TODO", &SearchOptions{MaxPerFile: &two})
	if len(h) != 2 {
		t.Fatalf("hits = %+v", h)
	}
}

func TestBSearchCommitMatchesTree(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.PlaygroundRepo(t, "code-search")
	// validateToken exists at the commit that added it and is gone from HEAD.
	added := testutil.Git(t, repo, "log", "--format=%H", "--grep=add validateToken")
	hits, err := s.SearchCommitMatches(ctx, repo, added, "validateToken", nil)
	if err != nil || len(hits) < 1 || hits[0].File != "src/auth/token.js" || hits[0].Line != 1 {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	if h, _ := s.SearchCommitMatches(ctx, repo, "HEAD", "validateToken", nil); len(h) != 0 {
		t.Fatalf("HEAD hits = %+v", h)
	}
}

func TestBSearchCommitMatchesPaths(t *testing.T) {
	s, ctx := bSvc()
	h, _ := s.SearchCommitMatches(ctx, testutil.PlaygroundRepo(t, "code-search"), "HEAD", "TODO", &SearchOptions{Paths: []string{"README.md"}})
	if len(h) != 1 || h[0].File != "README.md" {
		t.Fatalf("hits = %+v", h)
	}
}

func TestBSearchHistoryPickaxe(t *testing.T) {
	s, ctx := bSvc()
	commits, err := s.SearchHistory(ctx, testutil.PlaygroundRepo(t, "code-search"), "validateToken", nil)
	if err != nil || len(commits) != 3 { // add, use, drop
		t.Fatalf("commits = %+v, %v", commits, err)
	}
}

func TestBCherryPickManyInOrder(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "changelog")
	base := testutil.Git(t, repo, "branch", "--show-current")
	// Two independent commits (new files ⇒ conflict-free picks) on a side
	// branch; then back to base and pick them across.
	testutil.Git(t, repo, "checkout", "-q", "-b", "donor")
	bCommitFile(t, repo, "cpm-one.txt", "1\n", "add cpm-one")
	bCommitFile(t, repo, "cpm-two.txt", "2\n", "add cpm-two")
	selection := []string{testutil.Git(t, repo, "rev-parse", "HEAD"), testutil.Git(t, repo, "rev-parse", "HEAD~1")}
	testutil.Git(t, repo, "checkout", "-q", base)
	s, ctx := bSvc()
	if err := s.CherryPickMany(ctx, repo, []string{selection[1], selection[0]}, false); err != nil { // oldest-first
		t.Fatal(err)
	}
	// The parent chain, not the log's date order: both picks land in the
	// same second on a loaded machine.
	if chain := testutil.Git(t, repo, "log", "--topo-order", "--format=%s", "-2"); chain != "add cpm-two\nadd cpm-one" {
		t.Fatalf("chain = %q", chain)
	}
	if _, err := os.Stat(filepath.Join(repo, "cpm-one.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestBCreateTagKinds(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "changelog")
	s, ctx := bSvc()
	if err := s.CreateTag(ctx, repo, "light-1", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo, "cat-file", "-t", "light-1"); got != "commit" {
		t.Fatalf("light-1 is a %s", got)
	}
	if err := s.CreateTag(ctx, repo, "annot-1", "", &CreateTagOptions{Message: "release notes"}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo, "cat-file", "-t", "annot-1"); got != "tag" {
		t.Fatalf("annot-1 is a %s", got)
	}
	if !strings.Contains(testutil.Git(t, repo, "tag", "-l", "--format=%(contents)", "annot-1"), "release notes") {
		t.Fatal("annotation lost")
	}
}

func TestBContributorsDistinct(t *testing.T) {
	s, ctx := bSvc()
	people, err := s.Contributors(ctx, testutil.PlaygroundRepo(t, "insights"), nil)
	if err != nil || len(people) < 3 {
		t.Fatalf("people = %+v, %v", people, err)
	}
	seen := map[string]bool{}
	for _, p := range people {
		if p.Name == "" || !strings.Contains(p.Email, "@") || seen[strings.ToLower(p.Email)] {
			t.Fatalf("people = %+v", people)
		}
		seen[strings.ToLower(p.Email)] = true
	}
}

// semanticDiff moved to the renderer: the Go side hands over the two texts.
func TestBSemanticDiffSidesOverBlobs(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.PlaygroundRepo(t, "conflict-radar")
	sides, err := s.SemanticDiff(ctx, repo, "src/app.js", BlobSpec{Kind: "ref", Ref: "main^1"}, BlobSpec{Kind: "ref", Ref: "main"})
	if err != nil || !strings.Contains(sides.OldText, "start") || !strings.Contains(sides.NewText, "launch") {
		t.Fatalf("sides = %+v, %v", sides, err)
	}
	missing, _ := s.SemanticDiff(ctx, repo, "nope.ts", BlobSpec{Kind: "ref", Ref: "main"}, BlobSpec{Kind: "ref", Ref: "main"})
	if missing != (SemanticDiffSides{}) {
		t.Fatalf("missing = %+v", missing)
	}
}

func bNames(entries []TreeEntry, path bool) []string {
	out := []string{}
	for _, e := range entries {
		if path {
			out = append(out, e.Path)
		} else {
			out = append(out, e.Name)
		}
	}
	return out
}

func TestBListDirAtPastTree(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.PlaygroundRepo(t, "semantic-diff")
	root, err := s.ListDirAt(ctx, repo, "HEAD", "")
	// Directories first, then files, each alphabetically — the order the
	// working-tree listing uses.
	if err != nil || !reflect.DeepEqual(bNames(root, false), []string{"src", "notes.txt", "README.md"}) || !root[0].Dir {
		t.Fatalf("root = %+v, %v", root, err)
	}
	// Reading history must not disturb the checkout.
	if testutil.Git(t, repo, "diff", "--cached", "--name-only") != "" {
		t.Fatal("listDirAt staged something")
	}
}

func TestBListDirAtSubdirPaths(t *testing.T) {
	s, ctx := bSvc()
	src, _ := s.ListDirAt(ctx, testutil.PlaygroundRepo(t, "semantic-diff"), "HEAD", "src")
	if !reflect.DeepEqual(bNames(src, true), []string{"src/api.py", "src/app.ts", "src/server.go"}) {
		t.Fatalf("src = %+v", src)
	}
	for _, e := range src {
		if e.Dir {
			t.Fatalf("src = %+v", src)
		}
	}
}

func TestBListDirAtAsItWas(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.PlaygroundRepo(t, "semantic-diff")
	now, _ := s.ListDirAt(ctx, repo, "HEAD", "src")
	before, _ := s.ListDirAt(ctx, repo, "HEAD~1", "src")
	if !reflect.DeepEqual(bNames(now, false), bNames(before, false)) {
		t.Fatalf("now %v, before %v", bNames(now, false), bNames(before, false))
	}
	old, err := s.FileContent(ctx, repo, "src/app.ts", "HEAD~1", false)
	if err != nil || !strings.Contains(old, "startServer") || strings.Contains(old, "bootServer") {
		t.Fatalf("old app.ts = %q, %v", old, err)
	}
}

func TestBListDirAtMissingIsEmpty(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.PlaygroundRepo(t, "semantic-diff")
	if e, err := s.ListDirAt(ctx, repo, "HEAD", "nope"); err != nil || len(e) != 0 {
		t.Fatalf("nope = %+v, %v", e, err)
	}
	if e, err := s.ListDirAt(ctx, repo, "deadbeef", ""); err != nil || len(e) != 0 {
		t.Fatalf("deadbeef = %+v, %v", e, err)
	}
}

func TestBTimelapseStream(t *testing.T) {
	s, ctx := bSvc()
	data, err := s.TimelapseData(ctx, testutil.PlaygroundRepo(t, "time-machine"), nil)
	if err != nil || len(data) != 12 || data[0].Subject != "init: one-file app" || data[len(data)-1].Subject != "chore: release 1.2.0" {
		t.Fatalf("data = %+v, %v", data, err)
	}
	if !reflect.DeepEqual(data[0].Files, []TimelapseFile{{Path: "index.js", Status: "A"}}) {
		t.Fatalf("first files = %+v", data[0].Files)
	}
	for _, c := range data {
		if c.Author != "Playground" || c.Date <= 0 {
			t.Fatalf("commit = %+v", c)
		}
	}
}

func TestBTimelapseKinds(t *testing.T) {
	s, ctx := bSvc()
	data, _ := s.TimelapseData(ctx, testutil.PlaygroundRepo(t, "time-machine"), nil)
	by := map[string]TimelapseCommit{}
	for _, c := range data {
		by[c.Subject] = c
	}
	want := map[string][]TimelapseFile{
		"refactor: extract lib/util.js":     {{"index.js", "M"}, {"lib/util.js", "A"}},
		"chore: drop unused formatter":      {{"src/utils/format.js", "D"}},
		"chore: move entry point into src/": {{"src/index.js", "R"}}, // one entry, not add + delete
	}
	for subject, files := range want {
		if !reflect.DeepEqual(by[subject].Files, files) {
			t.Errorf("%s: %+v", subject, by[subject].Files)
		}
	}
}

func TestBTimelapseCap(t *testing.T) {
	s, ctx := bSvc()
	three := 3
	if data, _ := s.TimelapseData(ctx, testutil.PlaygroundRepo(t, "time-machine"), &three); len(data) != 3 {
		t.Fatalf("got %d", len(data))
	}
}

func TestBProtectedBranchesDefaults(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.CloneFixture(t, "stacked-branches")
	if got, _ := s.ProtectedBranches(ctx, repo); !reflect.DeepEqual(got, []string{"main", "master"}) {
		t.Fatalf("default = %v", got)
	}
}

func TestBProtectedBranchesExplicitEmpty(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.CloneFixture(t, "stacked-branches")
	if err := s.SetProtectedBranches(ctx, repo, []string{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ProtectedBranches(ctx, repo); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestBProtectedBranchesConfigured(t *testing.T) {
	s, ctx := bSvc()
	repo := testutil.CloneFixture(t, "stacked-branches")
	_ = s.SetProtectedBranches(ctx, repo, []string{"release", "main"})
	if got, _ := s.ProtectedBranches(ctx, repo); !reflect.DeepEqual(got, []string{"release", "main"}) {
		t.Fatalf("got %v", got)
	}
}
