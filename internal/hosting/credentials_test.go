package hosting

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// fakeClock is a settable now().
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countingRunner answers `git credential fill` with reply and counts calls.
type countingRunner struct {
	mu    sync.Mutex
	reply string
	fail  bool
	calls []Command
}

func (r *countingRunner) run(_ context.Context, cmd Command) CommandResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, cmd)
	if r.fail {
		return CommandResult{Err: os.ErrNotExist}
	}
	return CommandResult{Stdout: r.reply}
}

func TestCredentialCache(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := &countingRunner{reply: "username=u\npassword=p\n"}
	c := NewCredentials(r.run, clock.now)

	t.Run("caches a hit per host", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if got := c.CredentialFor(ctx, "https://github.com/a/b.git", FillOpts{}); got == nil || got.Password != "p" {
				t.Fatalf("got %+v", got)
			}
		}
		c.CredentialFor(ctx, "https://github.com/other/repo", FillOpts{})
		if len(r.calls) != 1 {
			t.Errorf("spawned %d times, want 1", len(r.calls))
		}
	})
	t.Run("keys Azure DevOps per organization", func(t *testing.T) {
		before := len(r.calls)
		c.CredentialFor(ctx, "https://dev.azure.com/orgA/p/_git/r", FillOpts{})
		c.CredentialFor(ctx, "https://dev.azure.com/orgB/p/_git/r", FillOpts{})
		c.CredentialFor(ctx, "https://dev.azure.com/orgA/q/_git/s", FillOpts{})
		if got := len(r.calls) - before; got != 2 {
			t.Errorf("spawned %d times, want 2", got)
		}
	})
	t.Run("expires after the TTL", func(t *testing.T) {
		before := len(r.calls)
		clock.advance(credentialTTL)
		c.CredentialFor(ctx, "https://github.com/a/b.git", FillOpts{})
		if len(r.calls) != before+1 {
			t.Error("expected a fresh lookup after the TTL")
		}
	})
	t.Run("a cached miss is honoured, except by an interactive ask", func(t *testing.T) {
		miss := &countingRunner{fail: true}
		c := NewCredentials(miss.run, clock.now)
		c.CredentialFor(ctx, "https://gitlab.com/a/b", FillOpts{})
		c.CredentialFor(ctx, "https://gitlab.com/a/b", FillOpts{})
		if len(miss.calls) != 1 {
			t.Fatalf("background polling respawned git: %d", len(miss.calls))
		}
		c.CredentialFor(ctx, "https://gitlab.com/a/b", FillOpts{Interactive: true})
		if len(miss.calls) != 2 {
			t.Fatal("an interactive ask must ignore a cached miss")
		}
		last := miss.calls[1]
		if strings.Contains(strings.Join(last.Args, " "), "credential.interactive=false") || len(last.Env) != 0 || last.Timeout != timeoutInteractive {
			t.Errorf("interactive command = %+v", last)
		}
	})
	t.Run("ssh remotes never reach the helper", func(t *testing.T) {
		before := len(r.calls)
		if c.CredentialFor(ctx, "git@github.com:a/b.git", FillOpts{}) != nil || len(r.calls) != before {
			t.Error("ssh remote must not be looked up")
		}
	})
	t.Run("forget evicts the cache and rejects", func(t *testing.T) {
		c.ForgetCredential(ctx, "https://github.com/a/b.git", GitCredential{Username: "u", Password: "p"})
		last := r.calls[len(r.calls)-1]
		if last.Args[len(last.Args)-1] != "reject" || !strings.Contains(last.Stdin, "password=p\n") {
			t.Errorf("reject = %+v", last)
		}
		before := len(r.calls)
		c.CredentialFor(ctx, "https://github.com/a/b.git", FillOpts{})
		if len(r.calls) != before+1 {
			t.Error("forgotten credential still cached")
		}
	})
	t.Run("ClearCache drops everything", func(t *testing.T) {
		c.ClearCache()
		before := len(r.calls)
		c.CredentialFor(ctx, "https://github.com/a/b.git", FillOpts{})
		if len(r.calls) != before+1 {
			t.Error("cache survived ClearCache")
		}
	})
	t.Run("repo-local config applies with RepoPath", func(t *testing.T) {
		c.ClearCache()
		c.CredentialFor(ctx, "https://bitbucket.org/a/b", FillOpts{RepoPath: "/some/repo"})
		last := r.calls[len(r.calls)-1]
		if last.Args[0] != "-C" || last.Args[1] != "/some/repo" {
			t.Errorf("args = %v", last.Args)
		}
	})
}

func TestGithubCliAuthStatus(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	t.Run("caches a verdict for its TTL", func(t *testing.T) {
		var n atomic.Int32
		c := NewCredentials(func(context.Context, Command) CommandResult {
			n.Add(1)
			return CommandResult{Err: os.ErrPermission}
		}, clock.now)
		for i := 0; i < 3; i++ {
			if got := c.GithubCliAuthStatus(ctx); got != GHSignedOut {
				t.Fatalf("got %s", got)
			}
		}
		clock.advance(ghStatusTTL)
		c.GithubCliAuthStatus(ctx)
		if n.Load() != 2 {
			t.Errorf("probed %d times, want 2", n.Load())
		}
	})
	t.Run("an inconclusive probe is not remembered", func(t *testing.T) {
		var n atomic.Int32
		c := NewCredentials(func(context.Context, Command) CommandResult {
			n.Add(1)
			return CommandResult{Err: context.DeadlineExceeded, TimedOut: true}
		}, clock.now)
		c.GithubCliAuthStatus(ctx)
		c.GithubCliAuthStatus(ctx)
		if n.Load() != 2 {
			t.Errorf("probed %d times, want 2", n.Load())
		}
	})
	t.Run("concurrent callers share one probe", func(t *testing.T) {
		var n atomic.Int32
		release := make(chan struct{})
		c := NewCredentials(func(context.Context, Command) CommandResult {
			n.Add(1)
			<-release
			return CommandResult{}
		}, clock.now)
		var wg sync.WaitGroup
		results := make([]GitHubCLIAuthStatus, 4)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = c.GithubCliAuthStatus(ctx)
			}()
		}
		// Let every caller reach the in-flight probe before it answers.
		deadline := time.Now().Add(2 * time.Second)
		for n.Load() == 0 && time.Now().Before(deadline) {
			runtime.Gosched()
		}
		time.Sleep(20 * time.Millisecond)
		close(release)
		wg.Wait()
		if n.Load() != 1 {
			t.Errorf("probed %d times, want 1", n.Load())
		}
		for _, r := range results {
			if r != GHAuthenticated {
				t.Errorf("result %s", r)
			}
		}
	})
	t.Run("maps how the process ended", func(t *testing.T) {
		cases := map[GitHubCLIAuthStatus]CommandResult{
			GHAuthenticated: {},
			GHMissing:       {Err: os.ErrNotExist, NotFound: true},
			GHUnknown:       {Err: context.DeadlineExceeded, TimedOut: true},
			GHSignedOut:     {Err: os.ErrPermission},
		}
		for want, res := range cases {
			c := NewCredentials(func(_ context.Context, cmd Command) CommandResult {
				if cmd.Name != "gh" || strings.Join(cmd.Args, " ") != "auth status --hostname github.com" || cmd.Timeout != timeoutSilent {
					t.Errorf("command = %+v", cmd)
				}
				return res
			}, clock.now)
			if got := c.GithubCliAuthStatus(ctx); got != want {
				t.Errorf("got %s want %s", got, want)
			}
		}
	})
}

// The real runner against a real git: a credential helper script answers
// `git credential fill`, isolated from the user's global config.
func TestExecRunnerWithRealGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper")
	}
	testutil.IsolateGitEnv(t)
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper.sh")
	script := "#!/bin/sh\nif [ \"$1\" = get ]; then cat >/dev/null; printf 'username=real\\npassword=from-helper\\n'; fi\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := testutil.InitRepo(t)
	testutil.Git(t, repo, "config", "credential.helper", helper)

	ctx := context.Background()
	c := NewCredentials(nil, nil)
	got := c.CredentialFor(ctx, "https://example.test/org/repo.git", FillOpts{RepoPath: repo})
	if got == nil || got.Username != "real" || got.Password != "from-helper" {
		t.Fatalf("got %+v", got)
	}
	if !c.HasCredentialHelper(ctx, repo) {
		t.Error("HasCredentialHelper = false with a helper configured")
	}
	if c.HasCredentialHelper(ctx, t.TempDir()) {
		t.Error("HasCredentialHelper = true outside the repo")
	}
	// With no helper and prompts off, git fails and the lookup is a miss.
	if got := NewCredentials(nil, nil).CredentialFor(ctx, "https://example.test/x/y", FillOpts{}); got != nil {
		t.Errorf("no helper: got %+v", got)
	}
}

func TestExecRunnerMissingBinary(t *testing.T) {
	res := ExecRunner(context.Background(), Command{Name: "gitcito-no-such-binary-xyz", Timeout: time.Second})
	if res.Err == nil || !res.NotFound {
		t.Errorf("got %+v", res)
	}
}
