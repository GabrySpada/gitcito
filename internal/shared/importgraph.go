package shared

import (
	"regexp"
	"sort"
	"strings"
)

// Who imports whom, read straight from the source. Mirrors the parts of
// src/shared/importGraph.ts that the main process runs (wiki:imports); the
// layout helpers stay in the renderer.
//
// No symbol index and no model: imports are matched textually, resolved
// against the repo's own file list, and aggregated per folder. An import that
// cannot be resolved to a file in the repo is counted as external and dropped —
// the graph only ever draws edges between folders that really exist.

// ImportNode is one folder in the import graph.
type ImportNode struct {
	ID    string `json:"id"`
	Files int    `json:"files"`
	// Out counts imports this folder makes into other folders.
	Out int `json:"out"`
	// In counts imports other folders make into this one.
	In int `json:"in"`
}

// ImportEdge is ImportEdge in src/shared/types.ts.
type ImportEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

// ImportGraph is the folder-level import graph.
type ImportGraph struct {
	Nodes []ImportNode `json:"nodes"`
	Edges []ImportEdge `json:"edges"`
	Depth int          `json:"depth"`
	// Resolved counts imports that pointed at a file in this repo.
	Resolved int `json:"resolved"`
	// External counts imports that pointed outside it (packages, stdlib).
	External     int `json:"external"`
	OmittedEdges int `json:"omittedEdges"`
}

// SourceFile is a path with its text.
type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var jsExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte"}

var (
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment  = regexp.MustCompile(`(^|[^:])//[^\n]*`)
)

// stripComments strips line and block comments so a commented-out import
// isn't an edge.
func stripComments(text string) string {
	return reLineComment.ReplaceAllString(reBlockComment.ReplaceAllString(text, ""), "$1")
}

const jsS = JSSpace

// lineStart is `(?:^|\n)\s*` — JS's `^` without the m flag is the input start.
const lineStart = `(?:^|\n)` + jsS + `*`

var importPatterns = []struct {
	test    *regexp.Regexp
	extract []*regexp.Regexp
}{
	{
		// JavaScript / TypeScript and friends
		regexp.MustCompile(`(?i)\.(ts|tsx|js|jsx|mjs|cjs|mts|cts|vue|svelte)$`),
		[]*regexp.Regexp{
			regexp.MustCompile(lineStart + `import` + jsS + `+[^'"();]*?from` + jsS + `*['"]([^'"]+)['"]`),
			regexp.MustCompile(lineStart + `import` + jsS + `*['"]([^'"]+)['"]`),
			regexp.MustCompile(lineStart + `export` + jsS + `+[^'"();]*?from` + jsS + `*['"]([^'"]+)['"]`),
			regexp.MustCompile(`\brequire` + jsS + `*\(` + jsS + `*['"]([^'"]+)['"]` + jsS + `*\)`),
			regexp.MustCompile(`\bimport` + jsS + `*\(` + jsS + `*['"]([^'"]+)['"]` + jsS + `*\)`),
		},
	},
	{
		regexp.MustCompile(`(?i)\.py$`),
		[]*regexp.Regexp{
			regexp.MustCompile(lineStart + `from` + jsS + `+([.\w]+)` + jsS + `+import` + jsS + `+`),
			regexp.MustCompile(lineStart + `import` + jsS + `+([.\w]+)`),
		},
	},
	{
		regexp.MustCompile(`(?i)\.go$`),
		[]*regexp.Regexp{
			regexp.MustCompile(lineStart + `import` + jsS + `+(?:\w+` + jsS + `+)?"([^"]+)"`),
			// The JS pattern carries the m flag: `^` and `$` are line anchors.
			regexp.MustCompile(`(?m)(?:^|\n)` + jsS + `*"([^"]+)"` + jsS + `*(?://` + JSDot + `*)?$`),
		},
	},
	{
		regexp.MustCompile(`(?i)\.rs$`),
		[]*regexp.Regexp{
			regexp.MustCompile(lineStart + `(?:pub` + jsS + `+)?use` + jsS + `+([\w:{}, *]+);`),
			regexp.MustCompile(lineStart + `(?:pub` + jsS + `+)?mod` + jsS + `+(\w+)` + jsS + `*;`),
		},
	},
	{
		regexp.MustCompile(`(?i)\.dart$`),
		[]*regexp.Regexp{regexp.MustCompile(lineStart + `(?:import|export|part)` + jsS + `+['"]([^'"]+)['"]`)},
	},
	{
		regexp.MustCompile(`(?i)\.rb$`),
		[]*regexp.Regexp{regexp.MustCompile(lineStart + `require(?:_relative)?` + jsS + `+['"]([^'"]+)['"]`)},
	},
	{
		regexp.MustCompile(`(?i)\.(c|h|cc|cpp|cxx|hpp|hh|m|mm)$`),
		[]*regexp.Regexp{regexp.MustCompile(lineStart + `#include` + jsS + `+["<]([^">]+)[">]`)},
	},
	{
		regexp.MustCompile(`(?i)\.(php)$`),
		[]*regexp.Regexp{regexp.MustCompile(lineStart + `(?:require|include)(?:_once)?` + jsS + `*\(?` + jsS + `*['"]([^'"]+)['"]`)},
	},
}

// ExtractImports lists the raw import specifiers a file mentions, in source
// order, deduplicated.
func ExtractImports(path, content string) []string {
	out := []string{}
	for _, rules := range importPatterns {
		if !rules.test.MatchString(path) {
			continue
		}
		text := stripComments(content)
		seen := map[string]bool{}
		for _, re := range rules.extract {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				spec := JSTrim(m[1])
				if spec != "" && !seen[spec] {
					seen[spec] = true
					out = append(out, spec)
				}
			}
		}
		break
	}
	return out
}

func dirOf(path string) string {
	cut := strings.LastIndex(path, "/")
	if cut == -1 {
		return ""
	}
	return path[:cut]
}

// normalisePath turns `a/b/../c` and `./x` into `a/c` and `x`.
func normalisePath(path string) string {
	out := []string{}
	for _, part := range strings.Split(path, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	return strings.Join(out, "/")
}

// firstExisting tries a base path as-is, with known extensions, and as a
// directory index.
func firstExisting(base string, files map[string]bool) (string, bool) {
	if files[base] {
		return base, true
	}
	for _, ext := range jsExtensions {
		if files[base+ext] {
			return base + ext, true
		}
	}
	for _, ext := range jsExtensions {
		if p := base + "/index" + ext; files[p] {
			return p, true
		}
	}
	for _, ext := range []string{".py", ".go", ".rs", ".dart", ".rb", ".php"} {
		if files[base+ext] {
			return base + ext, true
		}
	}
	if p := base + "/__init__.py"; files[p] {
		return p, true
	}
	if p := base + "/mod.rs"; files[p] {
		return p, true
	}
	return "", false
}

var (
	rePythonDots = regexp.MustCompile(`^(\.+)([\w.]*)$`)
	reRootAlias  = regexp.MustCompile(`^(?:@|~|#)/(` + JSDot + `+)$`)
	reRustPrefix = regexp.MustCompile(`^(crate|self|super)::`)
	reSpecCut    = regexp.MustCompile(`[{ ,*]`)
	reTrailSlash = regexp.MustCompile(`/+$`)
)

// ResolveImport is the repo file an import points at, or ok=false when it
// resolves outside the repo (a package, a stdlib module) — those are not edges
// in this graph.
func ResolveImport(fromPath, spec string, files map[string]bool) (string, bool) {
	if spec == "" {
		return "", false
	}
	here := dirOf(fromPath)

	// Relative: the common, unambiguous case.
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." || spec == ".." {
		return firstExisting(normalisePath(here+"/"+spec), files)
	}
	// Python's leading dots mean "up from here".
	if dots := rePythonDots.FindStringSubmatch(spec); dots != nil && strings.HasPrefix(spec, ".") {
		up := strings.Repeat("../", max(0, len(dots[1])-1))
		return firstExisting(normalisePath(here+"/"+up+strings.ReplaceAll(dots[2], ".", "/")), files)
	}
	// Common aliases for the project root.
	if alias := reRootAlias.FindStringSubmatch(spec); alias != nil {
		if p, ok := firstExisting(alias[1], files); ok {
			return p, true
		}
		return firstExisting("src/"+alias[1], files)
	}
	// Rust paths, and dotted Python modules, both name folders in the repo.
	dotted := strings.ReplaceAll(strings.ReplaceAll(reRustPrefix.ReplaceAllString(spec, ""), "::", "/"), ".", "/")
	cleaned := reTrailSlash.ReplaceAllString(reSpecCut.Split(dotted, -1)[0], "")
	if cleaned != "" && !strings.Contains(cleaned, ":") {
		if p, ok := firstExisting(cleaned, files); ok {
			return p, true
		}
		if p, ok := firstExisting("src/"+cleaned, files); ok {
			return p, true
		}
		// A sibling module named without a prefix (`mod foo;`, `require 'foo'`).
		if p, ok := firstExisting(normalisePath(here+"/"+cleaned), files); ok {
			return p, true
		}
	}
	return "", false
}

// FolderOf is the folder a file belongs to, cut to depth levels; a root file
// belongs to "(root)".
func FolderOf(path string, depth int) string {
	parts := strings.Split(path, "/")
	parts = parts[:len(parts)-1]
	if len(parts) == 0 {
		return "(root)"
	}
	return strings.Join(parts[:min(len(parts), max(1, depth))], "/")
}

// ImportLink is one resolved file-to-file import.
type ImportLink struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PickDepth chooses how deep to cut folder paths so the graph has enough nodes
// to be interesting without turning into a hairball. Repos bury their
// structure at different depths — `src/main` in one,
// `src/renderer/src/components` in another — so the depth is chosen from the
// links themselves: the deepest cut that still fits. Folders with no links at
// all don't count, since they are never drawn. maxNodes <= 0 means 14.
func PickDepth(links []ImportLink, maxNodes int) int {
	if maxNodes <= 0 {
		maxNodes = 14
	}
	best, bestNodes := 1, -1
	for depth := 1; depth <= 6; depth++ {
		nodes := map[string]bool{}
		for _, l := range links {
			from, to := FolderOf(l.From, depth), FolderOf(l.To, depth)
			if from == to {
				continue
			}
			nodes[from] = true
			nodes[to] = true
		}
		if len(nodes) > maxNodes {
			break
		}
		// Deeper only wins if it actually separates more folders — past the
		// deepest real folder every cut looks the same, and the shallower one
		// is the truth.
		if len(nodes) > bestNodes {
			bestNodes = len(nodes)
			best = depth
		}
	}
	return best
}

// ImportGraphOpts are buildImportGraph's options: Depth <= 0 picks the depth
// from the repo's own layout; a nil MaxEdges means 40.
type ImportGraphOpts struct {
	Depth    int
	MaxEdges *int
}

// BuildImportGraph aggregates file-level imports into a folder-level graph.
// Edges inside a folder are dropped — the interesting part is which folder
// leans on which.
func BuildImportGraph(files []SourceFile, opts ImportGraphOpts) ImportGraph {
	maxEdges := 40
	if opts.MaxEdges != nil {
		maxEdges = *opts.MaxEdges
	}
	known := map[string]bool{}
	for _, f := range files {
		known[f.Path] = true
	}

	// Resolve every import to a file once; the folder depth is applied after,
	// so trying several depths costs nothing.
	var links []ImportLink
	resolved, external := 0, 0
	for _, f := range files {
		for _, spec := range ExtractImports(f.Path, f.Content) {
			target, ok := ResolveImport(f.Path, spec, known)
			if !ok {
				external++
				continue
			}
			resolved++
			links = append(links, ImportLink{From: f.Path, To: target})
		}
	}

	depth := opts.Depth
	if depth <= 0 {
		depth = PickDepth(links, 14)
	}
	fileCounts := map[string]int{}
	for _, f := range files {
		fileCounts[FolderOf(f.Path, depth)]++
	}

	counts := map[string]int{}
	var keys []string
	for _, l := range links {
		from, to := FolderOf(l.From, depth), FolderOf(l.To, depth)
		if from == to {
			continue // same folder: not a dependency between modules
		}
		key := from + " " + to
		if _, ok := counts[key]; !ok {
			keys = append(keys, key)
		}
		counts[key]++
	}

	all := make([]ImportEdge, 0, len(keys))
	for _, key := range keys {
		// Split on the space exactly as TS does, so a folder holding a space
		// splits the same (wrong) way on both sides.
		parts := strings.Split(key, " ")
		all = append(all, ImportEdge{From: parts[0], To: parts[1], Count: counts[key]})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return LocaleCompare(all[i].From, all[j].From) < 0
	})

	end := maxEdges
	if end < 0 {
		end = max(0, len(all)+end)
	}
	edges := all[:min(len(all), end)]
	var ids []string
	used := map[string]bool{}
	for _, e := range edges {
		for _, id := range []string{e.From, e.To} {
			if !used[id] {
				used[id] = true
				ids = append(ids, id)
			}
		}
	}
	nodes := make([]ImportNode, 0, len(ids))
	for _, id := range ids {
		n := ImportNode{ID: id, Files: fileCounts[id]}
		for _, e := range edges {
			if e.From == id {
				n.Out += e.Count
			}
			if e.To == id {
				n.In += e.Count
			}
		}
		nodes = append(nodes, n)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i].In+nodes[i].Out, nodes[j].In+nodes[j].Out
		if a != b {
			return a > b
		}
		return LocaleCompare(nodes[i].ID, nodes[j].ID) < 0
	})
	return ImportGraph{
		Nodes:        nodes,
		Edges:        append([]ImportEdge{}, edges...),
		Depth:        depth,
		Resolved:     resolved,
		External:     external,
		OmittedEdges: len(all) - len(edges),
	}
}
