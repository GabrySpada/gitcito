package gitsvc

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/paths"
)

// Per-repo state git.ts kept in module-level Maps, for the methods of slice A.
// Package-level, each behind its own mutex; listed for the lead to hoist onto
// GitService if other slices need them (treeStatus shares the status scan).

// ─── mergedIntoHead (branches) ──────────────────────────────────────────────

// `for-each-ref --merged=HEAD` has to prove ancestry for every ref it is given,
// so on a repo carrying thousands of never-deleted remote branches it costs more
// than the rest of branches() put together. Its answer depends on exactly two
// things: where HEAD is, and what every ref points at — both of which we already
// read, so the ref listing itself is the cache key. Staging a file, stashing,
// writing a note: none of them move a ref, and all of them now skip the walk.
var aMergedCache = struct {
	sync.Mutex
	m map[string]aMergedEntry
}{m: map[string]aMergedEntry{}}

type aMergedEntry struct {
	key  string
	refs map[string]bool
}

func aMergedIntoHead(ctx context.Context, repoPath, headSha, refListing string) map[string]bool {
	// No HEAD (unborn branch) means nothing can be merged into it.
	if headSha == "" {
		return map[string]bool{}
	}
	sum := sha1.Sum([]byte(refListing))
	key := headSha + "\n" + hex.EncodeToString(sum[:])
	aMergedCache.Lock()
	hit, ok := aMergedCache.m[repoPath]
	aMergedCache.Unlock()
	if ok && hit.key == key {
		return hit.refs
	}
	refs := map[string]bool{}
	out, err := gitx.RunGit(ctx, repoPath,
		[]string{"for-each-ref", "--merged=HEAD", "--format=%(refname)", "refs/heads", "refs/remotes"},
		gitx.RunOpts{MaxBuffer: 16 * 1024 * 1024})
	if err != nil {
		// An empty repo, or a HEAD git cannot resolve. Cache nothing rather than
		// caching an empty answer that a later valid read would have to undo.
		return refs
	}
	for _, ref := range gitx.SplitLines(out) {
		refs[ref] = true
	}
	aMergedCache.Lock()
	aMergedCache.m[repoPath] = aMergedEntry{key: key, refs: refs}
	aMergedCache.Unlock()
	return refs
}

// ─── statusScan (status) ────────────────────────────────────────────────────

// The one walk of the working tree a refresh needs.
//
// `status()` (the commit panel's file list) and `treeStatus()` (the file tree's
// per-path badges) used to spawn a `git status` each, and on a large working
// tree that walk is the most expensive thing a refresh does — paying for it
// twice was the single biggest waste left. The two commands differed only by
// `--ignored=matching`, which adds the `!!` lines and leaves everything else
// byte-identical, so one call now serves both.
//
// The map is in-flight coalescing, not a cache: a refresh issues both reads
// at once, so they share one process, and the entry is dropped the moment it
// settles. Nothing is ever served from a previous refresh — and as a side
// effect the two halves can no longer disagree, which two independent
// snapshots could.
var aStatusInFlight = struct {
	sync.Mutex
	m map[string]*aStatusCall
}{m: map[string]*aStatusCall{}}

type aStatusCall struct {
	done chan struct{}
	st   aStatusSummary
	err  error
}

func aStatusScan(ctx context.Context, repoPath string) (aStatusSummary, error) {
	aStatusInFlight.Lock()
	if c, ok := aStatusInFlight.m[repoPath]; ok {
		aStatusInFlight.Unlock()
		select {
		case <-c.done:
			return c.st, c.err
		case <-ctx.Done():
			return aStatusSummary{}, ctx.Err()
		}
	}
	c := &aStatusCall{done: make(chan struct{})}
	aStatusInFlight.m[repoPath] = c
	aStatusInFlight.Unlock()

	// Detached from the caller's cancellation: another caller may be sharing
	// this scan.
	c.st, c.err = aSimpleStatus(context.WithoutCancel(ctx), repoPath, "--ignored=matching")
	aStatusInFlight.Lock()
	delete(aStatusInFlight.m, repoPath)
	aStatusInFlight.Unlock()
	close(c.done)
	return c.st, c.err
}

// ─── absoluteGitDir (mergeState) ────────────────────────────────────────────

// The per-worktree git-dir (where MERGE_HEAD, rebase-merge, etc. live) never
// moves for the lifetime of a repo tab, so resolve it with `rev-parse` once
// and reuse it — `mergeState()` is polled on every refresh (watcher, 20s
// idle tick, focus), and spawning a subprocess per candidate path there adds
// up on a repo a user keeps open all day.
var aGitDirCache = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func aAbsoluteGitDir(ctx context.Context, repoPath string) (string, error) {
	aGitDirCache.Lock()
	cached := aGitDirCache.m[repoPath]
	aGitDirCache.Unlock()
	if cached != "" {
		return cached, nil
	}
	out, err := gitx.Raw(ctx, repoPath, []string{"rev-parse", "--absolute-git-dir"}, gitx.RunOpts{})
	if err != nil {
		return "", err
	}
	dir := gitx.TrimJS(out)
	aGitDirCache.Lock()
	aGitDirCache.m[repoPath] = dir
	aGitDirCache.Unlock()
	return dir, nil
}

// ─── settings (guardSnapshot) ───────────────────────────────────────────────

// aSnapshotGuardOn reads `snapshotGuard` from the settings file the way
// readSettings() did: a missing or unreadable file means defaults (on), and
// only an explicit `false` turns the guard off. It reads the file directly
// until internal/settings exists; the lead may swap this for that package.
func aSnapshotGuardOn() bool {
	// The settings store, once main has wired it (SetSnapshotGuardSetting), is
	// the one source of truth for every slice's guard; the file read below is
	// the fallback for tests and tools that run the service without it.
	dGuardSetting.Lock()
	fn := dGuardSetting.enabled
	dGuardSetting.Unlock()
	if fn != nil {
		return fn()
	}
	file, err := paths.File("gitcito-settings.json")
	if err != nil {
		return true
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return true
	}
	var s struct {
		SnapshotGuard *bool `json:"snapshotGuard"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return true
	}
	return s.SnapshotGuard == nil || *s.SnapshotGuard
}
