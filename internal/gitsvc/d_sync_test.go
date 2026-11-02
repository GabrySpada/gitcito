package gitsvc

// Ported vitest cases for slice D's sync, clone, lock and ignore methods
// (test/gitOps.test.ts, test/newFeatures.test.ts). Each test names its TS
// original in a comment.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// dGitErr runs git and returns its error, for assertions that a command fails.
func dGitErr(dir string, args ...string) error {
	return exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
}

func dMustMatch(t *testing.T, err error, pattern string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error matching %q, got none", pattern)
	}
	if !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("error %q does not match %q", err.Error(), pattern)
	}
}

func dReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dTwoClones is gitOps.test.ts' twoClones(): a working clone of a bare repo,
// plus a second clone used to publish to it.
func dTwoClones(t *testing.T) (mine, theirs string) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	testutil.Git(t, tmp, "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(tmp, "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, seed, "init", "-b", "main")
	testutil.WriteFile(t, seed, "a.txt", "one\n")
	testutil.Git(t, seed, "add", "-A")
	testutil.Git(t, seed, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-m", "feat: seed")
	testutil.Git(t, seed, "remote", "add", "origin", bare)
	testutil.Git(t, seed, "push", "-u", "origin", "main")
	testutil.Git(t, tmp, "clone", bare, "mine")
	testutil.Git(t, tmp, "clone", bare, "theirs")
	return filepath.Join(tmp, "mine"), filepath.Join(tmp, "theirs")
}

func dPublish(t *testing.T, theirs, branch, text string) {
	testutil.Git(t, theirs, "checkout", "-B", branch, "origin/main")
	testutil.WriteFile(t, theirs, "a.txt", text)
	testutil.Git(t, theirs, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-am", "feat: theirs")
	testutil.Git(t, theirs, "push", "-u", "origin", branch)
}

// gitOps: pull a branch you are not on > fast-forwards a branch without checking it out
func TestDPullBranchFastForwardsWithoutCheckout(t *testing.T) {
	dIsolate(t)
	s, ctx := New(nil, nil), context.Background()
	mine, theirs := dTwoClones(t)
	dPublish(t, theirs, "feature", "two\n")
	testutil.Git(t, mine, "fetch", "origin")
	testutil.Git(t, mine, "branch", "--track", "feature", "origin/feature")
	testutil.Git(t, mine, "checkout", "main")
	head := testutil.Git(t, mine, "rev-parse", "HEAD")
	if err := s.PullBranch(ctx, mine, "feature"); err != nil {
		t.Fatal(err)
	}
	// The branch moved, HEAD did not, and the working tree is untouched.
	if testutil.Git(t, mine, "rev-parse", "feature") != testutil.Git(t, mine, "rev-parse", "origin/feature") {
		t.Fatal("feature did not move to origin/feature")
	}
	if testutil.Git(t, mine, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved")
	}
	if dReadFile(t, filepath.Join(mine, "a.txt")) != "one\n" {
		t.Fatal("working tree changed")
	}
}

// gitOps: pull a branch you are not on > falls back to a normal pull when the branch is the checked-out one
func TestDPullBranchCheckedOutFallsBackToPull(t *testing.T) {
	dIsolate(t)
	s, ctx := New(nil, nil), context.Background()
	mine, theirs := dTwoClones(t)
	testutil.Git(t, theirs, "checkout", "main")
	testutil.WriteFile(t, theirs, "a.txt", "two\n")
	testutil.Git(t, theirs, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-am", "feat: theirs")
	testutil.Git(t, theirs, "push", "origin", "main")
	if err := s.PullBranch(ctx, mine, "main"); err != nil {
		t.Fatal(err)
	}
	// A plain pull updates the working tree too — that is why HEAD takes this path.
	if dReadFile(t, filepath.Join(mine, "a.txt")) != "two\n" {
		t.Fatal("working tree was not updated")
	}
}

// gitOps: pull a branch you are not on > refuses a branch that tracks nothing
func TestDPullBranchRefusesUntracked(t *testing.T) {
	dIsolate(t)
	mine, _ := dTwoClones(t)
	testutil.Git(t, mine, "branch", "local-only")
	dMustMatch(t, New(nil, nil).PullBranch(context.Background(), mine, "local-only"), `(?i)no upstream`)
}

// gitOps: pull a branch you are not on > refuses a diverged branch instead of merging behind your back
func TestDPullBranchRefusesDiverged(t *testing.T) {
	dIsolate(t)
	mine, theirs := dTwoClones(t)
	dPublish(t, theirs, "feature", "theirs\n")
	testutil.Git(t, mine, "fetch", "origin")
	testutil.Git(t, mine, "branch", "--track", "feature", "origin/feature")
	// Rewrite the local side so it is no longer a fast-forward of the remote.
	testutil.Git(t, mine, "checkout", "feature")
	testutil.WriteFile(t, mine, "a.txt", "mine\n")
	testutil.Git(t, mine, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "--amend", "-am", "feat: mine")
	testutil.Git(t, mine, "checkout", "main")
	before := testutil.Git(t, mine, "rev-parse", "feature")
	dMustMatch(t, New(nil, nil).PullBranch(context.Background(), mine, "feature"), `(?i)diverged`)
	if testutil.Git(t, mine, "rev-parse", "feature") != before {
		t.Fatal("diverged branch moved")
	}
}

// gitOps: pull a branch you are not on > moves a branch back to where it was, and refuses to move the current one
func TestDSetBranchRefUndoesBranchPull(t *testing.T) {
	dIsolate(t)
	s, ctx := New(nil, nil), context.Background()
	mine, theirs := dTwoClones(t)
	dPublish(t, theirs, "feature", "two\n")
	testutil.Git(t, mine, "fetch", "origin")
	testutil.Git(t, mine, "branch", "--track", "feature", "origin/feature")
	testutil.Git(t, mine, "checkout", "main")
	start := testutil.Git(t, mine, "rev-parse", "main")
	testutil.Git(t, mine, "branch", "-f", "feature", start)
	if err := s.PullBranch(ctx, mine, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBranchRef(ctx, mine, "feature", start); err != nil {
		t.Fatal(err)
	}
	if testutil.Git(t, mine, "rev-parse", "feature") != start {
		t.Fatal("feature was not moved back")
	}
	if err := s.SetBranchRef(ctx, mine, "main", start+"~0"); err == nil {
		t.Fatal("moving the checked-out branch should fail")
	}
}

// gitOps: pull a branch you are not on > takes the exclusive lock — a branch pull is a write
func TestDPullBranchIsAWrite(t *testing.T) {
	if IsReadMethod("pullBranch") || IsReadMethod("setBranchRef") {
		t.Fatal("pullBranch and setBranchRef must take the exclusive lock")
	}
}

// dMultiRemote is gitOps' isolatedClone(): the multi-remote fixture with its
// own copies of both bare remotes.
func dMultiRemote(t *testing.T) (repo, origin, upstream string) {
	repo = testutil.CloneFixture(t, "multi-remote")
	origin = testutil.CloneFixture(t, "multi-remote-origin.git")
	upstream = testutil.CloneFixture(t, "multi-remote-upstream.git")
	testutil.Git(t, repo, "remote", "set-url", "origin", origin)
	testutil.Git(t, repo, "remote", "set-url", "upstream", upstream)
	return
}

func dPushTestBranch(t *testing.T, repo string) string {
	testutil.Git(t, repo, "checkout", "-b", "feature/push-test")
	return "feature/push-test"
}

// gitOps: pushing to several remotes > pushes one branch to every remote asked for
func TestDPushToRemotesPushesEveryRemote(t *testing.T) {
	dIsolate(t)
	repo, origin, upstream := dMultiRemote(t)
	branch := dPushTestBranch(t, repo)
	local := testutil.Git(t, repo, "rev-parse", branch)
	results, err := New(nil, nil).PushToRemotes(context.Background(), repo, branch, []string{"origin", "upstream"}, PushToRemotesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Remote != "origin" || results[1].Remote != "upstream" || !results[0].OK || !results[1].OK {
		t.Fatalf("results = %+v", results)
	}
	if testutil.Git(t, origin, "rev-parse", branch) != local || testutil.Git(t, upstream, "rev-parse", branch) != local {
		t.Fatal("a remote did not get the branch")
	}
}

// gitOps: pushing to several remotes > keeps going when one remote rejects, and says which
func TestDPushToRemotesKeepsGoing(t *testing.T) {
	dIsolate(t)
	repo, origin, _ := dMultiRemote(t)
	testutil.Git(t, repo, "remote", "set-url", "upstream", "/nowhere/at/all.git")
	branch := dPushTestBranch(t, repo)
	local := testutil.Git(t, repo, "rev-parse", branch)
	results, err := New(nil, nil).PushToRemotes(context.Background(), repo, branch, []string{"origin", "upstream"}, PushToRemotesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].OK || results[1].OK || results[1].Error == "" {
		t.Fatalf("results = %+v", results)
	}
	// The working remote still got the branch — that is the whole point.
	if testutil.Git(t, origin, "rev-parse", branch) != local {
		t.Fatal("origin did not get the branch")
	}
}

// gitOps: pushing to several remotes > sets the upstream from the first remote only
func TestDPushToRemotesFirstRemoteIsUpstream(t *testing.T) {
	dIsolate(t)
	repo, _, _ := dMultiRemote(t)
	branch := dPushTestBranch(t, repo)
	if _, err := New(nil, nil).PushToRemotes(context.Background(), repo, branch, []string{"upstream", "origin"}, PushToRemotesOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo, "rev-parse", "--abbrev-ref", branch+"@{upstream}"); got != "upstream/"+branch {
		t.Fatalf("upstream = %q", got)
	}
}

// gitOps: pushing to several remotes > refuses an empty remote list rather than guessing
func TestDPushToRemotesRefusesEmpty(t *testing.T) {
	dIsolate(t)
	repo, _, _ := dMultiRemote(t)
	s, ctx := New(nil, nil), context.Background()
	_, err := s.PushToRemotes(ctx, repo, "main", []string{}, PushToRemotesOptions{})
	dMustMatch(t, err, `(?i)at least one remote`)
	_, err = s.PushToRemotes(ctx, repo, "main", []string{"  "}, PushToRemotesOptions{})
	dMustMatch(t, err, `(?i)at least one remote`)
}

// gitOps: pushing to several remotes > publishes every local tag, annotated and lightweight alike
func TestDPushAllTags(t *testing.T) {
	dIsolate(t)
	repo, origin, _ := dMultiRemote(t)
	testutil.Git(t, repo, "tag", "v9.9.9-light")
	testutil.Git(t, repo, "tag", "-a", "v9.9.9", "-m", "Release")
	remote := "origin"
	if err := New(nil, nil).PushAllTags(context.Background(), repo, &remote); err != nil {
		t.Fatal(err)
	}
	tags := testutil.Git(t, origin, "tag", "--list")
	if !strings.Contains(tags, "v9.9.9") || !strings.Contains(tags, "v9.9.9-light") {
		t.Fatalf("tags = %q", tags)
	}
}

// newFeatures: stacked-PR autopilot plumbing > pushes a non-current stack level to a remote without checking it out
func TestDPushNonCurrentBranch(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "stacked-branches") // checked out on feature/ui
	bare := filepath.Join(t.TempDir(), "origin.git")
	testutil.Git(t, filepath.Dir(bare), "init", "-q", "--bare", bare)
	testutil.Git(t, repo, "remote", "add", "origin", bare)
	if err := New(nil, nil).Push(context.Background(), repo, "feature/api", PushOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if testutil.Git(t, bare, "rev-parse", "refs/heads/feature/api") != testutil.Git(t, repo, "rev-parse", "feature/api") {
		t.Fatal("feature/api was not pushed")
	}
	// Still on the leaf — the push never touched the working tree.
	if got := testutil.Git(t, repo, "branch", "--show-current"); got != "feature/ui" {
		t.Fatalf("current branch = %q", got)
	}
}

// newFeatures: rangeDiff + refTips (force-push playground) > reports the force-pushed ref, with the commit it used to point at
func TestDFetchAllReportsForcedUpdate(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "force-push")
	forced, err := New(nil, nil).FetchAll(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(forced) != 1 || forced[0].Ref != "origin/feature/login" || forced[0].OldSha == forced[0].NewSha {
		t.Fatalf("forced = %+v", forced)
	}
}

// newFeatures: lastFetchAt > is null for a repository that has never fetched
func TestDLastFetchAtNeverFetched(t *testing.T) {
	at, err := New(nil, nil).LastFetchAt(context.Background(), testutil.PlaygroundRepo(t, "empty-repo"))
	if err != nil || at != nil {
		t.Fatalf("at = %v, err = %v", at, err)
	}
}

// newFeatures: lastFetchAt > reports FETCH_HEAD's mtime, so a fetch run outside the app counts too
func TestDLastFetchAtAfterFetch(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "force-push")
	s, ctx := New(nil, nil), context.Background()
	before := float64(time.Now().UnixMilli())
	if _, err := s.FetchAll(ctx, repo); err != nil {
		t.Fatal(err)
	}
	at, err := s.LastFetchAt(ctx, repo)
	if err != nil || at == nil {
		t.Fatalf("at = %v, err = %v", at, err)
	}
	// Filesystem timestamps can round down a little; a second of slack is plenty.
	if *at < before-1000 || *at > float64(time.Now().UnixMilli())+1000 {
		t.Fatalf("at = %v, before = %v", *at, before)
	}
}

// newFeatures: upstreamSuggestion + setUpstream (force-push playground) — the
// five cases share one repository, in order, as they did in vitest.
func TestDUpstreamSuggestion(t *testing.T) {
	dIsolate(t)
	repo := testutil.CloneFixture(t, "force-push")
	s, ctx := New(nil, nil), context.Background()
	// The suggestion reads remote-tracking refs, so they have to exist first.
	if _, err := s.FetchAll(ctx, repo); err != nil {
		t.Fatal(err)
	}
	suggest := func() *UpstreamSuggestion {
		t.Helper()
		got, err := s.UpstreamSuggestion(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	t.Run("has nothing to suggest while the branch already tracks something", func(t *testing.T) {
		if got := suggest(); got != nil {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("offers the remote branch that is already there once tracking is dropped", func(t *testing.T) {
		testutil.Git(t, repo, "checkout", "feature/login")
		if err := s.SetUpstream(ctx, repo, "feature/login", nil); err != nil {
			t.Fatal(err)
		}
		if got := suggest(); got == nil || *got != (UpstreamSuggestion{Branch: "feature/login", Remote: "origin", RemoteRefExists: true}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("links the branch back to its remote, and then has nothing left to offer", func(t *testing.T) {
		origin := "origin"
		if err := s.SetUpstream(ctx, repo, "feature/login", &origin); err != nil {
			t.Fatal(err)
		}
		if got := testutil.Git(t, repo, "rev-parse", "--abbrev-ref", "feature/login@{u}"); got != "origin/feature/login" {
			t.Fatalf("upstream = %q", got)
		}
		if got := suggest(); got != nil {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("marks a branch the remote has never seen, so the repair is a push", func(t *testing.T) {
		testutil.Git(t, repo, "checkout", "-b", "local-only")
		if got := suggest(); got == nil || *got != (UpstreamSuggestion{Branch: "local-only", Remote: "origin", RemoteRefExists: false}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("suggests nothing on a detached HEAD — there is no branch to track with", func(t *testing.T) {
		testutil.Git(t, repo, "checkout", "--detach", "main")
		if got := suggest(); got != nil {
			t.Fatalf("got %+v", got)
		}
	})
}

// newFeatures: leftover git locks > reports a lock with its age and kind, and refuses to delete a fresh one
func TestDLocksFreshLockStays(t *testing.T) {
	repo := testutil.CloneFixture(t, "stacked-branches")
	gitDir := filepath.Join(repo, ".git")
	testutil.WriteFile(t, gitDir, "index.lock", "")
	s, ctx := New(nil, nil), context.Background()
	fresh, err := s.StaleLocks(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	var index *GitLockFile
	for i := range fresh {
		if fresh[i].Path == "index.lock" {
			index = &fresh[i]
		}
	}
	if index == nil || index.Kind != "index" {
		t.Fatalf("locks = %+v", fresh)
	}
	// Just created: a running git could own it, so it stays.
	if n, _ := s.ClearLocks(ctx, repo, []string{"index.lock"}); n != 0 {
		t.Fatalf("cleared %d", n)
	}
	if !dExists(filepath.Join(gitDir, "index.lock")) {
		t.Fatal("fresh lock was removed")
	}
}

// newFeatures: leftover git locks > removes an old lock, including one on a single ref
func TestDLocksOldLocksGo(t *testing.T) {
	repo := testutil.CloneFixture(t, "stacked-branches")
	gitDir := filepath.Join(repo, ".git")
	refLock := filepath.Join(gitDir, "refs", "heads", "feature", "api.lock")
	testutil.WriteFile(t, gitDir, "index.lock", "")
	testutil.WriteFile(t, gitDir, "refs/heads/feature/api.lock", "")
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{filepath.Join(gitDir, "index.lock"), refLock} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	s, ctx := New(nil, nil), context.Background()
	locks, _ := s.StaleLocks(ctx, repo)
	for _, l := range locks {
		if l.AgeSeconds <= 60 {
			t.Fatalf("lock %+v is too young", l)
		}
		if l.Path == "refs/heads/feature/api.lock" && l.Kind != "ref" {
			t.Fatalf("lock %+v should be a ref lock", l)
		}
	}
	if n, _ := s.ClearLocks(ctx, repo, []string{"index.lock", "refs/heads/feature/api.lock"}); n != 2 {
		t.Fatalf("cleared %d", n)
	}
	if dExists(filepath.Join(gitDir, "index.lock")) || dExists(refLock) {
		t.Fatal("old locks were not removed")
	}
	if after, _ := s.StaleLocks(ctx, repo); len(after) != 0 {
		t.Fatalf("locks left: %+v", after)
	}
}

// newFeatures: leftover git locks > refuses a path that escapes the git directory or is not a lock
func TestDLocksRefuseEscape(t *testing.T) {
	repo := testutil.CloneFixture(t, "stacked-branches")
	testutil.WriteFile(t, repo, "victim.txt", "still here")
	if n, _ := New(nil, nil).ClearLocks(context.Background(), repo, []string{"../victim.txt", "../../etc/hosts", "config"}); n != 0 {
		t.Fatalf("cleared %d", n)
	}
	if !dExists(filepath.Join(repo, "victim.txt")) {
		t.Fatal("victim.txt was removed")
	}
}

// gitOps: clone options > lists the branches a remote advertises
func TestDRemoteBranches(t *testing.T) {
	source := testutil.CloneFixture(t, "merge-conflict")
	names, err := New(nil, nil).RemoteBranches(context.Background(), source, "", "")
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
		if strings.HasPrefix(n, "refs/") {
			t.Fatalf("unstripped name %q", n)
		}
	}
	if !has["main"] || !has["feature"] || !sort.StringsAreSorted(names) {
		t.Fatalf("names = %v", names)
	}
}

func dCountCommits(t *testing.T, repo string) string {
	return testutil.Git(t, repo, "rev-list", "--count", "HEAD")
}

// gitOps: clone options > clones the whole history when no options are given
func TestDCloneFull(t *testing.T) {
	dIsolate(t)
	source := testutil.CloneFixture(t, "merge-conflict")
	target, err := New(nil, nil).Clone(context.Background(), t.TempDir(), source, "full", CloneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if dCountCommits(t, target) == "1" {
		t.Fatal("expected the whole history")
	}
}

// gitOps: clone options > keeps only the requested number of commits when shallow
func TestDCloneShallow(t *testing.T) {
	dIsolate(t)
	source := testutil.CloneFixture(t, "merge-conflict")
	var mu sync.Mutex
	var events []CloneProgress
	s := New(dEmitterFunc(func(name string, data any) {
		if name == EventCloneProgress {
			mu.Lock()
			events = append(events, data.(CloneProgress))
			mu.Unlock()
		}
	}), nil)
	target, err := s.Clone(context.Background(), t.TempDir(), source, "shallow", CloneOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if dCountCommits(t, target) != "1" || !dExists(filepath.Join(target, ".git", "shallow")) {
		t.Fatal("clone is not shallow")
	}
	// The file:// transport reports progress. Each event is parsed from one
	// stderr chunk as it arrives (gitx serializes the chunk callbacks, so
	// events reach the emitter in git's order); like simple-git, only a
	// chunk's first line counts, so a busy pipe that merges lines skips some.
	if len(events) == 0 {
		t.Fatal("no clone:progress events")
	}
	for _, e := range events {
		if e.Stage == "" || e.Progress < 0 || e.Progress > 100 || e.Processed > e.Total {
			t.Fatalf("malformed event %+v", e)
		}
	}
}

// The progress parser is simple-git's: the first progress line of a chunk,
// its label's first word lower-cased as the stage.
func TestDParseCloneProgress(t *testing.T) {
	cases := []struct {
		chunk string
		want  CloneProgress
		ok    bool
	}{
		{"Receiving objects:  45% (9/20)\r", CloneProgress{Stage: "receiving", Progress: 45, Processed: 9, Total: 20}, true},
		{"remote: Counting objects: 100% (6/6), done.\n", CloneProgress{Stage: "remote:", Progress: 100, Processed: 6, Total: 6}, true},
		{"Resolving deltas:   0% (0/3)\rResolving deltas: 100% (3/3)", CloneProgress{Stage: "resolving", Progress: 0, Processed: 0, Total: 3}, true},
		{"Cloning into 'x'...\n", CloneProgress{}, false},
	}
	for _, c := range cases {
		got, ok := dParseCloneProgress(c.chunk)
		if ok != c.ok || got != c.want {
			t.Errorf("%q → %+v %v, want %+v %v", c.chunk, got, ok, c.want, c.ok)
		}
	}
}

type dEmitterFunc func(name string, data any)

func (f dEmitterFunc) Emit(name string, data any) { f(name, data) }

// gitOps: clone options > checks out the branch it was asked for instead of the default
func TestDCloneBranch(t *testing.T) {
	dIsolate(t)
	source := testutil.CloneFixture(t, "merge-conflict")
	target, err := New(nil, nil).Clone(context.Background(), t.TempDir(), source, "onbranch", CloneOptions{Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, target, "branch", "--show-current"); got != "feature" {
		t.Fatalf("current = %q", got)
	}
}

// gitOps: clone options > fetches one branch only when asked, and every branch otherwise
func TestDCloneSingleBranch(t *testing.T) {
	dIsolate(t)
	source := testutil.CloneFixture(t, "merge-conflict")
	s, ctx := New(nil, nil), context.Background()
	yes := true
	one, err := s.Clone(ctx, t.TempDir(), source, "single", CloneOptions{SingleBranch: &yes})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.Clone(ctx, t.TempDir(), source, "every", CloneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	remotes := func(repo string) []string {
		return strings.Fields(testutil.Git(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/remotes"))
	}
	if len(remotes(one)) >= len(remotes(all)) {
		t.Fatalf("single %v vs every %v", remotes(one), remotes(all))
	}
	// The branch that was checked out is the one that came along.
	if !strings.Contains(strings.Join(remotes(one), " "), "main") {
		t.Fatalf("single = %v", remotes(one))
	}
}

// gitOps: clone options > refuses to clone over an existing folder
func TestDCloneRefusesExisting(t *testing.T) {
	dIsolate(t)
	source := testutil.CloneFixture(t, "merge-conflict")
	parent := t.TempDir()
	s, ctx := New(nil, nil), context.Background()
	if _, err := s.Clone(ctx, parent, source, "taken", CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Clone(ctx, parent, source, "taken", CloneOptions{})
	dMustMatch(t, err, `already exists`)
}

func dPorcelain(t *testing.T, repo string) string {
	return testutil.Git(t, repo, "status", "--porcelain")
}

// gitOps: gitignore + untrack > adds patterns to .gitignore without duplicating
func TestDAddToGitignore(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	testutil.WriteFile(t, repo, "debug.log", "noise\n")
	if !strings.Contains(dPorcelain(t, repo), "debug.log") {
		t.Fatal("debug.log should show as untracked first")
	}
	s, ctx := New(nil, nil), context.Background()
	added, err := s.AddToGitignore(ctx, repo, []string{"/debug.log"})
	if err != nil || len(added) != 1 || added[0] != "/debug.log" {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	if !strings.Contains(dReadFile(t, filepath.Join(repo, ".gitignore")), "/debug.log") {
		t.Fatal(".gitignore lacks the pattern")
	}
	if strings.Contains(dPorcelain(t, repo), "debug.log") {
		t.Fatal("debug.log should now be ignored")
	}
	if again, _ := s.AddToGitignore(ctx, repo, []string{"/debug.log"}); len(again) != 0 {
		t.Fatalf("re-adding added %v", again)
	}
}

// gitOps: gitignore + untrack > stops tracking a file but keeps it on disk
func TestDUntrackKeepsFile(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	if err := New(nil, nil).Untrack(context.Background(), repo, []string{"math.js"}, false); err != nil {
		t.Fatal(err)
	}
	if !dExists(filepath.Join(repo, "math.js")) || !strings.Contains(dPorcelain(t, repo), "D  math.js") {
		t.Fatalf("status = %q", dPorcelain(t, repo))
	}
}

// gitOps: gitignore + untrack > deletes a tracked file from Git and disk
func TestDUntrackDeletesFile(t *testing.T) {
	repo := testutil.CloneFixture(t, "bisect-bug")
	if err := New(nil, nil).Untrack(context.Background(), repo, []string{"tax.js"}, true); err != nil {
		t.Fatal(err)
	}
	if dExists(filepath.Join(repo, "tax.js")) || !strings.Contains(dPorcelain(t, repo), "D  tax.js") {
		t.Fatalf("status = %q", dPorcelain(t, repo))
	}
}

// newFeatures: remote branch worktree checkout > starts the new local branch at the selected remote ref
func TestDWorktreeAddFromRemoteRef(t *testing.T) {
	dIsolate(t)
	root := t.TempDir()
	repo, worktree := filepath.Join(root, "repo"), filepath.Join(root, "worktree")
	testutil.Git(t, root, "init", "-q", "-b", "main", repo)
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

	if err := New(nil, nil).WorktreeAdd(context.Background(), repo, worktree, "feature/topic", true, "origin/feature/topic"); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, worktree, "branch", "--show-current"); got != "feature/topic" {
		t.Fatalf("branch = %q", got)
	}
	if dReadFile(t, filepath.Join(worktree, "state.txt")) != "remote\n" {
		t.Fatal("worktree is not at the remote ref")
	}
}

// gitService.test: submodules-worktrees > lists the main worktree plus two linked worktrees
func TestDWorktreesLinked(t *testing.T) {
	wts, err := New(nil, nil).Worktrees(context.Background(), testutil.PlaygroundRepo(t, "submodules-worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	mains, branches := 0, map[string]bool{}
	for _, w := range wts {
		if w.IsMain {
			mains++
		}
		if w.Branch != nil {
			branches[*w.Branch] = true
		}
	}
	if len(wts) != 3 || mains != 1 || !branches["release/1.x"] || !branches["hotfix/login"] {
		t.Fatalf("worktrees = %+v", wts)
	}
}

// newFeatures: WIP snapshots > guards a discard: the destroyed state lands in a guard snapshot
func TestDDiscardTakesGuardSnapshot(t *testing.T) {
	// Real dates: snapshots are listed newest first by commit date.
	testutil.IsolateGitEnv(t)
	repo := testutil.CloneFixture(t, "snapshots")
	before := len(dListSnapshotRefs(context.Background(), repo))
	if err := New(nil, nil).Discard(context.Background(), repo, []string{"draft.md"}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dReadFile(t, filepath.Join(repo, "draft.md")), "Even more uncommitted edits.") {
		t.Fatal("draft.md was not discarded")
	}
	after := dListSnapshotRefs(context.Background(), repo)
	if len(after) != before+1 || !strings.HasSuffix(after[0].ref, "-g") {
		t.Fatalf("snapshots %d → %+v", before, after)
	}
	// restoreSnapshot (slice A) is `git restore --source <sha> --worktree -- <files>`.
	testutil.Git(t, repo, "restore", "--source", after[0].sha, "--worktree", "--", "draft.md")
	if !strings.Contains(dReadFile(t, filepath.Join(repo, "draft.md")), "Even more uncommitted edits.") {
		t.Fatal("the guard snapshot does not hold the discarded edits")
	}
}
