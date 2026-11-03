package shared

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

// Ports of the main-process cases in test/pureLogic.test.ts ("repo facts — …"
// and "import graph — …"). Cases for renderer-only helpers (topLanguages,
// entryPoints, foundations, commonPrefix, shortLabel, layerNodes, the layouts)
// stay in vitest with the TS they test.

func TestRepoFactsLanguages(t *testing.T) {
	t.Run("maps files to languages by extension and by name", func(t *testing.T) {
		for path, want := range map[string]string{"src/app.tsx": "TypeScript", "lib/thing.rb": "Ruby", "Dockerfile": "Docker", "deploy/Makefile": "Make", "LICENSE": ""} {
			if got := LanguageOf(path); got != want {
				t.Errorf("%s = %q, want %q", path, got, want)
			}
		}
	})
	t.Run("weighs languages by bytes, biggest first", func(t *testing.T) {
		stats := LanguageBreakdown([]SizedFile{{"a.ts", 700}, {"b.ts", 300}, {"c.py", 250}}, false)
		if len(stats) != 2 || stats[0].Language != "TypeScript" || stats[1].Language != "Python" ||
			stats[0].Bytes != 1000 || stats[0].Files != 2 || math.Abs(stats[0].Share-0.8) > 0.01 || math.Abs(stats[1].Share-0.2) > 0.01 {
			t.Fatal(stats)
		}
	})
	t.Run("leaves config and docs out so fixtures do not read as the language", func(t *testing.T) {
		files := []SizedFile{{"a.ts", 100}, {"data.json", 9000}, {"README.md", 500}}
		langs := func(s []LanguageStat) []string {
			out := []string{}
			for _, l := range s {
				out = append(out, l.Language)
			}
			return out
		}
		if got := langs(LanguageBreakdown(files, false)); !reflect.DeepEqual(got, []string{"TypeScript"}) {
			t.Fatal(got)
		}
		if got := langs(LanguageBreakdown(files, true)); !reflect.DeepEqual(got, []string{"JSON", "Markdown", "TypeScript"}) {
			t.Fatal(got)
		}
	})
	t.Run("handles a repo with nothing countable", func(t *testing.T) {
		if got := LanguageBreakdown(nil, false); len(got) != 0 || got == nil {
			t.Fatal(got)
		}
	})
}

func TestRepoFactsManifests(t *testing.T) {
	t.Run("finds manifests shallowest first", func(t *testing.T) {
		got := FindManifests([]string{"src/app.ts", "web/package.json", "package.json", "Cargo.toml"}, 0)
		if !reflect.DeepEqual(got, []string{"Cargo.toml", "package.json", "web/package.json"}) {
			t.Fatal(got)
		}
	})
	t.Run("reads npm dependencies, marking dev ones", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"dependencies": map[string]string{"react": "^18.0.0"}, "devDependencies": map[string]string{"vitest": "^2.0.0"}})
		want := []DependencyRef{{"react", "^18.0.0", false}, {"vitest", "^2.0.0", true}}
		if got := ParseManifest("package.json", string(raw)); !reflect.DeepEqual(got, want) {
			t.Fatal(got)
		}
	})
	t.Run("survives a malformed manifest instead of throwing", func(t *testing.T) {
		if got := ParseManifest("package.json", "{ not json"); len(got) != 0 {
			t.Fatal(got)
		}
	})
	t.Run("reads Cargo, go.mod, pyproject, requirements, pubspec and Gemfile", func(t *testing.T) {
		check := func(name string, got, want []DependencyRef) {
			t.Helper()
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s = %v, want %v", name, got, want)
			}
		}
		check("Cargo.toml", ParseManifest("Cargo.toml", "[dependencies]\nserde = \"1.0\"\ntokio = { version = \"1.35\" }\n"),
			[]DependencyRef{{"serde", "1.0", false}, {"tokio", "1.35", false}})
		if got := ParseManifest("go.mod", "module x\n\nrequire (\n\tgithub.com/foo/bar v1.2.3\n)\n"); got[0].Name != "github.com/foo/bar" || got[0].Version != "v1.2.3" {
			t.Error(got)
		}
		if got := ParseManifest("pyproject.toml", `dependencies = ["requests>=2.0", "flask"]`); len(got) != 2 {
			t.Error(got)
		}
		check("requirements.txt", ParseManifest("requirements.txt", "# comment\nrequests==2.0\n-e .\n"), []DependencyRef{{"requests", "==2.0", false}})
		check("pubspec.yaml", ParseManifest("pubspec.yaml", "dependencies:\n  http: ^1.0.0\n  sdk: flutter\n"), []DependencyRef{{"http", "^1.0.0", false}})
		check("Gemfile", ParseManifest("Gemfile", "source 'x'\ngem 'rails', '7.0'\n"), []DependencyRef{{"rails", "7.0", false}})
	})
}

func deps(names ...string) []DependencyRef {
	out := make([]DependencyRef, len(names))
	for i, n := range names {
		out[i] = DependencyRef{Name: n, Version: "1.0.0"}
	}
	return out
}

func TestRepoFactsSignalOverScaffolding(t *testing.T) {
	t.Run("drops type stubs, loaders and lint plugins", func(t *testing.T) {
		for _, n := range []string{"@types/node", "ts-loader", "eslint-plugin-react", "prettier", "@babel/core", "tslib"} {
			if !IsNoiseDependency(n) {
				t.Error(n)
			}
		}
		for _, n := range []string{"react", "electron", "tailwindcss", "go-sqlite3"} {
			if IsNoiseDependency(n) {
				t.Error(n)
			}
		}
	})
	t.Run("folds away sub-packages implied by their framework", func(t *testing.T) {
		got := MeaningfulDependencies(deps("react", "react-dom", "vue-router"))
		if len(got) != 2 || got[0].Name != "react" || got[1].Name != "vue-router" {
			t.Fatal(got)
		}
	})
	t.Run("recognises frameworks and gives each a badge", func(t *testing.T) {
		hits := DetectFrameworks(deps("next", "react", "tailwindcss", "left-pad"))
		if len(hits) != 3 || hits[0].Label != "Next.js" || hits[1].Label != "React" || hits[2].Label != "Tailwind" || hits[0].Badge == "" || hits[0].Kind != "framework" {
			t.Fatal(hits)
		}
	})
	t.Run("reports a framework once, whichever package revealed it", func(t *testing.T) {
		if hits := DetectFrameworks(deps("react", "react")); len(hits) != 1 || hits[0].Label != "React" {
			t.Fatal(hits)
		}
	})
}

func TestImportGraphReading(t *testing.T) {
	t.Run("reads every JS/TS import form", func(t *testing.T) {
		src := "\nimport { a } from './a'\nimport b from \"../b\"\nimport './side-effect'\nexport { c } from './c'\nconst d = require('./d')\nconst e = await import('./e')\n"
		got := map[string]bool{}
		for _, s := range ExtractImports("src/x.ts", src) {
			got[s] = true
		}
		for _, want := range []string{"../b", "./a", "./c", "./d", "./e", "./side-effect"} {
			if !got[want] {
				t.Error(want)
			}
		}
		if len(got) != 6 {
			t.Fatal(got)
		}
	})
	t.Run("ignores imports inside comments", func(t *testing.T) {
		if got := ExtractImports("src/x.ts", "// import { x } from './commented'\n/* import './block' */\nimport { y } from './real'"); !reflect.DeepEqual(got, []string{"./real"}) {
			t.Fatal(got)
		}
	})
	t.Run("reads Python, Go, Rust, Dart and Ruby", func(t *testing.T) {
		if got := ExtractImports("app.py", "from .models import User\nimport os"); !reflect.DeepEqual(got, []string{".models", "os"}) {
			t.Error(got)
		}
		if got := ExtractImports("main.go", `import "example.com/x/y"`); !reflect.DeepEqual(got, []string{"example.com/x/y"}) {
			t.Error(got)
		}
		if got := ExtractImports("lib.rs", "use crate::db::pool;\nmod helpers;"); !reflect.DeepEqual(got, []string{"crate::db::pool", "helpers"}) {
			t.Error(got)
		}
		if got := ExtractImports("main.dart", "import 'package:http/http.dart';"); !reflect.DeepEqual(got, []string{"package:http/http.dart"}) {
			t.Error(got)
		}
		if got := ExtractImports("app.rb", "require_relative 'thing'"); !reflect.DeepEqual(got, []string{"thing"}) {
			t.Error(got)
		}
	})
	t.Run("returns nothing for a language it does not parse", func(t *testing.T) {
		if got := ExtractImports("README.md", "import this"); len(got) != 0 {
			t.Fatal(got)
		}
	})
}

func TestImportGraphResolving(t *testing.T) {
	files := map[string]bool{"src/app.ts": true, "src/db/pool.ts": true, "src/db/index.ts": true, "src/ui/panel.tsx": true, "app/models/user.py": true, "app/__init__.py": true}
	res := func(from, spec string) string {
		p, _ := ResolveImport(from, spec, files)
		return p
	}
	t.Run("resolves relative paths, adding the extension", func(t *testing.T) {
		if res("src/app.ts", "./db/pool") != "src/db/pool.ts" || res("src/ui/panel.tsx", "../db/pool") != "src/db/pool.ts" {
			t.Fatal("relative")
		}
	})
	t.Run("resolves a folder to its index file", func(t *testing.T) {
		if res("src/app.ts", "./db") != "src/db/index.ts" {
			t.Fatal("index")
		}
	})
	t.Run("resolves root aliases", func(t *testing.T) {
		if res("src/ui/panel.tsx", "@/db/pool") != "src/db/pool.ts" {
			t.Fatal("alias")
		}
	})
	t.Run("resolves dotted Python modules", func(t *testing.T) {
		if res("app/main.py", "app.models.user") != "app/models/user.py" {
			t.Fatal("python")
		}
	})
	t.Run("returns null for packages outside the repo", func(t *testing.T) {
		for _, c := range [][2]string{{"src/app.ts", "react"}, {"main.go", "fmt"}, {"src/app.ts", "./nope"}} {
			if _, ok := ResolveImport(c[0], c[1], files); ok {
				t.Error(c)
			}
		}
	})
}

func TestImportGraphFolders(t *testing.T) {
	repo := []SourceFile{
		{"src/ui/panel.tsx", "import { query } from '../db/pool'\nimport './styles'"},
		{"src/ui/styles.ts", ""},
		{"src/db/pool.ts", "import { log } from '../util/log'"},
		{"src/util/log.ts", ""},
		{"src/app.ts", "import { Panel } from './ui/panel'\nimport react from 'react'"},
	}
	edge := func(g ImportGraph, from, to string) int {
		for _, e := range g.Edges {
			if e.From == from && e.To == to {
				return e.Count
			}
		}
		return 0
	}
	g2 := BuildImportGraph(repo, ImportGraphOpts{Depth: 2})
	t.Run("aggregates file imports into folder edges, with direction", func(t *testing.T) {
		if edge(g2, "src/ui", "src/db") != 1 || edge(g2, "src/db", "src/util") != 1 || edge(g2, "src", "src/ui") != 1 || edge(g2, "src/db", "src/ui") != 0 {
			t.Fatal(g2.Edges)
		}
	})
	t.Run("drops imports within the same folder", func(t *testing.T) {
		for _, e := range g2.Edges {
			if e.From == e.To {
				t.Fatal(e)
			}
		}
	})
	t.Run("counts what pointed outside the repo separately", func(t *testing.T) {
		if g2.Resolved != 4 || g2.External != 1 {
			t.Fatal(g2)
		}
	})
	t.Run("rolls folders up as depth shrinks", func(t *testing.T) {
		for _, n := range BuildImportGraph(repo, ImportGraphOpts{Depth: 1}).Nodes {
			if len(n.ID) > 0 && contains(n.ID, '/') {
				t.Fatal(n)
			}
		}
	})
	t.Run("keeps the busiest edges and reports the rest", func(t *testing.T) {
		one := 1
		g := BuildImportGraph(repo, ImportGraphOpts{Depth: 2, MaxEdges: &one})
		if len(g.Edges) != 1 || g.OmittedEdges != 2 {
			t.Fatal(g)
		}
	})
}

func contains(s string, r byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == r {
			return true
		}
	}
	return false
}

func TestImportGraphChoosingDepth(t *testing.T) {
	links := []ImportLink{
		{"src/renderer/src/components/A.tsx", "src/renderer/src/stores/ui.ts"},
		{"src/renderer/src/components/B.tsx", "src/shared/types.ts"},
		{"src/main/git.ts", "src/shared/types.ts"},
	}
	t.Run("cuts deep enough to separate sibling folders", func(t *testing.T) {
		if d := PickDepth(links, 14); d != 4 {
			t.Fatal(d)
		}
	})
	t.Run("backs off when a deep cut would make too many nodes", func(t *testing.T) {
		if d := PickDepth(links, 3); d >= 4 {
			t.Fatal(d)
		}
	})
	t.Run("survives a repo with no internal links at all", func(t *testing.T) {
		if d := PickDepth(nil, 14); d != 1 {
			t.Fatal(d)
		}
	})
}
