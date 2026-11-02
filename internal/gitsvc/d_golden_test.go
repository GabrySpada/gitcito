package gitsvc

// Replays the slice D goldens captured from TypeScript by
// test/port/goldens-d.test.ts. Each golden is self-describing (repository
// kind, setup ops, args with placeholders, captures, masks); this file is the
// Go twin of that runner, and the normalization must stay byte-for-byte the
// same as the TypeScript one.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// dSliceMethods are the gitService methods this slice owns.
var dSliceMethods = []string{
	"addToGitignore", "addToGitignoreAt", "applyDoctorFix", "archiveCreate", "attributeFiles", "attributeWrite",
	"bundleCreate", "bundleFetch", "bundleInspect", "checkAttributes", "clean", "cleanPreview", "clearLocks", "clone",
	"credentialStatus", "deleteHook", "diffDrivers", "diffDriverSuggestions", "discard", "fetchAll", "filterDrivers",
	"filterDryRun", "forgetCredential", "fsck", "getUser", "hooksInfo", "init", "initHere", "lastFetchAt", "lfsInfo",
	"lfsPrune", "lfsPull", "lfsTrack", "lfsUntrack", "maintenanceRun", "maintenanceSchedule", "maintenanceStats",
	"pull", "pullBranch", "push", "pushAllTags", "pushToRemotes", "readHook", "remoteBranches", "replaceDelete",
	"replaceGraft", "replacements", "replaceObject", "repoConfig", "repoDoctor", "setBranchRef",
	"setCredentialHelper", "setDiffDriver", "setFilterDriver", "setHookEnabled", "setRepoConfig", "setUpstream",
	"setUser", "setUseReplaceRefs", "staleLocks", "submoduleAdd", "submoduleDeinit", "submoduleRemove", "submodules",
	"submoduleSetUrl", "submoduleSync", "submoduleUpdate", "subtreeAdd", "subtreeForget", "subtreePull",
	"subtreePush", "subtrees", "subtreeSplit", "suggestRepoConfig", "untrack", "upstreamSuggestion", "worktreeAdd",
	"worktreeRemove", "worktrees", "writeHook",
}

type dGolden struct {
	Method  string           `json:"method"`
	Fixture string           `json:"fixture"`
	Case    string           `json:"case"`
	Repo    string           `json:"repo"`
	Setup   []map[string]any `json:"setup"`
	Args    []any            `json:"args"`
	StateOf string           `json:"stateOf"`
	Capture []map[string]any `json:"capture"`
	Mask    []string         `json:"mask"`
	raw     map[string]any
}

// The fixed environment both runners use: an isolated HOME/global config and
// pinned author/committer identities and dates, so commits made during a case
// get the same content on both sides.
var dGoldenEnv = map[string]string{
	"GIT_CONFIG_NOSYSTEM": "1",
	"GIT_TERMINAL_PROMPT": "0",
	"GIT_AUTHOR_NAME":     "Golden Author",
	"GIT_AUTHOR_EMAIL":    "author@gitcito.invalid",
	"GIT_AUTHOR_DATE":     "2001-02-03T04:05:06Z",
	"GIT_COMMITTER_NAME":  "Golden Committer",
	"GIT_COMMITTER_EMAIL": "committer@gitcito.invalid",
	"GIT_COMMITTER_DATE":  "2001-02-03T04:05:06Z",
}

const dGlobalConfig = "[user]\n\tname = Gitcito Test\n\temail = test@gitcito.invalid\n" +
	"[init]\n\tdefaultBranch = main\n" +
	"[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n"

// dIsolate installs the golden environment for the calling test.
func dIsolate(t *testing.T) testutil.GitEnv {
	t.Helper()
	env := testutil.IsolateGitEnv(t)
	for k, v := range dGoldenEnv {
		t.Setenv(k, v)
	}
	xdgData := filepath.Join(filepath.Dir(env.Home), "xdg-data")
	if err := os.MkdirAll(xdgData, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", xdgData)
	return env
}

func TestDGoldens(t *testing.T) {
	playground := testutil.Playground(t)
	root := testutil.MustRepoRoot(t)
	env := dIsolate(t)
	dir := filepath.Join("testdata", "goldens")
	total := 0
	for _, method := range dSliceMethods {
		files, _ := filepath.Glob(filepath.Join(dir, method, "*.json"))
		if len(files) == 0 {
			t.Errorf("no golden for %s", method)
		}
		for _, file := range files {
			total++
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var g dGolden
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if err := json.Unmarshal(raw, &g.raw); err != nil {
				t.Fatal(err)
			}
			t.Run(method+"/"+strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
				dReplay(t, g, playground, root, env)
			})
		}
	}
	if total < len(dSliceMethods) {
		t.Errorf("only %d goldens for %d methods", total, len(dSliceMethods))
	}
}

type dPlaces map[string]string

var dRevRe = regexp.MustCompile(`<(REV|OBJ)(?:@(\w+))?:([^>]+)>`)

func dGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func (ph dPlaces) expandString(t *testing.T, s string) string {
	out := dRevRe.ReplaceAllStringFunc(s, func(m string) string {
		sm := dRevRe.FindStringSubmatch(m)
		dir := ph["REPO"]
		if sm[2] != "" {
			dir = ph[sm[2]]
		}
		sha := strings.TrimSpace(dGitOut(t, dir, "rev-parse", sm[3]))
		if sm[1] == "REV" {
			return sha
		}
		return filepath.Join(dir, ".git", "objects", sha[:2], sha[2:])
	})
	for name, path := range ph {
		out = strings.ReplaceAll(out, "<"+name+">", path)
	}
	return out
}

func (ph dPlaces) expand(t *testing.T, v any) any {
	switch x := v.(type) {
	case string:
		return ph.expandString(t, x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ph.expand(t, e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = ph.expand(t, e)
		}
		return out
	}
	return v
}

func (ph dPlaces) path(t *testing.T, p string) string {
	e := ph.expandString(t, p)
	if filepath.IsAbs(e) {
		return e
	}
	return filepath.Join(ph["REPO"], e)
}

func dStrs(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, a := range arr {
		s, _ := a.(string)
		out = append(out, s)
	}
	return out
}

func (ph dPlaces) applyOp(t *testing.T, op map[string]any) {
	t.Helper()
	switch {
	case op["git"] != nil:
		dirArg, _ := op["dir"].(string)
		if dirArg == "" {
			dirArg = "<REPO>"
		}
		dGitOut(t, ph.path(t, dirArg), dStrs(ph.expand(t, op["git"]))...)
	case op["write"] != nil:
		p := ph.path(t, op["write"].(string))
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(op["content"].(string)), 0o666); err != nil {
			t.Fatal(err)
		}
	case op["mkdir"] != nil:
		if err := os.MkdirAll(ph.path(t, op["mkdir"].(string)), 0o777); err != nil {
			t.Fatal(err)
		}
	case op["rm"] != nil:
		_ = os.RemoveAll(ph.path(t, op["rm"].(string)))
	case op["mtime"] != nil:
		ms := op["ms"].(float64)
		when := time.UnixMilli(int64(ms))
		if err := os.Chtimes(ph.path(t, op["mtime"].(string)), when, when); err != nil {
			t.Fatal(err)
		}
	case op["chmod"] != nil:
		if err := os.Chmod(ph.path(t, op["chmod"].(string)), os.FileMode(int(op["mode"].(float64)))); err != nil {
			t.Fatal(err)
		}
	case op["copy"] != nil:
		ph[op["as"].(string)] = testutil.CloneFixture(t, op["copy"].(string))
	case op["tmpdir"] != nil:
		name := op["tmpdir"].(string)
		d, err := os.MkdirTemp(ph["TMP"], "gitcito-"+strings.ToLower(name)+"-")
		if err != nil {
			t.Fatal(err)
		}
		ph[name] = d
	default:
		t.Fatalf("unknown setup op %v", op)
	}
}

func (ph dPlaces) capture(t *testing.T, c map[string]any) any {
	switch {
	case c["git"] != nil:
		dirArg, _ := c["dir"].(string)
		if dirArg == "" {
			dirArg = "<REPO>"
		}
		cmd := exec.Command("git", append([]string{"-C", ph.path(t, dirArg)}, dStrs(ph.expand(t, c["git"]))...)...)
		out, err := cmd.Output()
		if err != nil {
			return map[string]any{"failed": true}
		}
		return map[string]any{"out": strings.TrimSpace(string(out))}
	case c["read"] != nil:
		b, err := os.ReadFile(ph.path(t, c["read"].(string)))
		if err != nil {
			return nil
		}
		return dDecodeUTF8(b)
	case c["exists"] != nil:
		return dExists(ph.path(t, c["exists"].(string)))
	case c["mode"] != nil:
		st, err := os.Stat(ph.path(t, c["mode"].(string)))
		if err != nil {
			return nil
		}
		return float64(st.Mode().Perm())
	case c["tar"] != nil:
		out, err := exec.Command("tar", "-tf", ph.path(t, c["tar"].(string))).Output()
		if err != nil {
			t.Fatal(err)
		}
		list := []any{}
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				list = append(list, l)
			}
		}
		return list
	case c["list"] != nil:
		entries, err := os.ReadDir(ph.path(t, c["list"].(string)))
		if err != nil {
			return nil
		}
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		dSortJS(names)
		out := []any{}
		for _, n := range names {
			out = append(out, n)
		}
		return out
	}
	t.Fatalf("unknown capture %v", c)
	return nil
}

func dLinesOf(s string) []any {
	out := []any{}
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

var dHexRe = regexp.MustCompile(`\b[0-9a-f]{7,64}\b`)

// dCanonicalLog orders `log --all` lines by what does not move between
// playground runs (see canonicalLog in the TS runner).
func dCanonicalLog(lines []any) []any {
	type row struct {
		line, sha, subject string
		parents            []string
	}
	rows := make([]row, 0, len(lines))
	bySha := map[string]*row{}
	for _, l := range lines {
		s := l.(string)
		f := strings.Split(s, "\x00")
		r := row{line: s, sha: f[0]}
		if len(f) > 1 && f[1] != "" {
			r.parents = strings.Split(f[1], " ")
		}
		if len(f) > 2 {
			r.subject = dHexRe.ReplaceAllString(f[2], "#")
		}
		rows = append(rows, r)
	}
	for i := range rows {
		bySha[rows[i].sha] = &rows[i]
	}
	depth := map[string]int{}
	depthOf := func(sha string) int {
		if d, ok := depth[sha]; ok {
			return d
		}
		stack := []string{sha}
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			r := bySha[top]
			var pending []string
			if r != nil {
				for _, p := range r.parents {
					if _, in := bySha[p]; in {
						if _, done := depth[p]; !done {
							pending = append(pending, p)
						}
					}
				}
			}
			if len(pending) > 0 {
				stack = append(stack, pending...)
				continue
			}
			stack = stack[:len(stack)-1]
			if _, done := depth[top]; done {
				continue
			}
			d := 0
			if r != nil {
				for _, p := range r.parents {
					if _, in := bySha[p]; in && depth[p]+1 > d {
						d = depth[p] + 1
					}
				}
			}
			depth[top] = d
		}
		return depth[sha]
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		var ps []string
		for _, p := range r.parents {
			sub := ""
			if pr := bySha[p]; pr != nil {
				sub = pr.subject
			}
			ps = append(ps, sub)
		}
		d := strconv.Itoa(depthOf(r.sha))
		keys[i] = strings.Join([]string{r.subject, strings.Repeat("0", 8-len(d)) + d, strconv.Itoa(len(r.parents)), strings.Join(ps, "\x01")}, "\x02")
	}
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		if c := dCompareUTF16(keys[idx[a]], keys[idx[b]]); c != 0 {
			return c < 0
		}
		return dCompareUTF16(rows[idx[a]].line, rows[idx[b]].line) < 0
	})
	out := make([]any, len(rows))
	for i, j := range idx {
		out[i] = rows[j].line
	}
	return out
}

var (
	dTSRe   = regexp.MustCompile(`\d{10,}`)
	dTimeRe = regexp.MustCompile(`\d{4}-\d\d-\d\dT[\d:.]+Z`)
)

func dSnapshot(dir string) any {
	try := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			return ""
		}
		return string(out)
	}
	snaps := []any{}
	for _, l := range dLinesOf(try("for-each-ref", "--format=%(refname)%00%(objectname)", "refs/gitcito")) {
		f := strings.SplitN(l.(string), "\x00", 2)
		sha := f[1]
		snaps = append(snaps, map[string]any{
			"ref":     dTSRe.ReplaceAllString(f[0], "<TS>"),
			"tree":    strings.TrimSpace(try("rev-parse", sha+"^{tree}")),
			"parents": strings.TrimSpace(try("log", "-1", "--format=%P", sha)),
			"subject": dTimeRe.ReplaceAllString(strings.TrimSpace(try("log", "-1", "--format=%s", sha)), "<TIME>"),
		})
	}
	refs := []any{}
	for _, l := range dLinesOf(try("for-each-ref", "--format=%(refname) %(objectname)")) {
		if !strings.HasPrefix(l.(string), "refs/gitcito/") {
			refs = append(refs, l)
		}
	}
	return map[string]any{
		"status": dLinesOf(try("status", "--porcelain=v2")),
		"refs":   refs,
		"log": dCanonicalLog(dLinesOf(try("--no-replace-objects", "log", "--format=%H%x00%P%x00%s",
			"--exclude=refs/gitcito/*", "--exclude=refs/replace/*", "--all"))),
		"stash":     dLinesOf(try("stash", "list")),
		"index":     dLinesOf(try("ls-files", "-s")),
		"snapshots": snaps,
	}
}

// ─── normalization (the TS one, step for step) ─────────────────────────────

func dWalk(v any, fn func(string) string) any {
	switch x := v.(type) {
	case string:
		return fn(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = dWalk(e, fn)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return dCompareUTF16(keys[i], keys[j]) < 0 })
		out := map[string]any{}
		for _, k := range keys {
			out[k] = dWalk(x[k], fn)
		}
		return out
	}
	return v
}

func dApplyMask(v any, keys map[string]bool) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = dApplyMask(e, keys)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			switch {
			case !keys[k]:
				out[k] = dApplyMask(e, keys)
			case dIsNumber(e):
				if e.(float64) > 0 {
					out[k] = "<POS>"
				} else {
					out[k] = e
				}
			case e == nil || e == "":
				out[k] = e
			default:
				out[k] = "<MASKED>"
			}
		}
		return out
	}
	return v
}

func dIsNumber(v any) bool { _, ok := v.(float64); return ok }

func dRenameShas(v any) any {
	labels := map[string]string{}
	var full []string
	dWalk(v, func(s string) string {
		for _, m := range dHexRe.FindAllString(s, -1) {
			if (len(m) == 40 || len(m) == 64) && labels[m] == "" {
				labels[m] = "<SHA" + strconv.Itoa(len(labels)+1) + ">"
				full = append(full, m)
			}
		}
		return s
	})
	abbrevs := map[string]string{}
	return dWalk(v, func(s string) string {
		return dHexRe.ReplaceAllStringFunc(s, func(m string) string {
			if l, ok := labels[m]; ok {
				return l
			}
			var owners []string
			for _, f := range full {
				if strings.HasPrefix(f, m) {
					owners = append(owners, f)
				}
			}
			if len(owners) == 1 {
				return labels[owners[0]] + "~"
			}
			if len(m) > 12 {
				return m
			}
			if _, ok := abbrevs[m]; !ok {
				abbrevs[m] = "<ABBR" + strconv.Itoa(len(abbrevs)+1) + ">"
			}
			return abbrevs[m]
		})
	})
}

func dNormalize(raw map[string]any, ph dPlaces, mask []string) any {
	type pair struct{ path, label string }
	var pairs []pair
	for name, p := range ph {
		if p != "" {
			pairs = append(pairs, pair{p, "<" + name + ">"})
		}
	}
	// Longest first; equal lengths cannot both match the same text.
	sort.SliceStable(pairs, func(i, j int) bool {
		if len(pairs[i].path) != len(pairs[j].path) {
			return len(pairs[i].path) > len(pairs[j].path)
		}
		return pairs[i].label < pairs[j].label
	})
	b, _ := json.Marshal(raw)
	var generic any
	_ = json.Unmarshal(b, &generic)
	if len(mask) > 0 {
		keys := map[string]bool{}
		for _, k := range mask {
			keys[k] = true
		}
		generic = dApplyMask(generic, keys)
	}
	generic = dWalk(generic, func(s string) string {
		for _, p := range pairs {
			s = strings.ReplaceAll(s, p.path, p.label)
		}
		return s
	})
	return dRenameShas(generic)
}

// ─── replay ─────────────────────────────────────────────────────────────────

func dReplay(t *testing.T, g dGolden, playground, root string, env testutil.GitEnv) {
	if err := os.WriteFile(env.GlobalConfig, []byte(dGlobalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(env.Home, ".git-credentials"))

	ph := dPlaces{"HOME": env.Home, "PLAYGROUND": playground, "ROOT": root}
	var repo string
	switch g.Repo {
	case "shared":
		if g.Fixture != "" {
			repo = filepath.Join(playground, g.Fixture)
		} else {
			repo = t.TempDir()
		}
		ph["TMP"] = t.TempDir()
	case "clone":
		repo = testutil.CloneFixture(t, g.Fixture)
		// The TS runner keeps every temp dir side by side under one base, and a
		// relative submodule URL resolves against that parent.
		ph["TMP"] = filepath.Dir(repo)
	default:
		base := t.TempDir()
		var err error
		repo, err = os.MkdirTemp(base, "gitcito-tmp-")
		if err != nil {
			t.Fatal(err)
		}
		if g.Repo == "init" {
			dGitOut(t, repo, "init", "-q", "-b", "main")
		}
		ph["TMP"] = base
	}
	if g.Repo == "shared" && g.Fixture == "" {
		ph["TMP"] = repo
	}
	ph["REPO"] = repo
	for _, op := range g.Setup {
		ph.applyOp(t, op)
	}
	args := ph.expand(t, any(g.Args)).([]any)

	s := New(nil, nil)
	result, err := dCall(t, s, g.Method, args)
	out := map[string]any{}
	if err != nil {
		out["error"] = err.Error()
	} else {
		testutil.NoNulls(t, result)
		out["result"] = result
	}
	if _, ok := g.raw["state"]; ok {
		dir := repo
		if g.StateOf == "result" {
			dir, _ = result.(string)
		}
		if dir == "" {
			out["state"] = nil
		} else {
			out["state"] = dSnapshot(dir)
		}
	}
	if len(g.Capture) > 0 {
		var extra []any
		for _, c := range g.Capture {
			extra = append(extra, ph.capture(t, c))
		}
		out["extra"] = extra
	}
	got := dNormalize(out, ph, g.Mask)
	want := map[string]any{}
	for _, k := range []string{"result", "error", "state", "extra"} {
		if v, ok := g.raw[k]; ok {
			want[k] = v
		}
	}
	if !reflect.DeepEqual(got, any(want)) {
		gb, _ := json.MarshalIndent(got, "", "  ")
		wb, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("%s %s-%s differs from the TypeScript golden\n--- want\n%s\n--- got\n%s", g.Method, g.Fixture, g.Case, wb, gb)
	}
}

// dArgs decodes positional args the way the bridge will: each JSON value into
// the parameter's type; a missing trailing arg is the zero value (nil for
// pointers, which is how TS defaults are recognised).
type dArgs []any

func (a dArgs) into(t *testing.T, i int, target any) {
	t.Helper()
	if i >= len(a) || a[i] == nil {
		return
	}
	b, err := json.Marshal(a[i])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, target); err != nil {
		t.Fatalf("arg %d: %v", i, err)
	}
}

func (a dArgs) str(t *testing.T, i int) string {
	var s string
	a.into(t, i, &s)
	return s
}

func (a dArgs) strp(t *testing.T, i int) *string {
	if i >= len(a) || a[i] == nil {
		return nil
	}
	s := a.str(t, i)
	return &s
}

func (a dArgs) boolean(t *testing.T, i int) bool {
	var b bool
	a.into(t, i, &b)
	return b
}

func (a dArgs) boolp(t *testing.T, i int) *bool {
	if i >= len(a) || a[i] == nil {
		return nil
	}
	b := a.boolean(t, i)
	return &b
}

func (a dArgs) strs(t *testing.T, i int) []string {
	var s []string
	a.into(t, i, &s)
	return s
}

// dCall invokes the Go method for a TS method name with decoded args, and
// returns its result as a JSON-ready value (nil for void).
func dCall(t *testing.T, s *GitService, method string, raw []any) (any, error) {
	ctx := context.Background()
	a := dArgs(raw)
	r := a.str(t, 0)
	wrap := func(v any, err error) (any, error) { return v, err }
	void := func(err error) (any, error) { return nil, err }
	switch method {
	case "fetchAll":
		return wrap(s.FetchAll(ctx, r))
	case "lastFetchAt":
		return wrap(s.LastFetchAt(ctx, r))
	case "pull":
		return void(s.Pull(ctx, r, a.str(t, 1)))
	case "upstreamSuggestion":
		return wrap(s.UpstreamSuggestion(ctx, r))
	case "setUpstream":
		return void(s.SetUpstream(ctx, r, a.str(t, 1), a.strp(t, 2)))
	case "push":
		var o PushOptions
		a.into(t, 2, &o)
		return void(s.Push(ctx, r, a.str(t, 1), o))
	case "pullBranch":
		return void(s.PullBranch(ctx, r, a.str(t, 1)))
	case "setBranchRef":
		return void(s.SetBranchRef(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "pushToRemotes":
		var o PushToRemotesOptions
		a.into(t, 3, &o)
		return wrap(s.PushToRemotes(ctx, r, a.str(t, 1), a.strs(t, 2), o))
	case "pushAllTags":
		return void(s.PushAllTags(ctx, r, a.strp(t, 1)))
	case "staleLocks":
		return wrap(s.StaleLocks(ctx, r))
	case "clearLocks":
		return wrap(s.ClearLocks(ctx, r, a.strs(t, 1)))
	case "remoteBranches":
		return wrap(s.RemoteBranches(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "clone":
		var o CloneOptions
		a.into(t, 3, &o)
		return wrap(s.Clone(ctx, r, a.str(t, 1), a.str(t, 2), o))
	case "init":
		return wrap(s.Init(ctx, r, a.str(t, 1)))
	case "initHere":
		return void(s.InitHere(ctx, r))
	case "replacements":
		return wrap(s.Replacements(ctx, r))
	case "replaceGraft":
		return wrap(s.ReplaceGraft(ctx, r, a.str(t, 1), a.strs(t, 2)))
	case "replaceObject":
		return void(s.ReplaceObject(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "replaceDelete":
		return void(s.ReplaceDelete(ctx, r, a.str(t, 1)))
	case "setUseReplaceRefs":
		return void(s.SetUseReplaceRefs(ctx, r, a.boolean(t, 1)))
	case "credentialStatus":
		return wrap(s.CredentialStatus(ctx, r))
	case "setCredentialHelper":
		return void(s.SetCredentialHelper(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "forgetCredential":
		return void(s.ForgetCredential(ctx, r, a.str(t, 1), a.strp(t, 2)))
	case "attributeFiles":
		return wrap(s.AttributeFiles(ctx, r))
	case "attributeWrite":
		return void(s.AttributeWrite(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "checkAttributes":
		return wrap(s.CheckAttributes(ctx, r, a.strs(t, 1)))
	case "diffDrivers":
		return wrap(s.DiffDrivers(ctx, r))
	case "diffDriverSuggestions":
		return wrap(s.DiffDriverSuggestions(ctx, r))
	case "setDiffDriver":
		return void(s.SetDiffDriver(ctx, r, a.str(t, 1), a.str(t, 2), a.boolean(t, 3)))
	case "filterDrivers":
		return wrap(s.FilterDrivers(ctx, r))
	case "filterDryRun":
		return wrap(s.FilterDryRun(ctx, r, a.str(t, 1), a.str(t, 2), a.str(t, 3)))
	case "setFilterDriver":
		var d FilterDriverInput
		a.into(t, 2, &d)
		return wrap(s.SetFilterDriver(ctx, r, a.str(t, 1), d, a.boolean(t, 3)))
	case "maintenanceStats":
		return wrap(s.MaintenanceStats(ctx, r))
	case "maintenanceRun":
		return wrap(s.MaintenanceRun(ctx, r, a.str(t, 1)))
	case "maintenanceSchedule":
		return wrap(s.MaintenanceSchedule(ctx, r, a.boolean(t, 1)))
	case "fsck":
		return wrap(s.Fsck(ctx, r))
	case "bundleCreate":
		var sc BundleScope
		a.into(t, 2, &sc)
		return wrap(s.BundleCreate(ctx, r, a.str(t, 1), sc))
	case "bundleInspect":
		return wrap(s.BundleInspect(ctx, r, a.str(t, 1)))
	case "bundleFetch":
		return wrap(s.BundleFetch(ctx, r, a.str(t, 1), a.strs(t, 2)))
	case "archiveCreate":
		return wrap(s.ArchiveCreate(ctx, r, a.str(t, 1), a.str(t, 2), a.str(t, 3), a.str(t, 4), a.str(t, 5)))
	case "cleanPreview":
		return wrap(s.CleanPreview(ctx, r))
	case "clean":
		return wrap(s.Clean(ctx, r, a.strs(t, 1), a.boolean(t, 2)))
	case "discard":
		return void(s.Discard(ctx, r, a.strs(t, 1), a.boolean(t, 2)))
	case "addToGitignore":
		return wrap(s.AddToGitignore(ctx, r, a.strs(t, 1)))
	case "addToGitignoreAt":
		return wrap(s.AddToGitignoreAt(ctx, r, a.str(t, 1), a.strs(t, 2)))
	case "untrack":
		return void(s.Untrack(ctx, r, a.strs(t, 1), a.boolean(t, 2)))
	case "repoConfig":
		return wrap(s.RepoConfig(ctx, r))
	case "setRepoConfig":
		var c RepoConfig
		a.into(t, 1, &c)
		return wrap(s.SetRepoConfig(ctx, r, c))
	case "repoDoctor":
		return wrap(s.RepoDoctor(ctx, r))
	case "applyDoctorFix":
		var f DoctorFix
		a.into(t, 1, &f)
		return void(s.ApplyDoctorFix(ctx, r, f))
	case "suggestRepoConfig":
		return wrap(s.SuggestRepoConfig(ctx, r))
	case "subtrees":
		return wrap(s.Subtrees(ctx, r))
	case "subtreeAdd":
		return void(s.SubtreeAdd(ctx, r, a.str(t, 1), a.str(t, 2), a.str(t, 3), a.boolp(t, 4)))
	case "subtreePull":
		return void(s.SubtreePull(ctx, r, a.str(t, 1), a.str(t, 2), a.str(t, 3), a.boolp(t, 4)))
	case "subtreePush":
		return void(s.SubtreePush(ctx, r, a.str(t, 1), a.str(t, 2), a.str(t, 3)))
	case "subtreeSplit":
		return wrap(s.SubtreeSplit(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "subtreeForget":
		return void(s.SubtreeForget(ctx, r, a.str(t, 1)))
	case "hooksInfo":
		return wrap(s.HooksInfo(ctx, r))
	case "readHook":
		return wrap(s.ReadHook(ctx, r, a.str(t, 1)))
	case "writeHook":
		return void(s.WriteHook(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "setHookEnabled":
		return void(s.SetHookEnabled(ctx, r, a.str(t, 1), a.boolean(t, 2)))
	case "deleteHook":
		return void(s.DeleteHook(ctx, r, a.str(t, 1)))
	case "lfsInfo":
		return wrap(s.LfsInfo(ctx, r))
	case "lfsTrack":
		return void(s.LfsTrack(ctx, r, a.str(t, 1)))
	case "lfsUntrack":
		return void(s.LfsUntrack(ctx, r, a.str(t, 1)))
	case "lfsPull":
		return void(s.LfsPull(ctx, r))
	case "lfsPrune":
		return void(s.LfsPrune(ctx, r))
	case "worktrees":
		return wrap(s.Worktrees(ctx, r))
	case "worktreeAdd":
		return void(s.WorktreeAdd(ctx, r, a.str(t, 1), a.str(t, 2), a.boolean(t, 3), a.str(t, 4)))
	case "worktreeRemove":
		return void(s.WorktreeRemove(ctx, r, a.str(t, 1), a.boolean(t, 2)))
	case "submodules":
		return wrap(s.Submodules(ctx, r))
	case "submoduleAdd":
		return void(s.SubmoduleAdd(ctx, r, a.str(t, 1), a.str(t, 2), a.str(t, 3)))
	case "submoduleSetUrl":
		return void(s.SubmoduleSetUrl(ctx, r, a.str(t, 1), a.str(t, 2)))
	case "submoduleUpdate":
		return void(s.SubmoduleUpdate(ctx, r, a.str(t, 1), a.boolp(t, 2)))
	case "submoduleSync":
		return void(s.SubmoduleSync(ctx, r, a.str(t, 1)))
	case "submoduleDeinit":
		return void(s.SubmoduleDeinit(ctx, r, a.str(t, 1), a.boolean(t, 2)))
	case "submoduleRemove":
		return void(s.SubmoduleRemove(ctx, r, a.str(t, 1)))
	case "getUser":
		return wrap(s.GetUser(ctx, r))
	case "setUser":
		return void(s.SetUser(ctx, r, a.str(t, 1), a.str(t, 2)))
	}
	t.Fatalf("no Go dispatch for %s", method)
	return nil, fmt.Errorf("unreachable")
}
