package gitsvc

// Diffs: working-tree, staged, commit and stash diffs, the file lists behind
// them, image sides and the two texts a semantic diff compares.
//
// Renderer channels served (through the 'git' dispatcher): diffFile,
// stagedDiff, commitFiles, commitFileDiff, commitDiff, stashFiles,
// stashFileDiff, semanticDiff, imageDiff.

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// A commit's file list and diff never change for a given sha, so re-opening
// the same commit (or paging its files) is served from memory instead of
// re-shelling git. Rewritten shas (amend/rebase) simply orphan their old
// entries, which age out. Budgets are the TS ones, measured in JS bytes.
var (
	bCommitDiffCache  = gitx.NewLRU[string](500, 48*1024*1024, gitx.StrBytes)
	bCommitFilesCache = gitx.NewLRU[[]FileEntry](500, 16*1024*1024, func(files []FileEntry) int {
		n := 0
		for _, f := range files {
			n += gitx.StrBytes(f.Path) + 64
		}
		return n
	})
)

// gitsvc: diffFile
func (s *GitService) DiffFile(ctx context.Context, repoPath, file string, staged, untracked, ignoreWs bool) (string, error) {
	return locked(s, ctx, "diffFile", repoPath, func(ctx context.Context) (string, error) {
		if untracked {
			content, err := bReadText(repoPath + "/" + file)
			if err != nil {
				return "", nil
			}
			lines := strings.Split(content, "\n")
			out := []string{
				"diff --git a/" + file + " b/" + file,
				// Git's own spelling: the renderer recognises it as a header. Bare
				// "new file" parsed as a context line numbered 0.
				"new file mode 100644",
				"--- /dev/null",
				"+++ b/" + file,
				"@@ -0,0 +1," + strconv.Itoa(len(lines)) + " @@",
			}
			for _, l := range lines {
				out = append(out, "+"+l)
			}
			return strings.Join(out, "\n"), nil
		}
		args := []string{"diff"}
		if staged {
			args = append(args, "--cached")
		}
		if ignoreWs {
			args = append(args, "-w")
		}
		return bRaw(ctx, repoPath, append(args, "--", file)...)
	})
}

// gitsvc: stagedDiff
func (s *GitService) StagedDiff(ctx context.Context, repoPath string) (string, error) {
	return locked(s, ctx, "stagedDiff", repoPath, func(ctx context.Context) (string, error) {
		return bRaw(ctx, repoPath, "diff", "--cached")
	})
}

// bCommitFiles is commitFiles without the lock, for callers already holding
// it (snapshots count a snapshot's files with it).
func bCommitFiles(ctx context.Context, repoPath, hash string) ([]FileEntry, error) {
	return gitx.Memo(bCommitFilesCache, repoPath+"\x00"+hash, func() ([]FileEntry, error) {
		// -M: detect renames so moved files report R (with old→new paths) instead
		// of an unrelated A/D pair. diff-tree is plumbing and ignores diff.renames.
		out, err := bRaw(ctx, repoPath, "diff-tree", "--no-commit-id", "--name-status", "-M", "-r", "--root", "-m", "--first-parent", hash)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		files := []FileEntry{}
		for _, line := range gitx.SplitLines(out) {
			code, path := bNameStatus(line)
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, FileEntry{Path: path, Status: gitx.MapStatusCode(code)})
		}
		return files, nil
	})
}

// bNameStatus splits a `--name-status` row into its status letter and the
// last path on it (the new path, for a rename).
func bNameStatus(line string) (code, path string) {
	parts := strings.Split(line, "\t")
	if len(parts) > 1 {
		path = parts[len(parts)-1]
	}
	if parts[0] != "" {
		code = parts[0][:1]
	}
	return code, path
}

// gitsvc: commitFiles
func (s *GitService) CommitFiles(ctx context.Context, repoPath, hash string) ([]FileEntry, error) {
	return locked(s, ctx, "commitFiles", repoPath, func(ctx context.Context) ([]FileEntry, error) {
		files, err := bCommitFiles(ctx, repoPath, hash)
		if err != nil {
			return nil, err
		}
		// The cache hands out one slice to every caller: copy it, so nobody
		// can edit a cached answer.
		return append([]FileEntry{}, files...), nil
	})
}

// gitsvc: commitFileDiff
func (s *GitService) CommitFileDiff(ctx context.Context, repoPath, hash, file string, ignoreWs bool) (string, error) {
	return locked(s, ctx, "commitFileDiff", repoPath, func(ctx context.Context) (string, error) {
		ws := "0"
		if ignoreWs {
			ws = "1"
		}
		// `--first-parent` so merge commits diff against their first parent
		// (matching commitFiles). Without it, `git show` falls back to a
		// combined diff (--cc) that's empty for files which only changed on
		// the merged-in branch — the "No changes to display" bug.
		return gitx.Memo(bCommitDiffCache, "f\x00"+repoPath+"\x00"+hash+"\x00"+ws+"\x00"+file, func() (string, error) {
			args := []string{"show", "--format=", "--first-parent"}
			if ignoreWs {
				args = append(args, "-w")
			}
			return bRaw(ctx, repoPath, append(args, hash, "--", file)...)
		})
	})
}

// gitsvc: commitDiff
//
// Full patch of a single commit (vs its first parent; a root commit shows the
// full tree).
func (s *GitService) CommitDiff(ctx context.Context, repoPath, hash string) (string, error) {
	return locked(s, ctx, "commitDiff", repoPath, func(ctx context.Context) (string, error) {
		return gitx.Memo(bCommitDiffCache, "d\x00"+repoPath+"\x00"+hash, func() (string, error) {
			return bRaw(ctx, repoPath, "show", "--format=", "--first-parent", hash)
		})
	})
}

// gitsvc: stashFiles
func (s *GitService) StashFiles(ctx context.Context, repoPath, sha, untrackedSha string) ([]FileEntry, error) {
	return locked(s, ctx, "stashFiles", repoPath, func(ctx context.Context) ([]FileEntry, error) {
		out, err := bRaw(ctx, repoPath, "diff", "--name-status", "-M", sha+"^1", sha)
		if err != nil {
			return nil, err
		}
		files := []FileEntry{}
		for _, line := range gitx.SplitLines(out) {
			code, path := bNameStatus(line)
			if path != "" {
				files = append(files, FileEntry{Path: path, Status: gitx.MapStatusCode(code)})
			}
		}
		if untrackedSha != "" {
			// An unavailable untracked tree just contributes nothing.
			if u, err := bRaw(ctx, repoPath, "ls-tree", "-r", "--name-only", untrackedSha); err == nil {
				for _, path := range gitx.SplitLines(u) {
					files = append(files, FileEntry{Path: path, Status: "?", Untracked: true})
				}
			}
		}
		return files, nil
	})
}

// gitsvc: stashFileDiff
func (s *GitService) StashFileDiff(ctx context.Context, repoPath, sha, file string, untracked, ignoreWs bool) (string, error) {
	return locked(s, ctx, "stashFileDiff", repoPath, func(ctx context.Context) (string, error) {
		var ws []string
		if ignoreWs {
			ws = []string{"-w"}
		}
		if untracked {
			args := append(append([]string{"diff-tree", "--root", "--no-commit-id", "-p"}, ws...), sha+"^3", "--", file)
			return bRaw(ctx, repoPath, args...)
		}
		args := append(append([]string{"diff"}, ws...), sha+"^1", sha, "--", file)
		return bRaw(ctx, repoPath, args...)
	})
}

// BlobSpec is BlobSpec in src/shared/types.ts: which version of a file to
// read — `{kind:'ref', ref}`, `{kind:'index'}`, `{kind:'worktree'}` or
// `{kind:'empty'}`.
type BlobSpec struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

// SemanticDiffSides is what semanticDiff returns now that the semantic
// comparison runs in the renderer (brief §5.3): the two versions of the file,
// read exactly as TS read them. The renderer then does what the rest of the TS
// method did:
//
//	if (!oldText && !newText) return { language: null, changes: [] }
//	return semanticCompare(file, oldText, newText)
type SemanticDiffSides struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// gitsvc: semanticDiff
//
// Read-only: both sides come out of the object database (or the working tree
// copy). A path that doesn't exist on a side (added/deleted file) reads as
// empty.
func (s *GitService) SemanticDiff(ctx context.Context, repoPath, file string, oldSide, newSide BlobSpec) (SemanticDiffSides, error) {
	return locked(s, ctx, "semanticDiff", repoPath, func(ctx context.Context) (SemanticDiffSides, error) {
		read := func(spec BlobSpec) string {
			switch spec.Kind {
			case "empty":
				return ""
			case "worktree":
				text, err := bReadText(filepath.Join(repoPath, file))
				if err != nil {
					return ""
				}
				return text
			}
			// `:file` is the staged copy; `<ref>:file` any committed one.
			rev := spec.Ref + ":" + file
			if spec.Kind == "index" {
				rev = ":" + file
			}
			return bRawOr(ctx, repoPath, "", "show", rev)
		}
		return SemanticDiffSides{OldText: read(oldSide), NewText: read(newSide)}, nil
	})
}

// ImageDiffSides is imageDiff's result: each side as a data URL, null when the
// file does not exist on that side.
type ImageDiffSides struct {
	Before *string `json:"before"`
	After  *string `json:"after"`
}

// gitsvc: imageDiff
//
// beforeRef null means "no before side" (an added file); "" would read the
// working tree, as in TS. afterRef "" (undefined) reads the working tree.
func (s *GitService) ImageDiff(ctx context.Context, repoPath, file string, beforeRef *string, afterRef string) (ImageDiffSides, error) {
	return locked(s, ctx, "imageDiff", repoPath, func(ctx context.Context) (ImageDiffSides, error) {
		var out ImageDiffSides
		if beforeRef != nil {
			if url, ok := gitx.ReadFileDataURL(ctx, repoPath, file, *beforeRef); ok {
				out.Before = &url
			}
		}
		if url, ok := gitx.ReadFileDataURL(ctx, repoPath, file, afterRef); ok {
			out.After = &url
		}
		return out, nil
	})
}
