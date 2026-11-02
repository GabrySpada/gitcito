package gitx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// newRepo makes a repository with one commit, under an isolated git env.
func newRepo(t *testing.T) string {
	t.Helper()
	testutil.IsolateGitEnv(t)
	dir := testutil.InitRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "hello\n")
	testutil.Git(t, dir, "add", "a.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func TestRunAndRunGit(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()

	t.Run("success returns raw stdout", func(t *testing.T) {
		res, err := Run(ctx, dir, []string{"log", "-1", "--format=%s"}, RunOpts{})
		if err != nil || res.Stdout != "first\n" || res.ExitCode != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})

	t.Run("silent non-zero exit: Node's Command failed message", func(t *testing.T) {
		_, err := Run(ctx, dir, []string{"config", "--get", "no.such"}, RunOpts{})
		want := "Command failed: git -C " + dir + " config --get no.such\n"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %q, want %q", err, want)
		}
		if code, ok := ExitCode(err); !ok || code != 1 {
			t.Fatalf("ExitCode = %d,%v", code, ok)
		}
		// runGit falls back to the (trimmed) Node message when stderr is empty.
		_, err = RunGit(ctx, dir, []string{"config", "--get", "no.such"}, RunOpts{})
		if err == nil || err.Error() != strings.TrimSpace(want) {
			t.Fatalf("RunGit err = %q", err)
		}
		if code, ok := ExitCode(err); !ok || code != 1 {
			t.Fatalf("RunGit keeps the exit code reachable: %d,%v", code, ok)
		}
	})

	t.Run("RunGit uses trimmed stderr", func(t *testing.T) {
		_, err := RunGit(ctx, dir, []string{"rev-parse", "--verify", "nope"}, RunOpts{})
		if err == nil || err.Error() != "fatal: Needed a single revision" {
			t.Fatalf("err = %q", err)
		}
		var ge *GitError
		if !errors.As(err, &ge) || ge.Exec == nil || ge.Exec.Code != 128 {
			t.Fatalf("GitError = %+v", ge)
		}
	})

	t.Run("RunGit redacts credentials", func(t *testing.T) {
		_, err := RunGit(ctx, dir, []string{"ls-files", "--error-unmatch", "https://user:s3cr3t@host/x"}, RunOpts{})
		if err == nil || err.Error() != "error: pathspec 'https://***@host/x' did not match any file(s) known to git\nDid you forget to 'git add'?" {
			t.Fatalf("err = %q", err)
		}
	})

	t.Run("RunGitFallback", func(t *testing.T) {
		_, err := RunGitFallback(ctx, dir, []string{"rev-parse", "--verify", "nope"}, RunOpts{}, "git ls-remote failed")
		if err == nil || err.Error() != "fatal: Needed a single revision" {
			t.Fatalf("err = %q", err)
		}
	})

	t.Run("env overrides", func(t *testing.T) {
		out, err := RunGit(ctx, dir, []string{"var", "GIT_AUTHOR_IDENT"}, RunOpts{Env: []string{"GIT_AUTHOR_NAME=Override Name"}})
		if err != nil || !strings.HasPrefix(out, "Override Name <") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("stdin", func(t *testing.T) {
		out, err := RunGit(ctx, dir, []string{"hash-object", "--stdin"}, RunOpts{Stdin: []byte("hello\n")})
		if err != nil || strings.TrimSpace(out) != "ce013625030ba8dba906f756967f9e9ca394464a" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("WithStdin ignores the exit status", func(t *testing.T) {
		if got := WithStdin(ctx, dir, []string{"hash-object", "--stdin"}, "hello\n"); strings.TrimSpace(got) != "ce013625030ba8dba906f756967f9e9ca394464a" {
			t.Fatalf("got %q", got)
		}
		if got := WithStdin(ctx, dir, []string{"cat-file", "--batch-check"}, "HEAD\nnope\n"); !strings.Contains(got, "commit") || !strings.Contains(got, "nope missing") {
			t.Fatalf("got %q", got)
		}
		if got := WithStdin(ctx, dir, []string{"no-such-command"}, ""); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("no -C without a repo", func(t *testing.T) {
		res, err := Run(ctx, "", []string{"--version"}, RunOpts{})
		if err != nil || !strings.HasPrefix(res.Stdout, "git version") {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
}

func TestRaw(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	testutil.WriteFile(t, dir, "a.txt", "second\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "second")

	tests := []struct {
		name    string
		repo    string
		args    []string
		allow   []string
		wantOut string
		wantErr string
	}{
		// simple-git: a non-zero exit with nothing on stderr is a success.
		{name: "config --get missing reads as empty", repo: dir, args: []string{"config", "--get", "no.such"}},
		{name: "merge-base --is-ancestor false does not throw", repo: dir, args: []string{"merge-base", "--is-ancestor", "HEAD", "HEAD~1"}},
		{name: "plain read", repo: dir, args: []string{"log", "-1", "--format=%s"}, wantOut: "second\n"},
		{name: "stderr + exit is an error, message untrimmed", repo: dir, args: []string{"rev-parse", "--verify", "nope"},
			wantErr: "fatal: Needed a single revision\n"},
		{name: "missing directory", repo: filepath.Join(dir, "nope"), args: []string{"status"},
			wantErr: "Cannot use simple-git on a directory that does not exist"},
		{name: "unsafe arg refused", repo: dir, args: []string{"-c", "core.editor=true", "status"},
			wantErr: "Configuring core.editor is not permitted without enabling allowUnsafeEditor"},
		{name: "unsafe arg allowed", repo: dir, args: []string{"-c", "core.editor=true", "log", "-1", "--format=%s"},
			allow: []string{"allowUnsafeEditor"}, wantOut: "second\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Raw(ctx, tt.repo, tt.args, RunOpts{AllowUnsafe: tt.allow})
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tt.wantErr || (tt.wantErr == "" && out != tt.wantOut) {
				t.Fatalf("out=%q err=%q; want out=%q err=%q", out, gotErr, tt.wantOut, tt.wantErr)
			}
		})
	}

	t.Run("error message is stdout then stderr", func(t *testing.T) {
		// rev-parse prints what it resolved to stdout, then fails on stderr.
		head := testutil.Git(t, dir, "rev-parse", "HEAD")
		_, err := Raw(ctx, dir, []string{"rev-parse", "HEAD", "nope-not-a-rev"}, RunOpts{})
		if err == nil || !strings.HasPrefix(err.Error(), head+"\nnope-not-a-rev\nfatal: ambiguous argument") {
			t.Fatalf("err = %q", err)
		}
	})

	t.Run("plain env: no GIT_TERMINAL_PROMPT", func(t *testing.T) {
		t.Setenv("GIT_TERMINAL_PROMPT", "")
		os.Unsetenv("GIT_TERMINAL_PROMPT") // t.Setenv restores it afterwards
		res, err := Exec(ctx, dir, "sh", []string{"-c", "printf %s \"${GIT_TERMINAL_PROMPT-unset}\""}, RunOpts{PlainEnv: true})
		if err != nil || res.Stdout != "unset" {
			t.Fatalf("plain env leaked GIT_TERMINAL_PROMPT: %q %v", res.Stdout, err)
		}
		res, err = Exec(ctx, dir, "sh", []string{"-c", "printf %s \"${GIT_TERMINAL_PROMPT-unset}\""}, RunOpts{})
		if err != nil || res.Stdout != "0" {
			t.Fatalf("default env lacks GIT_TERMINAL_PROMPT=0: %q %v", res.Stdout, err)
		}
	})
}

func TestExecErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ctx := context.Background()

	t.Run("stdout maxBuffer", func(t *testing.T) {
		res, err := Exec(ctx, "", "sh", []string{"-c", "head -c 3000000 /dev/zero"}, RunOpts{})
		if err == nil || err.Error() != "stdout maxBuffer length exceeded" || !errors.Is(err, ErrMaxBuffer) {
			t.Fatalf("err = %v", err)
		}
		if len(res.Stdout) != DefaultMaxBuffer {
			t.Fatalf("captured %d bytes", len(res.Stdout))
		}
	})
	t.Run("stderr maxBuffer", func(t *testing.T) {
		_, err := Exec(ctx, "", "sh", []string{"-c", "head -c 3000 /dev/zero >&2"}, RunOpts{MaxBuffer: 1000})
		if err == nil || err.Error() != "stderr maxBuffer length exceeded" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unlimited", func(t *testing.T) {
		res, err := Exec(ctx, "", "sh", []string{"-c", "head -c 3000000 /dev/zero"}, RunOpts{MaxBuffer: -1})
		if err != nil || len(res.Stdout) != 3000000 {
			t.Fatalf("len=%d err=%v", len(res.Stdout), err)
		}
	})
	t.Run("missing binary", func(t *testing.T) {
		_, err := Exec(ctx, "", "gitcito-no-such-binary", nil, RunOpts{})
		var ee *ExecError
		if err == nil || err.Error() != "spawn gitcito-no-such-binary ENOENT" || !errors.As(err, &ee) || ee.Started() {
			t.Fatalf("err = %v", err)
		}
		if _, ok := ExitCode(err); ok {
			t.Fatal("a start failure has no exit code")
		}
	})
	t.Run("missing cwd", func(t *testing.T) {
		_, err := Exec(ctx, filepath.Join(t.TempDir(), "gone"), "sh", []string{"-c", "true"}, RunOpts{})
		if err == nil || err.Error() != "spawn sh ENOENT" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("stderr in message", func(t *testing.T) {
		_, err := Exec(ctx, "", "sh", []string{"-c", "echo oops >&2; exit 3"}, RunOpts{})
		if err == nil || err.Error() != "Command failed: sh -c echo oops >&2; exit 3\noops\n" {
			t.Fatalf("err = %q", err)
		}
		if code, ok := ExitCode(err); !ok || code != 3 {
			t.Fatalf("code = %d %v", code, ok)
		}
	})
	t.Run("timeout kills", func(t *testing.T) {
		start := time.Now()
		_, err := Exec(ctx, "", "sh", []string{"-c", "sleep 30"}, RunOpts{Timeout: 100 * time.Millisecond})
		var ee *ExecError
		if err == nil || !errors.As(err, &ee) || !ee.Killed || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		if !strings.HasPrefix(err.Error(), "Command failed: sh -c sleep 30\n") {
			t.Fatalf("message = %q", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("timeout did not kill promptly")
		}
	})
	t.Run("callbacks are serialized and ordered", func(t *testing.T) {
		var mu sync.Mutex
		var got bytes.Buffer
		cb := func(c []byte) { mu.Lock(); got.Write(c); mu.Unlock() }
		_, err := Exec(ctx, "", "sh", []string{"-c", "echo one; sleep 0.05; echo two >&2; sleep 0.05; echo three"},
			RunOpts{OnStdout: cb, OnStderr: cb})
		if err != nil || got.String() != "one\ntwo\nthree\n" {
			t.Fatalf("got %q err %v", got.String(), err)
		}
	})
}

func TestReadBlob(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	bin := []byte{0, 1, 2, 0xff, 0xfe, '\n', 0}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "add", "b.bin")
	testutil.Git(t, dir, "commit", "-q", "-m", "bin")

	got, err := ReadBlob(ctx, dir, "HEAD:b.bin", RunOpts{})
	if err != nil || !bytes.Equal(got, bin) {
		t.Fatalf("got %v err %v", got, err)
	}
	_, err = ReadBlob(ctx, dir, "HEAD:missing", RunOpts{})
	if err == nil || !strings.HasPrefix(err.Error(), "fatal: path 'missing' does not exist in 'HEAD'") || !strings.HasSuffix(err.Error(), "\n") {
		t.Fatalf("err = %q", err)
	}
}
