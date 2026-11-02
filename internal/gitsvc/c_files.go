package gitsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Patches, tracked/ignored file listings, file sizes and the file tree's
// status badges.

// FormatPatch is a mailbox-style patch (format-patch) for count commits
// ending at ref; a nil count means 1.
// gitsvc: formatPatch
func (s *GitService) FormatPatch(ctx context.Context, repoPath, ref string, count *int) (string, error) {
	return locked(s, ctx, "formatPatch", repoPath, func(ctx context.Context) (string, error) {
		n := 1
		if count != nil {
			n = *count
		}
		return cRaw(ctx, repoPath, "format-patch", "-"+strconv.Itoa(n), ref, "--stdout")
	})
}

// ApplyPatch applies a patch. With am, `git am` applies AND commits,
// preserving the author/message of a format-patch mailbox; otherwise
// `git apply` patches the working tree without committing. Both use a 3-way
// merge for better fuzz.
// gitsvc: applyPatch
func (s *GitService) ApplyPatch(ctx context.Context, repoPath, content string, am bool) error {
	return lockedErr(s, ctx, "applyPatch", repoPath, func(ctx context.Context) error {
		tmp := filepath.Join(os.TempDir(), fmt.Sprintf("gitcito-apply-%d.patch", time.Now().UnixMilli()))
		if err := os.WriteFile(tmp, []byte(content), 0o666); err != nil {
			return err
		}
		defer os.Remove(tmp)
		cmd := "apply"
		if am {
			cmd = "am"
		}
		_, err := cRaw(ctx, repoPath, cmd, "--3way", tmp)
		return err
	})
}

// ListTrackedFiles is every tracked file (in the index) — for the push-time
// secret guard.
// gitsvc: listTrackedFiles
func (s *GitService) ListTrackedFiles(ctx context.Context, repoPath string) ([]string, error) {
	return locked(s, ctx, "listTrackedFiles", repoPath, func(ctx context.Context) ([]string, error) {
		return cListTrackedFiles(ctx, repoPath), nil
	})
}

func cListTrackedFiles(ctx context.Context, repoPath string) []string {
	return cSplitNul(cRawOr(ctx, repoPath, "", "ls-files", "--cached", "-z"))
}

// IgnoredTrackedFiles is the paths (from paths) that an ignore rule matches,
// tracked or not. Git normally keeps tracking such files, but AI context
// deliberately honours the rule as a privacy boundary. `--no-index` makes
// check-ignore consider tracked paths.
// gitsvc: ignoredTrackedFiles
func (s *GitService) IgnoredTrackedFiles(ctx context.Context, repoPath string, paths []string) ([]string, error) {
	return locked(s, ctx, "ignoredTrackedFiles", repoPath, func(ctx context.Context) ([]string, error) {
		return cIgnoredTrackedFiles(ctx, repoPath, paths)
	})
}

func cIgnoredTrackedFiles(ctx context.Context, repoPath string, paths []string) ([]string, error) {
	ignored := []string{}
	for i := 0; i < len(paths); i += 200 {
		chunk := paths[i:min(i+200, len(paths))]
		args := append([]string{"-C", repoPath, "check-ignore", "--no-index", "--"}, chunk...)
		res, err := gitx.Exec(ctx, "", "git", args, gitx.RunOpts{MaxBuffer: 16 * 1024 * 1024})
		if err != nil {
			// `check-ignore` exits 1 when nothing matched — success, not failure.
			// Any other failure has to propagate: swallowing it would report
			// "nothing is ignored" and hand ignored files to the caller.
			if code, ok := gitx.ExitCode(err); !ok || code != 1 {
				return nil, errors.New("Could not determine ignored files: " + err.Error())
			}
		}
		ignored = append(ignored, gitx.SplitTrimmedLines(res.Stdout)...)
	}
	return ignored, nil
}

// CommitsTouchingPath is the hashes of the commits that touched path (file or
// folder) — for the graph path filter. A nil max means 1000.
// gitsvc: commitsTouchingPath
func (s *GitService) CommitsTouchingPath(ctx context.Context, repoPath, path string, max *int) ([]string, error) {
	return locked(s, ctx, "commitsTouchingPath", repoPath, func(ctx context.Context) ([]string, error) {
		n := 1000
		if max != nil {
			n = *max
		}
		raw := cRawOr(ctx, repoPath, "", "log", "--format=%H", "--max-count="+strconv.Itoa(n), "--", path)
		return gitx.SplitLines(raw), nil
	})
}

// FileSizes is the byte size and binary-ness of working-tree files — for the
// large-file commit guard. Missing files are left out.
// gitsvc: fileSizes
func (s *GitService) FileSizes(ctx context.Context, repoPath string, files []string) (map[string]FileSizeInfo, error) {
	return locked(s, ctx, "fileSizes", repoPath, func(ctx context.Context) (map[string]FileSizeInfo, error) {
		out := map[string]FileSizeInfo{}
		for _, f := range files {
			abs := filepath.Join(repoPath, f)
			st, err := os.Stat(abs)
			if err != nil {
				continue // missing/deleted → skip
			}
			out[f] = FileSizeInfo{Size: st.Size(), Binary: cSniffBinary(abs)}
		}
		return out, nil
	})
}

// cSniffBinary looks for a NUL byte in the first 8KB. Only that window is
// read — reading whole files here would buffer every staged file at once, on
// exactly the very large files this guard exists for. Unreadable → false.
func cSniffBinary(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false
	}
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// treeStatus kinds, by priority when a folder aggregates mixed child
// statuses (higher wins).
var cTreeRank = map[string]int{
	"conflicted": 6, "modified": 5, "added": 4, "deleted": 3, "renamed": 2, "untracked": 1, "ignored": 0,
}

// TreeStatus maps repo-relative path → status kind for every
// changed/untracked/ignored path, from one `git status --ignored=matching`
// walk. Directories also get an aggregated status so a folder can show a dot
// when something inside changed. Clean tracked files are absent (= clean).
// gitsvc: treeStatus
func (s *GitService) TreeStatus(ctx context.Context, repoPath string) (map[string]string, error) {
	return locked(s, ctx, "treeStatus", repoPath, func(ctx context.Context) (map[string]string, error) {
		out := map[string]string{}
		// Failing soft: no badges is a worse tree, not a broken one.
		st, err := cStatusScan(ctx, repoPath)
		if err != nil {
			return out, nil
		}
		bump := func(p, kind string) {
			if cur, ok := out[p]; !ok || cTreeRank[kind] > cTreeRank[cur] {
				out[p] = kind
			}
		}
		// An untracked directory arrives as "tmp/" — strip the slash so it keys
		// the same way the tree addresses it.
		clean := func(p string) string { return strings.TrimSuffix(p, "/") }

		for _, f := range st.files {
			x, y := f.index, f.workingDir
			var kind string
			switch {
			case x == "?" || y == "?":
				kind = "untracked"
			case x == "U" || y == "U" || (x == "A" && y == "A") || (x == "D" && y == "D"):
				kind = "conflicted"
			case x == "R" || y == "R":
				kind = "renamed"
			case x == "A" || y == "A":
				kind = "added"
			case x == "D" || y == "D":
				kind = "deleted"
			default:
				kind = "modified"
			}
			path := clean(f.path)
			bump(path, kind)
			// Propagate to ancestor directories so a collapsed folder can show a dot.
			for slash := strings.LastIndexByte(path, '/'); slash > 0; slash = strings.LastIndexByte(path[:slash], '/') {
				bump(path[:slash], kind)
			}
		}

		// Ignored stays leaf-only, so a whole ignored tree does not paint every
		// parent grey. `--ignored=matching` reports the directory itself, and
		// the renderer resolves the files inside it.
		for _, p := range st.ignored {
			bump(clean(p), "ignored")
		}
		return out, nil
	})
}
