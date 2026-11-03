package wiki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Replays testdata/vectors/{wikiPack,wikiSchemas}.json, written by
// test/port/repochat.test.ts from src/main/wikiPack.ts and wikiSchemas.ts.

type fnCase struct {
	Fn     string            `json:"fn"`
	Args   []json.RawMessage `json:"args"`
	Out    json.RawMessage   `json:"out"`
	Throws *string           `json:"throws"`
}

func loadCases(t *testing.T, name string) []fnCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var file struct {
		Cases []fnCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(file.Cases) == 0 {
		t.Fatalf("%s has no cases", name)
	}
	return file.Cases
}

func arg[T any](t *testing.T, c fnCase, i int) T {
	t.Helper()
	var v T
	if i < len(c.Args) {
		if err := json.Unmarshal(c.Args[i], &v); err != nil {
			t.Fatalf("%s arg %d: %v", c.Fn, i, err)
		}
	}
	return v
}

func anyArg(t *testing.T, c fnCase, i int) any {
	t.Helper()
	v, err := shared.DecodeJSON(c.Args[i])
	if err != nil {
		t.Fatalf("%s arg %d: %v", c.Fn, i, err)
	}
	return v
}

func canon(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func assertCase(t *testing.T, i int, c fnCase, got any) {
	t.Helper()
	gv, w := canon(t, got), canon(t, c.Out)
	if !reflect.DeepEqual(gv, w) {
		args, _ := json.Marshal(c.Args)
		s := string(args)
		if len(s) > 200 {
			s = s[:200] + "…"
		}
		gj, _ := json.Marshal(gv)
		wj, _ := json.Marshal(w)
		t.Errorf("%s#%d %s:\n got  %s\n want %s", c.Fn, i, s, gj, wj)
	}
}

func setOf(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

func TestWikiPackVectors(t *testing.T) {
	for i, c := range loadCases(t, "wikiPack.json") {
		var got any
		switch c.Fn {
		case "isReadableSource":
			got = IsReadableSource(arg[string](t, c, 0))
		case "rankPlanFiles":
			got = RankPlanFiles(arg[[]string](t, c, 0), arg[[]string](t, c, 1), arg[int](t, c, 2))
		case "buildPagePack":
			got = BuildPagePack(arg[[]PackedFile](t, c, 0), arg[PackOpts](t, c, 1))
		case "serializePack":
			got = SerializePack(BuildPagePack(arg[[]PackedFile](t, c, 0), arg[PackOpts](t, c, 1)))
		case "serializeRepoFacts":
			got = SerializeRepoFacts(arg[PlanFacts](t, c, 0))
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
		assertCase(t, i, c, got)
	}
}

func TestWikiSchemaVectors(t *testing.T) {
	for i, c := range loadCases(t, "wikiSchemas.json") {
		var got any
		switch c.Fn {
		case "schemas":
			got = map[string]any{
				"WIKI_PLAN_SCHEMA":    g.JSONStringify(PlanSchema),
				"WIKI_PAGE_SCHEMA":    g.JSONStringify(PageSchema),
				"TECH_STACK_SCHEMA":   g.JSONStringify(StackSchema),
				"WIKI_PROMPT_VERSION": PromptVersion,
				"MAX_PAGES":           MaxPages,
				"MAX_SECTIONS":        MaxSections,
				"MAX_CLAIMS":          MaxClaims,
				"MAX_STACK_GROUPS":    MaxStackGroups,
				"WIKI_EXPORT_DIR":     ExportDir,
			}
		case "validateWikiPlan":
			got = ValidatePlan(anyArg(t, c, 0), setOf(arg[[]string](t, c, 1)))
		case "sanitizeWikiPlan":
			got = SanitizePlan(anyArg(t, c, 0), setOf(arg[[]string](t, c, 1)))
		case "validateWikiPage":
			a := arg[struct {
				Paths []string `json:"paths"`
				Slugs []string `json:"slugs"`
			}](t, c, 1)
			got = ValidatePage(anyArg(t, c, 0), PageAllowed{Paths: setOf(a.Paths), Slugs: setOf(a.Slugs)})
		case "validateTechStack":
			got = ValidateTechStack(anyArg(t, c, 0), setOf(arg[[]string](t, c, 1)))
		case "renderWikiPage":
			got = RenderPage(arg[Page](t, c, 0))
		case "pageSources":
			got = PageSources(arg[Page](t, c, 0))
		case "wikiFreshness":
			cur := arg[StoredStamp](t, c, 1)
			got = Freshness(arg[*StoredStamp](t, c, 0), cur.HeadSha, cur.Model)
		case "cleanPageTitle":
			got = CleanPageTitle(arg[string](t, c, 0))
		case "orderPlan":
			got = OrderPlan(arg[[]PagePlan](t, c, 0))
		case "wikiExportFiles":
			got = ExportFiles(arg[RepoWiki](t, c, 0), arg[string](t, c, 1))
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
		assertCase(t, i, c, got)
	}
}

