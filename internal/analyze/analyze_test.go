package analyze

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func sameJSON(t *testing.T, label string, got, want any) {
	t.Helper()
	if g, w := canon(t, got), canon(t, want); !reflect.DeepEqual(g, w) {
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		t.Errorf("%s:\n got %s\nwant %s", label, gb, wb)
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Replays testdata/vectors/analyze.json, written from src/main/analyze.ts by
// test/port/processes.test.ts.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "analyze.json"))
	if err != nil {
		t.Fatal(err)
	}
	type parseCase struct {
		Input  string          `json:"input"`
		Repo   string          `json:"repo"`
		Source string          `json:"source"`
		Out    json.RawMessage `json:"out"`
	}
	var v struct {
		ParseDartAnalyze     []parseCase `json:"parseDartAnalyze"`
		ParseTsc             []parseCase `json:"parseTsc"`
		ParseEslint          []parseCase `json:"parseEslint"`
		ParseLineDiagnostics []parseCase `json:"parseLineDiagnostics"`
		ParseRuff            []parseCase `json:"parseRuff"`
		NormaliseProblems    []struct {
			Input []Problem       `json:"input"`
			Out   json.RawMessage `json:"out"`
		} `json:"normaliseProblems"`
		Plan []struct {
			Files  map[string]string `json:"files"`
			Plan   []PlanEntry       `json:"plan"`
			Detect []string          `json:"detect"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	run := func(name string, cases []parseCase, f func(c parseCase) []Problem) {
		if len(cases) == 0 {
			t.Fatalf("%s: no vectors", name)
		}
		for _, c := range cases {
			sameJSON(t, name+"("+c.Input+")", f(c), c.Out)
		}
	}
	run("parseDartAnalyze", v.ParseDartAnalyze, func(c parseCase) []Problem { return ParseDartAnalyze(c.Input, c.Repo) })
	run("parseTsc", v.ParseTsc, func(c parseCase) []Problem { return ParseTsc(c.Input, c.Repo) })
	run("parseEslint", v.ParseEslint, func(c parseCase) []Problem { return ParseEslint(c.Input, c.Repo) })
	run("parseLineDiagnostics", v.ParseLineDiagnostics, func(c parseCase) []Problem {
		return ParseLineDiagnostics(c.Input, c.Repo, c.Source)
	})
	run("parseRuff", v.ParseRuff, func(c parseCase) []Problem { return ParseRuff(c.Input, c.Repo) })
	for i, c := range v.NormaliseProblems {
		sameJSON(t, "normaliseProblems #"+string(rune('0'+i)), NormaliseProblems(c.Input), c.Out)
	}
	for _, c := range v.Plan {
		root := writeTree(t, c.Files)
		got := AnalyzerPlan(root)
		for i := range got {
			got[i].Dir = strings.Replace(got[i].Dir, root, "<ROOT>", 1)
		}
		if c.Plan == nil {
			c.Plan = []PlanEntry{}
		}
		sameJSON(t, "plan", got, c.Plan)
		sameJSON(t, "detect", DetectAnalyzers(root), c.Detect)
	}
}

// ─── test/analyze.test.ts ───────────────────────────────────────────────────

func TestAnalyzerParsers(t *testing.T) {
	repo := "/repo"
	t.Run("parses dart analyze --format=machine, unescaping the message", func(t *testing.T) {
		out := strings.Join([]string{
			`INFO|LINT|unused_import|/repo/lib/main.dart|3|8|29|Unused import: \|package:x\|.`,
			"WARNING|STATIC_WARNING|unused_local|/repo/lib/a.dart|10|5|4|The value of the local variable is not used.",
			"ERROR|COMPILE_TIME_ERROR|undefined_method|/repo/lib/b.dart|42|11|6|The method is not defined.",
			"not a diagnostic line",
		}, "\n")
		p := ParseDartAnalyze(out, repo)
		var got [][]any
		for _, x := range p {
			got = append(got, []any{x.File, x.Line, x.Severity, x.Code})
		}
		want := [][]any{
			{"lib/main.dart", 3.0, "info", "unused_import"},
			{"lib/a.dart", 10.0, "warning", "unused_local"},
			{"lib/b.dart", 42.0, "error", "undefined_method"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
		// The message keeps its pipes: they are escaped, not separators.
		if p[0].Message != "Unused import: |package:x|." {
			t.Errorf("message = %q", p[0].Message)
		}
	})
	t.Run("parses tsc --pretty false", func(t *testing.T) {
		out := "src/a.ts(12,5): error TS2304: Cannot find name 'x'.\nsrc/b.tsx(1,1): warning TS6133: Declared but never read.\nFound 2 errors."
		p := ParseTsc(out, repo)
		if len(p) != 2 || p[0].File != "src/a.ts" || p[0].Line != 12 || p[0].Col != 5 || p[0].Severity != "error" || p[0].Code != "TS2304" ||
			p[1].File != "src/b.tsx" || p[1].Severity != "warning" || p[1].Code != "TS6133" {
			t.Errorf("got %+v", p)
		}
	})
	t.Run("parses eslint -f json, mapping its numeric severity", func(t *testing.T) {
		js := `[{"filePath":"/repo/src/a.ts","messages":[
			{"ruleId":"no-unused-vars","severity":2,"message":"x is defined but never used.","line":4,"column":7},
			{"ruleId":"eqeqeq","severity":1,"message":"Expected ===.","line":9,"column":3}]},
			{"filePath":"/repo/src/clean.ts","messages":[]}]`
		p := ParseEslint(js, repo)
		if len(p) != 2 || p[0].File != "src/a.ts" || p[0].Severity != "error" || p[0].Code != "no-unused-vars" ||
			p[1].Severity != "warning" || p[1].Code != "eqeqeq" {
			t.Errorf("got %+v", p)
		}
	})
	t.Run("parses the file:line:col shape shared by clippy and go vet", func(t *testing.T) {
		clippy := "src/main.rs:10:5: warning[clippy::needless_return]: unneeded `return` statement\n" +
			"src/lib.rs:3:1: error: cannot find value `foo` in this scope\n" +
			"src/lib.rs:3:1: note: this is a continuation line"
		p := ParseLineDiagnostics(clippy, repo, "clippy")
		// The note is part of the diagnostic above it, not a problem of its own.
		if len(p) != 2 || p[0].Severity != "warning" || p[0].Code != "clippy::needless_return" || p[1].Severity != "error" || p[1].Code != "" {
			t.Errorf("got %+v", p)
		}
		// go vet prints no level at all; unlabelled is a warning, not an error.
		vet := ParseLineDiagnostics("cmd/main.go:7:2: unreachable code", repo, "go vet")
		if vet[0].File != "cmd/main.go" || vet[0].Line != 7 || vet[0].Severity != "warning" || vet[0].Source != "go vet" {
			t.Errorf("got %+v", vet[0])
		}
	})
	t.Run("parses ruff --output-format=json", func(t *testing.T) {
		p := ParseRuff(`[{"filename":"/repo/app.py","code":"F401","message":"`+"`os`"+` imported but unused","location":{"row":1,"column":8}}]`, repo)
		if p[0].File != "app.py" || p[0].Line != 1 || p[0].Col != 8 || p[0].Code != "F401" {
			t.Errorf("got %+v", p[0])
		}
	})
	t.Run("returns nothing rather than throwing on junk", func(t *testing.T) {
		if len(ParseEslint("command not found", repo))+len(ParseRuff("", repo))+len(ParseTsc("", repo))+len(ParseDartAnalyze("Analyzing...\n", repo)) != 0 {
			t.Error("expected empty")
		}
	})
}

func TestPathsFromASymlinkedRepoRoot(t *testing.T) {
	// relativises a physical path against the root the user opened. Node's
	// process.cwd() resolves symlinks, so a tool started in a symlinked
	// checkout reports paths under the physical directory.
	base := t.TempDir()
	real, link := filepath.Join(base, "real"), filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(real, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	out := ParseTsc(filepath.Join(real, "src", "a.ts")+"(3,1): error TS1005: ';' expected.", link)
	if out[0].File != "src/a.ts" {
		t.Errorf("file = %q", out[0].File)
	}
}

func p(file string, line float64, severity string) Problem {
	return Problem{File: file, Line: line, Col: 1, Severity: severity, Message: "m", Source: "s"}
}

func TestNormalisingTheMergedList(t *testing.T) {
	t.Run("drops duplicates two tools both reported", func(t *testing.T) {
		if n := len(NormaliseProblems([]Problem{p("a.ts", 1, "error"), p("a.ts", 1, "error")})); n != 1 {
			t.Errorf("len = %d", n)
		}
	})
	t.Run("sorts by file, then severity, then position", func(t *testing.T) {
		sorted := NormaliseProblems([]Problem{p("b.ts", 5, "warning"), p("a.ts", 9, "warning"), p("a.ts", 2, "error"), p("a.ts", 1, "info")})
		var got [][2]string
		for _, x := range sorted {
			got = append(got, [2]string{x.File, x.Severity})
		}
		want := [][2]string{{"a.ts", "error"}, {"a.ts", "warning"}, {"a.ts", "info"}, {"b.ts", "warning"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
}

func eslintP(file string) Problem {
	return Problem{File: file, Line: 1, Col: 1, Severity: "error", Message: "m", Source: "eslint"}
}

func TestIgnoredFiles(t *testing.T) {
	testutil.IsolateGitEnv(t)
	t.Run("drops what git ignores and keeps what it tracks", func(t *testing.T) {
		dir := t.TempDir()
		testutil.Git(t, dir, "init", "-q")
		testutil.WriteFile(t, dir, ".gitignore", ".next/\ndist/\n")
		kept := DropIgnored(context.Background(), dir, []Problem{
			eslintP("src/app.ts"),
			eslintP(".next/build/chunks/[root-of-the-server]__51225daf._.js"),
			eslintP("dist/bundle.js"),
			eslintP("node_modules/lib/index.js"),
		})
		if len(kept) != 1 || kept[0].File != "src/app.ts" {
			t.Errorf("kept %+v", kept)
		}
	})
	t.Run("keeps everything when git cannot answer", func(t *testing.T) {
		// Outside a repository there is no opinion to honour — showing less
		// than the analyzers found would be the worse failure.
		dir := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		kept := DropIgnored(context.Background(), dir, []Problem{eslintP("src/app.ts"), eslintP("dist/bundle.js")})
		if len(kept) != 2 {
			t.Errorf("kept %+v", kept)
		}
	})
}

func TestAnalyzerDetection(t *testing.T) {
	t.Run("finds projects that are not at the repository root", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"mobile/pubspec.yaml": "name: app\n", "apps/web/tsconfig.json": "{}"})
		plan := AnalyzerPlan(dir)
		var ids []string
		dartDir := ""
		for _, j := range plan {
			ids = append(ids, j.ID)
			if j.ID == "dart" {
				dartDir = j.Dir
			}
		}
		sort.Strings(ids)
		if !reflect.DeepEqual(ids, []string{"dart", "tsc"}) || dartDir != filepath.Join(dir, "mobile") {
			t.Errorf("plan = %+v", plan)
		}
	})
	t.Run("does not run the same analyzer again inside a project it already covers", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"tsconfig.json": "{}", "packages/ui/tsconfig.json": "{}"})
		if got := AnalyzerPlan(dir); !reflect.DeepEqual(got, []PlanEntry{{"tsc", dir}}) {
			t.Errorf("plan = %+v", got)
		}
	})
	t.Run("asks for exactly the tools the repo carries config for", func(t *testing.T) {
		dir := t.TempDir()
		if got := DetectAnalyzers(dir); len(got) != 0 {
			t.Errorf("detect = %v", got)
		}
		testutil.WriteFile(t, dir, "pubspec.yaml", "name: app\n")
		testutil.WriteFile(t, dir, "tsconfig.json", "{}")
		got := DetectAnalyzers(dir)
		sort.Strings(got)
		if !reflect.DeepEqual(got, []string{"dart", "tsc"}) {
			t.Errorf("detect = %v", got)
		}
	})
	t.Run("caps how many project roots a monorepo can spawn", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < 20; i++ {
			files["pkgs/p"+string(rune('a'+i))+"/go.mod"] = ""
		}
		if n := len(AnalyzerPlan(writeTree(t, files))); n != maxRoots {
			t.Errorf("plan has %d jobs, want %d", n, maxRoots)
		}
	})
}

// A full sweep with a real analyzer: `go vet` on a tiny module with one
// finding, plus a missing tool reported as missing.
func TestAnalyzeRunsTheToolsAndReportsMissing(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	testutil.IsolateGitEnv(t)
	dir := writeTree(t, map[string]string{
		"go.mod":       "module example.com/x\n\ngo 1.21\n",
		"main.go":      "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Printf(\"%d\\n\", \"str\") }\n",
		"ruff.toml":    "",
		".gitignore":   "",
		"sub/.keep.md": "",
	})
	testutil.Git(t, dir, "init", "-q")
	t.Setenv("PATH", filepath.Dir(goBinary(t))) // go only: ruff is missing
	r := NewRunner()
	res := r.Analyze(context.Background(), dir)
	if !reflect.DeepEqual(res.Ran, []string{"go vet"}) || !reflect.DeepEqual(res.Missing, []string{"ruff"}) {
		t.Errorf("ran %v missing %v", res.Ran, res.Missing)
	}
	if len(res.Problems) == 0 || res.Problems[0].File != "main.go" || res.Problems[0].Source != "go vet" {
		t.Errorf("problems = %+v", res.Problems)
	}
	if res.Truncated || res.Ms < 0 {
		t.Errorf("result = %+v", res)
	}
}

func goBinary(t *testing.T) string {
	p, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	return p
}

func TestCancelKillsARunningSweep(t *testing.T) {
	// A fake analyzer that never finishes: a local `tsc` shim that sleeps.
	dir := writeTree(t, map[string]string{"tsconfig.json": "{}", "node_modules/.bin/tsc": "#!/bin/sh\nsleep 60\n"})
	if err := os.Chmod(filepath.Join(dir, "node_modules", ".bin", "tsc"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner()
	done := make(chan AnalyzeResult)
	go func() { done <- r.Analyze(context.Background(), dir) }()
	time.Sleep(300 * time.Millisecond)
	r.Cancel(dir)
	select {
	case res := <-done:
		// Like TS, a cancelled tool still counts as ran with whatever it printed.
		if !reflect.DeepEqual(res.Ran, []string{"tsc"}) {
			t.Errorf("ran = %v", res.Ran)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the sweep")
	}
}
