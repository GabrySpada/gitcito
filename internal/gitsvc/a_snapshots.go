package gitsvc

// Slice A — WIP snapshots. A safety net for uncommitted work: the whole
// working tree — modified, staged AND untracked files — is committed through a
// throwaway index (read-tree/add/write-tree/commit-tree, so neither the real
// index nor the stash list is touched) and pinned under refs/gitcito/wip/
// where gc cannot reach it. Taken on a timer, by hand, or as a guard right
// before a destructive operation (see aGuardSnapshot). `git stash create` was
// the old mechanism; it cannot capture untracked files, which is exactly what
// a `clean` destroys — old stash-shaped refs are still listed and restorable.
// Renderer channel: the `'git'` dispatcher, methods createSnapshot,
// listSnapshots, restoreSnapshot, deleteSnapshot.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

var aSnapshotSuffix = map[string]string{"auto": "-a", "manual": "-m", "guard": "-g"}

func aKindForSnapshotRef(ref string) string {
	switch {
	case strings.HasSuffix(ref, "-a"):
		return "auto"
	case strings.HasSuffix(ref, "-g"):
		return "guard"
	}
	return "manual"
}

type aSnapshotRef struct {
	ref, sha string
	time     int64
}

// aListSnapshotRefs lists snapshot refs, newest first. Cheap: one
// for-each-ref, no per-ref subprocesses. (for-each-ref does NOT interpret %xHH
// escapes — that is a git-log feature — and refname/sha/unixtime contain no
// spaces, so a plain space is a safe field separator here.)
func aListSnapshotRefs(ctx context.Context, repoPath string) []aSnapshotRef {
	raw, err := gitx.Raw(ctx, repoPath, []string{"for-each-ref", "--sort=-creatordate",
		"--format=%(refname) %(objectname) %(creatordate:unix)", "refs/gitcito/wip"}, gitx.RunOpts{})
	if err != nil {
		raw = ""
	}
	var out []aSnapshotRef
	for _, line := range strings.Split(raw, "\n") {
		if gitx.TrimJS(line) == "" {
			continue
		}
		f := strings.Split(line, " ")
		out = append(out, aSnapshotRef{ref: f[0], sha: aField(f, 1), time: aJSInt(aField(f, 2))})
	}
	return out
}

// aCommitFileCount is `(await gitService.commitFiles(repo, sha)).length`:
// commitFiles belongs to slice B; this is its parse, without the LRU.
func aCommitFileCount(ctx context.Context, repoPath, hash string) (int, error) {
	// -M: detect renames so moved files report R (with old→new paths) instead
	// of an unrelated A/D pair. diff-tree is plumbing and ignores diff.renames.
	out, err := gitx.Raw(ctx, repoPath, []string{"diff-tree", "--no-commit-id", "--name-status", "-M", "-r", "--root",
		"-m", "--first-parent", hash}, gitx.RunOpts{})
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	for _, line := range gitx.SplitLines(out) {
		f := strings.Split(line, "\t")
		path := f[len(f)-1]
		if len(f) < 2 || path == "" || seen[path] {
			continue
		}
		seen[path] = true
	}
	return len(seen), nil
}

// gitsvc: createSnapshot
//
// Take a snapshot of the current working tree. Returns nil when nothing
// changed. kind defaults to 'manual' and max to 50 when nil.
func (s *GitService) CreateSnapshot(ctx context.Context, repoPath string, kind *string, max *int) (*SnapshotInfo, error) {
	return locked(s, ctx, "createSnapshot", repoPath, func(ctx context.Context) (*SnapshotInfo, error) {
		k, n := "manual", 50
		if kind != nil {
			k = *kind
		}
		if max != nil {
			n = *max
		}
		return aCreateSnapshot(ctx, repoPath, k, n)
	})
}

func aCreateSnapshot(ctx context.Context, repoPath, kind string, max int) (*SnapshotInfo, error) {
	st, err := aSimpleStatus(ctx, repoPath)
	if err != nil || len(st.Files) == 0 {
		return nil, nil
	}
	run := func(args []string, env ...string) (string, error) {
		return gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{Env: env})
	}
	gitDirOut, err := run([]string{"rev-parse", "--absolute-git-dir"})
	if err != nil {
		return nil, err
	}
	tmpIndex := filepath.Join(gitx.TrimJS(gitDirOut),
		"gitcito-snap-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10))
	defer os.Remove(tmpIndex)
	idx := "GIT_INDEX_FILE=" + tmpIndex

	headOut, _ := run([]string{"rev-parse", "--verify", "HEAD"})
	head := gitx.TrimJS(headOut)
	// Seed from HEAD so unchanged entries reuse existing blobs, then stage
	// everything — `add -A` in the throwaway index is what pulls untracked
	// files in, the one thing `git stash create` cannot do.
	if _, err := run([]string{"read-tree", aOr(head, "--empty")}, idx); err != nil {
		return nil, err
	}
	if _, err := run([]string{"add", "-A"}, idx); err != nil {
		return nil, err
	}
	treeOut, err := run([]string{"write-tree"}, idx)
	if err != nil {
		return nil, err
	}
	tree := gitx.TrimJS(treeOut)

	// Nothing effectively changed (e.g. only ignored files were touched).
	headTree := ""
	if head != "" {
		out, err := run([]string{"rev-parse", head + "^{tree}"})
		if err != nil {
			return nil, err
		}
		headTree = gitx.TrimJS(out)
	}
	if tree == headTree {
		return nil, nil
	}
	// Timer/guard ticks dedupe against the latest snapshot; a manual
	// "snapshot now" always records, because the user asked for a receipt.
	if kind != "manual" {
		latestTree := ""
		if refs := aListSnapshotRefs(ctx, repoPath); len(refs) > 0 {
			out, _ := run([]string{"rev-parse", refs[0].sha + "^{tree}"})
			latestTree = gitx.TrimJS(out)
		}
		if tree == latestTree {
			return nil, nil
		}
	}

	ts := time.Now().Unix()
	label := "gitcito-wip " + time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05.000Z") + " (" + kind + ")"
	ctArgs := []string{"commit-tree", tree, "-m", label}
	if head != "" {
		ctArgs = append(ctArgs, "-p", head)
	}
	shaOut, err := run(ctArgs)
	if err != nil {
		return nil, err
	}
	sha := gitx.TrimJS(shaOut)
	// Millisecond ref names keep a guard and a timer tick within the same
	// second from colliding; displayed time comes from the commit, not the name.
	suffix, ok := aSnapshotSuffix[kind]
	if !ok {
		suffix = "undefined" // SNAPSHOT_SUFFIX[kind] for a kind it does not know
	}
	ref := "refs/gitcito/wip/" + strconv.FormatInt(time.Now().UnixMilli(), 10) + suffix
	if _, err := gitx.Raw(ctx, repoPath, []string{"update-ref", ref, sha}, gitx.RunOpts{}); err != nil {
		return nil, err
	}
	// Prune oldest beyond `max`.
	all := aListSnapshotRefs(ctx, repoPath)
	for _, old := range all[aSliceFrom(max, len(all)):] {
		_, _ = gitx.Raw(ctx, repoPath, []string{"update-ref", "-d", old.ref}, gitx.RunOpts{})
	}
	files, err := aCommitFileCount(ctx, repoPath, sha)
	if err != nil {
		return nil, err
	}
	return &SnapshotInfo{Ref: ref, Sha: sha, Time: ts, Files: files, Kind: kind}, nil
}

// aGuardSnapshot is a best-effort snapshot taken right before a destructive
// operation, so the state about to be destroyed stays recoverable. Never
// blocks the operation: every failure here is swallowed — the guard is a
// parachute, not a gate.
func aGuardSnapshot(ctx context.Context, repoPath string) {
	if !aSnapshotGuardOn() {
		return
	}
	_, _ = aCreateSnapshot(ctx, repoPath, "guard", 50)
}

// gitsvc: listSnapshots
//
// All saved snapshots, newest first.
func (s *GitService) ListSnapshots(ctx context.Context, repoPath string) ([]SnapshotInfo, error) {
	return locked(s, ctx, "listSnapshots", repoPath, func(ctx context.Context) ([]SnapshotInfo, error) {
		out := []SnapshotInfo{}
		for _, r := range aListSnapshotRefs(ctx, repoPath) {
			files, _ := aCommitFileCount(ctx, repoPath, r.sha)
			out = append(out, SnapshotInfo{Ref: r.ref, Sha: r.sha, Time: r.time, Files: files, Kind: aKindForSnapshotRef(r.ref)})
		}
		return out, nil
	})
}

// gitsvc: restoreSnapshot
//
// Copy files out of a snapshot back into the working tree (the snapshot is
// kept). With no `files`, the snapshot's entire tree is restored. Files
// created after the snapshot are left alone — restore overwrites and
// recreates, it never deletes.
func (s *GitService) RestoreSnapshot(ctx context.Context, repoPath, sha string, files []string) error {
	return lockedErr(s, ctx, "restoreSnapshot", repoPath, func(ctx context.Context) error {
		if len(files) == 0 {
			files = []string{"."}
		}
		_, err := gitx.Raw(ctx, repoPath, append([]string{"restore", "--source", sha, "--worktree", "--"}, files...), gitx.RunOpts{})
		return err
	})
}

// gitsvc: deleteSnapshot
func (s *GitService) DeleteSnapshot(ctx context.Context, repoPath, ref string) error {
	return lockedErr(s, ctx, "deleteSnapshot", repoPath, func(ctx context.Context) error {
		_, _ = gitx.Raw(ctx, repoPath, []string{"update-ref", "-d", ref}, gitx.RunOpts{})
		return nil
	})
}
