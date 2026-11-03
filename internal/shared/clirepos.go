package shared

// Which repositories `gitcito repos` and `gitcito open <name>` can see.
// Mirrors src/shared/cliRepos.ts.
//
// The headless CLI cannot ask the app — it may not even be running — so it
// reads the settings file the app writes. That makes this a *reader of someone
// else's format*: every field is optional and every shape is checked, because
// a settings file written by a newer build must degrade to a shorter list
// rather than to a crash in a shell pipeline.
//
// Read-only by design. The running app owns that file and would overwrite
// anything the CLI wrote underneath it on its next save.

import (
	"regexp"
	"strings"
)

// KnownRepo mirrors KnownRepo.
type KnownRepo struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Group is the group tab it lives in, when it lives in one.
	Group string `json:"group,omitempty"`
}

var pathSepRe = regexp.MustCompile(`[/\\]`)

func repoBasename(path string) string {
	parts := pathSepRe.Split(path, -1)
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return path
}

// truthyString returns v when it is a non-empty string — JS `x || fallback`
// for the string fields this reader cares about.
func truthyString(v any) string {
	s, _ := v.(string)
	return s
}

// KnownRepos lists every repository with a tab, then anything else in the
// recents list. Tabs come first so a repo that is currently open keeps its
// tab's display name. settings is the decoded settings JSON (any shape).
func KnownRepos(settings any) []KnownRepo {
	out := []KnownRepo{}
	seen := map[string]bool{}
	s, _ := settings.(map[string]any)
	tabs, _ := s["tabs"].([]any)
	for _, rawTab := range tabs {
		tab, _ := rawTab.(map[string]any)
		repos, _ := tab["repos"].([]any)
		for _, rawRepo := range repos {
			repo, _ := rawRepo.(map[string]any)
			path, ok := repo["path"].(string)
			// First tab wins: tab order is stable, so a repo open both standalone
			// and inside a group keeps one identity between runs.
			if !ok || path == "" || seen[path] {
				continue
			}
			seen[path] = true
			k := KnownRepo{Path: path, Name: truthyString(repo["name"])}
			if k.Name == "" {
				k.Name = repoBasename(path)
			}
			if kind, _ := tab["kind"].(string); kind == "group" {
				k.Group = truthyString(tab["name"])
			}
			out = append(out, k)
		}
	}
	recents, _ := s["recentRepos"].([]any)
	for _, rawRepo := range recents {
		repo, _ := rawRepo.(map[string]any)
		path, ok := repo["path"].(string)
		if !ok || path == "" || seen[path] {
			continue
		}
		seen[path] = true
		k := KnownRepo{Path: path, Name: truthyString(repo["name"])}
		if k.Name == "" {
			k.Name = repoBasename(path)
		}
		out = append(out, k)
	}
	return out
}

// MatchesRepo is a case-insensitive substring match on either the display
// name or the path.
func MatchesRepo(repo KnownRepo, needle string) bool {
	n := strings.ToLower(needle)
	return strings.Contains(strings.ToLower(repo.Name), n) || strings.Contains(strings.ToLower(repo.Path), n)
}

// ResolveRepo is what `gitcito open <name>` resolves to: an exact name wins,
// otherwise the first substring match, so the result does not depend on how
// much of the name was typed. Nil when nothing matches.
func ResolveRepo(repos []KnownRepo, needle string) *KnownRepo {
	n := strings.ToLower(needle)
	for i := range repos {
		if strings.ToLower(repos[i].Name) == n {
			return &repos[i]
		}
	}
	for i := range repos {
		if MatchesRepo(repos[i], n) {
			return &repos[i]
		}
	}
	return nil
}
