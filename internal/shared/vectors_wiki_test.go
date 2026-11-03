package shared

import (
	"encoding/json"
	"os"
	"testing"
)

// Replays testdata/vectors/{repoFacts,importGraph}.json, written by
// test/port/repochat.test.ts from src/shared/repoFacts.ts, importGraph.ts and
// secretFiles.ts.

type fnCase struct {
	Fn     string            `json:"fn"`
	Args   []json.RawMessage `json:"args"`
	Out    json.RawMessage   `json:"out"`
	Throws *string           `json:"throws"`
}

func loadCases(t *testing.T, name string) []fnCase {
	t.Helper()
	raw, err := os.ReadFile(vectorPath(name))
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

func setOf(paths []string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func TestRepoFactsVectors(t *testing.T) {
	for i, c := range loadCases(t, "repoFacts.json") {
		var got any
		switch c.Fn {
		case "languageOf":
			l := LanguageOf(arg[string](t, c, 0))
			if l == "" {
				got = nil
			} else {
				got = l
			}
		case "languageBreakdown":
			opts := arg[struct {
				IncludeSupporting bool `json:"includeSupporting"`
			}](t, c, 1)
			got = LanguageBreakdown(arg[[]SizedFile](t, c, 0), opts.IncludeSupporting)
		case "findManifests":
			got = FindManifests(arg[[]string](t, c, 0), arg[int](t, c, 1))
		case "parseManifest":
			got = ParseManifest(arg[string](t, c, 0), arg[string](t, c, 1))
		case "isNoiseDependency":
			got = IsNoiseDependency(arg[string](t, c, 0))
		case "meaningfulDependencies":
			got = MeaningfulDependencies(arg[[]DependencyRef](t, c, 0))
		case "detectFrameworks":
			got = DetectFrameworks(arg[[]DependencyRef](t, c, 0))
		case "isSecretFile":
			got = IsSecretFile(arg[string](t, c, 0))
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
		assertJSON(t, caseLabel(i, c), got, c.Out)
	}
}

func TestImportGraphVectors(t *testing.T) {
	for i, c := range loadCases(t, "importGraph.json") {
		var got any
		switch c.Fn {
		case "extractImports":
			got = ExtractImports(arg[string](t, c, 0), arg[string](t, c, 1))
		case "resolveImport":
			p, ok := ResolveImport(arg[string](t, c, 0), arg[string](t, c, 1), setOf(arg[[]string](t, c, 2)))
			if ok {
				got = p
			}
		case "folderOf":
			got = FolderOf(arg[string](t, c, 0), arg[int](t, c, 1))
		case "pickDepth":
			got = PickDepth(arg[[]ImportLink](t, c, 0), arg[int](t, c, 1))
		case "buildImportGraph":
			opts := arg[struct {
				Depth    int  `json:"depth"`
				MaxEdges *int `json:"maxEdges"`
			}](t, c, 1)
			got = BuildImportGraph(arg[[]SourceFile](t, c, 0), ImportGraphOpts{Depth: opts.Depth, MaxEdges: opts.MaxEdges})
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
		assertJSON(t, caseLabel(i, c), got, c.Out)
	}
}

func caseLabel(i int, c fnCase) string {
	args, _ := json.Marshal(c.Args)
	s := string(args)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return c.Fn + "#" + itoa(i) + " " + s
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
