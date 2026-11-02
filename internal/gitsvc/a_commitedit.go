package gitsvc

// Slice A — commit editing: "edit any commit like a document". Rewrite a
// historical commit's files or message and replay everything above it —
// previewed in memory first, so the user sees the whole cascade (including
// conflicts) before a single ref moves. Merge commits in the range are
// replayed with their recorded conflict resolutions; the only requirement is
// ancestry to HEAD. Renderer channel: the `'git'` dispatcher, methods
// commitEditInfo, blobAtCommit, commitEditPreview, commitEditApply.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: commitEditInfo
//
// Can this commit be edited in place, and what would a rewrite drag along?
func (s *GitService) CommitEditInfo(ctx context.Context, repoPath, sha string) (CommitEditInfo, error) {
	return locked(s, ctx, "commitEditInfo", repoPath, func(ctx context.Context) (CommitEditInfo, error) {
		ancestor, merges := aCommitEditRange(ctx, repoPath, sha)
		descendants := 0
		if ancestor {
			out, err := gitx.RunGit(ctx, repoPath, []string{"rev-list", "--count", sha + "..HEAD"}, gitx.RunOpts{})
			if err != nil {
				return CommitEditInfo{}, err
			}
			descendants = int(aJSInt(out))
		}
		contains, err := gitx.RunGit(ctx, repoPath, []string{"branch", "-r", "--contains", sha}, gitx.RunOpts{})
		if err != nil {
			contains = ""
		}
		id, err := gitx.CommitIdentity(ctx, repoPath, sha)
		if err != nil {
			return CommitEditInfo{}, err
		}
		return CommitEditInfo{
			Linear: ancestor && merges == 0, Ancestor: ancestor, Merges: merges, Descendants: descendants,
			Pushed: gitx.TrimJS(contains) != "", Message: id.Message, AuthorName: id.Name, AuthorDate: id.Date,
		}, nil
	})
}

// gitsvc: blobAtCommit
//
// A file's content at a commit, gated for the editor: binary and oversized
// blobs come back with `content: null` rather than garbage.
func (s *GitService) BlobAtCommit(ctx context.Context, repoPath, sha, file string) (BlobAtCommit, error) {
	return locked(s, ctx, "blobAtCommit", repoPath, func(ctx context.Context) (BlobAtCommit, error) {
		out, err := gitx.RunGit(ctx, repoPath, []string{"cat-file", "-s", sha + ":" + file}, gitx.RunOpts{})
		if err != nil {
			return BlobAtCommit{}, err
		}
		size := aJSInt(out)
		if size > 2_000_000 {
			return BlobAtCommit{Size: size}, nil
		}
		buf, err := gitx.ReadBlob(ctx, repoPath, sha+":"+file, gitx.RunOpts{})
		if err != nil {
			return BlobAtCommit{}, err
		}
		head := buf
		if len(head) > 8192 {
			head = head[:8192]
		}
		binary := bytes.IndexByte(head, 0) >= 0
		res := BlobAtCommit{Binary: binary, Size: size}
		if !binary {
			res.Content = aStrPtr(string(buf))
		}
		return res, nil
	})
}

// gitsvc: commitEditPreview
//
// Dry-run the rewrite: same engine as apply, no ref ever moves.
func (s *GitService) CommitEditPreview(ctx context.Context, repoPath, sha string, edits map[string]string, message string) (CommitEditPreview, error) {
	return locked(s, ctx, "commitEditPreview", repoPath, func(ctx context.Context) (CommitEditPreview, error) {
		if ancestor, _ := aCommitEditRange(ctx, repoPath, sha); !ancestor {
			return CommitEditPreview{}, &aMessageError{"Commit is not an ancestor of HEAD."}
		}
		return aRewriteWithEdits(ctx, repoPath, sha, edits, message)
	})
}

// gitsvc: commitEditApply
//
// Rewrite for real: guard-snapshot the working tree, rebuild the chain, then
// `reset --keep` the current branch onto the new tip — index and worktree
// follow, local uncommitted changes are carried over (or the reset aborts
// and nothing has moved). Refuses detached HEAD and conflicted cascades.
func (s *GitService) CommitEditApply(ctx context.Context, repoPath, sha string, edits map[string]string, message string) (CommitEditResult, error) {
	return locked(s, ctx, "commitEditApply", repoPath, func(ctx context.Context) (CommitEditResult, error) {
		if ancestor, _ := aCommitEditRange(ctx, repoPath, sha); !ancestor {
			return CommitEditResult{}, &aMessageError{"Commit is not an ancestor of HEAD."}
		}
		if _, err := gitx.RunGit(ctx, repoPath, []string{"symbolic-ref", "-q", "HEAD"}, gitx.RunOpts{}); err != nil {
			return CommitEditResult{}, &aMessageError{"HEAD is detached — check out a branch first."}
		}
		aGuardSnapshot(ctx, repoPath)
		headOut, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", "HEAD"}, gitx.RunOpts{})
		if err != nil {
			return CommitEditResult{}, err
		}
		preview, err := aRewriteWithEdits(ctx, repoPath, sha, edits, message)
		if err != nil {
			return CommitEditResult{}, err
		}
		if preview.NewTip == nil {
			return CommitEditResult{}, &aMessageError{"The rewrite would conflict — see the preview."}
		}
		if _, err := gitx.RunGit(ctx, repoPath, []string{"reset", "--keep", *preview.NewTip}, gitx.RunOpts{}); err != nil {
			return CommitEditResult{}, err
		}
		return CommitEditResult{OldHead: gitx.TrimJS(headOut), NewTip: *preview.NewTip, Rewritten: len(preview.Steps)}, nil
	})
}

// aCommitEditRange is what stands between sha and HEAD. Editing needs only
// ancestry — merges in the range are replayed with their recorded
// resolutions, so they inform the UI rather than disable the feature.
func aCommitEditRange(ctx context.Context, repoPath, sha string) (ancestor bool, merges int) {
	if _, err := gitx.RunGit(ctx, repoPath, []string{"merge-base", "--is-ancestor", sha, "HEAD"}, gitx.RunOpts{}); err != nil {
		return false, 0
	}
	out, err := gitx.RunGit(ctx, repoPath, []string{"rev-list", "--merges", "--count", sha + "..HEAD"}, gitx.RunOpts{})
	if err != nil {
		out = "0"
	}
	n, _ := strconv.Atoi(aLeadingInt(gitx.TrimJS(out)))
	return true, n
}

// aLeadingInt is the digits parseInt(s, 10) would read.
func aLeadingInt(s string) string {
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return s[:end]
}

// aCherryPickTree cherry-picks commit onto onto entirely in memory:
// merge-tree with the commit's own parent as the explicit base, so only the
// commit's patch moves. Exit 1 (conflict) is a result, not an error.
func aCherryPickTree(ctx context.Context, repoPath, base, onto, commit string) aMergeTreeRecord {
	return aMergeTreeExec(ctx, repoPath, []string{"merge-tree", "--write-tree", "--name-only", "--messages",
		"--merge-base=" + base, onto, commit})
}

// aRewriteWithEdits is the engine under commit editing: build a replacement
// for sha with edited file contents and/or a new message, then replay every
// descendant up to HEAD on top of it — all with plumbing (temp index,
// hash-object, commit-tree, in-memory merge-tree). No ref moves, no worktree
// writes: the caller decides whether the resulting tip becomes real. Dangling
// objects from a discarded preview are ordinary gc food.
//
// Edits are applied in sorted path order (TS used the object's insertion
// order); the result is the same tree either way, and only which file an
// "Not an editable file" error names can differ when several are invalid.
func aRewriteWithEdits(ctx context.Context, repoPath, sha string, edits map[string]string, message string) (CommitEditPreview, error) {
	run := func(args []string, env ...string) (string, error) {
		return gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{Env: env})
	}
	gitDirOut, err := run([]string{"rev-parse", "--absolute-git-dir"})
	if err != nil {
		return CommitEditPreview{}, err
	}
	gitDir := gitx.TrimJS(gitDirOut)
	stamp := strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	tmpIndex := filepath.Join(gitDir, "gitcito-edit-index-"+stamp)
	tmpBlob := filepath.Join(gitDir, "gitcito-edit-blob-"+stamp)
	defer os.Remove(tmpIndex)
	defer os.Remove(tmpBlob)
	idx := "GIT_INDEX_FILE=" + tmpIndex

	// 1. The edited commit's replacement tree.
	if _, err := run([]string{"read-tree", sha + "^{tree}"}, idx); err != nil {
		return CommitEditPreview{}, err
	}
	files := make([]string, 0, len(edits))
	for f := range edits {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, file := range files {
		entry, err := run([]string{"ls-tree", sha, "--", file})
		if err != nil {
			return CommitEditPreview{}, err
		}
		mode := strings.Split(gitx.TrimJS(entry), " ")[0]
		if mode != "100644" && mode != "100755" {
			return CommitEditPreview{}, &aMessageError{"Not an editable file in this commit: " + file}
		}
		if err := aWriteFile(tmpBlob, edits[file]); err != nil {
			return CommitEditPreview{}, err
		}
		blobOut, err := run([]string{"hash-object", "-w", "--", tmpBlob})
		if err != nil {
			return CommitEditPreview{}, err
		}
		if _, err := run([]string{"update-index", "--add", "--cacheinfo", mode + "," + gitx.TrimJS(blobOut) + "," + file}, idx); err != nil {
			return CommitEditPreview{}, err
		}
	}
	treeOut, err := run([]string{"write-tree"}, idx)
	if err != nil {
		return CommitEditPreview{}, err
	}
	newTree := gitx.TrimJS(treeOut)

	// 2. Replacement commit, same parents and author, possibly a new message.
	parentLine, err := run([]string{"rev-list", "--parents", "-n", "1", sha})
	if err != nil {
		return CommitEditPreview{}, err
	}
	args := []string{"commit-tree", newTree}
	for _, p := range strings.Split(gitx.TrimJS(parentLine), " ")[1:] {
		args = append(args, "-p", p)
	}
	id, err := gitx.CommitIdentity(ctx, repoPath, sha)
	if err != nil {
		return CommitEditPreview{}, err
	}
	tipOut, err := run(append(args, "-m", aOr(message, id.Message)), gitx.AuthorEnv(id)...)
	if err != nil {
		return CommitEditPreview{}, err
	}
	tip := gitx.TrimJS(tipOut)

	// 3. Replay descendants oldest → newest, parent-aware. Every affected
	//    commit — one whose parents were rewritten — is re-created:
	//
	//    - An ordinary commit is an in-memory cherry-pick relative to its own
	//      first parent (merge-tree, no worktree).
	//    - A MERGE commit is replayed the same way: its recorded tree already
	//      contains both sides *and the conflict resolutions its author made*,
	//      so cherry-picking that result onto the rewritten first parent
	//      carries the resolutions verbatim — no rerere, no re-merge. All
	//      parent pointers are preserved, mapped through the rewrite.
	//
	//    Side-branch commits inside `sha..HEAD` that don't descend from the
	//    edited commit are untouched. The first conflict stops the cascade and
	//    marks the affected remainder.
	rangeOut, err := run([]string{"log", "--reverse", "--topo-order", "--format=%H%x00%P%x00%s", sha + "..HEAD"})
	if err != nil {
		return CommitEditPreview{}, err
	}
	steps := []CommitEditStep{}
	rewritten := map[string]string{sha: tip}
	// Commits that would have been rewritten but sit behind a conflict.
	stuck := map[string]bool{}
	conflicted := false
	for _, line := range gitx.SplitLines(rangeOut) {
		f := strings.Split(line, "\x00")
		cSha := f[0]
		var parents []string
		for _, p := range strings.Split(aField(f, 1), " ") {
			if p != "" {
				parents = append(parents, p)
			}
		}
		subject := ""
		if len(f) > 2 {
			subject = strings.Join(f[2:], "\x00")
		}
		touchesStuck, affected := false, false
		mapped := make([]string, len(parents))
		for i, p := range parents {
			if stuck[p] {
				touchesStuck = true
			}
			mapped[i] = p
			if r, ok := rewritten[p]; ok {
				mapped[i] = r
			}
			if mapped[i] != p {
				affected = true
			}
		}
		if !touchesStuck && !affected {
			continue // side-branch commit — keeps its identity
		}
		isMerge := len(parents) > 1
		if conflicted {
			stuck[cSha] = true
			steps = append(steps, CommitEditStep{Sha: cSha, Subject: subject, Status: "blocked", Files: []string{}, Merge: isMerge})
			continue
		}
		rec := aCherryPickTree(ctx, repoPath, parents[0], mapped[0], cSha)
		if rec.Status != "clean" {
			conflicted = true
			stuck[cSha] = true
			steps = append(steps, CommitEditStep{Sha: cSha, Subject: subject, Status: "conflict", Files: rec.Files, Merge: isMerge})
			continue
		}
		cid, err := gitx.CommitIdentity(ctx, repoPath, cSha)
		if err != nil {
			return CommitEditPreview{}, err
		}
		ctArgs := []string{"commit-tree", rec.Tree}
		for _, p := range mapped {
			ctArgs = append(ctArgs, "-p", p)
		}
		nextOut, err := run(append(ctArgs, "-m", cid.Message), gitx.AuthorEnv(cid)...)
		if err != nil {
			return CommitEditPreview{}, err
		}
		rewritten[cSha] = gitx.TrimJS(nextOut)
		steps = append(steps, CommitEditStep{Sha: cSha, Subject: subject, Status: "clean", Files: []string{}, Merge: isMerge})
	}
	headOut, err := run([]string{"rev-parse", "HEAD"})
	if err != nil {
		return CommitEditPreview{}, err
	}
	var newTip *string
	if !conflicted {
		if t, ok := rewritten[gitx.TrimJS(headOut)]; ok {
			newTip = &t
		}
	}
	return CommitEditPreview{NewTip: newTip, Steps: steps}, nil
}
