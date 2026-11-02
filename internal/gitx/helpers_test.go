package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

func TestResolveHooksDir(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	env := testutil.IsolateGitEnv(t)

	got, custom := ResolveHooksDir(ctx, dir)
	if custom || got != filepath.Join(dir, ".git", "hooks") {
		t.Fatalf("default = %q,%v", got, custom)
	}
	tests := []struct{ set, want string }{
		{".githooks", filepath.Join(dir, ".githooks")},
		{"/abs/hooks", "/abs/hooks"},
		{"~/myhooks", filepath.Join(env.Home, "myhooks")},
		{"~", env.Home},
	}
	for _, tt := range tests {
		testutil.Git(t, dir, "config", "core.hooksPath", tt.set)
		got, custom := ResolveHooksDir(ctx, dir)
		if !custom || got != tt.want {
			t.Errorf("hooksPath %q → %q,%v want %q", tt.set, got, custom, tt.want)
		}
	}
}

func TestBranchAndRemoteHelpers(t *testing.T) {
	ctx := context.Background()
	testutil.IsolateGitEnv(t)
	origin := testutil.InitRepo(t)
	testutil.WriteFile(t, origin, "f", "1\n")
	testutil.Git(t, origin, "add", "f")
	testutil.Git(t, origin, "commit", "-q", "-m", "c1")

	clone := filepath.Join(t.TempDir(), "clone")
	testutil.Git(t, origin, "clone", "-q", origin, clone)
	testutil.Git(t, clone, "config", "user.name", "T")
	testutil.Git(t, clone, "config", "user.email", "t@x")
	testutil.Git(t, clone, "branch", "feature")

	if got := LocalBranches(ctx, clone); !reflect.DeepEqual(got, []string{"feature", "main"}) {
		t.Errorf("LocalBranches = %q", got)
	}
	if got := LocalBranches(ctx, filepath.Join(clone, "nope")); len(got) != 0 || got == nil {
		t.Errorf("LocalBranches(missing) = %#v", got)
	}
	if got := GetRemoteURL(ctx, clone, "origin"); got != origin {
		t.Errorf("GetRemoteURL = %q", got)
	}
	if got := GetRemoteURL(ctx, clone, "nope"); got != "" {
		t.Errorf("GetRemoteURL(nope) = %q", got)
	}
	if got := BranchUpstream(ctx, clone, "main"); got != "origin/main" {
		t.Errorf("BranchUpstream(main) = %q", got)
	}
	if got := BranchUpstream(ctx, clone, "feature"); got != "" {
		t.Errorf("BranchUpstream(feature) = %q", got)
	}
	if got := UpstreamRemote(ctx, clone); got != "origin" {
		t.Errorf("UpstreamRemote = %q", got)
	}
	testutil.Git(t, clone, "remote", "add", "upstream", origin)
	testutil.Git(t, clone, "fetch", "-q", "upstream")
	testutil.Git(t, clone, "branch", "-u", "upstream/main", "main")
	if got := UpstreamRemote(ctx, clone); got != "upstream" {
		t.Errorf("UpstreamRemote = %q", got)
	}
	testutil.Git(t, clone, "checkout", "-q", "feature")
	if got := UpstreamRemote(ctx, clone); got != "origin" {
		t.Errorf("UpstreamRemote(no upstream) = %q", got)
	}

	// Divergence: two local commits, one remote commit.
	testutil.Git(t, clone, "checkout", "-q", "main")
	testutil.WriteFile(t, clone, "g", "x\n")
	testutil.Git(t, clone, "add", "g")
	testutil.Git(t, clone, "commit", "-q", "-m", "l1")
	testutil.Git(t, clone, "commit", "-q", "--allow-empty", "-m", "l2")
	testutil.Git(t, origin, "commit", "-q", "--allow-empty", "-m", "r1")
	testutil.Git(t, clone, "fetch", "-q", "origin")
	tr, err := Divergence(ctx, clone, "main", "origin/main")
	if err != nil || tr != (Track{Ahead: 2, Behind: 1}) {
		t.Errorf("Divergence = %+v %v", tr, err)
	}
	if _, err := Divergence(ctx, clone, "main", "nope"); err == nil {
		t.Error("Divergence with a bad ref should fail")
	}
}

func TestWithRemoteAuth(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	remote := "https://github.com/acme/widget.git"
	testutil.Git(t, dir, "remote", "add", "origin", remote)

	tokenFor := func(want string) TokenFunc {
		return func(_ context.Context, host, repoPath string) string {
			if host != HostGitHub || repoPath != dir {
				t.Errorf("token asked for %q %q", host, repoPath)
			}
			return want
		}
	}

	t.Run("injects then restores", func(t *testing.T) {
		var during string
		err := WithRemoteAuth(ctx, dir, "origin", tokenFor("ghp_TOKEN"), func(context.Context) error {
			during = testutil.Git(t, dir, "remote", "get-url", "origin")
			return nil
		})
		if err != nil || during != "https://oauth2:ghp_TOKEN@github.com/acme/widget.git" {
			t.Fatalf("during=%q err=%v", during, err)
		}
		if after := testutil.Git(t, dir, "remote", "get-url", "origin"); after != remote {
			t.Fatalf("after = %q", after)
		}
	})
	t.Run("restores after a failing op", func(t *testing.T) {
		boom := errors.New("boom")
		err := WithRemoteAuth(ctx, dir, "origin", tokenFor("ghp_TOKEN"), func(context.Context) error { return boom })
		if err != boom {
			t.Fatal(err)
		}
		if after := testutil.Git(t, dir, "remote", "get-url", "origin"); after != remote {
			t.Fatalf("token left on disk: %q", after)
		}
	})
	t.Run("restores even when the context is cancelled", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		_ = WithRemoteAuth(cctx, dir, "origin", tokenFor("ghp_TOKEN"), func(context.Context) error {
			cancel()
			return nil
		})
		if after := testutil.Git(t, dir, "remote", "get-url", "origin"); after != remote {
			t.Fatalf("token left on disk: %q", after)
		}
	})
	t.Run("no token runs as is", func(t *testing.T) {
		ran := false
		err := WithRemoteAuth(ctx, dir, "origin", tokenFor(""), func(context.Context) error {
			ran = true
			if got := testutil.Git(t, dir, "remote", "get-url", "origin"); got != remote {
				t.Errorf("url changed: %q", got)
			}
			return nil
		})
		if err != nil || !ran {
			t.Fatal(err)
		}
		if err := WithRemoteAuth(ctx, dir, "origin", nil, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCommitIdentity(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	testutil.Git(t, dir, "-c", "user.name=Ada L", "-c", "user.email=ada@x", "commit", "-q", "--allow-empty",
		"-m", "subject", "-m", "body line", "--date=2020-01-02T03:04:05+00:00")
	id, err := CommitIdentity(ctx, dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Ada L" || id.Email != "ada@x" || id.Date != "2020-01-02T03:04:05+00:00" || id.Message != "subject\n\nbody line" {
		t.Fatalf("id = %+v", id)
	}
	if got := AuthorEnv(id); !reflect.DeepEqual(got, []string{"GIT_AUTHOR_NAME=Ada L", "GIT_AUTHOR_EMAIL=ada@x", "GIT_AUTHOR_DATE=2020-01-02T03:04:05+00:00"}) {
		t.Fatalf("AuthorEnv = %q", got)
	}
	if _, err := CommitIdentity(ctx, dir, "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestBlobHelpers(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t) // a.txt = "hello\n" committed
	testutil.WriteFile(t, dir, "img.png", "PNGDATA")

	if n, ok := BlobSize(ctx, dir, "a.txt", ""); !ok || n != 6 {
		t.Errorf("BlobSize(worktree) = %d,%v", n, ok)
	}
	if n, ok := BlobSize(ctx, dir, "a.txt", "HEAD"); !ok || n != 6 {
		t.Errorf("BlobSize(HEAD) = %d,%v", n, ok)
	}
	if _, ok := BlobSize(ctx, dir, "missing", ""); ok {
		t.Error("BlobSize(missing worktree) should be unknowable")
	}
	if _, ok := BlobSize(ctx, dir, "missing", "HEAD"); ok {
		t.Error("BlobSize(missing at HEAD) should be unknowable")
	}

	if err := AssertUnderCap(ctx, dir, "a.txt", "HEAD", 6); err != nil {
		t.Errorf("at cap: %v", err)
	}
	if err := AssertUnderCap(ctx, dir, "a.txt", "", 5); err == nil || err.Error() != "GITCITO_FILE_TOO_LARGE:6" {
		t.Errorf("over cap: %v", err)
	}
	if err := AssertUnderCap(ctx, dir, "missing", "", 0); err != nil {
		t.Errorf("unknown size passes: %v", err)
	}

	if got, ok := ReadFileDataURL(ctx, dir, "img.png", ""); !ok || got != "data:image/png;base64,UE5HREFUQQ==" {
		t.Errorf("ReadFileDataURL(worktree) = %q,%v", got, ok)
	}
	if got, ok := ReadFileDataURL(ctx, dir, "a.txt", "HEAD"); !ok || got != "data:application/octet-stream;base64,aGVsbG8K" {
		t.Errorf("ReadFileDataURL(HEAD) = %q,%v", got, ok)
	}
	if _, ok := ReadFileDataURL(ctx, dir, "img.png", "HEAD"); ok {
		t.Error("absent at HEAD should be not-ok")
	}
}

func TestAutoStash(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)

	if dirty, err := IsDirty(ctx, dir); err != nil || dirty {
		t.Fatalf("clean repo dirty=%v err=%v", dirty, err)
	}
	ran := false
	if err := WithAutoStash(ctx, dir, "pull", func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatal(err)
	}
	if testutil.Git(t, dir, "stash", "list") != "" {
		t.Fatal("clean tree must not stash")
	}

	testutil.WriteFile(t, dir, "untracked.txt", "u\n")
	testutil.WriteFile(t, dir, "a.txt", "changed\n")
	err := WithAutoStash(ctx, dir, "pull", func(context.Context) error {
		if d, _ := IsDirty(ctx, dir); d {
			t.Error("op ran on a dirty tree")
		}
		if got := testutil.Git(t, dir, "stash", "list", "--format=%gs"); !strings.HasSuffix(got, "Auto-stash before pull") {
			t.Errorf("stash = %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "changed\n" {
		t.Fatal("changes not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "untracked.txt")); err != nil {
		t.Fatal("untracked file not restored")
	}

	boom := errors.New("boom")
	if err := WithAutoStash(ctx, dir, "rebase", func(context.Context) error { return boom }); err != boom {
		t.Fatal(err)
	}
	if got := testutil.Git(t, dir, "stash", "list", "--format=%gs"); !strings.Contains(got, "Auto-stash before rebase") {
		t.Fatalf("failed op must leave the named stash: %q", got)
	}
}

func TestCleanCandidates(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, ".gitignore", "*.log\n")
	testutil.WriteFile(t, dir, "dist/a.js", "x")
	testutil.WriteFile(t, dir, "dist/b.js", "x")
	testutil.WriteFile(t, dir, "debug.log", "x")
	untracked, ignored := CleanCandidates(ctx, dir)
	if !reflect.DeepEqual(untracked, []string{".gitignore", "dist/"}) || !reflect.DeepEqual(ignored, []string{"debug.log"}) {
		t.Fatalf("untracked=%q ignored=%q", untracked, ignored)
	}
	u, i := CleanCandidates(ctx, filepath.Join(dir, "nope"))
	if u == nil || i == nil || len(u)+len(i) != 0 {
		t.Fatalf("missing repo: %#v %#v", u, i)
	}
}

func TestRunSubtree(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	if _, err := RunSubtree(ctx, dir, []string{"--help"}); err == nil {
		// `git subtree --help` exits 129 printing usage; either way it ran.
		t.Log("subtree --help exited 0")
	}
	if runtime.GOOS == "windows" {
		return
	}
	// A git without the contrib script: point git's exec path at an empty dir.
	t.Setenv("GIT_EXEC_PATH", t.TempDir())
	_, err := RunSubtree(ctx, dir, []string{"split", "--prefix=x"})
	if err == nil || err.Error() != ErrNoSubtree {
		t.Fatalf("err = %v", err)
	}
}

func TestBinaries(t *testing.T) {
	ctx := context.Background()
	if !HasBinary("git") || HasBinary("gitcito-no-such-tool") || HasBinary("") {
		t.Fatal("HasBinary")
	}
	tool := filepath.Join(t.TempDir(), "conv")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	noexec := filepath.Join(t.TempDir(), "noexec")
	if err := os.WriteFile(noexec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	conv := []struct {
		in   string
		want bool
	}{
		{"git --no-pager", true},
		{"  git", true},
		{"gitcito-no-such-tool --x", false},
		{`"` + tool + `" --flag`, true},
		{tool, true},
		{"/nonexistent/conv", false},
		{"", false},
	}
	if runtime.GOOS != "windows" {
		conv = append(conv, struct {
			in   string
			want bool
		}{noexec, false})
	}
	for _, tt := range conv {
		if got := ConverterAvailable(tt.in); got != tt.want {
			t.Errorf("ConverterAvailable(%q) = %v", tt.in, got)
		}
	}

	helpers := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"cache --timeout=3600", true},
		{"store", true},
		{"!git credential-cache", true},
		{`!"git" x`, true},
		{"!gitcito-no-such-tool get", false},
		{"/nonexistent/helper", false},
		{tool, true},
		{"gitcito-no-such-helper", false},
	}
	for _, tt := range helpers {
		if got := CredentialHelperExists(ctx, tt.in); got != tt.want {
			t.Errorf("CredentialHelperExists(%q) = %v", tt.in, got)
		}
	}

	cands := CredentialCandidates(ctx)
	rec := 0
	for _, c := range cands {
		if c.Recommended {
			rec++
		}
		if (c.Name == "cache" || c.Name == "store") && !c.Available {
			t.Errorf("%s is built into git", c.Name)
		}
	}
	if rec != 1 || len(cands) < 3 {
		t.Fatalf("candidates = %+v", cands)
	}
	if runtime.GOOS == "linux" && cands[0].Name != "libsecret" {
		t.Fatalf("linux candidates = %+v", cands)
	}
}

func TestRunFilterCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX tools")
	}
	ctx := context.Background()
	dir := t.TempDir()
	tests := []struct {
		name, cmd, file, input, want, wantErr string
	}{
		{name: "stdin to stdout", cmd: "tr a-z A-Z", input: "abc", want: "ABC"},
		{name: "%f is single-quoted", cmd: "printf '%s' %f", file: "it's a file.txt", want: "it's a file.txt"},
		{name: "stderr is the error", cmd: "echo '  nope  ' >&2; exit 2", wantErr: "nope"},
		{name: "silent failure", cmd: "exit 4", wantErr: "filter command exited with 4"},
		{name: "ignores unread stdin", cmd: "true", input: strings.Repeat("x", 1<<20), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := RunFilterCommand(ctx, tt.cmd, dir, tt.file, []byte(tt.input))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || string(out) != tt.want {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := runFilterCommand(ctx, "sleep 30", dir, "f", nil, 100*time.Millisecond)
		if err == nil || err.Error() != "filter command timed out after 0s" {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("not killed promptly")
		}
	})
	t.Run("timeout message", func(t *testing.T) {
		if FilterTimeout != 10*time.Second {
			t.Fatal("FilterTimeout drifted from TS")
		}
	})
}

func TestDiskBytes(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFile(t, dir, "a", strings.Repeat("x", 10))
	testutil.WriteFile(t, dir, "sub/b", strings.Repeat("y", 5000))
	testutil.WriteFile(t, dir, "sub/deeper/c", "z")
	outside := t.TempDir()
	testutil.WriteFile(t, outside, "huge", strings.Repeat("h", 100000))
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
	}

	st, err := os.Stat(filepath.Join(dir, "sub", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if got := DiskBytes(st); got < 5000 {
		t.Fatalf("DiskBytes = %d", got)
	}

	budget := CleanWalkCap
	total := DirBytes(context.Background(), dir, &budget)
	if total < 5011 || total >= 100000 {
		t.Fatalf("DirBytes = %d (symlink followed?)", total)
	}

	tiny := 1
	partial := DirBytes(context.Background(), dir, &tiny)
	if partial >= total {
		t.Fatalf("budget not honoured: %d vs %d", partial, total)
	}
	if tiny > 0 {
		t.Fatalf("budget not consumed: %d", tiny)
	}
}

func TestLoginShellPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no login shell")
	}
	ctx := context.Background()
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "fakeshell")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name, shell, current, want string
		ok                         bool
	}{
		{
			name:    "banner noise and merge order",
			shell:   write(`echo "Welcome!"; echo "__GITCITO_PATH_BEGIN__ /opt/a:/usr/bin::/opt/b __GITCITO_PATH_END__"; echo noise >&2`),
			current: "/usr/bin:/bin:/opt/a",
			want:    "/opt/a:/usr/bin:/opt/b:/bin", ok: true,
		},
		{
			name: "interactive fails, login works",
			shell: write(`case "$1" in -ilc) exit 1;; esac
echo "__GITCITO_PATH_BEGIN__/login/bin__GITCITO_PATH_END__"`),
			current: "/bin", want: "/login/bin:/bin", ok: true,
		},
		{
			name:  "marker on a failing exit is discarded",
			shell: write(`echo "__GITCITO_PATH_BEGIN__/x__GITCITO_PATH_END__"; exit 3`), current: "/bin",
		},
		{name: "no marker", shell: write(`echo hi`), current: "/bin"},
		{name: "empty PATH", shell: write(`echo "__GITCITO_PATH_BEGIN__   __GITCITO_PATH_END__"`), current: "/bin"},
		{name: "missing shell", shell: filepath.Join(t.TempDir(), "nope"), current: "/bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := loginShellPath(ctx, tt.shell, tt.current)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("got %q,%v want %q,%v", got, ok, tt.want, tt.ok)
			}
		})
	}

	t.Run("the script reaches the shell intact", func(t *testing.T) {
		// A real sh must expand ${PATH} between the markers.
		got, ok := loginShellPath(ctx, "/bin/sh", "/gitcito/extra")
		if !ok || !strings.HasSuffix(got, ":/gitcito/extra") {
			t.Fatalf("got %q,%v", got, ok)
		}
	})
}

func TestFixPathOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no-op on Windows")
	}
	shell := filepath.Join(t.TempDir(), "fakeshell")
	marker := filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\necho x >> '" + marker + "'\necho \"__GITCITO_PATH_BEGIN__/fake/bin__GITCITO_PATH_END__\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", "/usr/bin:/bin")

	fixPathState.mu.Lock()
	fixPathState.done = false
	fixPathState.mu.Unlock()

	FixPath(context.Background())
	FixPath(context.Background())
	if got := os.Getenv("PATH"); got != "/fake/bin:/usr/bin:/bin" {
		t.Fatalf("PATH = %q", got)
	}
	calls, _ := os.ReadFile(marker)
	if strings.Count(string(calls), "x") != 1 {
		t.Fatalf("shell ran %d times", strings.Count(string(calls), "x"))
	}
}
