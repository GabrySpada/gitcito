package gitsvc

// Replays the slice B goldens captured from the TypeScript gitService by
// test/port/goldens-b.test.ts. Each golden file names its method, fixture,
// prep steps and arguments; this test runs the Go method the same way,
// normalizes the outcome exactly as the TS capture did (bNormalize mirrors
// normalize() there, rule for rule) and compares.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// bGolden is one golden file.
type bGolden struct {
	Method    string            `json:"method"`
	Fixture   string            `json:"fixture"`
	Case      string            `json:"case"`
	Write     bool              `json:"write"`
	Args      []json.RawMessage `json:"args"`
	Prep      []bPrepStep       `json:"prep"`
	Extra     [][]string        `json:"extra"`
	MaskDates bool              `json:"maskDates"`
	Result    json.RawMessage   `json:"result"`
	Error     *string           `json:"error"`
	State     json.RawMessage   `json:"state"`
}

// bPrepStep is one prep step; exactly one field is set.
type bPrepStep struct {
	Git      []string `json:"git"`
	GitUp    []string `json:"gitUp"`
	MayFail  bool     `json:"mayFail"`
	Write    []string `json:"write"`
	WriteUp  []string `json:"writeUp"`
	WriteBig []any    `json:"writeBig"`
	Up       string   `json:"up"`
}

// bArgs reads the TS positional arguments after the repo path. A missing
// argument is `undefined`; a JSON null is `null`.
type bArgs []json.RawMessage

func (a bArgs) present(i int) bool { return i < len(a) && string(a[i]) != "null" }

func (a bArgs) decode(t *testing.T, i int, v any) {
	t.Helper()
	if !a.present(i) {
		return
	}
	if err := json.Unmarshal(a[i], v); err != nil {
		t.Fatalf("arg %d: %v", i, err)
	}
}

func (a bArgs) str(t *testing.T, i int) string {
	var s string
	a.decode(t, i, &s)
	return s
}

func (a bArgs) boolean(t *testing.T, i int) bool {
	var b bool
	a.decode(t, i, &b)
	return b
}

func (a bArgs) num(t *testing.T, i int) int {
	var n int
	a.decode(t, i, &n)
	return n
}

func (a bArgs) intPtr(t *testing.T, i int) *int {
	if !a.present(i) {
		return nil
	}
	n := a.num(t, i)
	return &n
}

func (a bArgs) strPtr(t *testing.T, i int) *string {
	if !a.present(i) {
		return nil
	}
	s := a.str(t, i)
	return &s
}

func (a bArgs) strs(t *testing.T, i int) []string {
	var s []string
	a.decode(t, i, &s)
	return s
}

func bOpt[T any](t *testing.T, a bArgs, i int) *T {
	if !a.present(i) {
		return nil
	}
	v := new(T)
	a.decode(t, i, v)
	return v
}

// bCall invokes one slice B method with TS-shaped arguments.
type bCall func(t *testing.T, ctx context.Context, s *GitService, repo string, a bArgs) (any, error)

func bVoid(err error) (any, error) { return nil, err }

// bMethods is every slice B method, by TS name.
var bMethods = map[string]bCall{
	"blameFile": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.BlameFile(ctx, r, a.str(t, 0), a.str(t, 1))
	},
	"cherryPick": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.CherryPick(ctx, r, a.str(t, 0), a.boolean(t, 1)))
	},
	"cherryPickMany": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.CherryPickMany(ctx, r, a.strs(t, 0), a.boolean(t, 1)))
	},
	"commitBranches": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CommitBranches(ctx, r, a.str(t, 0))
	},
	"commitDiff": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CommitDiff(ctx, r, a.str(t, 0))
	},
	"commitFileDiff": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CommitFileDiff(ctx, r, a.str(t, 0), a.str(t, 1), a.boolean(t, 2))
	},
	"commitFiles": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CommitFiles(ctx, r, a.str(t, 0))
	},
	"commitMenuProbe": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CommitMenuProbe(ctx, r, a.str(t, 0))
	},
	"commitTags": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CommitTags(ctx, r, a.str(t, 0))
	},
	"compareBranches": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CompareBranches(ctx, r, a.str(t, 0), a.str(t, 1))
	},
	"contributors": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.Contributors(ctx, r, a.intPtr(t, 0))
	},
	"cosmosData": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.CosmosData(ctx, r, a.intPtr(t, 0))
	},
	"createTag": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.CreateTag(ctx, r, a.str(t, 0), a.str(t, 1), bOpt[CreateTagOptions](t, a, 2)))
	},
	"deleteRemoteTag": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.DeleteRemoteTag(ctx, r, a.str(t, 0), a.str(t, 1)))
	},
	"deleteTag": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.DeleteTag(ctx, r, a.str(t, 0)))
	},
	"diffFile": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.DiffFile(ctx, r, a.str(t, 0), a.boolean(t, 1), a.boolean(t, 2), a.boolean(t, 3))
	},
	"fetchNotes": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.FetchNotes(ctx, r, a.str(t, 0)))
	},
	"fileContent": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.FileContent(ctx, r, a.str(t, 0), a.str(t, 1), a.boolean(t, 2))
	},
	"fileDataUrl": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.FileDataUrl(ctx, r, a.str(t, 0), a.str(t, 1), a.boolean(t, 2))
	},
	"fileHistory": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.FileHistory(ctx, r, a.str(t, 0))
	},
	"generateChangelog": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.GenerateChangelog(ctx, r, bOpt[ChangelogOptions](t, a, 0))
	},
	"getRemoteTags": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.GetRemoteTags(ctx, r, a.str(t, 0))
	},
	"gitObject": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.GitObject(ctx, r, a.str(t, 0))
	},
	"grepWorkingTree": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.GrepWorkingTree(ctx, r, a.str(t, 0), bOpt[SearchOptions](t, a, 1))
	},
	"historyPaths": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.HistoryPaths(ctx, r, a.intPtr(t, 0))
	},
	"historyPurge": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.HistoryPurge(ctx, r, a.strs(t, 0))
	},
	"historyPurgeBackups": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.HistoryPurgeBackups(ctx, r)
	},
	"historyPurgeDropBackup": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.HistoryPurgeDropBackup(ctx, r, a.str(t, 0)))
	},
	"historyPurgePreview": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.HistoryPurgePreview(ctx, r, a.strs(t, 0))
	},
	"historyPurgeRestore": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.HistoryPurgeRestore(ctx, r, a.str(t, 0)))
	},
	"imageDiff": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.ImageDiff(ctx, r, a.str(t, 0), a.strPtr(t, 1), a.str(t, 2))
	},
	"listDir": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.ListDir(ctx, r, a.str(t, 0))
	},
	"listDirAt": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.ListDirAt(ctx, r, a.str(t, 0), a.str(t, 1))
	},
	"listFiles": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.ListFiles(ctx, r)
	},
	"note": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.Note(ctx, r, a.str(t, 0))
	},
	"notedCommits": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.NotedCommits(ctx, r)
	},
	"objectRefs": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.ObjectRefs(ctx, r)
	},
	"previewRef": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.PreviewRef(ctx, r, a.str(t, 0), a.str(t, 1), a.str(t, 2), a.str(t, 3))
	},
	"protectedBranches": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.ProtectedBranches(ctx, r)
	},
	"pushNotes": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.PushNotes(ctx, r, a.str(t, 0)))
	},
	"pushTag": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.PushTag(ctx, r, a.str(t, 0), a.str(t, 1)))
	},
	"removeNote": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.RemoveNote(ctx, r, a.str(t, 0)))
	},
	"repoInsights": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.RepoInsights(ctx, r, a.num(t, 0))
	},
	"repoStats": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.RepoStats(ctx, r, a.num(t, 0))
	},
	"reset": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.Reset(ctx, r, a.str(t, 0), a.str(t, 1)))
	},
	"resolvePrRef": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.ResolvePrRef(ctx, r, a.str(t, 0), a.num(t, 1))
	},
	"restoreUndoneCommit": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.RestoreUndoneCommit(ctx, r, a.str(t, 0)))
	},
	"revertCommit": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.RevertCommit(ctx, r, a.str(t, 0)))
	},
	"searchCommitMatches": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.SearchCommitMatches(ctx, r, a.str(t, 0), a.str(t, 1), bOpt[SearchOptions](t, a, 2))
	},
	"searchFileContents": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.SearchFileContents(ctx, r, a.strs(t, 0), a.str(t, 1), bOpt[SearchOptions](t, a, 2))
	},
	"searchFileMatches": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.SearchFileMatches(ctx, r, a.strs(t, 0), a.str(t, 1), bOpt[SearchOptions](t, a, 2))
	},
	"searchHistory": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.SearchHistory(ctx, r, a.str(t, 0), bOpt[HistorySearchOptions](t, a, 1))
	},
	"semanticDiff": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		var oldSide, newSide BlobSpec
		a.decode(t, 1, &oldSide)
		a.decode(t, 2, &newSide)
		return s.SemanticDiff(ctx, r, a.str(t, 0), oldSide, newSide)
	},
	"setNote": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.SetNote(ctx, r, a.str(t, 0), a.str(t, 1)))
	},
	"setProtectedBranches": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.SetProtectedBranches(ctx, r, a.strs(t, 0)))
	},
	"sparseCheckoutDisable": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.SparseCheckoutDisable(ctx, r))
	},
	"sparseCheckoutInfo": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.SparseCheckoutInfo(ctx, r)
	},
	"sparseCheckoutSet": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return bVoid(s.SparseCheckoutSet(ctx, r, a.strs(t, 0)))
	},
	"stagedDiff": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.StagedDiff(ctx, r)
	},
	"stashFileDiff": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.StashFileDiff(ctx, r, a.str(t, 0), a.str(t, 1), a.boolean(t, 2), a.boolean(t, 3))
	},
	"stashFiles": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.StashFiles(ctx, r, a.str(t, 0), a.str(t, 1))
	},
	"timelapseData": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.TimelapseData(ctx, r, a.intPtr(t, 0))
	},
	"undoCommit": func(t *testing.T, ctx context.Context, s *GitService, r string, a bArgs) (any, error) {
		return s.UndoCommit(ctx, r)
	},
}

// bSliceBMethods is the slice B list from docs/port/WAVE1.md.
var bSliceBMethods = []string{
	"blameFile", "cherryPick", "cherryPickMany", "commitBranches", "commitDiff", "commitFileDiff", "commitFiles",
	"commitMenuProbe", "commitTags", "compareBranches", "contributors", "cosmosData", "createTag", "deleteRemoteTag",
	"deleteTag", "diffFile", "fetchNotes", "fileContent", "fileDataUrl", "fileHistory", "generateChangelog",
	"getRemoteTags", "gitObject", "grepWorkingTree", "historyPaths", "historyPurge", "historyPurgeBackups",
	"historyPurgeDropBackup", "historyPurgePreview", "historyPurgeRestore", "imageDiff", "listDir", "listDirAt",
	"listFiles", "note", "notedCommits", "objectRefs", "previewRef", "protectedBranches", "pushNotes", "pushTag",
	"removeNote", "repoInsights", "repoStats", "reset", "resolvePrRef", "restoreUndoneCommit", "revertCommit",
	"searchCommitMatches", "searchFileContents", "searchFileMatches", "searchHistory", "semanticDiff", "setNote",
	"setProtectedBranches", "sparseCheckoutDisable", "sparseCheckoutInfo", "sparseCheckoutSet", "stagedDiff",
	"stashFileDiff", "stashFiles", "timelapseData", "undoCommit",
}

func bGoldenDir(t *testing.T) string {
	return filepath.Join(testutil.MustRepoRoot(t), "internal", "gitsvc", "testdata", "goldens")
}

func TestBSliceMethodTable(t *testing.T) {
	if len(bSliceBMethods) != 63 {
		t.Fatalf("slice B lists %d methods, WAVE1.md has 63", len(bSliceBMethods))
	}
	for _, m := range bSliceBMethods {
		if bMethods[m] == nil {
			t.Errorf("no replay entry for %s", m)
		}
		if !isClassified(m) {
			t.Errorf("%s is not classified", m)
		}
		files, _ := filepath.Glob(filepath.Join(bGoldenDir(t), m, "*.json"))
		if len(files) == 0 {
			t.Errorf("no golden for %s", m)
		}
	}
	if len(bMethods) != len(bSliceBMethods) {
		t.Errorf("replay table has %d entries, want %d", len(bMethods), len(bSliceBMethods))
	}
}

func TestBGoldens(t *testing.T) {
	testutil.Playground(t)
	testutil.IsolateGitEnv(t)
	svc := New(nil, nil)
	ctx := context.Background()
	shared := map[string]*bNormCtx{}
	count := 0
	for _, method := range bSliceBMethods {
		files, err := filepath.Glob(filepath.Join(bGoldenDir(t), method, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(files)
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var g bGolden
			if err := json.Unmarshal(data, &g); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			count++
			t.Run(method+"/"+strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
				bReplay(t, ctx, svc, g, shared)
			})
		}
	}
	if count < len(bSliceBMethods) {
		t.Fatalf("only %d goldens", count)
	}
}

func bReplay(t *testing.T, ctx context.Context, svc *GitService, g bGolden, shared map[string]*bNormCtx) {
	isolated := g.Write || len(g.Prep) > 0
	var repo string
	if isolated {
		repo = testutil.CloneFixture(t, g.Fixture)
	} else {
		repo = testutil.PlaygroundRepo(t, g.Fixture)
	}
	up := bRunPrep(t, repo, g.Prep)
	args := bResolveArgs(t, g.Args, repo, up)

	value, callErr := bMethods[g.Method](t, ctx, svc, repo, args)

	var c *bNormCtx
	if isolated {
		c = bMakeNormCtx(t, repo, up, g.MaskDates)
	} else {
		key := g.Fixture + "\x00" + map[bool]string{true: "1", false: "0"}[g.MaskDates]
		if c = shared[key]; c == nil {
			c = bMakeNormCtx(t, repo, "", g.MaskDates)
			shared[key] = c
		}
	}

	if g.Error != nil {
		if callErr == nil {
			t.Fatalf("want error %q, got result %s", *g.Error, bMustJSON(t, value))
		}
		if got := c.normString(callErr.Error()); got != *g.Error {
			t.Errorf("error mismatch\n got: %q\nwant: %q", got, *g.Error)
		}
	} else {
		if callErr != nil {
			t.Fatalf("unexpected error: %v", callErr)
		}
		testutil.NoNulls(t, value)
		got := c.normalize(bToAny(t, value))
		want := bFromJSON(t, g.Result)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("result mismatch\n got: %s\nwant: %s", bMustJSON(t, got), bMustJSON(t, want))
		}
	}

	if len(g.State) > 0 {
		got := bSnapshot(t, repo, up, g.Extra, c)
		want := bFromJSON(t, g.State)
		if !reflect.DeepEqual(got, want) {
			gm, wm := got.(map[string]any), want.(map[string]any)
			for k := range wm {
				if !reflect.DeepEqual(gm[k], wm[k]) {
					t.Errorf("state.%s mismatch\n got: %s\nwant: %s", k, bMustJSON(t, gm[k]), bMustJSON(t, wm[k]))
				}
			}
			if len(gm) != len(wm) {
				t.Errorf("state keys differ: got %d want %d", len(gm), len(wm))
			}
		}
	}
}

func bMustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func bToAny(t *testing.T, v any) any {
	t.Helper()
	return bFromJSON(t, []byte(bMustJSON(t, v)))
}

func bFromJSON(t *testing.T, data []byte) any {
	t.Helper()
	if len(data) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// ─── prep and arguments ────────────────────────────────────────────────────

func bGitRun(dir string, args ...string) (stdout string, code int, stderr string) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code = 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return out.String(), code, errb.String()
}

func bRunPrep(t *testing.T, repo string, prep []bPrepStep) string {
	t.Helper()
	up := ""
	sub := func(xs []string) []string {
		out := make([]string, len(xs))
		for i, x := range xs {
			if up != "" {
				x = strings.ReplaceAll(x, "<UP>", up)
			}
			out[i] = x
		}
		return out
	}
	run := func(dir string, args []string, mayFail bool) {
		if _, code, stderr := bGitRun(dir, sub(args)...); code != 0 && !mayFail {
			t.Fatalf("prep git %v: exit %d\n%s", args, code, stderr)
		}
	}
	for _, step := range prep {
		switch {
		case step.Git != nil:
			run(repo, step.Git, step.MayFail)
		case step.GitUp != nil:
			run(up, step.GitUp, step.MayFail)
		case step.Write != nil:
			testutil.WriteFile(t, repo, step.Write[0], step.Write[1])
		case step.WriteUp != nil:
			testutil.WriteFile(t, up, step.WriteUp[0], step.WriteUp[1])
		case step.WriteBig != nil:
			name, _ := step.WriteBig[0].(string)
			size, _ := step.WriteBig[1].(float64)
			if err := os.WriteFile(filepath.Join(repo, name), bytes.Repeat([]byte{'a'}, int(size)), 0o644); err != nil {
				t.Fatal(err)
			}
		case step.Up != "":
			up = testutil.CloneFixture(t, step.Up)
		default:
			t.Fatalf("unknown prep step %+v", step)
		}
	}
	return up
}

func bResolveArgs(t *testing.T, args []json.RawMessage, repo, up string) bArgs {
	t.Helper()
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e)
			}
			return out
		case map[string]any:
			if r, ok := x["$rev"].(string); ok {
				return testutil.Git(t, repo, "rev-parse", r)
			}
			if r, ok := x["$revUp"].(string); ok {
				return testutil.Git(t, up, "rev-parse", r)
			}
			return x
		case string:
			if up != "" {
				return strings.ReplaceAll(x, "<UP>", up)
			}
			return x
		}
		return v
	}
	out := make(bArgs, len(args))
	for i, raw := range args {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(walk(v))
		if err != nil {
			t.Fatal(err)
		}
		out[i] = b
	}
	return out
}

// ─── normalization (mirrors test/port/goldens-b.test.ts) ──────────────────

var (
	bNormISORe     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
	bNormHexWordRe = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	bNormNoDateRe  = regexp.MustCompile(` \d+ [+-]\d{4}$`)
	bNormWipRe     = regexp.MustCompile(`refs/gitcito/wip/\d+`)
	bNormPurgeRe   = regexp.MustCompile(`pre-purge/\d+`)
	bNormPurgeKey  = regexp.MustCompile(`prepurge\.at\d+`)
	bNormStampTZ   = regexp.MustCompile(`\b\d{9,10} [+-]\d{4}\b`)
	bNormEpochRe   = regexp.MustCompile(`\b1[6-9]\d{8}\b`)
	bNormDateRe    = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
)

type bNormCtx struct {
	paths     [][2]string
	labels    map[string]string
	sorted    []string
	maskDates bool
}

func bMakeNormCtx(t *testing.T, repo, up string, maskDates bool) *bNormCtx {
	t.Helper()
	var paths [][2]string
	add := func(p, token string) {
		paths = append(paths, [2]string{p, token})
		if real, err := filepath.EvalSymlinks(p); err == nil && real != p {
			paths = append(paths, [2]string{real, token})
		}
	}
	add(repo, "<REPO>")
	if up != "" {
		add(up, "<UP>")
	}
	add(strings.TrimRight(os.TempDir(), "/"), "<TMP>")
	home, _ := os.UserHomeDir()
	add(home, "<HOME>")
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i][0]) > len(paths[j][0]) })
	labels := bObjectLabels(t, repo)
	if up != "" {
		for k, v := range bObjectLabels(t, up) {
			if _, ok := labels[k]; !ok {
				labels[k] = v
			}
		}
	}
	sorted := make([]string, 0, len(labels))
	for k := range labels {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	return &bNormCtx{paths: paths, labels: labels, sorted: sorted, maskDates: maskDates}
}

// bObjectLabels gives every commit and tag object a content label (see the TS
// capture for why): a hash of tree, parent labels, identities without dates
// and the message with timestamps and object ids masked.
func bObjectLabels(t *testing.T, repo string) map[string]string {
	t.Helper()
	listing, code, stderr := bGitRun(repo, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
	if code != 0 {
		t.Fatalf("cat-file: %s", stderr)
	}
	var wanted []string
	for _, l := range strings.Split(listing, "\n") {
		f := strings.Split(l, " ")
		if len(f) == 2 && (f[1] == "commit" || f[1] == "tag") {
			wanted = append(wanted, f[0])
		}
	}
	type object struct{ typ, body string }
	objects := map[string]object{}
	var order []string
	if len(wanted) > 0 {
		cmd := exec.Command("git", "-C", repo, "cat-file", "--batch")
		cmd.Stdin = strings.NewReader(strings.Join(wanted, "\n") + "\n")
		buf, err := cmd.Output()
		if err != nil {
			t.Fatalf("cat-file --batch: %v", err)
		}
		for pos := 0; pos < len(buf); {
			nl := bytes.IndexByte(buf[pos:], '\n') + pos
			head := strings.Split(string(buf[pos:nl]), " ")
			size := int(bJSInt(head[2]))
			start := nl + 1
			objects[head[0]] = object{typ: head[1], body: bDecodeUTF8(string(buf[start : start+size]))}
			order = append(order, head[0])
			pos = start + size + 1
		}
	}

	type parsed struct {
		headers [][2]string
		message string
	}
	cache := map[string]parsed{}
	parse := func(sha string) parsed {
		if p, ok := cache[sha]; ok {
			return p
		}
		body := objects[sha].body
		head, message := body, ""
		if cut := strings.Index(body, "\n\n"); cut >= 0 {
			head, message = body[:cut], body[cut+2:]
		}
		var headers [][2]string
		for _, line := range strings.Split(head, "\n") {
			if line == "" || strings.HasPrefix(line, " ") {
				continue
			}
			k, v, _ := strings.Cut(line, " ")
			headers = append(headers, [2]string{k, v})
		}
		p := parsed{headers, message}
		cache[sha] = p
		return p
	}
	get := func(p parsed, key string) []string {
		var out []string
		for _, h := range p.headers {
			if h[0] == key {
				out = append(out, h[1])
			}
		}
		return out
	}
	labels := map[string]string{}
	deps := func(sha string) []string {
		key := "parent"
		if objects[sha].typ != "commit" {
			key = "object"
		}
		var out []string
		for _, v := range get(parse(sha), key) {
			if _, ok := objects[v]; ok {
				out = append(out, v)
			}
		}
		return out
	}
	ref := func(v string) string {
		if l, ok := labels[v]; ok {
			return l
		}
		return v
	}
	mapJoin := func(xs []string, f func(string) string) string {
		out := make([]string, len(xs))
		for i, x := range xs {
			out[i] = f(x)
		}
		return strings.Join(out, ",")
	}
	noDate := func(s string) string { return bNormNoDateRe.ReplaceAllString(s, "") }
	normMsg := func(s string) string {
		return bNormHexWordRe.ReplaceAllString(bNormISORe.ReplaceAllString(s, "<ISO>"), "<H>")
	}
	ident := func(s string) string { return s }
	compute := func(sha string) string {
		p := parse(sha)
		var key, prefix string
		if objects[sha].typ == "commit" {
			prefix = "C"
			key = strings.Join([]string{"commit", mapJoin(get(p, "tree"), ident), mapJoin(get(p, "parent"), ref),
				mapJoin(get(p, "author"), noDate), mapJoin(get(p, "committer"), noDate), normMsg(p.message)}, "\n")
		} else {
			prefix = "T"
			key = strings.Join([]string{"tag", mapJoin(get(p, "object"), ref), mapJoin(get(p, "type"), ident),
				mapJoin(get(p, "tag"), ident), mapJoin(get(p, "tagger"), noDate), normMsg(p.message)}, "\n")
		}
		sum := sha1.Sum([]byte(key))
		return prefix + hex.EncodeToString(sum[:])[:10]
	}
	for _, start := range order {
		if _, ok := labels[start]; ok {
			continue
		}
		stack := []string{start}
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			if _, ok := labels[top]; ok {
				stack = stack[:len(stack)-1]
				continue
			}
			var missing []string
			for _, d := range deps(top) {
				if _, ok := labels[d]; !ok {
					missing = append(missing, d)
				}
			}
			if len(missing) > 0 {
				stack = append(stack, missing...)
				continue
			}
			labels[top] = compute(top)
			stack = stack[:len(stack)-1]
		}
	}
	return labels
}

func (c *bNormCtx) normString(s string) string {
	for _, p := range c.paths {
		if p[0] != "" {
			s = strings.ReplaceAll(s, p[0], p[1])
		}
	}
	s = bNormWipRe.ReplaceAllString(s, "refs/gitcito/wip/<N>")
	s = bNormPurgeRe.ReplaceAllString(s, "pre-purge/<N>")
	s = bNormPurgeKey.ReplaceAllString(s, "prepurge.at<N>")
	s = bNormISORe.ReplaceAllString(s, "<ISO>")
	s = bNormStampTZ.ReplaceAllString(s, "<TS> <TZ>")
	s = bNormEpochRe.ReplaceAllString(s, "<TS>")
	if c.maskDates {
		s = bNormDateRe.ReplaceAllString(s, "<DATE>")
	}
	return bNormHexWordRe.ReplaceAllStringFunc(s, func(m string) string {
		if l, ok := c.labels[m]; ok {
			return "<" + l + ">"
		}
		if len(m) == 40 {
			return m
		}
		i := sort.SearchStrings(c.sorted, m)
		if i >= len(c.sorted) || !strings.HasPrefix(c.sorted[i], m) {
			return m
		}
		if i+1 < len(c.sorted) && strings.HasPrefix(c.sorted[i+1], m) {
			return m
		}
		return "<" + c.labels[c.sorted[i]] + "~>"
	})
}

func (c *bNormCtx) normalize(v any) any {
	switch x := v.(type) {
	case string:
		return c.normString(x)
	case float64:
		if x == float64(int64(x)) && x >= 1e9 && x < 1e10 {
			return "<TS>"
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = c.normalize(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[c.normString(k)] = c.normalize(e)
		}
		return out
	}
	return v
}

// bGitOutcome is gitOutcome() in the TS capture: output, or `<exit N>` and
// stderr when the command fails.
func bGitOutcome(repo string, args ...string) string {
	out, code, stderr := bGitRun(repo, args...)
	if code != 0 {
		status := "?"
		if code > 0 {
			status = strconv.Itoa(code)
		}
		return "<exit " + status + ">" + bDecodeUTF8(stderr)
	}
	return bDecodeUTF8(out)
}

func bSnapshot(t *testing.T, repo, up string, extra [][]string, c *bNormCtx) any {
	t.Helper()
	var refs []string
	for _, l := range strings.Split(bGitOutcome(repo, "for-each-ref"), "\n") {
		if !strings.Contains(l, "\trefs/notes/") {
			refs = append(refs, l)
		}
	}
	raw := map[string]any{
		"status": bGitOutcome(repo, "status", "--porcelain=v2"),
		"refs":   strings.Join(refs, "\n"),
		"log":    bGitOutcome(repo, "log", "--format=%H%x00%P%x00%s", "--exclude=refs/notes/*", "--all"),
		"stash":  bGitOutcome(repo, "stash", "list"),
		"index":  bGitOutcome(repo, "ls-files", "-s"),
	}
	if extra != nil {
		var list []any
		for _, args := range extra {
			subbed := make([]string, len(args))
			argv := make([]any, len(args))
			for i, a := range args {
				argv[i] = a
				if up != "" {
					a = strings.ReplaceAll(a, "<UP>", up)
				}
				subbed[i] = a
			}
			list = append(list, map[string]any{"args": argv, "out": bGitOutcome(repo, subbed...)})
		}
		raw["extra"] = list
	}
	state := c.normalize(raw).(map[string]any)
	lines := strings.Split(state["log"].(string), "\n")
	sort.Strings(lines)
	state["log"] = strings.Join(lines, "\n")
	return state
}
