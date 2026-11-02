package gitsvc

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// Replays the slice C goldens written by test/port/goldens-c.test.ts: same
// fixture, same setup, same arguments, same normalization — the Go method
// must return what the TypeScript method returned (or fail with the same
// message) and leave the repository in the same state.

// cSliceMethods are the gitService methods of wave-1 slice C. Only their
// golden directories are replayed here; other slices replay their own.
var cSliceMethods = []string{
	"amendCommitMessage", "applyPatch", "applyRepoFileActions", "autosquash", "bisectCancel", "bisectMark",
	"bisectReset", "bisectRunScript", "bisectStart", "bisectStatus", "checkout", "checkoutRemote", "commit",
	"commitAuthor", "commitFixup", "commitsTouchingPath", "commitSummary", "commitTemplate", "conflictCommits",
	"createBranch", "deleteBranch", "deleteRemoteBranch", "fileSizes", "filesToPush", "formatPatch", "fsCreate",
	"fsDelete", "fsExisting", "fsImport", "fsMove", "fsRename", "getCommitMessage", "gitflowFinish",
	"gitflowInit", "gitflowStart", "gitflowStatus", "gitflowUndo", "ignoredTrackedFiles", "listTrackedFiles",
	"merge", "mergeInto", "rebase", "rebaseAbort", "rebaseOnto", "remoteHasBranches", "renameBranch",
	"renameBranchRemote", "rerereClear", "rerereForget", "rerereStatus", "resolveDivergedCheckout", "resolveRev",
	"setRerere", "setSigningConfig", "signingConfig", "squashCommits", "stackClearParent", "stackInfo",
	"stackInsert", "stackLeaves", "stackPruneMerged", "stackReorder", "stackRestack", "stackSetParent",
	"stackSetRoute", "treeStatus",
}

// cgGolden is one golden file.
type cgGolden struct {
	Method  string            `json:"method"`
	Fixture string            `json:"fixture"`
	Case    string            `json:"case"`
	Kind    string            `json:"kind"`
	Setup   []cgStep          `json:"setup"`
	Args    []json.RawMessage `json:"args"`
	Result  json.RawMessage   `json:"result"`
	Error   *string           `json:"error"`
	State   json.RawMessage   `json:"state"`
	Events  *string           `json:"events"`
}

// cgStep is one setup step (see the Step type in the TS file).
type cgStep struct {
	Git       []string          `json:"git"`
	NoRepo    bool              `json:"noRepo"`
	AllowFail bool              `json:"allowFail"`
	Write     *string           `json:"write"`
	Content   string            `json:"content"`
	Out       bool              `json:"out"`
	Mode      int               `json:"mode"`
	Mkdir     *string           `json:"mkdir"`
	Call      string            `json:"call"`
	Args      []json.RawMessage `json:"args"`
	As        string            `json:"as"`
}

// cgEmitter collects emitted events in order.
type cgEmitter struct {
	mu     sync.Mutex
	events []string
}

func (e *cgEmitter) Emit(name string, data any) {
	if name != EventBisectOutput {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, data.(string))
}

// cgCtx is a running case: where <REPO> and <OUT> point, setup results, and
// the paths normalization replaces.
type cgCtx struct {
	repo, out, home, playground string
	vars                        map[string]json.RawMessage
}

func TestCGoldens(t *testing.T) {
	root := filepath.Join(testutil.MustRepoRoot(t), "internal", "gitsvc", "testdata", "goldens")
	total := 0
	for _, method := range cSliceMethods {
		files, err := filepath.Glob(filepath.Join(root, method, "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Errorf("no goldens for %s", method)
		}
		for _, file := range files {
			total++
			t.Run(method+"/"+strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
				cgReplay(t, file)
			})
		}
	}
	t.Logf("%d goldens replayed", total)
}

func cgReplay(t *testing.T, file string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var g cgGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	env := cIsolate(t)
	c := &cgCtx{home: env.Home, playground: testutil.PlaygroundDir(t), vars: map[string]json.RawMessage{}}
	if g.Kind == "write" || len(g.Setup) > 0 {
		c.repo = testutil.CloneFixture(t, g.Fixture)
	} else {
		c.repo = testutil.PlaygroundRepo(t, g.Fixture)
	}
	c.out = t.TempDir()

	emitter := &cgEmitter{}
	svc := New(emitter, nil)
	ctx := context.Background()
	for i, step := range g.Setup {
		if err := cgRunStep(ctx, svc, c, step); err != nil {
			t.Fatalf("setup step %d: %v", i, err)
		}
	}

	result, callErr := cgCall(ctx, svc, g.Method, cgSubstArgs(t, c, g.Args))
	lab := newCgLabeler(c.repo)
	if g.Error != nil {
		if callErr == nil {
			t.Fatalf("want error %q, got result %s", *g.Error, cgJSON(result))
		}
		if got := cgNormString(callErr.Error(), c, lab); got != *g.Error {
			t.Errorf("error mismatch\n got: %q\nwant: %q", got, *g.Error)
		}
	} else {
		if callErr != nil {
			t.Fatalf("unexpected error: %v", callErr)
		}
		if result != nil {
			testutil.NoNulls(t, result)
		}
		got := cgNorm(cgDecode(t, cgJSON(result)), c, lab)
		want := cgDecode(t, g.Result)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("result mismatch\n got: %s\nwant: %s", cgJSON(got), cgJSON(want))
		}
	}
	if g.Kind == "write" {
		got := cgSnapshot(c, lab)
		want := cgDecode(t, g.State)
		if !reflect.DeepEqual(got, want) {
			gm, wm := got.(map[string]any), want.(map[string]any)
			for k := range wm {
				if !reflect.DeepEqual(gm[k], wm[k]) {
					t.Errorf("state.%s mismatch\n got: %s\nwant: %s", k, cgJSON(gm[k]), cgJSON(wm[k]))
				}
			}
		}
	}
	if g.Events != nil {
		if got := cgNormString(strings.Join(emitter.events, ""), c, lab); got != *g.Events {
			t.Errorf("events mismatch\n got: %q\nwant: %q", got, *g.Events)
		}
	}
}

func cgJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func cgDecode(t *testing.T, raw []byte) any {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// cgSubst replaces <REPO>/<OUT> in strings and `{ $var }` with setup results.
func cgSubst(v any, c *cgCtx) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(strings.ReplaceAll(x, "<REPO>", c.repo), "<OUT>", c.out)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cgSubst(e, c)
		}
		return out
	case map[string]any:
		if name, ok := x["$var"].(string); ok {
			var r any
			_ = json.Unmarshal(c.vars[name], &r)
			return r
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cgSubst(e, c)
		}
		return out
	}
	return v
}

func cgSubstArgs(t *testing.T, c *cgCtx, args []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(args))
	for i, a := range args {
		out[i] = cgJSON(cgSubst(cgDecode(t, a), c))
	}
	return out
}

// cgCall invokes GitService.<Method> by reflection, decoding each JSON arg
// into the parameter's type (missing args are zero values, as `undefined`).
func cgCall(ctx context.Context, svc *GitService, method string, args []json.RawMessage) (any, error) {
	name := strings.ToUpper(method[:1]) + method[1:]
	m := reflect.ValueOf(svc).MethodByName(name)
	if !m.IsValid() {
		panic("no GitService method " + name)
	}
	mt := m.Type()
	in := []reflect.Value{reflect.ValueOf(ctx)}
	for i := 1; i < mt.NumIn(); i++ {
		p := reflect.New(mt.In(i))
		if i-1 < len(args) {
			if err := json.Unmarshal(args[i-1], p.Interface()); err != nil {
				panic("decode arg " + method + ": " + err.Error())
			}
		}
		in = append(in, p.Elem())
	}
	outs := m.Call(in)
	errV := outs[len(outs)-1]
	var err error
	if !errV.IsNil() {
		err = errV.Interface().(error)
	}
	if len(outs) == 1 {
		return nil, err
	}
	return outs[0].Interface(), err
}

func cgRunStep(ctx context.Context, svc *GitService, c *cgCtx, s cgStep) error {
	switch {
	case s.Git != nil:
		args := make([]string, len(s.Git))
		for i, a := range s.Git {
			args[i] = cgSubst(a, c).(string)
		}
		if !s.NoRepo {
			args = append([]string{"-C", c.repo}, args...)
		}
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil && !s.AllowFail {
			return &cgStepError{args, string(out), err}
		}
	case s.Write != nil:
		base := c.repo
		if s.Out {
			base = c.out
		}
		p := filepath.Join(base, *s.Write)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o666)
		if s.Mode != 0 {
			mode = os.FileMode(s.Mode)
		}
		if err := os.WriteFile(p, []byte(s.Content), mode); err != nil {
			return err
		}
		if s.Mode != 0 {
			return os.Chmod(p, mode)
		}
	case s.Mkdir != nil:
		base := c.repo
		if s.Out {
			base = c.out
		}
		return os.MkdirAll(filepath.Join(base, *s.Mkdir), 0o755)
	default:
		res, err := cgCall(ctx, svc, s.Call, cgSubstArgsNoT(c, s.Args))
		if err != nil && !s.AllowFail {
			return err
		}
		if err == nil && s.As != "" {
			c.vars[s.As] = cgJSON(res)
		}
	}
	return nil
}

func cgSubstArgsNoT(c *cgCtx, args []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(args))
	for i, a := range args {
		var v any
		_ = json.Unmarshal(a, &v)
		out[i] = cgJSON(cgSubst(v, c))
	}
	return out
}

type cgStepError struct {
	args   []string
	output string
	err    error
}

func (e *cgStepError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + e.err.Error() + "\n" + e.output
}

// ─── Normalization (mirror of the TS Labeler / normString / snapshot) ──────

func cgGit(dir string, args ...string) string {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

type cgRow struct {
	parents []string
	subject string
}

type cgLabeler struct {
	repo   string
	rows   map[string]cgRow
	order  []string
	labels map[string]string
	kinds  map[string]string
}

func newCgLabeler(repo string) *cgLabeler {
	l := &cgLabeler{repo: repo, rows: map[string]cgRow{}, labels: map[string]string{}, kinds: map[string]string{}}
	for _, line := range strings.Split(cgGit(repo, "log", "--all", "--reflog", "--format=%H%x00%P%x00%s"), "\n") {
		f := strings.Split(line, "\x00")
		if len(f[0]) == 40 {
			l.add(f[0], f)
		}
	}
	return l
}

func (l *cgLabeler) add(sha string, f []string) cgRow {
	row := cgRow{}
	if len(f) > 1 && f[1] != "" {
		row.parents = strings.Split(f[1], " ")
	}
	if len(f) > 2 {
		row.subject = f[2]
	}
	if _, ok := l.rows[sha]; !ok {
		l.order = append(l.order, sha)
	}
	l.rows[sha] = row
	return row
}

func (l *cgLabeler) kind(sha string) string {
	k, ok := l.kinds[sha]
	if !ok {
		k = strings.TrimSpace(cgGit(l.repo, "cat-file", "-t", sha))
		l.kinds[sha] = k
	}
	return k
}

var cgTagLineRe = regexp.MustCompile(`(?m)^tag (.*)$`)

func (l *cgLabeler) label(sha string) (string, bool) {
	if lb, ok := l.labels[sha]; ok {
		return lb, true
	}
	row, ok := l.rows[sha]
	if !ok && l.kind(sha) == "commit" {
		out := strings.TrimSuffix(cgGit(l.repo, "log", "-1", "--format=%H%x00%P%x00%s", sha, "--"), "\n")
		f := strings.Split(out, "\x00")
		if f[0] == sha {
			row, ok = l.add(sha, f), true
		}
	}
	if !ok {
		if l.kind(sha) == "tag" {
			name := "?"
			if m := cgTagLineRe.FindStringSubmatch(cgGit(l.repo, "cat-file", "tag", sha)); m != nil {
				name = m[1]
			}
			lb := "<TAG:" + name + ">"
			l.labels[sha] = lb
			return lb, true
		}
		return "", false
	}
	parts := []string{row.subject}
	for _, p := range row.parents {
		if lb, ok := l.label(p); ok {
			parts = append(parts, lb)
		} else {
			parts = append(parts, p)
		}
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	lb := "<" + row.subject + "@" + hex.EncodeToString(sum[:])[:8] + ">"
	l.labels[sha] = lb
	return lb, true
}

func (l *cgLabeler) expand(abbrev string) (string, bool) {
	found := ""
	for _, sha := range l.order {
		if strings.HasPrefix(sha, abbrev) {
			if found != "" {
				return "", false
			}
			found = sha
		}
	}
	return found, found != ""
}

var (
	cgISODateRe    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2})`)
	cgMailDateRe   = regexp.MustCompile(`(?m)^Date: [^\n\r\x{2028}\x{2029}]*$`)
	cgStampRe      = regexp.MustCompile(`\d{8}-\d{6}`)
	cgApplyTmpRe   = regexp.MustCompile(`gitcito-apply-\d+\.patch`)
	cgGitVersionRe = regexp.MustCompile(`\n-- \n\d+\.\d+[^\n]*\n`)
	cgFullShaRe    = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
	cgAbbrevRe     = regexp.MustCompile(`\b[0-9a-f]{7,39}\b`)
)

func cgNormString(s string, c *cgCtx, l *cgLabeler) string {
	for _, p := range [][2]string{
		{c.repo, "<REPO>"}, {c.out, "<OUT>"}, {c.playground, "<PLAYGROUND>"}, {c.home, "<HOME>"}, {os.TempDir(), "<TMP>"},
	} {
		if p[0] != "" {
			s = strings.ReplaceAll(s, p[0], p[1])
		}
	}
	s = cgISODateRe.ReplaceAllString(s, "<DATE>")
	s = cgMailDateRe.ReplaceAllString(s, "Date: <DATE>")
	s = cgStampRe.ReplaceAllString(s, "<STAMP>")
	s = cgApplyTmpRe.ReplaceAllString(s, "gitcito-apply-<N>.patch")
	s = cgGitVersionRe.ReplaceAllString(s, "\n-- \n<GITVERSION>\n")
	s = cgFullShaRe.ReplaceAllStringFunc(s, func(sha string) string {
		if lb, ok := l.label(sha); ok {
			return lb
		}
		return sha
	})
	s = cgAbbrevRe.ReplaceAllStringFunc(s, func(abbr string) string {
		if full, ok := l.expand(abbr); ok {
			if lb, ok := l.label(full); ok {
				return lb
			}
		}
		return abbr
	})
	return s
}

func cgNorm(v any, c *cgCtx, l *cgLabeler) any {
	switch x := v.(type) {
	case string:
		return cgNormString(x, c, l)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cgNorm(e, c, l)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[cgNormString(k, c, l)] = cgNorm(e, c, l)
		}
		return out
	}
	return v
}

var (
	cgConfigKeysRe   = regexp.MustCompile(`^(branch|gitflow|rerere|commit|gpg|user|gitcito|remote)\.`)
	cgOps            = []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_START", "ORIG_HEAD"}
	cgVolatilePaths  = []string{".bisect-hint"}
	cgVolatileHashRe = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
)

func cgLines(s string) []any {
	out := []any{}
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func cgMaskVolatile(lines []any) []any {
	for i, l := range lines {
		s := l.(string)
		for _, p := range cgVolatilePaths {
			if strings.HasSuffix(s, "\t"+p) || strings.HasSuffix(s, " "+p) {
				lines[i] = cgVolatileHashRe.ReplaceAllString(s, "<VOLATILE>")
			}
		}
	}
	return lines
}

// cgSnapshot is the TS snapshot(): the repository after a write, normalized.
func cgSnapshot(c *cgCtx, l *cgLabeler) any {
	repo := c.repo
	gitDir := strings.TrimSpace(cgGit(repo, "rev-parse", "--absolute-git-dir"))
	head := strings.TrimSpace(cgGit(repo, "symbolic-ref", "-q", "HEAD"))
	if head == "" {
		head = "DETACHED"
	}
	config := []any{}
	for _, line := range cgLines(cgGit(repo, "config", "--local", "--list")) {
		if cgConfigKeysRe.MatchString(line.(string)) {
			config = append(config, line)
		}
	}
	ops := []any{}
	for _, name := range cgOps {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			ops = append(ops, name)
		}
	}
	rr := 0
	if entries, err := os.ReadDir(filepath.Join(gitDir, "rr-cache")); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				rr++
			}
		}
	}
	state := map[string]any{
		"head":         head,
		"headAuthor":   strings.TrimSpace(cgGit(repo, "log", "-1", "--format=%an <%ae>")),
		"status":       cgMaskVolatile(cgLines(cgGit(repo, "status", "--porcelain=v2", "--branch"))),
		"refs":         cgLines(cgGit(repo, "for-each-ref", "--format=%(refname) %(objectname)")),
		"log":          cgLines(cgGit(repo, "log", "--all", "--format=%H%x00%P%x00%s")),
		"stash":        cgLines(cgGit(repo, "stash", "list")),
		"index":        cgMaskVolatile(cgLines(cgGit(repo, "ls-files", "-s"))),
		"diff":         cgGit(repo, "diff", "--no-color", "--no-ext-diff"),
		"config":       config,
		"globalRerere": cgLines(cgGit(repo, "config", "--global", "--get-regexp", `^rerere\.`)),
		"ops":          ops,
		"rrCache":      float64(rr),
	}
	n := cgNorm(state, c, l).(map[string]any)
	for _, k := range []string{"log", "refs"} {
		xs := n[k].([]any)
		sort.Slice(xs, func(i, j int) bool { return xs[i].(string) < xs[j].(string) })
	}
	return n
}
