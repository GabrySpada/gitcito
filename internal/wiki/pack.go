package wiki

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// src/main/wikiPack.ts: choosing what the wiki generator is allowed to read.
//
// There is no symbol index here: the context is picked deterministically from
// the repo's own file list and its churn hotspots, then packed under a byte
// budget. Pure functions, so the choice is testable without a repo. Repository
// chat uses IsReadableSource and RankPlanFiles too.

var reDocFirst = regexp.MustCompile(`(?i)^(readme|contributing|architecture|design|changelog)`)

// Manifests worth reading first — they name the project and its dependencies.
var planManifests = map[string]bool{
	"package.json": true, "pyproject.toml": true, "cargo.toml": true, "go.mod": true, "pubspec.yaml": true,
	"composer.json": true, "gemfile": true, "build.gradle": true, "pom.xml": true, "makefile": true, "dockerfile": true,
}

var skipDirs = []string{
	"node_modules/", "dist/", "build/", "out/", "target/", "vendor/", ".git/",
	"coverage/", ".next/", ".nuxt/", "__pycache__/", ".venv/", "venv/",
	"pods/", ".gradle/", ".idea/", ".dart_tool/",
}

var skipExt = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`png jpg jpeg gif webp ico icns bmp tiff svg
		pdf zip gz tar bz2 7z rar jar war
		mp3 mp4 mov avi webm wav ogg
		ttf otf woff woff2 eot
		exe dll so dylib bin class o a pyc wasm
		lock snap map`) {
		skipExt[e] = true
	}
}

var skipFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "cargo.lock": true,
	"poetry.lock": true, "gemfile.lock": true, "composer.lock": true, "pubspec.lock": true,
	".ds_store": true, "thumbs.db": true,
}

var reMinified = regexp.MustCompile(`\.min\.[a-z0-9]+$`)

func lastSegment(path string) string { return path[strings.LastIndex(path, "/")+1:] }

func extensionOf(path string) string {
	name := lastSegment(path)
	dot := strings.LastIndex(name, ".")
	if dot <= 0 {
		return ""
	}
	return strings.ToLower(name[dot+1:])
}

// IsReadableSource reports whether a file's text could plausibly describe how
// the project works.
func IsReadableSource(path string) bool {
	if path == "" || gitx.UTF16Len(path) > 400 {
		return false
	}
	lower := strings.ToLower(path)
	for _, dir := range skipDirs {
		if strings.HasPrefix(lower, dir) || strings.Contains(lower, "/"+dir) {
			return false
		}
	}
	name := lastSegment(lower)
	if skipFiles[name] {
		return false
	}
	// Minified bundles are source in name only.
	if reMinified.MatchString(name) {
		return false
	}
	return !skipExt[extensionOf(lower)]
}

// planRank is where a path sorts in the planner's list: lower is more important.
func planRank(path string, hotspots map[string]int) int {
	name := strings.ToLower(lastSegment(path))
	depth := strings.Count(path, "/") + 1
	isRoot := depth == 1
	if isRoot && reDocFirst.MatchString(name) {
		return 0
	}
	if isRoot && planManifests[name] {
		return 1
	}
	if reDocFirst.MatchString(name) {
		return 2
	}
	if _, hot := hotspots[path]; hot {
		return 3
	}
	return 4 + min(depth, 5)
}

// RankPlanFiles is the file list handed to the planner: documentation and
// manifests first, then the files the repo actually churns, then everything
// else shallowest-first. max <= 0 means the TS default of 400.
func RankPlanFiles(paths, hotspots []string, max int) []string {
	if max <= 0 {
		max = 400
	}
	rank := map[string]int{}
	for i, p := range hotspots {
		rank[p] = i
	}
	type ranked struct {
		path string
		rank int
		hot  int
	}
	list := []ranked{}
	for _, p := range paths {
		if !IsReadableSource(p) {
			continue
		}
		hot, ok := rank[p]
		if !ok {
			hot = math.MaxInt
		}
		list = append(list, ranked{p, planRank(p, rank), hot})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.hot != b.hot {
			return a.hot < b.hot
		}
		return shared.LocaleCompare(a.path, b.path) < 0
	})
	out := make([]string, 0, min(len(list), max))
	for i := 0; i < len(list) && i < max; i++ {
		out = append(out, list[i].path)
	}
	return out
}

// PackedFile is one file of a page's prompt.
type PackedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// PagePack is what BuildPagePack keeps, and how many files it left out.
type PagePack struct {
	Files   []PackedFile `json:"files"`
	Dropped int          `json:"dropped"`
}

// PackOpts are buildPagePack's budgets in UTF-16 units; nil means the TS
// default (32000 overall, 6000 per file).
type PackOpts struct {
	MaxBytes     *int `json:"maxBytes,omitempty"`
	MaxFileBytes *int `json:"maxFileBytes,omitempty"`
}

// BuildPagePack packs a page's files under a byte budget: each file is
// clipped, and files are dropped from the end once the budget is spent, so a
// page never half-reads a file it then cites.
func BuildPagePack(files []PackedFile, opts PackOpts) PagePack {
	maxBytes, maxFileBytes := 32000, 6000
	if opts.MaxBytes != nil {
		maxBytes = *opts.MaxBytes
	}
	if opts.MaxFileBytes != nil {
		maxFileBytes = *opts.MaxFileBytes
	}
	packed := []PackedFile{}
	used, dropped := 0, 0
	for _, f := range files {
		if shared.JSTrim(f.Content) == "" {
			dropped++
			continue
		}
		content := f.Content
		if gitx.UTF16Len(content) > maxFileBytes {
			content = g.SliceJS(content, maxFileBytes) + "\n…(truncated)"
		}
		n := gitx.UTF16Len(content)
		if used+n > maxBytes && len(packed) > 0 {
			dropped++
			continue
		}
		used += n
		packed = append(packed, PackedFile{Path: f.Path, Content: content})
	}
	return PagePack{Files: packed, Dropped: dropped}
}

// SerializePack is the pack as the model sees it: a delimited block per file,
// path-labelled.
func SerializePack(pack PagePack) string {
	blocks := make([]string, 0, len(pack.Files)+1)
	for _, f := range pack.Files {
		blocks = append(blocks, "--- FILE: "+f.Path+" ---\n"+f.Content)
	}
	if pack.Dropped > 0 {
		blocks = append(blocks, "("+strconv.Itoa(pack.Dropped)+" further file(s) were not included — do not cite them.)")
	}
	return strings.Join(blocks, "\n\n")
}

// FactsAuthor and FactsHotspot are the parts of repo insights the planner sees.
type FactsAuthor struct {
	Name    string `json:"name"`
	Commits int    `json:"commits"`
}

// FactsHotspot is one frequently changed file.
type FactsHotspot struct {
	Path    string `json:"path"`
	Commits int    `json:"commits"`
}

// PlanFacts is serializeRepoFacts' input.
type PlanFacts struct {
	Name         string         `json:"name"`
	Branch       string         `json:"branch"`
	TotalCommits int            `json:"totalCommits"`
	Authors      []FactsAuthor  `json:"authors"`
	Hotspots     []FactsHotspot `json:"hotspots"`
}

// SerializeRepoFacts is the compact repo facts for the planner: churn, size
// and who works on it.
func SerializeRepoFacts(facts PlanFacts) string {
	authors := []string{}
	for i, a := range facts.Authors {
		if i == 5 {
			break
		}
		authors = append(authors, a.Name+" ("+strconv.Itoa(a.Commits)+")")
	}
	hotspots := []string{}
	for i, h := range facts.Hotspots {
		if i == 15 {
			break
		}
		hotspots = append(hotspots, h.Path+" ("+strconv.Itoa(h.Commits)+" commits)")
	}
	authorText := strings.Join(authors, ", ")
	if authorText == "" {
		authorText = "(unknown)"
	}
	hotText := strings.Join(hotspots, "\n")
	if hotText == "" {
		hotText = "(none)"
	}
	return "Repository: " + facts.Name + "\nCurrent branch: " + facts.Branch + "\nCommits analysed: " +
		strconv.Itoa(facts.TotalCommits) + "\nMost active authors: " + authorText +
		"\n\nMost frequently changed files:\n" + hotText
}
