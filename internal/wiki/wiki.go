// Package wiki is src/main/wiki.ts, wikiPack.ts and wikiSchemas.ts: a
// generated wiki for a repository. The model plans the pages, writes each one
// from files the app hands it, and may only cite those files. Everything is
// built in memory and stored in one go, so a half-generated wiki never
// replaces a good one.
package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitsvc"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// FileName is the wiki store, in the data directory.
const FileName = "gitcito-wiki.json"

// EventProgress is pushed while a wiki is being generated.
const EventProgress = "wiki:progress"

// Emitter delivers events to the renderer.
type Emitter interface {
	Emit(name string, data any)
}

// Git is the slice of *gitsvc.GitService the wiki reads through.
type Git interface {
	Log(ctx context.Context, repoPath string, maxCount *int, skip int) ([]gitsvc.GraphCommit, error)
	ListTrackedFiles(ctx context.Context, repoPath string) ([]string, error)
	Open(ctx context.Context, repoPath string) (gitsvc.RepoSummary, error)
	RepoInsights(ctx context.Context, repoPath string, sinceDays int) (gitsvc.RepoInsights, error)
	FileContent(ctx context.Context, repoPath, file, ref string, force bool) (string, error)
	FileSizes(ctx context.Context, repoPath string, files []string) (map[string]gitsvc.FileSizeInfo, error)
}

// RepoFacts is RepoFacts: counted facts about a repo — no model involved.
type RepoFacts struct {
	Languages    []shared.LanguageStat  `json:"languages"`
	TotalBytes   int64                  `json:"totalBytes"`
	Manifests    []string               `json:"manifests"`
	Dependencies []shared.DependencyRef `json:"dependencies"`
}

// Wiki holds the generator's dependencies and the store.
type Wiki struct {
	git     Git
	model   g.Completer
	emitter Emitter
	store   string
	now     func() time.Time
	// mu serializes load-modify-save of the store file: two generations (or a
	// generate and a clear) must not interleave their writes.
	mu sync.Mutex
}

// New builds the generator. storePath is the gitcito-wiki.json file; now
// stamps generatedAt (time.Now when nil).
func New(git Git, model g.Completer, emitter Emitter, storePath string, now func() time.Time) *Wiki {
	if now == nil {
		now = time.Now
	}
	return &Wiki{git: git, model: model, emitter: emitter, store: storePath, now: now}
}

type wikiData struct {
	Repos map[string]RepoWiki `json:"repos"`
}

func (w *Wiki) load() wikiData {
	data := wikiData{Repos: map[string]RepoWiki{}}
	raw, err := os.ReadFile(w.store)
	if err != nil {
		return data // missing → no wiki yet
	}
	var parsed wikiData
	if json.Unmarshal(raw, &parsed) != nil || parsed.Repos == nil {
		return data // corrupt → no wiki yet
	}
	return parsed
}

func (w *Wiki) save(data wikiData) error {
	if err := os.MkdirAll(filepath.Dir(w.store), 0o700); err != nil {
		return err
	}
	raw, err := shared.MarshalIndent(data)
	if err != nil {
		return err
	}
	return os.WriteFile(w.store, raw, 0o600)
}

// workers is how many pages are written at once. Enough to be quick, few
// enough to be polite.
const workers = 3

const (
	// planFileLimit is how many file paths the planner is shown.
	planFileLimit = 400
	// pageFileLimit is how many files are packed into one page's prompt.
	pageFileLimit = 12
)

func nativeWikiSchema(cfg shared.Object) bool {
	provider, ok := cfg["provider"]
	if !ok || provider == nil {
		provider = "custom"
	}
	return provider != "custom" && provider != "ollama"
}

func (w *Wiki) headSha(ctx context.Context, repoPath string) string {
	one := 1
	log, err := w.git.Log(ctx, repoPath, &one, 0)
	if err != nil || len(log) == 0 {
		return ""
	}
	return log[0].Hash
}

// candidateFiles is every readable, non-secret file the repo tracks.
func (w *Wiki) candidateFiles(ctx context.Context, repoPath string) []string {
	tracked, err := w.git.ListTrackedFiles(ctx, repoPath)
	out := []string{}
	if err != nil {
		return out
	}
	// A wiki is a summary of the code, never of the credentials sitting next to it.
	for _, p := range tracked {
		if !shared.IsSecretFile(p) {
			out = append(out, p)
		}
	}
	return out
}

func (w *Wiki) fileContent(ctx context.Context, repoPath, path string) string {
	s, err := w.git.FileContent(ctx, repoPath, path, "", false)
	if err != nil {
		return ""
	}
	return s
}

// decodeInto round-trips a validated model value into a typed struct.
func decodeInto(value, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (w *Wiki) planWiki(ctx context.Context, repoPath, repoName string, files []string, cfg shared.Object) ([]PagePlan, error) {
	branch := ""
	if r, err := w.git.Open(ctx, repoPath); err == nil {
		branch = r.Current
	}
	facts := PlanFacts{Name: repoName, Branch: branch, Authors: []FactsAuthor{}, Hotspots: []FactsHotspot{}}
	var hot []string
	if insights, err := w.git.RepoInsights(ctx, repoPath, 0); err == nil {
		facts.TotalCommits = insights.TotalCommits
		for _, a := range insights.Authors {
			facts.Authors = append(facts.Authors, FactsAuthor{Name: a.Name, Commits: a.Commits})
		}
		for _, h := range insights.Hotspots {
			facts.Hotspots = append(facts.Hotspots, FactsHotspot{Path: h.Path, Commits: h.Commits})
			hot = append(hot, h.Path)
		}
	}
	ranked := RankPlanFiles(files, hot, planFileLimit)

	system := `You are a staff engineer planning a short wiki that explains a codebase to a new contributor.

You are given the repository's file list and some facts about it. Plan the pages.

Rules:
- Between 3 and ` + strconv.Itoa(MaxPages) + ` pages. Fewer, better pages beat many thin ones.
- Exactly one page must have archetype "overview": what the project is and how it fits together.
- Every other page must list, in "scopePaths", the files it is about — copied EXACTLY from the file list. Never invent a path.
- Group by what the code does (a subsystem, a workflow), not by folder depth.
- "slug" is kebab-case and unique.
- "title" names the thing itself, in sentence case: "Data layer", "Release process", "Commit graph". Never repeat the archetype in it — no "… Module", "… Page", "… Overview", "… Reference".

Reply ONLY with valid JSON (no markdown fences):
{"pages":[{"slug":"overview","title":"Overview","archetype":"overview","scopePaths":["README.md"]}]}
- "archetype" is one of: overview, module, workflow, reference.`

	known := map[string]bool{}
	for _, p := range ranked {
		known[p] = true
	}
	value, err := w.model.ChatCompleteJSON(ctx, cfg, []g.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: SerializeRepoFacts(facts) + "\n\nFiles:\n" + strings.Join(ranked, "\n")},
	}, "wikiPlan", g.JSONSpec{
		Name:      "wiki_plan",
		Schema:    g.Raw(PlanSchema),
		Normalize: func(v any) any { return SanitizePlan(v, known) },
		Validate: func(_ context.Context, v any) ([]string, error) {
			return ValidatePlan(v, known), nil
		},
		MaxTokens:              g.Int(2048),
		NativeStructuredOutput: g.Bool(nativeWikiSchema(cfg)),
	}, 0.2)
	if err != nil {
		return nil, err
	}
	var plan struct {
		Pages []PagePlan `json:"pages"`
	}
	if err := decodeInto(value, &plan); err != nil {
		return nil, err
	}
	for i := range plan.Pages {
		plan.Pages[i].Title = CleanPageTitle(plan.Pages[i].Title)
		if plan.Pages[i].ScopePaths == nil {
			plan.Pages[i].ScopePaths = []string{}
		}
	}
	return OrderPlan(plan.Pages), nil
}

func (w *Wiki) writePage(ctx context.Context, repoPath string, plan PagePlan, slugs []string, cfg shared.Object) (Page, error) {
	paths := plan.ScopePaths
	if len(paths) > pageFileLimit {
		paths = paths[:pageFileLimit]
	}
	contents := make([]PackedFile, 0, len(paths))
	for _, p := range paths {
		contents = append(contents, PackedFile{Path: p, Content: w.fileContent(ctx, repoPath, p)})
	}
	pack := BuildPagePack(contents, PackOpts{})
	allowed := PageAllowed{Paths: map[string]bool{}, Slugs: map[string]bool{}}
	for _, f := range pack.Files {
		allowed.Paths[f.Path] = true
	}
	var others []string
	for _, s := range slugs {
		if s != plan.Slug {
			allowed.Slugs[s] = true
			others = append(others, s)
		}
	}
	related := strings.Join(others, ", ")
	if related == "" {
		related = "(none)"
	}

	system := `You are writing one page of a wiki that explains a codebase, for a new contributor.

You are given the full text of the files this page covers. Rules:
- Every claim must be supported by one of those files, named in "sourcePaths" exactly as given. If no file supports a claim, leave the claim out.
- Never invent file paths, function names or behaviour you cannot see.
- Never write Markdown, links or line numbers — the app renders those from what you return.
- At most 6 sections, at most 8 claims each. Prefer explaining how things fit together over listing what exists.
- "related" may only contain slugs of other pages in this wiki: ` + related + `.

Reply ONLY with valid JSON (no markdown fences):
{"summary":"one or two sentences","sections":[{"heading":"...","claims":[{"text":"...","sourcePaths":["src/x.ts"]}]}],"related":[]}`

	user := "Page: " + plan.Title + " (" + plan.Archetype + ")\n\nFiles:\n\n" + SerializePack(pack)

	value, err := w.model.ChatCompleteJSON(ctx, cfg, []g.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, "wikiPage", g.JSONSpec{
		Name:   "wiki_page",
		Schema: g.Raw(PageSchema),
		Validate: func(_ context.Context, v any) ([]string, error) {
			return ValidatePage(v, allowed), nil
		},
		MaxTokens:              g.Int(4096),
		NativeStructuredOutput: g.Bool(nativeWikiSchema(cfg)),
	}, 0.3)
	if err != nil {
		return Page{}, err
	}
	var content struct {
		Summary  string    `json:"summary"`
		Sections []Section `json:"sections"`
		Related  []string  `json:"related"`
	}
	if err := decodeInto(value, &content); err != nil {
		return Page{}, err
	}
	page := Page{
		Slug:      plan.Slug,
		Title:     plan.Title,
		Archetype: plan.Archetype,
		Summary:   shared.JSTrim(content.Summary),
		Sections:  content.Sections,
		Related:   content.Related,
	}.normalized()
	page.Markdown = RenderPage(page)
	return page, nil
}

// Facts is repoFacts: what the repo is made of, counted rather than guessed —
// language shares by bytes and the dependencies its manifests declare.
func (w *Wiki) Facts(ctx context.Context, repoPath string, files []string) RepoFacts {
	sizes, err := w.git.FileSizes(ctx, repoPath, files)
	if err != nil {
		sizes = map[string]gitsvc.FileSizeInfo{}
	}
	sized := []shared.SizedFile{}
	var total int64
	for _, p := range files {
		info := sizes[p]
		if info.Binary {
			continue
		}
		sized = append(sized, shared.SizedFile{Path: p, Size: info.Size})
		total += info.Size
	}
	manifests := shared.FindManifests(files, 6)
	deps := []shared.DependencyRef{}
	seen := map[string]bool{}
	// Same package declared by two manifests counts once.
	for _, m := range manifests {
		for _, dep := range shared.ParseManifest(m, w.fileContent(ctx, repoPath, m)) {
			if !seen[dep.Name] {
				seen[dep.Name] = true
				deps = append(deps, dep)
			}
		}
	}
	return RepoFacts{
		Languages:    shared.LanguageBreakdown(sized, false),
		TotalBytes:   total,
		Manifests:    manifests,
		Dependencies: deps,
	}
}

// describeStack groups the declared dependencies into a readable stack. The
// names come from the manifests — the model only decides where each belongs
// and what it is for, and is rejected if it names something the project does
// not declare.
func (w *Wiki) describeStack(ctx context.Context, facts RepoFacts, cfg shared.Object) (*TechStack, error) {
	// Scaffolding out, sub-packages folded in: the model should be reasoning
	// about what this project is built on, not about ts-loader.
	notable := shared.MeaningfulDependencies(facts.Dependencies)
	if len(notable) == 0 {
		return nil, nil
	}
	declared := map[string]bool{}
	lines := make([]string, 0, len(notable))
	for _, d := range notable {
		declared[d.Name] = true
		line := d.Name
		if d.Version != "" {
			line += " " + d.Version
		}
		if d.Dev {
			line += " (dev)"
		}
		lines = append(lines, line)
	}
	var langs []string
	for i, l := range facts.Languages {
		if i == 5 {
			break
		}
		// Math.round: halves round up.
		langs = append(langs, l.Language+" "+strconv.Itoa(int(math.Floor(l.Share*100+0.5)))+"%")
	}
	var labels []string
	for _, f := range shared.DetectFrameworks(notable) {
		labels = append(labels, f.Label)
	}
	languages := strings.Join(langs, ", ")
	if languages == "" {
		languages = "(unknown)"
	}
	detected := ""
	if len(labels) > 0 {
		detected = "Frameworks detected: " + strings.Join(labels, ", ") + "\n"
	}

	system := `You are explaining what a project is built on, to someone deciding whether they could work on it.

You get the languages it is written in and the dependencies it declares. Write the stack, not an inventory.

Rules:
- "summary": 2-3 sentences describing the ARCHITECTURE — what kind of application this is, what runs where, and which pieces carry the weight. Name frameworks, not packages. Say "a desktop app built with Electron, React in the renderer and a Go service for indexing", never "this project uses a mix of technologies".
- Group by architectural role: "Frontend", "Backend", "Desktop shell", "Data", "Testing", "Build". At most ` + strconv.Itoa(MaxStackGroups) + ` groups.
- Include only dependencies that tell someone something. Skip a package whose presence is implied by another (a React app obviously has react-dom), skip plugins and helpers. It is correct to leave most of the list out.
- "dep" must be copied EXACTLY from the list. Never invent one, never list one twice.
- "role" says what THIS project uses it for, in a few words. Not a description of the package in general — "renders the commit graph", not "a library for rendering".

Reply ONLY with valid JSON (no markdown fences):
{"summary":"…","groups":[{"name":"Frontend","items":[{"dep":"react","role":"renders the whole interface"}]}]}`

	user := "Languages: " + languages + "\n" + detected + "\nDeclared dependencies:\n" + strings.Join(lines, "\n")

	value, err := w.model.ChatCompleteJSON(ctx, cfg, []g.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, "wikiStack", g.JSONSpec{
		Name:   "tech_stack",
		Schema: g.Raw(StackSchema),
		Validate: func(_ context.Context, v any) ([]string, error) {
			return ValidateTechStack(v, declared), nil
		},
		MaxTokens:              g.Int(2048),
		NativeStructuredOutput: g.Bool(nativeWikiSchema(cfg)),
	}, 0.2)
	if err != nil {
		// The stack is a bonus on top of the pages; a bad reply just means no stack.
		if g.IsInvalidAIResponse(err) {
			return nil, nil
		}
		return nil, err
	}
	var stack TechStack
	if err := decodeInto(value, &stack); err != nil {
		return nil, err
	}
	if stack.Groups == nil {
		stack.Groups = []TechGroup{}
	}
	for i := range stack.Groups {
		if stack.Groups[i].Items == nil {
			stack.Groups[i].Items = []TechItem{}
		}
	}
	return &stack, nil
}

// importable are source files worth parsing for imports.
var importable = regexp.MustCompile(`(?i)\.(ts|tsx|js|jsx|mjs|cjs|mts|cts|vue|svelte|py|go|rs|dart|rb|php|c|h|cc|cpp|cxx|hpp|hh|m|mm)$`)

const (
	importFileLimit    = 1200
	importMaxFileBytes = 400_000
)

// ImportGraph is which folder imports which, read from the source. Big repos
// are capped at the largest slice we can read quickly; the graph says how much
// it left out.
func (w *Wiki) ImportGraph(ctx context.Context, repoPath string, depth int) shared.ImportGraph {
	var files []string
	for _, p := range w.candidateFiles(ctx, repoPath) {
		if importable.MatchString(p) {
			files = append(files, p)
		}
	}
	sizes, err := w.git.FileSizes(ctx, repoPath, files)
	if err != nil {
		sizes = map[string]gitsvc.FileSizeInfo{}
	}
	contents := []shared.SourceFile{}
	for _, p := range files {
		if len(contents) == importFileLimit {
			break
		}
		if info := sizes[p]; !info.Binary && info.Size <= importMaxFileBytes {
			contents = append(contents, shared.SourceFile{Path: p, Content: w.fileContent(ctx, repoPath, p)})
		}
	}
	return shared.BuildImportGraph(contents, shared.ImportGraphOpts{Depth: depth})
}

func (w *Wiki) send(repoPath string, p Progress) {
	if w.emitter != nil {
		w.emitter.Emit(EventProgress, ProgressEvent{RepoPath: repoPath, Progress: p})
	}
}

func repoNameOf(repoPath string) string {
	if name := repoPath[strings.LastIndex(repoPath, "/")+1:]; name != "" {
		return name
	}
	return repoPath
}

// Generate is wiki:generate: plan, write every page (three at a time), place
// the stack, and only then replace the stored wiki.
func (w *Wiki) Generate(ctx context.Context, repoPath string, cfg shared.Object) (*RepoWiki, error) {
	wiki, err := w.generate(ctx, repoPath, cfg)
	if err != nil {
		w.send(repoPath, Progress{Phase: "error", Message: err.Error()})
		return nil, err
	}
	return wiki, nil
}

func (w *Wiki) generate(ctx context.Context, repoPath string, cfg shared.Object) (*RepoWiki, error) {
	w.send(repoPath, Progress{Phase: "planning"})
	files := w.candidateFiles(ctx, repoPath)
	if len(files) == 0 {
		return nil, errors.New("This repository has no tracked files to read.")
	}
	facts := w.Facts(ctx, repoPath, files)
	plan, err := w.planWiki(ctx, repoPath, repoNameOf(repoPath), files, cfg)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(plan))
	seen := map[string]bool{}
	for _, p := range plan {
		if !seen[p.Slug] {
			seen[p.Slug] = true
			slugs = append(slugs, p.Slug)
		}
	}

	pages, err := w.writePages(ctx, repoPath, plan, slugs, cfg)
	if err != nil {
		return nil, err
	}
	stack, err := w.describeStack(ctx, facts, cfg)
	if err != nil {
		return nil, err
	}

	model, _ := cfg["model"].(string)
	// Publish all at once: if any page failed, the previous wiki is still there.
	wiki := RepoWiki{
		HeadSha:       w.headSha(ctx, repoPath),
		GeneratedAt:   w.now().UnixMilli(),
		Model:         model,
		PromptVersion: PromptVersion,
		Pages:         pages,
		Stack:         stack,
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	data := w.load()
	data.Repos[repoPath] = wiki
	if err := w.save(data); err != nil {
		return nil, err
	}
	w.send(repoPath, Progress{Phase: "done"})
	return &wiki, nil
}

// writePages runs writePage over the plan, `workers` at a time, keeping plan
// order in the result. The first failure cancels the pages not yet started.
func (w *Wiki) writePages(ctx context.Context, repoPath string, plan []PagePlan, slugs []string, cfg shared.Object) ([]Page, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pages := make([]Page, len(plan))
	var (
		mu       sync.Mutex
		next     int
		done     int
		firstErr error
		wg       sync.WaitGroup
	)
	total := len(plan)
	for range min(workers, len(plan)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if next >= len(plan) || firstErr != nil {
					mu.Unlock()
					return
				}
				index := next
				next++
				mu.Unlock()

				page, err := w.writePage(ctx, repoPath, plan[index], slugs, cfg)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
				pages[index] = page
				done++
				// Emitted under the lock so the running count arrives in order.
				d, tot := done, total
				w.send(repoPath, Progress{Phase: "page", Slug: plan[index].Slug, Title: plan[index].Title, Done: &d, Total: &tot})
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return pages, nil
}

// Get is wiki:get: the stored wiki, whether it still describes the repo, and
// HEAD now.
func (w *Wiki) Get(ctx context.Context, repoPath, model string) StoredWiki {
	w.mu.Lock()
	data := w.load()
	w.mu.Unlock()
	head := w.headSha(ctx, repoPath)
	out := StoredWiki{HeadSha: head}
	var stamp *StoredStamp
	if wiki, ok := data.Repos[repoPath]; ok {
		out.Wiki = &wiki
		stamp = &StoredStamp{HeadSha: wiki.HeadSha, PromptVersion: wiki.PromptVersion, Model: wiki.Model}
	}
	out.Freshness = Freshness(stamp, head, model)
	return out
}

// Export is wiki:export: writes the stored wiki into the repo as Markdown, so
// it can be committed and read on the host. Returns the paths written,
// relative to the repo.
func (w *Wiki) Export(repoPath string) ([]string, error) {
	w.mu.Lock()
	data := w.load()
	w.mu.Unlock()
	wiki, ok := data.Repos[repoPath]
	if !ok {
		return nil, errors.New("There is no wiki for this repository yet.")
	}
	files := ExportFiles(wiki, repoNameOf(repoPath))
	out := make([]string, 0, len(files))
	for _, f := range files {
		// Slugs are validated kebab-case, but this writes into the user's repo
		// — check anyway rather than trust the shape of stored data.
		if !shared.IsSafeRepoPath(f.Path) {
			return nil, errors.New("Refusing to write " + f.Path)
		}
		full := filepath.Join(repoPath, f.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, []byte(f.Content), 0o644); err != nil {
			return nil, err
		}
		out = append(out, f.Path)
	}
	return out, nil
}

// Clear is wiki:clear: forgets the stored wiki of one repository.
func (w *Wiki) Clear(repoPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	data := w.load()
	delete(data.Repos, repoPath)
	return w.save(data)
}
