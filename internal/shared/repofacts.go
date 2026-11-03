package shared

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Facts about a repository that can be counted rather than guessed: what it is
// written in, and what it declares as a dependency. Pure functions over file
// lists and manifest text. Mirrors the parts of src/shared/repoFacts.ts the
// main process uses (the wiki); the renderer keeps the TS copy for formatting.

// LanguageStat is LanguageStat in src/shared/types.ts: the share of the
// codebase written in one language, counted by bytes.
type LanguageStat struct {
	Language string `json:"language"`
	Bytes    int64  `json:"bytes"`
	Files    int    `json:"files"`
	// Share is 0–1, of the code counted (config and docs excluded).
	Share float64 `json:"share"`
}

// DependencyRef is DependencyRef in src/shared/types.ts: a dependency exactly
// as a manifest declares it.
type DependencyRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Dev     bool   `json:"dev"`
}

var languageByExtension = map[string]string{
	"ts": "TypeScript", "tsx": "TypeScript", "mts": "TypeScript", "cts": "TypeScript",
	"js": "JavaScript", "jsx": "JavaScript", "mjs": "JavaScript", "cjs": "JavaScript",
	"py": "Python", "rb": "Ruby", "go": "Go", "rs": "Rust", "java": "Java", "kt": "Kotlin",
	"kts": "Kotlin", "swift": "Swift", "m": "Objective-C", "mm": "Objective-C++",
	"c": "C", "h": "C", "cc": "C++", "cpp": "C++", "cxx": "C++", "hpp": "C++", "hh": "C++",
	"cs": "C#", "php": "PHP", "dart": "Dart", "ex": "Elixir", "exs": "Elixir",
	"scala": "Scala", "clj": "Clojure", "hs": "Haskell", "lua": "Lua", "pl": "Perl",
	"r": "R", "jl": "Julia", "zig": "Zig", "vue": "Vue", "svelte": "Svelte",
	"css": "CSS", "scss": "Sass", "sass": "Sass", "less": "Less",
	"html": "HTML", "htm": "HTML", "sh": "Shell", "bash": "Shell", "zsh": "Shell",
	"fish": "Shell", "ps1": "PowerShell", "sql": "SQL", "graphql": "GraphQL",
	"gql": "GraphQL", "proto": "Protobuf", "md": "Markdown", "mdx": "Markdown",
	"json": "JSON", "yml": "YAML", "yaml": "YAML", "toml": "TOML", "xml": "XML",
}

var languageByFilename = map[string]string{
	"dockerfile":     "Docker",
	"makefile":       "Make",
	"rakefile":       "Ruby",
	"gemfile":        "Ruby",
	"cmakelists.txt": "CMake",
}

// supportingLanguages describe configuration rather than the program itself.
var supportingLanguages = map[string]bool{"JSON": true, "YAML": true, "TOML": true, "XML": true, "Markdown": true}

func lowerBase(path string) string {
	return strings.ToLower(path[strings.LastIndex(path, "/")+1:])
}

// LanguageOf is the language a file counts as, or "" when it counts as none.
func LanguageOf(path string) string {
	name := lowerBase(path)
	if l := languageByFilename[name]; l != "" {
		return l
	}
	dot := strings.LastIndex(name, ".")
	if dot <= 0 {
		return ""
	}
	return languageByExtension[name[dot+1:]]
}

// SizedFile is one entry of LanguageBreakdown's input.
type SizedFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// LanguageBreakdown is the share of the codebase per language, by bytes,
// largest first. Config and docs are counted separately so a repo full of JSON
// fixtures doesn't read as a JSON project; includeSupporting folds them back in.
func LanguageBreakdown(files []SizedFile, includeSupporting bool) []LanguageStat {
	type entry struct {
		bytes int64
		files int
	}
	totals := map[string]*entry{}
	var order []string
	for _, f := range files {
		language := LanguageOf(f.Path)
		if language == "" {
			continue
		}
		if !includeSupporting && supportingLanguages[language] {
			continue
		}
		e := totals[language]
		if e == nil {
			e = &entry{}
			totals[language] = e
			order = append(order, language)
		}
		if f.Size > 0 {
			e.bytes += f.Size
		}
		e.files++
	}
	var total int64
	for _, l := range order {
		total += totals[l].bytes
	}
	out := make([]LanguageStat, 0, len(order))
	for _, l := range order {
		e := totals[l]
		share := 0.0
		if total > 0 {
			share = float64(e.bytes) / float64(total)
		}
		out = append(out, LanguageStat{Language: l, Bytes: e.bytes, Files: e.files, Share: share})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return LocaleCompare(out[i].Language, out[j].Language) < 0
	})
	return out
}

// ManifestFiles are the manifest basenames worth parsing, lowercased.
var ManifestFiles = []string{
	"package.json",
	"pyproject.toml",
	"requirements.txt",
	"cargo.toml",
	"go.mod",
	"pubspec.yaml",
	"composer.json",
	"gemfile",
}

func parsePackageJSON(content string) []DependencyRef {
	var root any
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return []DependencyRef{}
	}
	if root == nil {
		// JSON.parse('null')[field] throws; the catch turns that into [].
		return []DependencyRef{}
	}
	fields := map[string]json.RawMessage{}
	if _, isObject := root.(map[string]any); isObject {
		_ = json.Unmarshal([]byte(content), &fields)
	}
	out := []DependencyRef{}
	for _, field := range []string{"dependencies", "devDependencies", "peerDependencies"} {
		raw, ok := fields[field]
		if !ok || strings.TrimSpace(string(raw)) == "null" {
			continue
		}
		for _, kv := range OrderedEntries(raw) {
			out = append(out, DependencyRef{Name: kv[0].(string), Version: JSString(kv[1]), Dev: field == "devDependencies"})
		}
	}
	return out
}

var (
	reTomlSection    = regexp.MustCompile(`^\[([^\]]+)\]$`)
	reCargoDeps      = regexp.MustCompile(`(^|\.)(dependencies|dev-dependencies|build-dependencies)$`)
	reCargoDev       = regexp.MustCompile(`dev-dependencies|build-dependencies`)
	reCargoNested    = regexp.MustCompile(`^(?:` + JSDot + `*\.)?(?:dev-)?dependencies\.(` + JSDot + `+)$`)
	reCargoEntry     = regexp.MustCompile(`^([A-Za-z0-9_.-]+)` + JSSpace + `*=` + JSSpace + `*(` + JSDot + `+)$`)
	reQuoted         = regexp.MustCompile(`"([^"]+)"`)
	rePep621         = regexp.MustCompile(`dependencies` + JSSpace + `*=` + JSSpace + `*\[([^\]]*)\]`)
	rePyQuoted       = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	rePyName         = regexp.MustCompile(`^[A-Za-z0-9._-]+`)
	rePoetrySection  = regexp.MustCompile(`^tool\.poetry\.(dev-)?dependencies$|^tool\.poetry\.group\.` + JSDot + `+\.dependencies$`)
	rePyEntry        = regexp.MustCompile(`^([A-Za-z0-9._-]+)` + JSSpace + `*=` + JSSpace + `*(` + JSDot + `+)$`)
	reGoRequireOpen  = regexp.MustCompile(`^require` + JSSpace + `*\($`)
	reGoRequireOne   = regexp.MustCompile(`^require` + JSSpace + `+(` + JSNotSpace + `+)` + JSSpace + `+(` + JSNotSpace + `+)`)
	reGoEntry        = regexp.MustCompile(`^(` + JSNotSpace + `+)` + JSSpace + `+(` + JSNotSpace + `+)`)
	rePubDeps        = regexp.MustCompile(`^dependencies:` + JSSpace + `*$`)
	rePubDevDeps     = regexp.MustCompile(`^dev_dependencies:` + JSSpace + `*$`)
	rePubTopLevel    = regexp.MustCompile(`^` + JSNotSpace)
	rePubEntry       = regexp.MustCompile(`^` + JSSpace + `{2}([A-Za-z0-9_]+):` + JSSpace + `*(` + JSDot + `*)$`)
	rePubQuotes      = regexp.MustCompile(`['"]`)
	reGemfileEntry   = regexp.MustCompile(`(?m)^` + JSSpace + `*gem` + JSSpace + `+['"]([^'"]+)['"](?:` + JSSpace + `*,` + JSSpace + `*['"]([^'"]+)['"])?`)
	reRequirementsNm = regexp.MustCompile(`^[A-Za-z0-9._-]+`)
)

// quotedVersion is `/"([^"]+)"/.exec(s)?.[1] ?? ”`.
func quotedVersion(s string) string {
	if m := reQuoted.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// parseCargoToml reads `name = "1.0"`, `name = { version = "1.0" }` and bare
// `name` table headers.
func parseCargoToml(content string) []DependencyRef {
	out := []DependencyRef{}
	dev, inDeps := false, false
	for _, raw := range strings.Split(content, "\n") {
		line := JSTrim(raw)
		if section := reTomlSection.FindStringSubmatch(line); section != nil {
			name := section[1]
			inDeps = reCargoDeps.MatchString(name)
			dev = reCargoDev.MatchString(name)
			// `[dependencies.foo]` declares foo itself.
			if nested := reCargoNested.FindStringSubmatch(name); nested != nil {
				out = append(out, DependencyRef{Name: nested[1], Version: "", Dev: dev})
			}
			continue
		}
		if !inDeps || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry := reCargoEntry.FindStringSubmatch(line)
		if entry == nil {
			continue
		}
		out = append(out, DependencyRef{Name: entry[1], Version: quotedVersion(entry[2]), Dev: dev})
	}
	return out
}

func parsePyprojectToml(content string) []DependencyRef {
	out := []DependencyRef{}
	// PEP 621: dependencies = ["requests>=2", …]
	if pep621 := rePep621.FindStringSubmatch(content); pep621 != nil {
		for _, m := range rePyQuoted.FindAllStringSubmatch(pep621[1], -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2]
			}
			spec = JSTrim(spec)
			if name := rePyName.FindString(spec); name != "" {
				out = append(out, DependencyRef{Name: name, Version: JSTrim(spec[len(name):]), Dev: false})
			}
		}
	}
	// Poetry: [tool.poetry.dependencies] name = "^1.0"
	inPoetry, dev := false, false
	for _, raw := range strings.Split(content, "\n") {
		line := JSTrim(raw)
		if section := reTomlSection.FindStringSubmatch(line); section != nil {
			inPoetry = rePoetrySection.MatchString(section[1])
			dev = strings.Contains(section[1], "dev")
			continue
		}
		if !inPoetry || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if entry := rePyEntry.FindStringSubmatch(line); entry != nil && strings.ToLower(entry[1]) != "python" {
			out = append(out, DependencyRef{Name: entry[1], Version: quotedVersion(entry[2]), Dev: dev})
		}
	}
	return out
}

func parseRequirementsTxt(content string) []DependencyRef {
	out := []DependencyRef{}
	for _, raw := range strings.Split(content, "\n") {
		line := JSTrim(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if name := reRequirementsNm.FindString(line); name != "" {
			out = append(out, DependencyRef{Name: name, Version: JSTrim(line[len(name):]), Dev: false})
		}
	}
	return out
}

func parseGoMod(content string) []DependencyRef {
	out := []DependencyRef{}
	inBlock := false
	for _, raw := range strings.Split(content, "\n") {
		line := JSTrim(raw)
		if reGoRequireOpen.MatchString(line) {
			inBlock = true
			continue
		}
		if inBlock && line == ")" {
			inBlock = false
			continue
		}
		if single := reGoRequireOne.FindStringSubmatch(line); single != nil {
			out = append(out, DependencyRef{Name: single[1], Version: single[2], Dev: false})
			continue
		}
		if !inBlock || line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if entry := reGoEntry.FindStringSubmatch(line); entry != nil {
			out = append(out, DependencyRef{Name: entry[1], Version: entry[2], Dev: strings.Contains(line, "// indirect")})
		}
	}
	return out
}

func parsePubspecYaml(content string) []DependencyRef {
	out := []DependencyRef{}
	dev, inDeps := false, false
	for _, raw := range strings.Split(content, "\n") {
		if rePubDeps.MatchString(raw) {
			inDeps, dev = true, false
			continue
		}
		if rePubDevDeps.MatchString(raw) {
			inDeps, dev = true, true
			continue
		}
		if rePubTopLevel.MatchString(raw) {
			inDeps = false
			continue
		}
		if !inDeps {
			continue
		}
		if entry := rePubEntry.FindStringSubmatch(raw); entry != nil && entry[1] != "sdk" {
			out = append(out, DependencyRef{Name: entry[1], Version: rePubQuotes.ReplaceAllString(JSTrim(entry[2]), ""), Dev: dev})
		}
	}
	return out
}

func parseGemfile(content string) []DependencyRef {
	out := []DependencyRef{}
	for _, m := range reGemfileEntry.FindAllStringSubmatch(content, -1) {
		out = append(out, DependencyRef{Name: m[1], Version: m[2], Dev: false})
	}
	return out
}

// ParseManifest returns the direct dependencies one manifest declares, or []
// when it cannot be read.
func ParseManifest(path, content string) []DependencyRef {
	switch lowerBase(path) {
	case "package.json", "composer.json":
		return parsePackageJSON(content)
	case "cargo.toml":
		return parseCargoToml(content)
	case "pyproject.toml":
		return parsePyprojectToml(content)
	case "requirements.txt":
		return parseRequirementsTxt(content)
	case "go.mod":
		return parseGoMod(content)
	case "pubspec.yaml":
		return parsePubspecYaml(content)
	case "gemfile":
		return parseGemfile(content)
	}
	return []DependencyRef{}
}

// ─── Signal vs scaffolding ──────────────────────────────────────────────────

// Packages that say nothing about what a project *is*: type stubs, loaders,
// lint/format plugins, polyfills. Nobody describes their stack as "react-dom
// and ts-loader".
var noiseDependencies = []*regexp.Regexp{
	regexp.MustCompile(`^@types/`),
	regexp.MustCompile(`^@typescript-eslint/`),
	regexp.MustCompile(`^eslint(-|$)`),
	regexp.MustCompile(`^prettier(-|$)`),
	regexp.MustCompile(`-loader$`),
	regexp.MustCompile(`^babel-|^@babel/`),
	regexp.MustCompile(`^postcss(-|$)`),
	regexp.MustCompile(`^autoprefixer$`),
	regexp.MustCompile(`^core-js$`),
	regexp.MustCompile(`^tslib$`),
	regexp.MustCompile(`^rimraf$|^cross-env$|^npm-run-all$|^concurrently$`),
	regexp.MustCompile(`^husky$|^lint-staged$`),
	regexp.MustCompile(`^@rollup/|^rollup-plugin-`),
	regexp.MustCompile(`^vite-plugin-`),
	regexp.MustCompile(`^webpack-(cli|dev-server|merge)$`),
	regexp.MustCompile(`^ts-node$|^tsx$|^nodemon$`),
}

// Sub-packages that ride along with the framework they belong to: knowing a
// React app also has react-dom is not information.
var impliedDependencies = map[string]string{
	"react-dom":                 "react",
	"react-native":              "react",
	"vue-router":                "vue",
	"@angular/common":           "@angular/core",
	"@angular/platform-browser": "@angular/core",
	"next-auth":                 "next",
}

// IsNoiseDependency reports whether a dependency is scaffolding rather than
// part of the stack's story.
func IsNoiseDependency(name string) bool {
	for _, re := range noiseDependencies {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// FrameworkHit is one recognised framework, with the badge the stack shows.
type FrameworkHit struct {
	Label string `json:"label"`
	Badge string `json:"badge"`
	Kind  string `json:"kind"`
	Dep   string `json:"dep"`
}

type framework struct {
	match *regexp.Regexp
	label string
	badge string
	kind  string
}

func fw(re, label, badge, kind string) framework {
	return framework{regexp.MustCompile(re), label, badge, kind}
}

// The frameworks a project is built on, recognised by name. Each carries a
// badge so the stack reads at a glance instead of as a list of packages.
var frameworks = []framework{
	fw(`^next$`, "Next.js", "▲", "framework"),
	fw(`^nuxt$`, "Nuxt", "◈", "framework"),
	fw(`^@remix-run/`, "Remix", "💿", "framework"),
	fw(`^astro$`, "Astro", "🚀", "framework"),
	fw(`^@angular/core$`, "Angular", "🅰", "framework"),
	fw(`^react$`, "React", "⚛", "ui"),
	fw(`^vue$`, "Vue", "🟩", "ui"),
	fw(`^svelte$|^@sveltejs/kit$`, "Svelte", "🔥", "ui"),
	fw(`^solid-js$`, "Solid", "🔷", "ui"),
	fw(`^electron$`, "Electron", "⚡", "runtime"),
	fw(`^tauri|^@tauri-apps/`, "Tauri", "🦀", "runtime"),
	fw(`^react-native$`, "React Native", "📱", "runtime"),
	fw(`^flutter$`, "Flutter", "🐦", "framework"),
	fw(`^tailwindcss$`, "Tailwind", "🌊", "styling"),
	fw(`^bootstrap$`, "Bootstrap", "🅱", "styling"),
	fw(`^express$`, "Express", "🚏", "server"),
	fw(`^fastify$`, "Fastify", "🏎", "server"),
	fw(`^@nestjs/core$`, "NestJS", "🐱", "server"),
	fw(`(?i)^django$`, "Django", "🎸", "framework"),
	fw(`(?i)^flask$`, "Flask", "🧪", "framework"),
	fw(`(?i)^fastapi$`, "FastAPI", "⚡", "framework"),
	fw(`(?i)^rails$`, "Rails", "🛤", "framework"),
	fw(`(?i)^laravel/`, "Laravel", "🎯", "framework"),
	fw(`^github\.com/gin-gonic/gin$`, "Gin", "🍸", "server"),
	fw(`^github\.com/labstack/echo`, "Echo", "📣", "server"),
	fw(`^axum$|^actix-web$`, "Rust web", "🦀", "server"),
	fw(`^tokio$`, "Tokio", "🌀", "runtime"),
	fw(`^spring-boot|^org\.springframework`, "Spring", "🌱", "framework"),
	fw(`^vite$`, "Vite", "⚡", "build"),
	fw(`^webpack$`, "webpack", "📦", "build"),
	fw(`^esbuild$`, "esbuild", "⚙", "build"),
	fw(`^typescript$`, "TypeScript", "TS", "language"),
	fw(`^vitest$|^jest$`, "Vitest/Jest", "✅", "testing"),
	fw(`^@playwright/test$|^playwright$`, "Playwright", "🎭", "testing"),
	fw(`^cypress$`, "Cypress", "🌲", "testing"),
	fw(`^prisma$|^@prisma/client$`, "Prisma", "🔺", "data"),
	fw(`^mongoose$`, "Mongoose", "🍃", "data"),
	fw(`^sqlite3$|^better-sqlite3$|go-sqlite3$`, "SQLite", "🗄", "data"),
	fw(`^pg$|^postgres$`, "Postgres", "🐘", "data"),
	fw(`^redis$`, "Redis", "🧠", "data"),
}

// DetectFrameworks lists recognised frameworks, one per label, in the order
// they are declared.
func DetectFrameworks(deps []DependencyRef) []FrameworkHit {
	out := []FrameworkHit{}
	seen := map[string]bool{}
	for _, dep := range deps {
		for _, f := range frameworks {
			if f.match.MatchString(dep.Name) && !seen[f.label] {
				seen[f.label] = true
				out = append(out, FrameworkHit{Label: f.label, Badge: f.badge, Kind: f.kind, Dep: dep.Name})
			}
		}
	}
	return out
}

// MeaningfulDependencies keeps the dependencies worth showing a human:
// scaffolding dropped, and sub-packages folded into the framework that
// implies them.
func MeaningfulDependencies(deps []DependencyRef) []DependencyRef {
	present := map[string]bool{}
	for _, d := range deps {
		present[d.Name] = true
	}
	out := []DependencyRef{}
	for _, dep := range deps {
		if IsNoiseDependency(dep.Name) {
			continue
		}
		if by, ok := impliedDependencies[dep.Name]; ok && present[by] {
			continue
		}
		out = append(out, dep)
	}
	return out
}

// FindManifests lists the manifests present in the repo, shallowest first —
// the root one leads. max <= 0 means the TS default of 6.
func FindManifests(paths []string, max int) []string {
	if max <= 0 {
		max = 6
	}
	out := []string{}
	for _, p := range paths {
		name := lowerBase(p)
		for _, m := range ManifestFiles {
			if m == name {
				out = append(out, p)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := strings.Count(out[i], "/"), strings.Count(out[j], "/")
		if di != dj {
			return di < dj
		}
		return LocaleCompare(out[i], out[j]) < 0
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}
