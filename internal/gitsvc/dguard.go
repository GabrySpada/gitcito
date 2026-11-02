package gitsvc

// The pre-destructive "guard" snapshot (guardSnapshot in git.ts), used by
// clean and discard. Slice A owns the snapshot methods (createSnapshot,
// listSnapshots…); this is the guard path of createSnapshot only, kept here so
// slice D does not depend on another slice's unexported code. The lead hoists
// the two into one.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

var dGuardSetting = struct {
	sync.Mutex
	enabled func() bool
}{}

// SetSnapshotGuardSetting wires the user's `snapshotGuard` setting (default
// on): fn is consulted before every destructive operation. Until the settings
// service is ported, the guard is always on. A package function, not a
// GitService method, so it is never bound to the page.
func SetSnapshotGuardSetting(fn func() bool) {
	dGuardSetting.Lock()
	dGuardSetting.enabled = fn
	dGuardSetting.Unlock()
}

func dSnapshotGuardEnabled() bool {
	dGuardSetting.Lock()
	fn := dGuardSetting.enabled
	dGuardSetting.Unlock()
	return fn == nil || fn()
}

// dSnapshotMax is how many snapshots a repository keeps; older ones are pruned.
const dSnapshotMax = 50

type dSnapshotRef struct {
	ref, sha string
}

// dListSnapshotRefs lists snapshot refs, newest first. NB: for-each-ref does
// NOT interpret %xHH escapes, but refname/sha/unixtime hold no spaces, so a
// plain space separates them.
func dListSnapshotRefs(ctx context.Context, repoPath string) []dSnapshotRef {
	raw, err := gitx.Raw(ctx, repoPath, []string{"for-each-ref", "--sort=-creatordate",
		"--format=%(refname) %(objectname) %(creatordate:unix)", "refs/gitcito/wip"}, gitx.RunOpts{})
	if err != nil {
		return nil
	}
	var out []dSnapshotRef
	for _, line := range dLines(raw) {
		if gitx.TrimJS(line) == "" {
			continue
		}
		f := strings.Split(line, " ")
		r := dSnapshotRef{ref: f[0]}
		if len(f) > 1 {
			r.sha = f[1]
		}
		out = append(out, r)
	}
	return out
}

// dGuardSnapshot is a best-effort snapshot taken right before a destructive
// operation, so the state about to be destroyed stays recoverable. It never
// blocks the operation: every failure here is swallowed — the guard is a
// parachute, not a gate.
func dGuardSnapshot(ctx context.Context, repoPath string) {
	if !dSnapshotGuardEnabled() {
		return
	}
	_ = dTakeGuardSnapshot(ctx, repoPath)
}

// dTakeGuardSnapshot is createSnapshot(repoPath, 'guard'): the working tree,
// untracked files included, written as a commit under refs/gitcito/wip/ through
// a throwaway index — `git stash create` cannot capture untracked files, which
// is exactly what a `clean` destroys.
func dTakeGuardSnapshot(ctx context.Context, repoPath string) error {
	dirty, err := gitx.IsDirty(ctx, repoPath)
	if err != nil || !dirty {
		return err
	}
	gitDir, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", "--absolute-git-dir"}, gitx.RunOpts{})
	if err != nil {
		return err
	}
	tmpIndex := filepath.Join(gitx.TrimJS(gitDir), "gitcito-snap-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10))
	defer os.Remove(tmpIndex)
	env := gitx.RunOpts{Env: []string{"GIT_INDEX_FILE=" + tmpIndex}}

	head := dGitTrim(ctx, repoPath, "rev-parse", "--verify", "HEAD")
	// Seed from HEAD so unchanged entries reuse existing blobs, then stage
	// everything — `add -A` in the throwaway index is what pulls untracked
	// files in.
	seed := head
	if seed == "" {
		seed = "--empty"
	}
	if _, err := gitx.RunGit(ctx, repoPath, []string{"read-tree", seed}, env); err != nil {
		return err
	}
	if _, err := gitx.RunGit(ctx, repoPath, []string{"add", "-A"}, env); err != nil {
		return err
	}
	treeOut, err := gitx.RunGit(ctx, repoPath, []string{"write-tree"}, env)
	if err != nil {
		return err
	}
	tree := gitx.TrimJS(treeOut)

	// Nothing effectively changed (e.g. only ignored files were touched).
	if head != "" {
		headTree, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", head + "^{tree}"}, gitx.RunOpts{})
		if err != nil {
			return err
		}
		if tree == gitx.TrimJS(headTree) {
			return nil
		}
	} else if tree == "" {
		return nil
	}
	// Guard ticks dedupe against the latest snapshot.
	if refs := dListSnapshotRefs(ctx, repoPath); len(refs) > 0 {
		if tree == dGitTrim(ctx, repoPath, "rev-parse", refs[0].sha+"^{tree}") {
			return nil
		}
	}

	ts := time.Now().Unix()
	label := "gitcito-wip " + time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05.000Z") + " (guard)"
	args := []string{"commit-tree", tree, "-m", label}
	if head != "" {
		args = append(args, "-p", head)
	}
	shaOut, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
	if err != nil {
		return err
	}
	// Millisecond ref names keep a guard and a timer tick within the same
	// second from colliding; displayed time comes from the commit, not the name.
	ref := "refs/gitcito/wip/" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "-g"
	if _, err := gitx.Raw(ctx, repoPath, []string{"update-ref", ref, gitx.TrimJS(shaOut)}, gitx.RunOpts{}); err != nil {
		return err
	}
	all := dListSnapshotRefs(ctx, repoPath)
	for i := dSnapshotMax; i < len(all); i++ {
		_, _ = gitx.Raw(ctx, repoPath, []string{"update-ref", "-d", all[i].ref}, gitx.RunOpts{})
	}
	return nil
}
