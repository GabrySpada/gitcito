package gitsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// File-system actions of the Files tab: create, rename/move, delete (to the
// trash), and drops from the OS file manager.

// cStripSlashes is `s.replace(/^\/+|\/+$/g, ”)`.
var cEdgeSlashesRe = regexp.MustCompile(`^/+|/+$`)

func cStripSlashes(s string) string { return cEdgeSlashesRe.ReplaceAllString(s, "") }

// cExists is existsSync: the path resolves (a dangling symlink does not).
func cExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// cBasename is path.basename: trailing slashes ignored, ” for ” or "/".
func cBasename(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	return p[strings.LastIndexByte(p, '/')+1:]
}

// FsCreate creates an empty file, or a directory, at relPath (repo-relative).
// gitsvc: fsCreate
func (s *GitService) FsCreate(ctx context.Context, repoPath, relPath string, isDir bool) error {
	return lockedErr(s, ctx, "fsCreate", repoPath, func(ctx context.Context) error {
		abs := filepath.Join(repoPath, relPath)
		if cExists(abs) {
			return errors.New("Already exists: " + relPath)
		}
		if isDir {
			return cNodeFsErr(os.MkdirAll(abs, 0o777))
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o777); err != nil {
			return cNodeFsErr(err)
		}
		return cNodeFsErr(os.WriteFile(abs, nil, 0o666))
	})
}

// FsRename renames/moves a path within the repo — `git mv` when it is
// tracked, so history follows; a plain rename for untracked paths.
// gitsvc: fsRename
func (s *GitService) FsRename(ctx context.Context, repoPath, from, to string) error {
	return lockedErr(s, ctx, "fsRename", repoPath, func(ctx context.Context) error {
		return cFsRename(ctx, repoPath, from, to)
	})
}

func cFsRename(ctx context.Context, repoPath, from, to string) error {
	if from == to {
		return nil
	}
	dest := filepath.Join(repoPath, to)
	if cExists(dest) {
		return errors.New("Already exists: " + to)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
		return cNodeFsErr(err)
	}
	if tracked := gitx.TrimJS(cRawOr(ctx, repoPath, "", "ls-files", "--", from)); tracked != "" {
		_, err := cRaw(ctx, repoPath, "mv", from, to)
		return err
	}
	return cNodeFsErr(os.Rename(filepath.Join(repoPath, from), dest))
}

// FsDelete moves paths to the OS trash (recoverable). It refuses paths
// outside the repo.
// gitsvc: fsDelete
func (s *GitService) FsDelete(ctx context.Context, repoPath string, relPaths []string) error {
	return lockedErr(s, ctx, "fsDelete", repoPath, func(ctx context.Context) error {
		for _, rel := range relPaths {
			abs := filepath.Join(repoPath, rel)
			if !strings.HasPrefix(abs, repoPath) {
				return errors.New("Refusing to delete outside repo: " + rel)
			}
			if err := cTrashItem(ctx, abs); err != nil {
				return err
			}
		}
		return nil
	})
}

// FsExisting is the base names of names that already exist in destDir — the
// renderer asks before a drop so it can offer "Replace" / "Keep both".
// gitsvc: fsExisting
func (s *GitService) FsExisting(ctx context.Context, repoPath, destDir string, names []string) ([]string, error) {
	return locked(s, ctx, "fsExisting", repoPath, func(ctx context.Context) ([]string, error) {
		destAbs := filepath.Join(repoPath, cStripSlashes(destDir))
		out := []string{}
		for _, n := range names {
			if cExists(filepath.Join(destAbs, cBasename(n))) {
				out = append(out, n)
			}
		}
		return out, nil
	})
}

// FsMove moves repo-relative paths into destDir (” = repo root), keeping
// their base names. It refuses to move a folder into itself or one of its
// descendants. mode decides what an existing name at the destination means:
// fail ('error', also when empty), trash it first ('replace'), or land next
// to it under a free name ('keepBoth').
// gitsvc: fsMove
func (s *GitService) FsMove(ctx context.Context, repoPath string, froms []string, destDir, mode string) error {
	return lockedErr(s, ctx, "fsMove", repoPath, func(ctx context.Context) error {
		return cFsMove(ctx, repoPath, froms, destDir, mode)
	})
}

func cFsMove(ctx context.Context, repoPath string, froms []string, destDir, mode string) error {
	dir := cStripSlashes(destDir)
	for _, from := range froms {
		src := cStripSlashes(from)
		if dir == src || strings.HasPrefix(dir, src+"/") {
			return errors.New("Cannot move " + src + " into itself")
		}
		name, err := cResolveDrop(ctx, repoPath, dir, cBasename(src), mode)
		if err != nil {
			return err
		}
		to := name
		if dir != "" {
			to = dir + "/" + name
		}
		if err := cFsRename(ctx, repoPath, src, to); err != nil {
			return err
		}
	}
	return nil
}

// FsImport copies absolute paths dropped from the OS into destDir (” = repo
// root). Sources that already live inside this repo are moved instead of
// duplicated.
// gitsvc: fsImport
func (s *GitService) FsImport(ctx context.Context, repoPath string, srcPaths []string, destDir, mode string) error {
	return lockedErr(s, ctx, "fsImport", repoPath, func(ctx context.Context) error {
		dir := cStripSlashes(destDir)
		destAbs := filepath.Join(repoPath, dir)
		if !strings.HasPrefix(destAbs, repoPath) {
			return errors.New("Refusing to write outside repo: " + destDir)
		}
		if err := os.MkdirAll(destAbs, 0o777); err != nil {
			return cNodeFsErr(err)
		}
		for _, src := range srcPaths {
			if src == filepath.Join(destAbs, cBasename(src)) {
				continue
			}
			if strings.HasPrefix(src, repoPath+"/") {
				// Dropped from inside this repo (e.g. Finder) — a move keeps git history.
				if err := cFsMove(ctx, repoPath, []string{src[len(repoPath)+1:]}, dir, mode); err != nil {
					return err
				}
				continue
			}
			name, err := cResolveDrop(ctx, repoPath, dir, cBasename(src), mode)
			if err != nil {
				return err
			}
			if err := cCopyTree(src, filepath.Join(destAbs, name)); err != nil {
				return err
			}
		}
		return nil
	})
}

// cResolveDrop decides the base name a dropped item lands under in dir,
// honouring the caller's conflict choice. 'replace' trashes the existing entry
// first (so the drop is recoverable); 'keepBoth' picks the next free
// "name 2"-style suffix.
func cResolveDrop(ctx context.Context, repoPath, dir, name, mode string) (string, error) {
	dirAbs := filepath.Join(repoPath, dir)
	target := filepath.Join(dirAbs, name)
	if !cExists(target) {
		return name, nil
	}
	switch mode {
	case "", "error":
		shown := name
		if dir != "" {
			shown = dir + "/" + name
		}
		return "", errors.New("Already exists: " + shown)
	case "replace":
		if err := cTrashItem(ctx, target); err != nil {
			return "", err
		}
		return name, nil
	}
	stem, ext := name, ""
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		stem, ext = name[:dot], name[dot:]
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s %d%s", stem, i, ext)
		if !cExists(filepath.Join(dirAbs, candidate)) {
			return candidate, nil
		}
	}
	return "", errors.New("No free name left for " + name)
}

// cCopyTree is `fs.cp(src, dest, { recursive: true })`: directories
// recursively, files with their mode, existing files overwritten. A relative
// symlink is re-pointed at its absolute target, as Node's cp does without
// `verbatimSymlinks`.
func cCopyTree(src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return cNodeFsErr(err)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return cNodeFsErr(err)
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(src), link)
		}
		_ = os.Remove(dest)
		return cNodeFsErr(os.Symlink(link, dest))
	case info.IsDir():
		if err := os.MkdirAll(dest, info.Mode().Perm()|0o700); err != nil {
			return cNodeFsErr(err)
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return cNodeFsErr(err)
		}
		for _, e := range entries {
			if err := cCopyTree(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		return cCopyFile(src, dest, info.Mode().Perm())
	}
}

func cCopyFile(src, dest string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return cNodeFsErr(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return cNodeFsErr(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return cNodeFsErr(err)
	}
	if err := out.Close(); err != nil {
		return cNodeFsErr(err)
	}
	return os.Chmod(dest, perm)
}

// cNodeFsErr rewords a Go *fs.PathError / *os.LinkError the way Node words
// the same failure — "ENOENT: no such file or directory, rename 'a' -> 'b'" —
// since that text is what the renderer showed. Other errors pass through.
func cNodeFsErr(err error) error {
	if err == nil {
		return nil
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	code, desc := cErrnoName(errno)
	if code == "" {
		return err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Errorf("%s: %s, %s '%s' -> '%s'", code, desc, cNodeOp(le.Op), le.Old, le.New)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %s, %s '%s'", code, desc, cNodeOp(pe.Op), pe.Path)
	}
	return err
}

// cNodeOp maps Go's operation names to the libuv syscall Node reports.
func cNodeOp(op string) string {
	switch op {
	case "open":
		return "open"
	case "mkdir", "mkdirall":
		return "mkdir"
	case "symlink":
		return "symlink"
	case "readlink":
		return "readlink"
	case "lstat":
		return "lstat"
	case "stat":
		return "stat"
	}
	return op
}

func cErrnoName(e syscall.Errno) (code, desc string) {
	switch e {
	case syscall.ENOENT:
		return "ENOENT", "no such file or directory"
	case syscall.EEXIST:
		return "EEXIST", "file already exists"
	case syscall.EACCES:
		return "EACCES", "permission denied"
	case syscall.EPERM:
		return "EPERM", "operation not permitted"
	case syscall.ENOTDIR:
		return "ENOTDIR", "not a directory"
	case syscall.EISDIR:
		return "EISDIR", "illegal operation on a directory"
	case syscall.ENOTEMPTY:
		return "ENOTEMPTY", "directory not empty"
	case syscall.EXDEV:
		return "EXDEV", "cross-device link not permitted"
	}
	return "", ""
}
