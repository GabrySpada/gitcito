package gitsvc

// Replays the slice A goldens captured from TypeScript
// (test/port/goldens-a.test.ts → testdata/goldens/<method>/*.json): run the Go
// method on the same fixture (a clone for writes and prepared reads), apply
// the same normalization, and compare the JSON (result or error, plus the
// repository state a write leaves behind).

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// aSliceMethods is every gitService method slice A owns.
var aSliceMethods = []string{
	"absorbApply", "absorbPlan", "addRemote", "blobAtCommit", "branches", "commitEditApply", "commitEditInfo",
	"commitEditPreview", "conflictContext", "conflictOpAbort", "conflictOpContinue", "conflictTakeSide",
	"conflictVersions", "createSnapshot", "deleteSnapshot", "editRemote", "fetchRemote", "interactiveRebaseSteps",
	"listSnapshots", "log", "mergeMessage", "mergePreview", "mergeState", "open", "rangeDiff", "reflog", "refTips",
	"remotes", "removeRemote", "renameStash", "repoDetail", "repoPulse", "resolveConflict", "restoreFromCommit",
	"restoreSnapshot", "runInteractiveRebase", "stage", "stageAll", "stagePatch", "stash", "stashApply",
	"stashApplyFiles", "stashApplyOverwrite", "stashBaseShas", "stashDrop", "stashes", "stashPop", "stashPush",
	"stashToBranch", "status", "teammateRadar", "unstage", "unstageAll", "version", "writeChangelogFile",
}

type aGolden struct {
	Method  string            `json:"method"`
	Fixture string            `json:"fixture"`
	Args    []json.RawMessage `json:"args"`
	Setup   []aStep           `json:"setup"`
	Result  json.RawMessage   `json:"result"`
	Error   *string           `json:"error"`
	State   json.RawMessage   `json:"state"`
}

type aStep struct {
	Git         []string `json:"git"`
	GitTry      []string `json:"gitTry"`
	Write       *string  `json:"write"`
	Content     string   `json:"content"`
	Rm          *string  `json:"rm"`
	ApplyCached *string  `json:"applyCached"`
	Reverse     bool     `json:"reverse"`
}

// aPinnedDate pins every commit a test creates (TS sets the same).
const aPinnedDate = "@1700000000 +0000"

// aGoldenEnv isolates git like the TS capture did and returns the HOME it
// installed. It uses t.Setenv, so callers are never parallel.
func aGoldenEnv(t *testing.T) string {
	t.Helper()
	env := testutil.IsolateGitEnv(t)
	t.Setenv("GIT_AUTHOR_DATE", aPinnedDate)
	t.Setenv("GIT_COMMITTER_DATE", aPinnedDate)
	// The guard snapshot reads the settings file: point it at nothing.
	t.Setenv("GITCITO_DATA_DIR", t.TempDir())
	return env.Home
}

func TestSliceAGoldens(t *testing.T) {
	home := aGoldenEnv(t)
	root := filepath.Join("testdata", "goldens")
	s := New(nil, nil)
	ctx := context.Background()
	gitVersion := aGitVersion(t)
	covered := map[string]int{}

	for _, method := range aSliceMethods {
		files, _ := filepath.Glob(filepath.Join(root, method, "*.json"))
		covered[method] = len(files)
		for _, file := range files {
			name := method + "/" + strings.TrimSuffix(filepath.Base(file), ".json")
			t.Run(name, func(t *testing.T) {
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var g aGolden
				if err := json.Unmarshal(raw, &g); err != nil {
					t.Fatal(err)
				}
				aReplay(t, s, ctx, g, home, gitVersion)
			})
		}
	}
	for _, m := range aSliceMethods {
		if covered[m] == 0 {
			t.Errorf("no golden for %s", m)
		}
	}
}

func aReplay(t *testing.T, s *GitService, ctx context.Context, g aGolden, home, gitVersion string) {
	t.Helper()
	write := !IsReadMethod(g.Method)
	repo, clone := "", false
	switch g.Fixture {
	case "@missing":
		repo = "/definitely/not/a/repo"
	case "@plain":
		repo = t.TempDir()
	case "@none":
	default:
		if write || len(g.Setup) > 0 {
			repo, clone = testutil.CloneFixture(t, g.Fixture), true
		} else {
			repo = testutil.PlaygroundRepo(t, g.Fixture)
		}
	}
	aRunSetup(t, repo, g.Setup)
	args := aResolveArgs(t, repo, g.Args)

	result, err := aDispatch(ctx, s, g.Method, repo, args)
	got := map[string]any{}
	if err != nil {
		got["error"] = err.Error()
	} else {
		testutil.NoNulls(t, result)
		b, merr := json.Marshal(result)
		if merr != nil {
			t.Fatal(merr)
		}
		var v any
		_ = json.Unmarshal(b, &v)
		if g.Method == "version" && v == gitVersion {
			v = "<GITVERSION>"
		}
		got["result"] = v
	}
	if write && repo != "" && g.Fixture != "@missing" {
		got["state"] = aRepoState(repo)
	}

	n := newANormalizer(t, repo, clone, home)
	gotNorm := n.normalize(got)

	want := map[string]any{}
	if g.Error != nil {
		want["error"] = *g.Error
	} else {
		var v any
		_ = json.Unmarshal(g.Result, &v)
		want["result"] = v
	}
	if g.State != nil {
		var v any
		_ = json.Unmarshal(g.State, &v)
		want["state"] = v
	}
	if !reflect.DeepEqual(gotNorm, want) {
		gj, _ := json.MarshalIndent(gotNorm, "", "  ")
		wj, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("mismatch\n--- got\n%s\n--- want\n%s", gj, wj)
	}
}

// ─── Setup and args ─────────────────────────────────────────────────────────

func aGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

func aRunSetup(t *testing.T, repo string, steps []aStep) {
	t.Helper()
	for _, st := range steps {
		switch {
		case st.Git != nil:
			testutil.Git(t, repo, st.Git...)
		case st.GitTry != nil:
			_, _ = aGit(repo, st.GitTry...)
		case st.Write != nil:
			testutil.WriteFile(t, repo, *st.Write, st.Content)
		case st.Rm != nil:
			if err := os.Remove(filepath.Join(repo, *st.Rm)); err != nil {
				t.Fatal(err)
			}
		case st.ApplyCached != nil:
			args := []string{"-C", repo, "apply", "--cached"}
			if st.Reverse {
				args = append(args, "--reverse")
			}
			cmd := exec.Command("git", append(args, "-")...)
			cmd.Stdin = strings.NewReader(*st.ApplyCached)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("apply --cached: %v\n%s", err, out)
			}
		default:
			t.Fatalf("unknown setup step %+v", st)
		}
	}
}

var aPlaceholderRe = regexp.MustCompile(`^\{(rev|each|eachref):(.+)\}$`)

// aResolveArgs swaps {rev:…}/{each:…}/{eachref:…} placeholders for the values
// they name in this repository, as the TS capture did.
func aResolveArgs(t *testing.T, repo string, args []json.RawMessage) []json.RawMessage {
	t.Helper()
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			m := aPlaceholderRe.FindStringSubmatch(x)
			if m == nil {
				return x
			}
			if m[1] == "rev" {
				return testutil.Git(t, repo, "rev-parse", m[2])
			}
			format := "%(objectname)"
			if m[1] == "eachref" {
				format = "%(refname)"
			}
			return testutil.Git(t, repo, "for-each-ref", "--count=1", "--format="+format, m[2])
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
			return x
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
			return x
		}
		return v
	}
	out := make([]json.RawMessage, len(args))
	for i, a := range args {
		var v any
		if err := json.Unmarshal(a, &v); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(walk(v))
		out[i] = b
	}
	return out
}

// ─── Dispatch ───────────────────────────────────────────────────────────────

// aArgs decodes positional TS arguments; a missing one is `undefined`, which
// leaves the Go zero value (or nil pointer) in place.
type aArgs []json.RawMessage

func (a aArgs) into(i int, dst any) {
	if i < len(a) {
		_ = json.Unmarshal(a[i], dst)
	}
}
func (a aArgs) str(i int) string     { var s string; a.into(i, &s); return s }
func (a aArgs) num(i int) int        { var n int; a.into(i, &n); return n }
func (a aArgs) flag(i int) bool      { var b bool; a.into(i, &b); return b }
func (a aArgs) strs(i int) []string  { var s []string; a.into(i, &s); return s }
func (a aArgs) numPtr(i int) *int    { var n *int; a.into(i, &n); return n }
func (a aArgs) strPtr(i int) *string { var s *string; a.into(i, &s); return s }
func (a aArgs) edits(i int) map[string]string {
	var m map[string]string
	a.into(i, &m)
	return m
}

func aDispatch(ctx context.Context, s *GitService, method, repo string, raw []json.RawMessage) (any, error) {
	a := aArgs(raw)
	none := func(err error) (any, error) { return nil, err }
	switch method {
	case "absorbApply":
		var o *AbsorbApplyOpts
		a.into(0, &o)
		return s.AbsorbApply(ctx, repo, o)
	case "absorbPlan":
		return s.AbsorbPlan(ctx, repo)
	case "addRemote":
		return none(s.AddRemote(ctx, repo, a.str(0), a.str(1), a.str(2)))
	case "blobAtCommit":
		return s.BlobAtCommit(ctx, repo, a.str(0), a.str(1))
	case "branches":
		return s.Branches(ctx, repo)
	case "commitEditApply":
		return s.CommitEditApply(ctx, repo, a.str(0), a.edits(1), a.str(2))
	case "commitEditInfo":
		return s.CommitEditInfo(ctx, repo, a.str(0))
	case "commitEditPreview":
		return s.CommitEditPreview(ctx, repo, a.str(0), a.edits(1), a.str(2))
	case "conflictContext":
		return s.ConflictContext(ctx, repo)
	case "conflictOpAbort":
		return none(s.ConflictOpAbort(ctx, repo, a.str(0)))
	case "conflictOpContinue":
		return none(s.ConflictOpContinue(ctx, repo, a.str(0)))
	case "conflictTakeSide":
		return none(s.ConflictTakeSide(ctx, repo, a.str(0), a.str(1)))
	case "conflictVersions":
		return s.ConflictVersions(ctx, repo, a.str(0))
	case "createSnapshot":
		return s.CreateSnapshot(ctx, repo, a.strPtr(0), a.numPtr(1))
	case "deleteSnapshot":
		return none(s.DeleteSnapshot(ctx, repo, a.str(0)))
	case "editRemote":
		return none(s.EditRemote(ctx, repo, a.str(0), a.str(1), a.str(2), a.str(3)))
	case "fetchRemote":
		return none(s.FetchRemote(ctx, repo, a.str(0)))
	case "interactiveRebaseSteps":
		return s.InteractiveRebaseSteps(ctx, repo, a.str(0))
	case "listSnapshots":
		return s.ListSnapshots(ctx, repo)
	case "log":
		return s.Log(ctx, repo, a.numPtr(0), a.num(1))
	case "mergeMessage":
		return s.MergeMessage(ctx, repo)
	case "mergePreview":
		return s.MergePreview(ctx, repo, a.str(0), a.strs(1))
	case "mergeState":
		return s.MergeState(ctx, repo)
	case "open":
		return s.Open(ctx, repo)
	case "rangeDiff":
		return s.RangeDiff(ctx, repo, a.str(0), a.str(1), a.str(2))
	case "reflog":
		return s.Reflog(ctx, repo, a.strPtr(0), a.numPtr(1))
	case "refTips":
		return s.RefTips(ctx, repo, a.str(0), a.numPtr(1))
	case "remotes":
		return s.Remotes(ctx, repo)
	case "removeRemote":
		return none(s.RemoveRemote(ctx, repo, a.str(0)))
	case "renameStash":
		return none(s.RenameStash(ctx, repo, a.num(0), a.str(1)))
	case "repoDetail":
		return s.RepoDetail(ctx, repo, a.numPtr(0))
	case "repoPulse":
		return s.RepoPulse(ctx, repo)
	case "resolveConflict":
		return none(s.ResolveConflict(ctx, repo, a.str(0), a.str(1)))
	case "restoreFromCommit":
		return none(s.RestoreFromCommit(ctx, repo, a.str(0), a.strs(1)))
	case "restoreSnapshot":
		return none(s.RestoreSnapshot(ctx, repo, a.str(0), a.strs(1)))
	case "runInteractiveRebase":
		var steps []RebaseStep
		a.into(1, &steps)
		return none(s.RunInteractiveRebase(ctx, repo, a.str(0), steps))
	case "stage":
		return none(s.Stage(ctx, repo, a.strs(0)))
	case "stageAll":
		return none(s.StageAll(ctx, repo))
	case "stagePatch":
		return none(s.StagePatch(ctx, repo, a.str(0), a.flag(1)))
	case "stash":
		return none(s.Stash(ctx, repo, a.str(0)))
	case "stashApply":
		return none(s.StashApply(ctx, repo, a.num(0)))
	case "stashApplyFiles":
		return none(s.StashApplyFiles(ctx, repo, a.str(0), a.strs(1), a.strs(2)))
	case "stashApplyOverwrite":
		return none(s.StashApplyOverwrite(ctx, repo, a.num(0), a.flag(1)))
	case "stashBaseShas":
		return s.StashBaseShas(ctx, repo)
	case "stashDrop":
		return none(s.StashDrop(ctx, repo, a.num(0)))
	case "stashes":
		return s.Stashes(ctx, repo)
	case "stashPop":
		return none(s.StashPop(ctx, repo, a.num(0)))
	case "stashPush":
		return none(s.StashPush(ctx, repo, a.str(0), a.strs(1), a.flag(2)))
	case "stashToBranch":
		return none(s.StashToBranch(ctx, repo, a.str(0), a.num(1)))
	case "status":
		return s.Status(ctx, repo)
	case "teammateRadar":
		var o *TeammateRadarOpts
		a.into(0, &o)
		return s.TeammateRadar(ctx, repo, o)
	case "unstage":
		return none(s.Unstage(ctx, repo, a.strs(0)))
	case "unstageAll":
		return none(s.UnstageAll(ctx, repo))
	case "version":
		return s.Version(ctx)
	case "writeChangelogFile":
		return none(s.WriteChangelogFile(ctx, repo, a.str(0)))
	}
	return nil, fmt.Errorf("no dispatch for %s", method)
}

// ─── State snapshot (as test/port/goldens-a.test.ts takes it) ───────────────

func aRepoState(repo string) map[string]any {
	gitOk := func(args ...string) string {
		out, _ := aGit(repo, args...)
		return out
	}
	lines := func(s string) []any {
		out := []any{}
		for _, l := range strings.Split(s, "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	untracked := []any{}
	for _, f := range lines(gitOk("ls-files", "-o", "--exclude-standard")) {
		untracked = append(untracked, strings.TrimSpace(gitOk("hash-object", "--", f.(string)))+" "+f.(string))
	}
	gitDir := strings.TrimSpace(gitOk("rev-parse", "--absolute-git-dir"))
	ops := []any{}
	for _, n := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "rebase-merge", "rebase-apply"} {
		if gitDir != "" && aExists(filepath.Join(gitDir, n)) {
			ops = append(ops, n)
		}
	}
	return map[string]any{
		"status":    lines(gitOk("status", "--porcelain=v2", "--branch")),
		"refs":      lines(gitOk("for-each-ref", "--format=%(objectname) %(refname)")),
		"log":       lines(gitOk("log", "--format=%H%x00%P%x00%s", "--all")),
		"stash":     lines(gitOk("stash", "list", "--format=%H %gs")),
		"index":     lines(gitOk("ls-files", "-s")),
		"diff":      gitOk("diff", "--no-ext-diff"),
		"untracked": untracked,
		"ops":       ops,
	}
}

func aGitVersion(t *testing.T) string {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`version (\d+\.\d+\.\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// ─── Normalization (the same rules as the TS capture) ───────────────────────

var (
	aNormISORe      = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z`)
	aNormWipRe      = regexp.MustCompile(`refs/gitcito/wip/\d+`)
	aNormDateLineRe = regexp.MustCompile(`(?m)^(\s*Date:\s+).+$`)
	aNormTokenRe    = regexp.MustCompile(`[0-9A-Za-z]+`)
	aNormHexRe      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	aNormDateKeys   = map[string]bool{"date": true, "time": true, "lastCommitAt": true, "scannedAt": true, "authorDate": true}
)

type aCommitInfo struct {
	tree, author, committer, body string
	parents                       []string
}

type aNormalizer struct {
	repo, home, playground string
	clone                  bool
	info                   map[string]aCommitInfo
	labels                 map[string]string
	busy                   map[string]bool
	tags                   map[string]string
	shas                   []string
	loaded                 bool
}

func newANormalizer(t *testing.T, repo string, clone bool, home string) *aNormalizer {
	return &aNormalizer{repo: repo, clone: clone, home: home, playground: testutil.PlaygroundDir(t),
		info: map[string]aCommitInfo{}, labels: map[string]string{}, busy: map[string]bool{}, tags: map[string]string{}}
}

func (n *aNormalizer) gitOk(args ...string) string {
	out, _ := aGit(n.repo, args...)
	return out
}

func (n *aNormalizer) load(v any) {
	n.loaded = true
	if n.repo == "" || !aExists(n.repo) {
		return
	}
	seeds := []string{"--all", "--reflog"}
	for _, h := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "ORIG_HEAD"} {
		if sha := strings.TrimSpace(n.gitOk("rev-parse", "--verify", "-q", h+"^{commit}")); sha != "" {
			seeds = append(seeds, sha)
		}
	}
	n.walk(seeds)
	// A dangling commit (a commit-edit preview's tip) is in no ref: seed it.
	var extra []string
	tokens := map[string]bool{}
	aCollectTokens(v, tokens)
	for tok := range tokens {
		if len(tok) == 40 {
			if _, ok := n.info[tok]; !ok && strings.TrimSpace(n.gitOk("cat-file", "-t", tok)) == "commit" {
				extra = append(extra, tok)
			}
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		n.walk(extra)
	}
	for _, line := range strings.Split(n.gitOk("for-each-ref", "--format=%(objectname) %(objecttype) %(refname)", "refs/tags"), "\n") {
		f := strings.Split(line, " ")
		if len(f) >= 3 && f[1] == "tag" && f[2] != "" {
			n.tags[f[0]] = f[2]
		}
	}
	for s := range n.info {
		n.shas = append(n.shas, s)
	}
	for s := range n.tags {
		n.shas = append(n.shas, s)
	}
	sort.Strings(n.shas)
}

func (n *aNormalizer) walk(seeds []string) {
	raw := n.gitOk(append([]string{"log", "--no-show-signature",
		"--format=%H%x00%T%x00%P%x00%an <%ae>%x00%cn <%ce>%x00%B%x1e"}, seeds...)...)
	for _, rec := range strings.Split(raw, "\x1e") {
		rec = strings.TrimPrefix(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x00")
		if len(f) < 5 || f[0] == "" {
			continue
		}
		if _, ok := n.info[f[0]]; ok {
			continue
		}
		var parents []string
		for _, p := range strings.Split(f[2], " ") {
			if p != "" {
				parents = append(parents, p)
			}
		}
		n.info[f[0]] = aCommitInfo{tree: f[1], parents: parents, author: f[3], committer: f[4], body: strings.Join(f[5:], "\x00")}
	}
}

func (n *aNormalizer) unique(prefix string) string {
	found := ""
	for _, s := range n.shas {
		if strings.HasPrefix(s, prefix) {
			if found != "" {
				return ""
			}
			found = s
		}
	}
	return found
}

func (n *aNormalizer) label(sha string) string {
	if l, ok := n.labels[sha]; ok {
		return l
	}
	c, ok := n.info[sha]
	if !ok || n.busy[sha] {
		return ""
	}
	n.busy[sha] = true
	parents := make([]string, len(c.parents))
	for i, p := range c.parents {
		parents[i] = n.label(p)
		if parents[i] == "" {
			parents[i] = p
		}
	}
	msg := n.text(aNormISORe.ReplaceAllString(c.body, "<ISO>"))
	sum := sha1.Sum([]byte(c.tree + "\n" + strings.Join(parents, " ") + "\n" + c.author + "\n" + c.committer + "\n" + msg))
	label := "c" + hex.EncodeToString(sum[:])[:10]
	delete(n.busy, sha)
	n.labels[sha] = label
	return label
}

func (n *aNormalizer) text(s string) string {
	return aNormTokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		if !aNormHexRe.MatchString(tok) {
			return tok
		}
		full := n.unique(tok)
		if full == "" {
			return tok
		}
		if tag, ok := n.tags[full]; ok {
			if len(tok) == 40 {
				return "<tag:" + tag + ">"
			}
			return "<tag:" + tag + ":short>"
		}
		l := n.label(full)
		if l == "" {
			return tok
		}
		if len(tok) == 40 {
			return "<" + l + ">"
		}
		return "<" + l + ":short>"
	})
}

func aCollectTokens(v any, out map[string]bool) {
	switch x := v.(type) {
	case string:
		for _, m := range aNormTokenRe.FindAllString(x, -1) {
			if aNormHexRe.MatchString(m) {
				out[m] = true
			}
		}
	case []any:
		for _, e := range x {
			aCollectTokens(e, out)
		}
	case map[string]any:
		for _, e := range x {
			aCollectTokens(e, out)
		}
	}
}

func (n *aNormalizer) str(s string) string {
	type pair struct{ from, to string }
	var pairs []pair
	if n.repo != "" {
		pairs = append(pairs, pair{n.repo, "<REPO>"})
		if real, err := filepath.EvalSymlinks(n.repo); err == nil && real != n.repo {
			pairs = append(pairs, pair{real, "<REPO>"})
		}
	}
	pairs = append(pairs, pair{n.playground, "<PLAYGROUND>"})
	if n.home != "" {
		pairs = append(pairs, pair{n.home, "<HOME>"})
	}
	pairs = append(pairs, pair{os.TempDir(), "<TMP>"})
	for _, p := range pairs {
		if p.from != "" {
			s = strings.ReplaceAll(s, p.from, p.to)
		}
	}
	if n.clone && n.repo != "" {
		s = strings.ReplaceAll(s, filepath.Base(n.repo), "<REPONAME>")
	}
	s = aNormISORe.ReplaceAllString(s, "<ISO>")
	s = aNormWipRe.ReplaceAllString(s, "refs/gitcito/wip/<N>")
	s = aNormDateLineRe.ReplaceAllString(s, "$1<DATE>")
	return n.text(s)
}

func (n *aNormalizer) value(v any, key string) any {
	if aNormDateKeys[key] {
		switch x := v.(type) {
		case float64:
			if x != 0 {
				return "<DATE>"
			}
		case string:
			if x != "" {
				return "<DATE>"
			}
		}
	}
	if arr, ok := v.([]any); ok && key == "activity" {
		return fmt.Sprintf("<ACTIVITY:%d>", len(arr))
	}
	switch x := v.(type) {
	case string:
		return n.str(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = n.value(e, "")
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = n.value(e, k)
		}
		return out
	}
	return v
}

func (n *aNormalizer) normalize(v map[string]any) map[string]any {
	if !n.loaded {
		n.load(v)
	}
	out := n.value(v, "").(map[string]any)
	// The state's log lists every commit; its order follows commit dates,
	// which the playground does not pin, so it is compared as a set.
	if st, ok := out["state"].(map[string]any); ok {
		if log, ok := st["log"].([]any); ok {
			sort.Slice(log, func(i, j int) bool { return log[i].(string) < log[j].(string) })
		}
	}
	return out
}
