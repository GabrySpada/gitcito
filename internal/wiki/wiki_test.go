package wiki

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Ports of the "repo wiki — …" cases in test/pureLogic.test.ts.

func jv(t *testing.T, s string) any {
	t.Helper()
	v, err := shared.DecodeJSON([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func firstContains(t *testing.T, errs []string, want string) {
	t.Helper()
	if len(errs) == 0 || !strings.Contains(errs[0], want) {
		t.Fatalf("first error %q does not contain %q", errs, want)
	}
}

func planPage(over string) string {
	base := map[string]string{"slug": `"data-layer"`, "title": `"Data layer"`, "archetype": `"module"`, "scopePaths": `["src/db.ts"]`}
	for _, kv := range strings.Split(over, ";") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			base[k] = v
		}
	}
	return `{"slug":` + base["slug"] + `,"title":` + base["title"] + `,"archetype":` + base["archetype"] + `,"scopePaths":` + base["scopePaths"] + `}`
}

func TestWikiPlan(t *testing.T) {
	known := setOf([]string{"README.md", "src/app.ts", "src/db.ts", "src/ui/panel.tsx"})
	overview := planPage(`slug="overview";title="Overview";archetype="overview";scopePaths=["README.md"]`)
	plan := func(pages ...string) any { return jv(t, `{"pages":[`+strings.Join(pages, ",")+`]}`) }

	t.Run("accepts a plan whose pages all cover real files", func(t *testing.T) {
		if e := ValidatePlan(plan(overview, planPage("")), known); len(e) != 0 {
			t.Fatal(e)
		}
	})
	t.Run("rejects a page scoped to a file that is not in the repo", func(t *testing.T) {
		firstContains(t, ValidatePlan(plan(overview, planPage(`scopePaths=["src/ghost.ts"]`)), known), "src/ghost.ts")
	})
	t.Run("removes unknown scope paths and drops pages with no real evidence left", func(t *testing.T) {
		got := SanitizePlan(plan(overview, planPage(`scopePaths=["src/db.ts","src/ghost.ts"]`), planPage(`slug="ghosts";scopePaths=["src/ghost.ts"]`)), known)
		want := plan(overview, planPage(""))
		if !reflect.DeepEqual(canon(t, got), canon(t, want)) {
			t.Fatal(got)
		}
		if e := ValidatePlan(got, known); len(e) != 0 {
			t.Fatal(e)
		}
	})
	t.Run("requires exactly one overview page", func(t *testing.T) {
		firstContains(t, ValidatePlan(plan(planPage("")), known), "overview")
		firstContains(t, ValidatePlan(plan(overview, planPage(`slug="o2";archetype="overview"`)), known), "found 2")
	})
	t.Run("rejects duplicate or non-kebab slugs", func(t *testing.T) {
		if e := strings.Join(ValidatePlan(plan(overview, planPage(""), planPage("")), known), " "); !strings.Contains(e, "used twice") {
			t.Fatal(e)
		}
		firstContains(t, ValidatePlan(plan(overview, planPage(`slug="Data Layer"`)), known), "kebab-case")
	})
	t.Run("requires non-overview pages to name at least one file", func(t *testing.T) {
		firstContains(t, ValidatePlan(plan(overview, planPage(`scopePaths=[]`)), known), "at least one file")
	})
	t.Run("caps the number of pages", func(t *testing.T) {
		pages := []string{overview}
		for i := 0; i < 20; i++ {
			pages = append(pages, planPage(`slug="page-`+strconv.Itoa(i)+`"`))
		}
		firstContains(t, ValidatePlan(plan(pages...), known), "at most 12")
	})
	t.Run("puts the overview first regardless of the order the model used", func(t *testing.T) {
		got := OrderPlan([]PagePlan{{Slug: "ui", Title: "UI", Archetype: "module"}, {Slug: "overview", Title: "Overview", Archetype: "overview"}, {Slug: "data", Title: "Data", Archetype: "module"}})
		if got[0].Slug != "overview" || got[1].Slug != "data" || got[2].Slug != "ui" {
			t.Fatal(got)
		}
	})
}

func TestWikiOnePage(t *testing.T) {
	allowed := PageAllowed{Paths: setOf([]string{"src/db.ts", "README.md"}), Slugs: setOf([]string{"overview", "ui"})}
	good := `"summary":"How data is read.","sections":[{"heading":"Queries","claims":[{"text":"Queries go through a pool.","sourcePaths":["src/db.ts"]}]}]`
	t.Run("accepts a page citing only the files it was given", func(t *testing.T) {
		if e := ValidatePage(jv(t, `{`+good+`,"related":["overview"]}`), allowed); len(e) != 0 {
			t.Fatal(e)
		}
	})
	t.Run("rejects a citation of a file that was not in the pack", func(t *testing.T) {
		firstContains(t, ValidatePage(jv(t, `{"summary":"x","sections":[{"heading":"x","claims":[{"text":"y","sourcePaths":["src/secret.ts"]}]}],"related":[]}`), allowed), "src/secret.ts")
	})
	t.Run("rejects an uncited claim rather than letting it through", func(t *testing.T) {
		firstContains(t, ValidatePage(jv(t, `{"summary":"x","sections":[{"heading":"x","claims":[{"text":"trust me","sourcePaths":[]}]}],"related":[]}`), allowed), "at least one file")
	})
	t.Run("rejects links to pages that do not exist", func(t *testing.T) {
		firstContains(t, ValidatePage(jv(t, `{`+good+`,"related":["ghost"]}`), allowed), "ghost")
	})
	t.Run("caps sections and claims", func(t *testing.T) {
		sections := strings.TrimSuffix(strings.Repeat(`{"heading":"h","claims":[]},`, 9), ",")
		firstContains(t, ValidatePage(jv(t, `{"summary":"x","sections":[`+sections+`],"related":[]}`), allowed), "at most 6")
	})
}

func TestWikiRenderingAndFreshness(t *testing.T) {
	page := Page{Slug: "data", Title: "Data layer", Archetype: "module", Summary: "How data is read.", Sections: []Section{{
		Heading: "Queries",
		Claims: []Claim{
			{Text: "Queries go through a pool.", SourcePaths: []string{"src/db.ts"}},
			{Text: "Search is separate.", SourcePaths: []string{"src/db.ts", "src/search.ts"}},
		},
	}}, Related: []string{}}
	t.Run("writes the markdown itself, citing the paths the model named", func(t *testing.T) {
		md := RenderPage(page)
		for _, want := range []string{"# Data layer", "## Queries", "- Queries go through a pool. — `src/db.ts`", "`src/db.ts`, `src/search.ts`"} {
			if !strings.Contains(md, want) {
				t.Fatalf("%q missing from %q", want, md)
			}
		}
	})
	t.Run("lists every cited file once, sorted", func(t *testing.T) {
		if got := PageSources(page); !reflect.DeepEqual(got, []string{"src/db.ts", "src/search.ts"}) {
			t.Fatal(got)
		}
	})
	t.Run("is current only at the commit it was generated from", func(t *testing.T) {
		stored := &StoredStamp{HeadSha: "abc", PromptVersion: PromptVersion, Model: "gpt-4o-mini"}
		if Freshness(stored, "abc", "gpt-4o-mini") != FreshCurrent || Freshness(stored, "def", "gpt-4o-mini") != FreshBehind {
			t.Fatal("freshness")
		}
	})
	t.Run("is outdated when the prompt version or the model changed", func(t *testing.T) {
		if Freshness(&StoredStamp{HeadSha: "abc", PromptVersion: "wiki.v0", Model: "m"}, "abc", "m") != FreshOutdated ||
			Freshness(&StoredStamp{HeadSha: "abc", PromptVersion: PromptVersion, Model: "old"}, "abc", "new") != FreshOutdated ||
			Freshness(nil, "abc", "m") != FreshOutdated {
			t.Fatal("outdated")
		}
	})
}

func TestWikiChoosingWhatToRead(t *testing.T) {
	t.Run("skips build output, binaries and lock files", func(t *testing.T) {
		for _, p := range []string{"node_modules/react/index.js", "dist/bundle.js", "src/assets/logo.png", "package-lock.json", "target/debug/app", "a/vendor/lib.rb", "app.min.js"} {
			if IsReadableSource(p) {
				t.Error(p)
			}
		}
	})
	t.Run("keeps ordinary source and docs", func(t *testing.T) {
		for _, p := range []string{"src/app.ts", "README.md", "lib/db/pool.rb", "Dockerfile"} {
			if !IsReadableSource(p) {
				t.Error(p)
			}
		}
	})
	t.Run("puts root docs and manifests first, then churn hotspots", func(t *testing.T) {
		got := RankPlanFiles([]string{"src/z.ts", "package.json", "src/hot.ts", "README.md", "src/a/b/c/deep.ts"}, []string{"src/hot.ts"}, 0)
		if !reflect.DeepEqual(got, []string{"README.md", "package.json", "src/hot.ts", "src/z.ts", "src/a/b/c/deep.ts"}) {
			t.Fatal(got)
		}
	})
	t.Run("drops unreadable files and honours the limit", func(t *testing.T) {
		if got := RankPlanFiles([]string{"README.md", "dist/x.js", "src/a.ts", "src/b.ts"}, nil, 2); !reflect.DeepEqual(got, []string{"README.md", "src/a.ts"}) {
			t.Fatal(got)
		}
	})
}

func TestWikiPackingAPage(t *testing.T) {
	file := func(path string, size int) PackedFile { return PackedFile{Path: path, Content: strings.Repeat("x", size)} }
	n := func(v int) *int { return &v }
	t.Run("clips a long file rather than dropping it", func(t *testing.T) {
		pack := BuildPagePack([]PackedFile{file("a.ts", 500)}, PackOpts{MaxFileBytes: n(100)})
		if !strings.Contains(pack.Files[0].Content, "…(truncated)") || len(pack.Files[0].Content) >= 200 {
			t.Fatal(pack)
		}
	})
	t.Run("drops whole files once the budget is spent, and counts them", func(t *testing.T) {
		pack := BuildPagePack([]PackedFile{file("a.ts", 80), file("b.ts", 80), file("c.ts", 80)}, PackOpts{MaxBytes: n(170)})
		if len(pack.Files) != 2 || pack.Files[1].Path != "b.ts" || pack.Dropped != 1 {
			t.Fatal(pack)
		}
	})
	t.Run("always keeps the first file even when it alone busts the budget", func(t *testing.T) {
		if pack := BuildPagePack([]PackedFile{file("big.ts", 500)}, PackOpts{MaxBytes: n(10), MaxFileBytes: n(500)}); len(pack.Files) != 1 {
			t.Fatal(pack)
		}
	})
	t.Run("skips empty files — an unreadable file is not evidence", func(t *testing.T) {
		pack := BuildPagePack([]PackedFile{{Path: "a.ts", Content: "   "}, file("b.ts", 10)}, PackOpts{})
		if len(pack.Files) != 1 || pack.Files[0].Path != "b.ts" || pack.Dropped != 1 {
			t.Fatal(pack)
		}
	})
	t.Run("labels each file so the model can cite it, and admits what was left out", func(t *testing.T) {
		text := SerializePack(BuildPagePack([]PackedFile{file("a.ts", 10), file("b.ts", 10)}, PackOpts{MaxBytes: n(15)}))
		if !strings.Contains(text, "--- FILE: a.ts ---") || !strings.Contains(text, "1 further file(s) were not included") {
			t.Fatal(text)
		}
	})
}

func TestWikiPageTitles(t *testing.T) {
	t.Run("drops the archetype word the icon already shows", func(t *testing.T) {
		if CleanPageTitle("Database Module") != "Database" || CleanPageTitle("Release process") != "Release process" || CleanPageTitle("Commit graph  ") != "Commit graph" {
			t.Fatal("titles")
		}
	})
	t.Run("keeps a title that is only the archetype word", func(t *testing.T) {
		if CleanPageTitle("Overview") != "Overview" || CleanPageTitle("Reference") != "Reference" {
			t.Fatal("titles")
		}
	})
}

func TestWikiExportingToTheRepo(t *testing.T) {
	wiki := RepoWiki{
		HeadSha:       "abc1234567",
		GeneratedAt:   time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).UnixMilli(),
		Model:         "gpt-4o-mini",
		PromptVersion: PromptVersion,
		Pages: []Page{
			{Slug: "overview", Title: "Overview", Archetype: "overview", Summary: "What this is.", Sections: []Section{}, Related: []string{"data"}, Markdown: "# Overview\n\nWhat this is.\n"},
			{Slug: "data", Title: "Data layer", Archetype: "module", Summary: "How data is read.", Sections: []Section{}, Related: []string{}, Markdown: "# Data layer\n\nHow data is read.\n"},
		},
	}
	files := ExportFiles(wiki, "gitcito")
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.Content
	}
	t.Run("writes an index plus one file per page, under docs/wiki", func(t *testing.T) {
		if len(files) != 3 || files[0].Path != "docs/wiki/README.md" || files[1].Path != "docs/wiki/overview.md" || files[2].Path != "docs/wiki/data.md" {
			t.Fatal(files)
		}
	})
	t.Run("links every page from the index, with its summary", func(t *testing.T) {
		if !strings.Contains(files[0].Content, "# gitcito wiki") || !strings.Contains(files[0].Content, "- [Data layer](data.md) — How data is read.") {
			t.Fatal(files[0].Content)
		}
	})
	t.Run("stamps each file with the commit it came from", func(t *testing.T) {
		for _, f := range files {
			if !strings.Contains(f.Content, "abc1234") || !strings.Contains(f.Content, "Generated by Gitcito") {
				t.Fatal(f)
			}
		}
	})
	t.Run("turns related pages into links, and back to the index", func(t *testing.T) {
		o := byPath["docs/wiki/overview.md"]
		if !strings.Contains(o, "## See also") || !strings.Contains(o, "- [Data layer](data.md)") || !strings.Contains(o, "[← Index](README.md)") {
			t.Fatal(o)
		}
	})
	t.Run("leaves out the See also section when a page links nowhere", func(t *testing.T) {
		if strings.Contains(byPath["docs/wiki/data.md"], "## See also") {
			t.Fatal("unexpected See also")
		}
	})
	t.Run("only ever writes paths inside the repo", func(t *testing.T) {
		for _, f := range files {
			if !shared.IsSafeRepoPath(f.Path) {
				t.Fatal(f.Path)
			}
		}
	})
}

func TestTechStackValidation(t *testing.T) {
	declared := setOf([]string{"react", "vitest", "electron"})
	good := `"summary":"A desktop app."`
	t.Run("accepts a grouping of declared dependencies", func(t *testing.T) {
		if e := ValidateTechStack(jv(t, `{`+good+`,"groups":[{"name":"UI","items":[{"dep":"react","role":"renders the interface"}]}]}`), declared); len(e) != 0 {
			t.Fatal(e)
		}
	})
	t.Run("rejects a package the project does not declare", func(t *testing.T) {
		firstContains(t, ValidateTechStack(jv(t, `{`+good+`,"groups":[{"name":"UI","items":[{"dep":"vue","role":"x"}]}]}`), declared), "vue")
	})
	t.Run("rejects listing the same dependency twice", func(t *testing.T) {
		firstContains(t, ValidateTechStack(jv(t, `{`+good+`,"groups":[{"name":"UI","items":[{"dep":"react","role":"a"}]},{"name":"Other","items":[{"dep":"react","role":"b"}]}]}`), declared), "appears twice")
	})
	t.Run("requires a role for each entry and caps the groups", func(t *testing.T) {
		firstContains(t, ValidateTechStack(jv(t, `{`+good+`,"groups":[{"name":"UI","items":[{"dep":"react","role":""}]}]}`), declared), "role")
		groups := make([]string, 9)
		for i := range groups {
			groups[i] = `{"name":"g` + strconv.Itoa(i) + `","items":[]}`
		}
		firstContains(t, ValidateTechStack(jv(t, `{`+good+`,"groups":[`+strings.Join(groups, ",")+`]}`), declared), "at most 6")
	})
}
