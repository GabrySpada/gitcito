package grounding

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Ports of the grounding cases in test/pureLogic.test.ts ("AI grounding — …"
// and "hover explain — context window / output validation / windows over a
// sparse diff"). The vector file covers the same functions exhaustively; these
// keep the behaviour each vitest case names readable on its own.

const testDiff = `diff --git a/src/app.ts b/src/app.ts
index 1111111..2222222 100644
--- a/src/app.ts
+++ b/src/app.ts
@@ -10,3 +10,4 @@ function main() {
   const a = 1
-  return a
+  const b = 2
+  return a + b
 }
diff --git a/README.md b/README.md
index 3333333..4444444 100644
--- a/README.md
+++ b/README.md
@@ -1,2 +1,2 @@
-# Old
+# New
 body
`

func jv(t *testing.T, s string) any {
	t.Helper()
	v, err := shared.DecodeJSON([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}


func TestDiffEvidence(t *testing.T) {
	t.Run("splits a diff into one evidence item per hunk with sequential ids", func(t *testing.T) {
		set := BuildDiffEvidence(testDiff, DiffEvidenceOpts{})
		if got := EvidenceIDs(set); !reflect.DeepEqual(got, []string{"E1", "E2"}) {
			t.Fatal(got)
		}
		if set.Items[0].Path != "src/app.ts" || set.Items[1].Path != "README.md" || set.Omitted != 0 {
			t.Fatal(set)
		}
	})
	t.Run("anchors each hunk to its new-file line range", func(t *testing.T) {
		items := BuildDiffEvidence(testDiff, DiffEvidenceOpts{}).Items
		if items[0].StartLine != 10 || items[0].EndLine < 12 || items[1].StartLine != 1 {
			t.Fatal(items)
		}
	})
	t.Run("keeps the hunk body so the model can read the change", func(t *testing.T) {
		first := BuildDiffEvidence(testDiff, DiffEvidenceOpts{}).Items[0]
		if !strings.Contains(first.Text, "const b = 2") || !strings.HasPrefix(first.Text, "@@") {
			t.Fatal(first.Text)
		}
	})
	t.Run("drops trailing hunks that do not fit the byte budget and reports them", func(t *testing.T) {
		set := BuildDiffEvidence(testDiff, DiffEvidenceOpts{MaxBytes: Int(60)})
		if len(set.Items) != 1 || set.Omitted != 1 || !strings.Contains(SerializeEvidence(set), "1 further hunk(s) omitted") {
			t.Fatal(set)
		}
	})
	t.Run("labels every hunk with its id and location in the prompt block", func(t *testing.T) {
		if s := SerializeEvidence(BuildDiffEvidence(testDiff, DiffEvidenceOpts{})); !strings.Contains(s, "[E1] src/app.ts:10-") {
			t.Fatal(s)
		}
	})
	t.Run("returns nothing for a diff with no hunks", func(t *testing.T) {
		if items := BuildDiffEvidence("diff --git a/x.png b/x.png\nBinary files differ\n", DiffEvidenceOpts{}).Items; len(items) != 0 {
			t.Fatal(items)
		}
	})
}

func finding(over string) string {
	base := `"kind":"risk","severity":"high","evidenceId":"E1","claim":"Null is dereferenced.","suggestion":"Guard it."`
	if over != "" {
		base += "," + over
	}
	return "{" + base + "}"
}

func TestReviewValidation(t *testing.T) {
	allowed := []string{"E1", "E2"}
	t.Run("accepts a well-formed grounded review", func(t *testing.T) {
		if errs := ValidateReview(jv(t, `{"summary":"Adds a helper.","findings":[`+finding("")+`]}`), allowed); len(errs) != 0 {
			t.Fatal(errs)
		}
	})
	t.Run("rejects a finding citing evidence that was never sent", func(t *testing.T) {
		errs := ValidateReview(jv(t, `{"summary":"x","findings":[`+finding(`"evidenceId":"E9"`)+`]}`), allowed)
		if len(errs) != 1 || !strings.Contains(errs[0], "E9") || !strings.Contains(errs[0], "E1, E2") {
			t.Fatal(errs)
		}
	})
	t.Run("rejects unknown kinds, severities and empty claims", func(t *testing.T) {
		errs := ValidateReview(jv(t, `{"summary":"x","findings":[`+finding(`"kind":"nit","severity":"critical","claim":"  "`)+`]}`), allowed)
		if len(errs) != 3 {
			t.Fatal(errs)
		}
	})
	t.Run("requires a summary and an actions-style array", func(t *testing.T) {
		if e := ValidateReview(jv(t, `{"findings":[]}`), allowed); !strings.Contains(e[0], "summary") {
			t.Fatal(e)
		}
		if e := ValidateReview(jv(t, `{"summary":"x"}`), allowed); !strings.Contains(e[0], "findings") {
			t.Fatal(e)
		}
	})
	t.Run("treats an empty findings list as valid", func(t *testing.T) {
		if e := ValidateReview(jv(t, `{"summary":"Looks fine.","findings":[]}`), allowed); len(e) != 0 {
			t.Fatal(e)
		}
	})
}

func TestResolvingFindings(t *testing.T) {
	index := EvidenceIndex(BuildDiffEvidence("diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -10,2 +10,3 @@\n   const a = 1\n+  const b = 2\n", DiffEvidenceOpts{}))
	ground := func(s string) []GroundedFinding { return GroundFindings(jv(t, s).([]any), index) }
	t.Run("replaces the evidence id with the path and line from the diff", func(t *testing.T) {
		f := ground(`[{"kind":"risk","severity":"high","evidenceId":"E1","claim":"c","suggestion":"s"}]`)[0]
		if f.Path != "src/app.ts" || f.Line != 10 {
			t.Fatal(f)
		}
	})
	t.Run("drops a finding whose evidence id cannot be resolved", func(t *testing.T) {
		if got := ground(`[{"kind":"risk","evidenceId":"E7","claim":"c"}]`); len(got) != 0 {
			t.Fatal(got)
		}
	})
	t.Run("falls back to medium severity when the model omits it", func(t *testing.T) {
		f := ground(`[{"kind":"risk","evidenceId":"E1","claim":"c"}]`)[0]
		if f.Severity != "medium" || f.Suggestion != "" {
			t.Fatal(f)
		}
	})
	t.Run("renders only the requested kind, with app-written locations", func(t *testing.T) {
		fs := ground(`[{"kind":"risk","severity":"high","evidenceId":"E1","claim":"Leaks a handle.","suggestion":"Close it."},
			{"kind":"suggestion","severity":"low","evidenceId":"E1","claim":"Naming.","suggestion":"Rename b."}]`)
		if got := RenderFindings(fs, "risk"); got != "- `src/app.ts:10` Leaks a handle. — Close it." {
			t.Fatal(got)
		}
		if got := RenderFindings(fs, "suggestion"); got != "- `src/app.ts:10` Rename b." {
			t.Fatal(got)
		}
	})
}

func TestAskPlanValidation(t *testing.T) {
	known := map[string]bool{"a.ts": true, "docs/b.md": true}
	cases := []struct {
		name, plan, want string // want "" = valid; otherwise a substring of the first error
	}{
		{"accepts actions that only touch listed files", `{"summary":"Stage a.ts","actions":[{"type":"stage","files":["a.ts"],"description":"Stage a.ts"}]}`, ""},
		{"rejects an invented file path", `{"summary":"x","actions":[{"type":"stage","files":["ghost.ts"],"description":"Stage ghost"}]}`, "ghost.ts"},
		{"rejects action types the app cannot execute", `{"summary":"x","actions":[{"type":"push","description":"Push it"}]}`, "push"},
		{"lets gitignore patterns through — they are globs, not existing paths", `{"summary":"x","actions":[{"type":"gitignore","patterns":["*.tsx"],"description":"Ignore tsx"}]}`, ""},
		{"requires a message on commit and a name on branch (commit)", `{"summary":"x","actions":[{"type":"commit","description":"Commit"}]}`, "message"},
		{"requires a message on commit and a name on branch (branch)", `{"summary":"x","actions":[{"type":"branch","description":"Branch"}]}`, "name"},
		{"accepts an empty plan", `{"summary":"","actions":[],"note":"Nothing to do."}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := ValidateAskPlan(jv(t, c.plan), known)
			if c.want == "" && len(errs) != 0 || c.want != "" && (len(errs) == 0 || !strings.Contains(errs[0], c.want)) {
				t.Fatal(errs)
			}
		})
	}
}

func TestLooseJSONParsing(t *testing.T) {
	section := func(heading, claims string) string { return `{"heading":"` + heading + `","claims":` + claims + `}` }
	page := func(sections, related string) string {
		return `{"summary":"x","sections":[` + sections + `],"related":` + related + `}`
	}
	cases := []struct{ name, in, want string }{
		{"parses a fenced JSON reply", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"returns null instead of throwing on prose", "Sure! Here is the answer.", "null"},
		{"repairs quoted objects emitted inside a DiffusionGemma array",
			`{"summary":"x","sections":[{"heading":"Core","claims":[{"text":"one","sourcePaths":["README.md"]},{"{"text":"two","sourcePaths":["README.md"]}"]}],"related":[]}`,
			page(section("Core", `[{"text":"one","sourcePaths":["README.md"]},{"text":"two","sourcePaths":["README.md"]}]`), "[]")},
		{"parses a JSON object escaped by one provider response layer", `{\"value\":\"x\"}`, `{"value":"x"}`},
		{"repairs quoted array objects after removing a provider escape layer",
			`{\"summary\":\"x\",\"sections\":[{\"heading\":\"Core\",\"claims\":[{\"text\":\"one\",\"sourcePaths\":[\"README.md\"]},{\"{\"text\":\"two\",\"sourcePaths\":[\"README.md\"]}\"]}],\"related\":[]}`,
			page(section("Core", `[{"text":"one","sourcePaths":["README.md"]},{"text":"two","sourcePaths":["README.md"]}]`), "[]")},
		{"restores a missing section brace in truncated model JSON",
			`{"summary":"x","sections":[{"heading":"CI","claims":[{"text":"one","sourcePaths":["src/main/localCi.ts"]}]],"related":[]}`,
			page(section("CI", `[{"text":"one","sourcePaths":["src/main/localCi.ts"]}]`), "[]")},
		{"repairs a section object closed as an array after quoted claims",
			`{"summary":"x","sections":[{"heading":"i18n","claims":[{"text":"one","sourcePaths":["a.ts"]},{"{"text":"two","sourcePaths":["b.ts"]}]]],"related":[]}`,
			page(section("i18n", `[{"text":"one","sourcePaths":["a.ts"]},{"text":"two","sourcePaths":["b.ts"]}]`), "[]")},
		{"removes a stray quote and colon before an object or array property value",
			`{"summary":"x","sections":[{"heading":"CLI","claims":":[{"text":"one","sourcePaths":["cli.ts"]}]}],"related":[]}`,
			page(section("CLI", `[{"text":"one","sourcePaths":["cli.ts"]}]`), "[]")},
		{"restores the sections array when a wiki page is flattened after summary",
			`{"summary":"x","heading":"First","claims":[{"text":"one","sourcePaths":["a.ts"]}]},{"heading":"Second","claims":[{"text":"two","sourcePaths":["b.ts"]}]}],"related":["overview"]}`,
			page(section("First", `[{"text":"one","sourcePaths":["a.ts"]}]`)+","+section("Second", `[{"text":"two","sourcePaths":["b.ts"]}]`), `["overview"]`)},
		{"drops an empty token inserted between object properties",
			`{"summary":"x","sections":[{"heading":"Security","claims":[{"text":"one","","sourcePaths":["keychain.ts"]}]}],"related":[]}`,
			page(section("Security", `[{"text":"one","sourcePaths":["keychain.ts"]}]`), "[]")},
		{"moves a trailing related object out of the wiki sections array",
			`{"summary":"x","sections":[{"heading":"Renderer","claims":[{"text":"one","sourcePaths":["App.tsx"]}]},{"related":["overview","cli"]}`,
			page(section("Renderer", `[{"text":"one","sourcePaths":["App.tsx"]}]`), `["overview","cli"]`)},
		{"uses the last complete JSON object when a model repeats itself and is truncated",
			"{\"value\":\"first\"}\nWait, here is the corrected object.\n{\"value\":\"second\"}\n{\"value\":\"truncated\"", `{"value":"second"}`},
		{"extracts a valid repeated object after repairing quoted array entries",
			"{\"items\":[{\"value\":\"one\"},{\"{\"value\":\"two\"}\"]}\nCorrected:\n{\"items\":[{\"value\":\"three\"}]}", `{"items":[{"value":"three"}]}`},
		{"removes a quote inserted before an indented object property", "{\"pages\":[{\"\n  \"slug\":\"overview\",\"title\":\"Overview\"}]}",
			`{"pages":[{"slug":"overview","title":"Overview"}]}`},
		{"does not alter valid strings that contain serialized JSON", `{"value":"{\"nested\":true}"}`, `{"value":"{\"nested\":true}"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseLooseJSON(c.in)
			if c.want == "null" {
				if ok {
					t.Fatalf("got %v, want null", got)
				}
				return
			}
			if !ok || JSONStringify(got) != JSONStringify(jv(t, c.want)) {
				t.Fatalf("got %s (ok=%v), want %s", JSONStringify(got), ok, c.want)
			}
		})
	}
}

func file200() string {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i+1) + " content"
	}
	return strings.Join(lines, "\n")
}

func TestHoverContextWindow(t *testing.T) {
	file := file200()
	t.Run("centres the window on the hovered line", func(t *testing.T) {
		w := BuildLineWindow(file, 100, WindowOpts{Radius: Int(5)})
		if w.StartLine != 95 || w.EndLine != 105 || strings.Count(w.Text, "\n")+1 != 11 {
			t.Fatal(w)
		}
	})
	t.Run("numbers every line so the model can cite one", func(t *testing.T) {
		if got := BuildLineWindow(file, 3, WindowOpts{Radius: Int(1)}).Text; got != "2 | line 2 content\n3 | line 3 content\n4 | line 4 content" {
			t.Fatal(got)
		}
	})
	t.Run("clamps at the start and end of the file", func(t *testing.T) {
		if BuildLineWindow(file, 1, WindowOpts{Radius: Int(10)}).StartLine != 1 ||
			BuildLineWindow(file, 200, WindowOpts{Radius: Int(10)}).EndLine != 200 ||
			BuildLineWindow(file, 999, WindowOpts{Radius: Int(2)}).EndLine != 200 {
			t.Fatal("not clamped")
		}
	})
	t.Run("shrinks to the byte budget but always keeps the hovered line", func(t *testing.T) {
		w := BuildLineWindow(file, 100, WindowOpts{Radius: Int(50), MaxBytes: Int(120)})
		if gitx.UTF16Len(w.Text) > 120 || w.StartLine > 100 || w.EndLine < 100 || !strings.Contains(w.Text, "100 | line 100 content") {
			t.Fatal(w)
		}
	})
	t.Run("handles a single-line file", func(t *testing.T) {
		want := LineWindow{StartLine: 1, EndLine: 1, Numbers: []int{1}, Text: "1 | only line"}
		if got := BuildLineWindow("only line", 1, WindowOpts{}); !reflect.DeepEqual(got, want) {
			t.Fatal(got)
		}
	})
}

func TestHoverOutputValidation(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i+1)
	}
	w := BuildLineWindow(strings.Join(lines, "\n"), 15, WindowOpts{Radius: Int(5)})
	t.Run("accepts a brief explanation citing lines from the window", func(t *testing.T) {
		if e := ValidateHoverExplain(jv(t, `{"summary":"A helper.","bullets":["Used twice."],"lines":[10,20]}`), w); len(e) != 0 {
			t.Fatal(e)
		}
	})
	t.Run("rejects a citation outside the window", func(t *testing.T) {
		e := ValidateHoverExplain(jv(t, `{"summary":"A helper.","lines":[42]}`), w)
		if len(e) != 1 || !strings.Contains(e[0], "between 10 and 20") {
			t.Fatal(e)
		}
	})
	t.Run("rejects an empty summary and an over-long bullet list", func(t *testing.T) {
		if e := ValidateHoverExplain(jv(t, `{"summary":"   "}`), w); !strings.Contains(e[0], "summary") {
			t.Fatal(e)
		}
		if e := ValidateHoverExplain(jv(t, `{"summary":"x","bullets":["a","b","c","d"]}`), w); !strings.Contains(e[0], "at most 3") {
			t.Fatal(e)
		}
	})
	t.Run("treats bullets and lines as optional", func(t *testing.T) {
		if e := ValidateHoverExplain(jv(t, `{"summary":"Defined elsewhere."}`), w); len(e) != 0 {
			t.Fatal(e)
		}
	})
}

func TestHoverSparseDiffWindows(t *testing.T) {
	hunk := []NumberedLine{{10, "function a() {"}, {11, "  return 1"}, {12, "}"}, {80, "function b() {"}, {81, "  return 2"}}
	t.Run("keeps the real line numbers, gaps and all", func(t *testing.T) {
		w := BuildWindowFromLines(hunk, 11, WindowOpts{Radius: Int(1)})
		if !reflect.DeepEqual(w.Numbers, []int{10, 11, 12}) || w.Text != "10 | function a() {\n11 |   return 1\n12 | }" {
			t.Fatal(w)
		}
	})
	t.Run("snaps to the nearest line it actually has", func(t *testing.T) {
		if n := BuildWindowFromLines(hunk, 30, WindowOpts{Radius: Int(0)}).Numbers; !reflect.DeepEqual(n, []int{12}) {
			t.Fatal(n)
		}
		if n := BuildWindowFromLines(hunk, 70, WindowOpts{Radius: Int(0)}).Numbers; !reflect.DeepEqual(n, []int{80}) {
			t.Fatal(n)
		}
	})
	t.Run("rejects a citation of a line inside a gap", func(t *testing.T) {
		w := BuildWindowFromLines(hunk, 11, WindowOpts{Radius: Int(1)})
		if e := ValidateHoverExplain(jv(t, `{"summary":"x","lines":[12]}`), w); len(e) != 0 {
			t.Fatal(e)
		}
		if e := ValidateHoverExplain(jv(t, `{"summary":"x","lines":[40]}`), w); !strings.Contains(e[0], "not a line you were shown") {
			t.Fatal(e)
		}
	})
	t.Run("returns an empty window when there are no lines at all", func(t *testing.T) {
		if w := BuildWindowFromLines(nil, 5, WindowOpts{}); w.Text != "" {
			t.Fatal(w)
		}
	})
}

