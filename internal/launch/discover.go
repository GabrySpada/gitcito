package launch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// ─── Discovery ──────────────────────────────────────────────────────────────

var skipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"build":        true,
	"out":          true,
	".next":        true,
	"vendor":       true,
	"coverage":     true,
	".cache":       true,
	"target":       true,
	".venv":        true,
}

const maxDepth = 4

// findVscodeDirs recursively collects directories that contain a
// `.vscode/launch.json`.
func findVscodeDirs(dir string, depth int, acc *[]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() == ".vscode" {
			if _, err := os.ReadFile(filepath.Join(dir, ".vscode", "launch.json")); err == nil {
				*acc = append(*acc, dir)
			}
			break
		}
	}
	if depth >= maxDepth {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || skipDirs[e.Name()] {
			continue
		}
		findVscodeDirs(filepath.Join(dir, e.Name()), depth+1, acc)
	}
}

func readText(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.ToValidUTF8(string(b), "�"), true
}

func readGroup(repoRoot, dir string) *LaunchGroup {
	launchRaw, ok := readText(filepath.Join(dir, ".vscode", "launch.json"))
	if !ok {
		return nil
	}
	parsed, _ := parseJsonc(launchRaw)
	launch, _ := obj(parsed)

	configs := []LaunchConfig{}
	if arr, ok := list(launch["configurations"]); ok {
		for _, c := range arr {
			if m, ok := obj(c); ok {
				if _, isStr := str(m, "name"); isStr {
					configs = append(configs, m)
				}
			}
		}
	}
	// Compounds run several configs together — surface them as synthetic configs
	// tagged with their member names so the picker lists them like VS Code does.
	if arr, ok := list(launch["compounds"]); ok {
		for _, c := range arr {
			m, ok := obj(c)
			if !ok {
				continue
			}
			name, isStr := str(m, "name")
			members, isArr := list(m["configurations"])
			if !isStr || !isArr {
				continue
			}
			syn := LaunchConfig{"name": name, "type": "compound", "compound": members}
			if b, _ := m["stopAll"].(bool); b {
				syn["compoundStopAll"] = true
			}
			if truthy(m["presentation"]) {
				syn["presentation"] = m["presentation"]
			}
			configs = append(configs, syn)
		}
	}
	if len(configs) == 0 {
		return nil
	}

	tasks := []LaunchTask{}
	if tasksRaw, ok := readText(filepath.Join(dir, ".vscode", "tasks.json")); ok && tasksRaw != "" {
		tp, _ := parseJsonc(tasksRaw)
		tm, _ := obj(tp)
		// TS threw on a non-array `tasks` (and took the whole discovery down);
		// here it is just "no tasks".
		if arr, ok := list(tm["tasks"]); ok {
			for _, t := range arr {
				if m, ok := obj(t); ok {
					if _, isStr := str(m, "label"); isStr {
						tasks = append(tasks, m)
					}
				}
			}
		}
	}

	// package.json scripts travel with the group so the renderer can tell what
	// `npm run dev` really starts (see lib/launchActions.ts).
	scripts := map[string]string{}
	if pkgRaw, ok := readText(filepath.Join(dir, "package.json")); ok && pkgRaw != "" {
		pp, _ := parseJsonc(pkgRaw)
		if pm, ok := obj(pp); ok {
			switch s := pm["scripts"].(type) {
			case map[string]any:
				for k, v := range s {
					if vs, ok := v.(string); ok {
						scripts[k] = vs
					}
				}
			case []any:
				// Object.entries of an array: index keys.
				for i, v := range s {
					if vs, ok := v.(string); ok {
						scripts[jsString(float64(i))] = vs
					}
				}
			}
		}
	}

	isRoot := dir == repoRoot
	rel, err := filepath.Rel(repoRoot, dir)
	if err != nil || rel == "." {
		rel = "."
	}
	inputs := []LaunchInput{}
	if arr, ok := list(launch["inputs"]); ok {
		for _, i := range arr {
			if m, ok := obj(i); ok {
				if _, isStr := str(m, "id"); isStr {
					inputs = append(inputs, m)
				}
			}
		}
	}
	label := "Workspace"
	if !isRoot {
		label = filepath.ToSlash(rel)
	}
	return &LaunchGroup{
		ID:      dir,
		Dir:     dir,
		Label:   label,
		IsRoot:  isRoot,
		Configs: configs,
		Tasks:   tasks,
		Inputs:  inputs,
		Scripts: scripts,
	}
}

// DiscoverLaunch lists every `.vscode/launch.json` group under repoPath: the
// root group first, deeper groups sorted by path so the divider reads well.
func DiscoverLaunch(repoPath string) []LaunchGroup {
	var dirs []string
	findVscodeDirs(repoPath, 0, &dirs)
	groups := []LaunchGroup{}
	for _, d := range dirs {
		if g := readGroup(repoPath, d); g != nil {
			groups = append(groups, *g)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.IsRoot != b.IsRoot {
			return a.IsRoot
		}
		return shared.LocaleCompare(a.Dir, b.Dir) < 0
	})
	return groups
}
