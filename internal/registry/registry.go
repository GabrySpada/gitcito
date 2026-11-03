package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// FileName is the registry's file in the data directory.
const FileName = "gitcito-repos.json"

// RegistryRepo is RegistryRepo in src/shared/types.ts.
type RegistryRepo struct {
	// Path is the canonical absolute path — the identity, and the key used
	// everywhere else.
	Path string `json:"path"`
	// Name is the folder name at index time. `repoAliases` still wins for display.
	Name string `json:"name"`
	// Owner is the first path segment of the remote's namespace: 'top-solution'
	// for a GitLab group, the org for GitHub. Nil with no remote or an
	// unparseable URL.
	Owner *string `json:"owner"`
	// Branch as of the last index, read from .git/HEAD. Nil when detached.
	Branch *string `json:"branch"`
	// Source is how it got here — 'opened' | 'scanned': a scanned repo the user
	// has never opened still lists.
	Source string `json:"source"`
	// LastOpenedAt is Unix seconds. Drives the Recent section, which is
	// therefore not capped.
	LastOpenedAt float64 `json:"lastOpenedAt"`
	// Missing: the folder was absent at the last existence check. Stored rather
	// than computed: stat-ing 200 paths belongs on page load, never in a render.
	Missing bool `json:"missing"`
}

// RepoScanRoot is RepoScanRoot in src/shared/types.ts: a folder Gitcito scans
// for repositories, and how deep.
type RepoScanRoot struct {
	Path  string  `json:"path"`
	Depth float64 `json:"depth"`
}

// RepoScanResult is RepoScanResult in src/shared/types.ts. Added is counted
// where the merge happens, because a caller cannot reconstruct it: the
// registry it is handed replaces one it may never have loaded, so diffing
// lengths around the call reports everything.
type RepoScanResult struct {
	Repos []RegistryRepo `json:"repos"`
	Added int            `json:"added"`
}

type registryData struct {
	Repos []RegistryRepo `json:"repos"`
}

// Registry is the index file plus the lock that serializes every
// load-mutate-save cycle on it. `remember` is fired without awaiting on every
// tab open, so overlap with a long scan is normal, not exotic; without the
// lock two overlapping calls would be last-writer-wins.
type Registry struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

// New opens the registry stored at path (normally FileName in the data
// directory). now is the clock, nil for the real one.
func New(path string, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{path: path, now: now}
}

// Path is the registry file.
func (r *Registry) Path() string { return r.path }

// isPlausibleRepoPath is a sanity check on a path before it reaches the
// filesystem.
//
// Deliberately not shared.IsSafeRepoPath: that one guards paths a model
// produced, which are joined onto a repo root, so it rejects anything
// absolute. Registry paths are absolute by definition and come from the user's
// own folder picker or from scanning folders they configured — a different
// threat model, and a different check.
func isPlausibleRepoPath(path string) bool {
	p := gitx.TrimJS(path)
	n := len(utf16.Encode([]rune(p)))
	return n > 0 && n <= 4096 && filepath.IsAbs(p) && !bytes.ContainsRune([]byte(p), 0)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// load reads the registry; missing or corrupt starts fresh — a rescan
// rebuilds it.
func (r *Registry) load() []RegistryRepo {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return []RegistryRepo{}
	}
	var data registryData
	if err := json.Unmarshal(raw, &data); err != nil || data.Repos == nil {
		return []RegistryRepo{}
	}
	return data.Repos
}

func (r *Registry) save(repos []RegistryRepo) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o777); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(registryData{Repos: repos}); err != nil {
		return err
	}
	return os.WriteFile(r.path, bytes.TrimSuffix(buf.Bytes(), []byte("\n")), 0o666)
}

// List is the registry with `missing` refreshed. Stat-ing every path is why
// this is called on page open and not per render.
func (r *Registry) List() ([]RegistryRepo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repos := r.load()
	changed := false
	for i := range repos {
		missing := !exists(repos[i].Path)
		if missing != repos[i].Missing {
			repos[i].Missing = missing
			changed = true
		}
	}
	if changed {
		if err := r.save(repos); err != nil {
			return nil, err
		}
	}
	return repos, nil
}

// Remember records a repository as opened. It upserts: the same folder is one
// entry.
func (r *Registry) Remember(repoPath string) ([]RegistryRepo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !isPlausibleRepoPath(repoPath) {
		return r.load(), nil
	}
	// The path is the registry key, and the validation above accepted it
	// trimmed: storing it untrimmed would file two spellings of one folder as
	// two rows.
	path := gitx.TrimJS(repoPath)
	repos := r.load()
	now := float64(r.now().Unix())
	branch, owner := ReadHeadBranch(path), ReadOriginOwner(path)
	found := false
	for i := range repos {
		if repos[i].Path == path {
			repos[i].LastOpenedAt = now
			repos[i].Missing = !exists(path)
			repos[i].Source = "opened"
			repos[i].Branch = branch
			repos[i].Owner = owner
			found = true
			break
		}
	}
	if !found {
		repos = append(repos, RegistryRepo{
			Path: path, Name: filepath.Base(path), Owner: owner, Branch: branch,
			Source: "opened", LastOpenedAt: now, Missing: !exists(path),
		})
	}
	if err := r.save(repos); err != nil {
		return nil, err
	}
	return repos, nil
}

// Forget drops an entry from the index. It never touches the folder on disk.
func (r *Registry) Forget(repoPath string) ([]RegistryRepo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repos := []RegistryRepo{}
	for _, repo := range r.load() {
		if repo.Path != repoPath {
			repos = append(repos, repo)
		}
	}
	if err := r.save(repos); err != nil {
		return nil, err
	}
	return repos, nil
}

// Refresh re-reads the branch for the given paths. Cheap enough to call
// whenever the page opens: one file read each, no process spawned.
func (r *Registry) Refresh(paths []string) ([]RegistryRepo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repos := r.load()
	wanted := map[string]bool{}
	for _, p := range paths {
		wanted[p] = true
	}
	for i := range repos {
		if wanted[repos[i].Path] && !repos[i].Missing {
			repos[i].Branch = ReadHeadBranch(repos[i].Path)
		}
	}
	if err := r.save(repos); err != nil {
		return nil, err
	}
	return repos, nil
}

// Scan indexes every repository under the configured roots. A repository
// already in the registry keeps its `source` and `lastOpenedAt` — a scan adds
// knowledge, it never demotes a repo the user has actually opened.
func (r *Registry) Scan(ctx context.Context, roots []RepoScanRoot) (RepoScanResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	repos := r.load()
	byPath := map[string]int{}
	for i, repo := range repos {
		if _, dup := byPath[repo.Path]; !dup {
			byPath[repo.Path] = i
		}
	}
	added := 0
	for _, root := range roots {
		if !isPlausibleRepoPath(root.Path) {
			continue
		}
		for _, found := range ScanForRepos(ctx, root.Path, root.Depth) {
			if i, ok := byPath[found]; ok {
				repos[i].Missing = false
				continue
			}
			repos = append(repos, RegistryRepo{
				Path: found, Name: filepath.Base(found), Owner: ReadOriginOwner(found), Branch: ReadHeadBranch(found),
				Source: "scanned", LastOpenedAt: 0, Missing: false,
			})
			byPath[found] = len(repos) - 1
			added++
		}
	}
	if err := r.save(repos); err != nil {
		return RepoScanResult{}, err
	}
	return RepoScanResult{Repos: repos, Added: added}, nil
}

// Locate re-points a missing entry at the folder it moved to. Favourites and
// aliases are path-keyed and live in settings, so the renderer migrates those;
// this moves the index entry and refreshes what it caches.
func (r *Registry) Locate(oldPath, newPath string) ([]RegistryRepo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Re-pointing a repository at a folder that is not one would leave an entry
	// that can never be opened. Refuse rather than record it.
	if !isPlausibleRepoPath(newPath) || GitDirOf(newPath) == "" {
		return r.load(), nil
	}
	repos := r.load()
	i := -1
	for j := range repos {
		if repos[j].Path == oldPath {
			i = j
			break
		}
	}
	if i < 0 {
		return repos, nil
	}
	repos[i].Path = newPath
	repos[i].Name = filepath.Base(newPath)
	repos[i].Branch = ReadHeadBranch(newPath)
	repos[i].Owner = ReadOriginOwner(newPath)
	repos[i].Missing = !exists(newPath)
	if err := r.save(repos); err != nil {
		return nil, err
	}
	return repos, nil
}
