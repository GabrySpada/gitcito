package difftool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/fakebin"
	"github.com/GabrySpada/gitcito/internal/testutil"
)

// Replays testdata/difftool.json, recorded from src/main/difftool.ts by
// test/port/tools.test.ts against the same fake git.
func TestDiffToolGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "difftool.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string          `json:"name"`
		Bins    []string        `json:"bins"`
		Replies []fakebin.Reply `json:"replies"`
		Call    struct {
			Method string            `json:"method"`
			Args   []json.RawMessage `json:"args"`
		} `json:"call"`
		Result json.RawMessage `json:"result"`
		Error  *string         `json:"error"`
		Calls  json.RawMessage `json:"calls"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			root := fakebin.TempRoot(t)
			if err := os.Mkdir(filepath.Join(root, "repo"), 0o755); err != nil {
				t.Fatal(err)
			}
			f := fakebin.Install(t, root, c.Bins, fakebin.Expand(t, c.Replies, root))
			f.Use(t)
			args := fakebin.Expand(t, c.Call.Args, root)
			str := func(i int) string {
				var s string
				if i < len(args) {
					_ = json.Unmarshal(args[i], &s)
				}
				return s
			}
			s := NewService()
			ctx := context.Background()
			var result any
			var callErr error
			switch c.Call.Method {
			case "config":
				result, callErr = s.Config(ctx, str(0))
			case "set":
				var v Values
				if err := json.Unmarshal(args[1], &v); err != nil {
					t.Fatal(err)
				}
				callErr = s.Set(ctx, str(0), v, str(2))
			case "diff":
				result = s.Diff(ctx, str(0), str(1), str(2), str(3))
			case "merge":
				result = s.Merge(ctx, str(0), str(1), str(2))
			}
			if c.Error != nil {
				if callErr == nil {
					t.Fatalf("no error, want %q", *c.Error)
				}
				got := strings.ReplaceAll(callErr.Error(), root, "<ROOT>")
				if got != *c.Error {
					t.Fatalf("error %q, want %q", got, *c.Error)
				}
			} else {
				if callErr != nil {
					t.Fatal(callErr)
				}
				fakebin.SameJSON(t, "result", result, c.Result, root)
			}
			calls := f.Calls(t)
			if c.Call.Method == "config" {
				// The five reads run concurrently: order canonically, as the capture did.
				sort.Slice(calls, func(i, j int) bool {
					return strings.Join(calls[i].Argv, "\x00") < strings.Join(calls[j].Argv, "\x00")
				})
			}
			fakebin.SameJSON(t, "calls", calls, c.Calls, root)
		})
	}
}

// Real git: the values Set writes are the ones Config reads back, globally by
// default and per repository on request — against an isolated global config.
func TestSetThenConfigWithRealGit(t *testing.T) {
	env := testutil.IsolateGitEnv(t)
	repo := testutil.InitRepo(t)
	ctx := context.Background()
	str := func(s string) *string { return &s }
	no := false

	if err := Set(ctx, repo, Values{DiffTool: str("meld"), MergeTool: str("kdiff3"), KeepBackup: &no}, ""); err != nil {
		t.Fatal(err)
	}
	cfg := Config(ctx, repo)
	if cfg.DiffTool != "meld" || cfg.MergeTool != "kdiff3" || cfg.KeepBackup {
		t.Fatalf("after global set: %+v", cfg)
	}
	global, _ := os.ReadFile(env.GlobalConfig)
	if !strings.Contains(string(global), "meld") {
		t.Fatalf("global config not written:\n%s", global)
	}
	if cfg.DiffTools == nil || cfg.MergeTools == nil || len(cfg.DiffTools) == 0 {
		t.Fatalf("tool lists: %+v", cfg)
	}

	if err := Set(ctx, repo, Values{DiffTool: str("vimdiff")}, "repo"); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo, "config", "--local", "--get", "diff.tool"); strings.TrimSpace(got) != "vimdiff" {
		t.Fatalf("repo diff.tool = %q", got)
	}

	// An empty choice unsets the key — and unsetting a missing key is not an error.
	if err := Set(ctx, repo, Values{MergeTool: str("")}, "global"); err != nil {
		t.Fatal(err)
	}
	if err := Set(ctx, repo, Values{MergeTool: str("")}, "global"); err != nil {
		t.Fatal(err)
	}
	if cfg := Config(ctx, repo); cfg.MergeTool != "" {
		t.Fatalf("after unset: %+v", cfg)
	}
}

// Real git: difftool and mergetool hand the file to the configured command, and
// a mergetool that resolves the conflict leaves it staged.
func TestDiffAndMergeWithRealGit(t *testing.T) {
	testutil.IsolateGitEnv(t)
	repo := testutil.InitRepo(t)
	ctx := context.Background()
	log := filepath.Join(t.TempDir(), "log")
	testutil.Git(t, repo, "config", "difftool.rec.cmd", `printf '%s\n' "$(basename "$REMOTE")" >> '`+log+`'`)
	testutil.Git(t, repo, "config", "mergetool.fix.cmd", `printf 'resolved\n' > "$MERGED"`)
	testutil.Git(t, repo, "config", "mergetool.fix.trustExitCode", "true")
	testutil.Git(t, repo, "config", "mergetool.keepBackup", "false")

	testutil.WriteFile(t, repo, "a.txt", "base\n")
	testutil.Git(t, repo, "add", "a.txt")
	testutil.Git(t, repo, "commit", "-qm", "base")
	testutil.WriteFile(t, repo, "a.txt", "changed\n")

	if got := Diff(ctx, repo, "a.txt", "", "rec"); got != "" {
		t.Fatalf("diff: %q", got)
	}
	if got := Diff(ctx, repo, "a.txt", "HEAD", "rec"); got != "" {
		t.Fatalf("diff HEAD: %q", got)
	}
	if b, _ := os.ReadFile(log); strings.Count(string(b), "a.txt") != 2 {
		t.Fatalf("difftool log: %q", b)
	}
	if got := Diff(ctx, repo, "a.txt", "", "no-such-tool"); got == "" {
		t.Fatal("an unknown tool should fail")
	}

	// A conflict on a.txt.
	testutil.Git(t, repo, "checkout", "-qb", "other")
	testutil.WriteFile(t, repo, "a.txt", "theirs\n")
	testutil.Git(t, repo, "commit", "-qam", "theirs")
	testutil.Git(t, repo, "checkout", "-q", "main")
	testutil.WriteFile(t, repo, "a.txt", "ours\n")
	testutil.Git(t, repo, "commit", "-qam", "ours")
	_, _ = runGitAllowFail(repo, "merge", "other")

	if got := Merge(ctx, repo, "a.txt", "fix"); got != "" {
		t.Fatalf("merge: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "resolved\n" {
		t.Fatalf("merged content %q", b)
	}
	if st := testutil.Git(t, repo, "status", "--porcelain"); strings.TrimSpace(st) != "M  a.txt" {
		t.Fatalf("status %q", st)
	}
}

func runGitAllowFail(repo string, args ...string) (string, error) {
	out, err := git(context.Background(), repo, args)
	return out, err
}
