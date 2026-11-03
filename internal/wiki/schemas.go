package wiki

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/GabrySpada/gitcito/internal/gitx"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// src/main/wikiSchemas.ts: contracts for the generated repo wiki.
//
// Two stages, both grounded the same way as the rest of the AI features: the
// planner may only name files that exist in the repo, and each page may only
// cite files that were put in front of it. Anything else is a validation error
// that the model is asked to fix, not a plausible-looking invention.

// PromptVersion invalidates every stored wiki when bumped.
const PromptVersion = "wiki.v1"

// Limits the validators enforce.
const (
	MaxPages       = 12
	MaxSections    = 6
	MaxClaims      = 8
	MaxStackGroups = 6
)

// ExportDir is where an exported wiki lands in the repo.
const ExportDir = "docs/wiki"

var reSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// archetypes keeps the TS Set's order: the error message lists them.
var archetypes = []string{"overview", "module", "workflow", "reference"}

func isArchetype(s string) bool {
	for _, a := range archetypes {
		if a == s {
			return true
		}
	}
	return false
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && shared.JSTrim(s) != ""
}

func strArr() g.Obj {
	return g.Obj{{K: "type", V: "array"}, {K: "items", V: g.Obj{{K: "type", V: "string"}}}}
}
func strType() g.Obj { return g.Obj{{K: "type", V: "string"}} }

// ─── Stage 1: the plan ──────────────────────────────────────────────────────

// PlanSchema is WIKI_PLAN_SCHEMA.
var PlanSchema = g.Obj{
	{K: "type", V: "object"},
	{K: "additionalProperties", V: false},
	{K: "required", V: []string{"pages"}},
	{K: "properties", V: g.Obj{
		{K: "pages", V: g.Obj{
			{K: "type", V: "array"},
			{K: "items", V: g.Obj{
				{K: "type", V: "object"},
				{K: "additionalProperties", V: false},
				{K: "required", V: []string{"slug", "title", "archetype", "scopePaths"}},
				{K: "properties", V: g.Obj{
					{K: "slug", V: strType()},
					{K: "title", V: strType()},
					{K: "archetype", V: g.Obj{{K: "type", V: "string"}, {K: "enum", V: archetypes}}},
					{K: "scopePaths", V: strArr()},
				}},
			}},
		}},
	}},
}

// ValidatePlan is validateWikiPlan: a plan is usable when every page is
// uniquely named, there is exactly one overview, and every path it claims to
// cover is really in the repo.
func ValidatePlan(value any, knownPaths map[string]bool) []string {
	root, ok := g.AsObject(value)
	if !ok {
		return []string{"The response must be a JSON object."}
	}
	pages, ok := root["pages"].([]any)
	if !ok {
		return []string{`"pages" must be an array of page plans.`}
	}
	if len(pages) == 0 {
		return []string{`"pages" is empty — plan at least an overview page.`}
	}

	errs := []string{}
	if len(pages) > MaxPages {
		errs = append(errs, `"pages" must have at most `+strconv.Itoa(MaxPages)+` entries, not `+strconv.Itoa(len(pages))+`.`)
	}

	seen := map[string]bool{}
	overviews := 0
	for i, raw := range pages {
		at := "pages[" + strconv.Itoa(i) + "]"
		page, ok := g.AsObject(raw)
		if !ok {
			errs = append(errs, at+" must be an object.")
			continue
		}
		slug, isStr := page["slug"].(string)
		switch {
		case !isStr || !reSlug.MatchString(slug):
			errs = append(errs, at+`.slug must be kebab-case, e.g. "data-layer".`)
		case seen[slug]:
			errs = append(errs, at+`.slug "`+slug+`" is used twice — slugs must be unique.`)
		default:
			seen[slug] = true
		}
		if !nonEmptyString(page["title"]) {
			errs = append(errs, at+".title must be the page title.")
		}
		arch, isStr := page["archetype"].(string)
		if !isStr || !isArchetype(arch) {
			errs = append(errs, at+".archetype must be one of: "+strings.Join(archetypes, ", ")+".")
		}
		if arch == "overview" {
			overviews++
		}

		scope, ok := page["scopePaths"].([]any)
		if !ok {
			errs = append(errs, at+".scopePaths must be an array of repo paths.")
			continue
		}
		for _, p := range scope {
			if s, ok := p.(string); !ok || !knownPaths[s] {
				errs = append(errs, at+".scopePaths contains "+g.JSONStringify(p)+", which is not a file in this repository. Use only paths from the file list.")
			}
		}
		if arch != "overview" && len(scope) == 0 {
			errs = append(errs, at+".scopePaths must name at least one file this page is about.")
		}
	}

	if overviews != 1 {
		errs = append(errs, `Exactly one page must have archetype "overview"; found `+strconv.Itoa(overviews)+".")
	}
	return errs
}

// SanitizePlan is sanitizeWikiPlan: drops planner paths that were not in its
// file list. A non-overview page with no evidence left is dropped as well;
// validation still enforces the remaining plan's slugs, shape and single
// overview.
func SanitizePlan(value any, knownPaths map[string]bool) any {
	root, ok := g.AsObject(value)
	if !ok {
		return value
	}
	pages, ok := root["pages"].([]any)
	if !ok {
		return value
	}
	kept := []any{}
	for _, raw := range pages {
		page, ok := g.AsObject(raw)
		scope, isArr := page["scopePaths"].([]any)
		if !ok || !isArr {
			kept = append(kept, raw)
			continue
		}
		paths := []any{}
		for _, p := range scope {
			if s, ok := p.(string); ok && knownPaths[s] {
				paths = append(paths, s)
			}
		}
		if page["archetype"] != "overview" && len(paths) == 0 {
			continue
		}
		next := make(map[string]any, len(page))
		for k, v := range page {
			next[k] = v
		}
		next["scopePaths"] = paths
		kept = append(kept, next)
	}
	out := make(map[string]any, len(root))
	for k, v := range root {
		out[k] = v
	}
	out["pages"] = kept
	return out
}

// ─── Stage 2: one page ──────────────────────────────────────────────────────

// PageSchema is WIKI_PAGE_SCHEMA.
var PageSchema = g.Obj{
	{K: "type", V: "object"},
	{K: "additionalProperties", V: false},
	{K: "required", V: []string{"summary", "sections", "related"}},
	{K: "properties", V: g.Obj{
		{K: "summary", V: strType()},
		{K: "sections", V: g.Obj{
			{K: "type", V: "array"},
			{K: "items", V: g.Obj{
				{K: "type", V: "object"},
				{K: "additionalProperties", V: false},
				{K: "required", V: []string{"heading", "claims"}},
				{K: "properties", V: g.Obj{
					{K: "heading", V: strType()},
					{K: "claims", V: g.Obj{
						{K: "type", V: "array"},
						{K: "items", V: g.Obj{
							{K: "type", V: "object"},
							{K: "additionalProperties", V: false},
							{K: "required", V: []string{"text", "sourcePaths"}},
							{K: "properties", V: g.Obj{
								{K: "text", V: strType()},
								{K: "sourcePaths", V: strArr()},
							}},
						}},
					}},
				}},
			}},
		}},
		{K: "related", V: strArr()},
	}},
}

// PageAllowed is what one page may cite: the files it was shown and the other
// pages' slugs.
type PageAllowed struct {
	Paths map[string]bool
	Slugs map[string]bool
}

// ValidatePage is validateWikiPage: a page may only cite the files it was
// shown and the slugs that exist.
func ValidatePage(value any, allowed PageAllowed) []string {
	root, ok := g.AsObject(value)
	if !ok {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	if !nonEmptyString(root["summary"]) {
		errs = append(errs, `"summary" must be one or two sentences about this page.`)
	}

	if sections, ok := root["sections"].([]any); !ok {
		errs = append(errs, `"sections" must be an array.`)
	} else {
		if len(sections) > MaxSections {
			errs = append(errs, `"sections" must have at most `+strconv.Itoa(MaxSections)+` entries, not `+strconv.Itoa(len(sections))+`.`)
		}
		for i, raw := range sections {
			at := "sections[" + strconv.Itoa(i) + "]"
			section, ok := g.AsObject(raw)
			if !ok {
				errs = append(errs, at+" must be an object.")
				continue
			}
			if !nonEmptyString(section["heading"]) {
				errs = append(errs, at+".heading must be a short heading.")
			}
			claims, ok := section["claims"].([]any)
			if !ok {
				errs = append(errs, at+".claims must be an array.")
				continue
			}
			if len(claims) > MaxClaims {
				errs = append(errs, at+".claims must have at most "+strconv.Itoa(MaxClaims)+" entries.")
			}
			for j, rawClaim := range claims {
				where := at + ".claims[" + strconv.Itoa(j) + "]"
				claim, ok := g.AsObject(rawClaim)
				if !ok {
					errs = append(errs, where+" must be an object.")
					continue
				}
				if !nonEmptyString(claim["text"]) {
					errs = append(errs, where+".text must be a sentence.")
				}
				sources, ok := claim["sourcePaths"].([]any)
				if !ok || len(sources) == 0 {
					errs = append(errs, where+".sourcePaths must name at least one file you were shown. Drop the claim if no file supports it.")
					continue
				}
				for _, p := range sources {
					if s, ok := p.(string); !ok || !allowed.Paths[s] {
						errs = append(errs, where+".sourcePaths contains "+g.JSONStringify(p)+", which is not one of the files you were given.")
					}
				}
			}
		}
	}

	if related, present := root["related"]; present {
		list, ok := related.([]any)
		if !ok {
			errs = append(errs, `"related" must be an array of page slugs.`)
		} else {
			for _, slug := range list {
				if s, ok := slug.(string); !ok || !allowed.Slugs[s] {
					errs = append(errs, `"related" contains `+g.JSONStringify(slug)+", which is not another page in the plan.")
				}
			}
		}
	}
	return errs
}

// ─── Tech stack ─────────────────────────────────────────────────────────────

// StackSchema is TECH_STACK_SCHEMA.
var StackSchema = g.Obj{
	{K: "type", V: "object"},
	{K: "additionalProperties", V: false},
	{K: "required", V: []string{"summary", "groups"}},
	{K: "properties", V: g.Obj{
		{K: "summary", V: strType()},
		{K: "groups", V: g.Obj{
			{K: "type", V: "array"},
			{K: "items", V: g.Obj{
				{K: "type", V: "object"},
				{K: "additionalProperties", V: false},
				{K: "required", V: []string{"name", "items"}},
				{K: "properties", V: g.Obj{
					{K: "name", V: strType()},
					{K: "items", V: g.Obj{
						{K: "type", V: "array"},
						{K: "items", V: g.Obj{
							{K: "type", V: "object"},
							{K: "additionalProperties", V: false},
							{K: "required", V: []string{"dep", "role"}},
							{K: "properties", V: g.Obj{{K: "dep", V: strType()}, {K: "role", V: strType()}}},
						}},
					}},
				}},
			}},
		}},
	}},
}

// ValidateTechStack is validateTechStack. The model groups and explains the
// stack, but the dependency names come from the manifests: it may only place
// packages that are really declared, and may not list the same one twice.
func ValidateTechStack(value any, declared map[string]bool) []string {
	root, ok := g.AsObject(value)
	if !ok {
		return []string{"The response must be a JSON object."}
	}
	errs := []string{}
	if !nonEmptyString(root["summary"]) {
		errs = append(errs, `"summary" must be a sentence or two about the stack.`)
	}
	groups, ok := root["groups"].([]any)
	if !ok {
		return append(errs, `"groups" must be an array.`)
	}
	if len(groups) > MaxStackGroups {
		errs = append(errs, `"groups" must have at most `+strconv.Itoa(MaxStackGroups)+` entries, not `+strconv.Itoa(len(groups))+`.`)
	}

	seen := map[string]bool{}
	for i, raw := range groups {
		at := "groups[" + strconv.Itoa(i) + "]"
		group, ok := g.AsObject(raw)
		if !ok {
			errs = append(errs, at+" must be an object.")
			continue
		}
		if !nonEmptyString(group["name"]) {
			errs = append(errs, at+`.name must be a short category, e.g. "UI" or "Build".`)
		}
		items, ok := group["items"].([]any)
		if !ok {
			errs = append(errs, at+".items must be an array.")
			continue
		}
		for j, rawItem := range items {
			where := at + ".items[" + strconv.Itoa(j) + "]"
			item, ok := g.AsObject(rawItem)
			if !ok {
				errs = append(errs, where+" must be an object.")
				continue
			}
			dep, isStr := item["dep"].(string)
			switch {
			case !isStr || !declared[dep]:
				errs = append(errs, where+".dep "+g.JSONStringify(item["dep"])+" is not a dependency this project declares. Use only names from the list.")
			case seen[dep]:
				errs = append(errs, where+`.dep "`+dep+`" appears twice — list each dependency once.`)
			default:
				seen[dep] = true
			}
			if !nonEmptyString(item["role"]) {
				errs = append(errs, where+".role must say what it is used for, in a few words.")
			}
		}
	}
	return errs
}

// ─── Rendering ──────────────────────────────────────────────────────────────

// RenderPage is renderWikiPage: renders a page to Markdown. The model supplies
// prose and file names; every heading, bullet and citation is written here, so
// a page can never contain a link or path the app did not resolve itself.
func RenderPage(page Page) string {
	out := []string{"# " + page.Title, "", page.Summary, ""}
	for _, section := range page.Sections {
		out = append(out, "## "+section.Heading, "")
		for _, claim := range section.Claims {
			cites := make([]string, 0, len(claim.SourcePaths))
			for _, p := range claim.SourcePaths {
				cites = append(cites, "`"+p+"`")
			}
			line := "- " + claim.Text
			if len(cites) > 0 {
				line += " — " + strings.Join(cites, ", ")
			}
			out = append(out, line)
		}
		out = append(out, "")
	}
	return shared.JSTrim(strings.Join(out, "\n")) + "\n"
}

// ExportFile is one Markdown file an export writes.
type ExportFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ExportFiles is wikiExportFiles: the wiki as files to write into the repo —
// one Markdown page each plus an index. Pure, so what lands on disk is decided
// (and tested) here rather than inside the writing.
func ExportFiles(wiki RepoWiki, repoName string) []ExportFile {
	stamp := time.UnixMilli(wiki.GeneratedAt).UTC().Format("2006-01-02")
	from := gitx.SliceUTF16(wiki.HeadSha, 7)
	if from == "" {
		from = "the working tree"
	}
	note := "<!-- Generated by Gitcito from " + from + " on " + stamp + ". Regenerate rather than editing by hand. -->"

	index := []string{"# " + repoName + " wiki", "", note, ""}
	for _, page := range wiki.Pages {
		index = append(index, "- ["+page.Title+"]("+page.Slug+".md) — "+page.Summary)
	}
	index = append(index, "")

	files := []ExportFile{{Path: ExportDir + "/README.md", Content: strings.Join(index, "\n")}}
	for _, page := range wiki.Pages {
		var related []Page
		for _, slug := range page.Related {
			for _, p := range wiki.Pages {
				if p.Slug == slug {
					related = append(related, p)
					break
				}
			}
		}
		var tail []string
		if len(related) > 0 {
			tail = []string{"", "## See also", ""}
			for _, p := range related {
				tail = append(tail, "- ["+p.Title+"]("+p.Slug+".md)")
			}
			tail = append(tail, "")
		}
		lines := append([]string{note, "", gitx.TrimEndJS(page.Markdown)}, tail...)
		lines = append(lines, "", "[← Index](README.md)", "")
		files = append(files, ExportFile{Path: ExportDir + "/" + page.Slug + ".md", Content: strings.Join(lines, "\n")})
	}
	return files
}

// utf16Less is JS's default sort order: by UTF-16 code units.
func utf16Less(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// PageSources lists every file a page cites, deduplicated and sorted, for the
// "sources" list in the UI.
func PageSources(page Page) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, section := range page.Sections {
		for _, claim := range section.Claims {
			for _, p := range claim.SourcePaths {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return utf16Less(out[i], out[j]) })
	return out
}

// ─── Freshness ──────────────────────────────────────────────────────────────

// Freshness values: 'current' | 'behind' | 'outdated'.
const (
	FreshCurrent  = "current"
	FreshBehind   = "behind"
	FreshOutdated = "outdated"
)

// StoredStamp is what freshness reads from a stored wiki.
type StoredStamp struct {
	HeadSha       string `json:"headSha"`
	PromptVersion string `json:"promptVersion"`
	Model         string `json:"model"`
}

// Freshness is wikiFreshness: whether a stored wiki still describes the repo —
// 'behind' when the repo moved on since it was written, 'outdated' when this
// build would generate it differently (prompt or model changed).
func Freshness(wiki *StoredStamp, headSha, model string) string {
	if wiki == nil {
		return FreshOutdated
	}
	if wiki.PromptVersion != PromptVersion || wiki.Model != model {
		return FreshOutdated
	}
	if wiki.HeadSha == headSha {
		return FreshCurrent
	}
	return FreshBehind
}

var reRedundantTail = regexp.MustCompile(`(?i)` + shared.JSSpace + `+(module|page|overview|reference|workflow|section)$`)

// CleanPageTitle drops the archetype from a title. The kind of page is
// already shown by its icon, so "Database module" reads as "Database" — unless
// that is all the title was, as with a page genuinely called "Overview".
func CleanPageTitle(title string) string {
	trimmed := shared.JSTrim(title)
	stripped := shared.JSTrim(reRedundantTail.ReplaceAllString(trimmed, ""))
	if gitx.UTF16Len(stripped) >= 2 {
		return stripped
	}
	return trimmed
}

// OrderPlan is orderPlan: plans arrive ordered by the model; the overview
// always reads first.
func OrderPlan(pages []PagePlan) []PagePlan {
	out := append([]PagePlan{}, pages...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Archetype == "overview" {
			return true
		}
		if out[j].Archetype == "overview" {
			return false
		}
		return shared.LocaleCompare(out[i].Title, out[j].Title) < 0
	})
	return out
}
