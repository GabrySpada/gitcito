package gitsvc

// Hooks and Git LFS.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// HookInfo is HookInfo in src/shared/types.ts: one git hook file.
type HookInfo struct {
	Name string `json:"name"`
	// Exists: a real (non-.sample) hook file is present.
	Exists bool `json:"exists"`
	// Executable: exec bit set — git only runs executable hooks.
	Executable bool `json:"executable"`
	// Sample: only the shipped `<name>.sample` template exists.
	Sample bool `json:"sample"`
	// Size of the real hook in bytes (0 when absent).
	Size int64 `json:"size"`
}

// HooksInfo is HooksInfo in src/shared/types.ts.
type HooksInfo struct {
	// HooksDir is the resolved hooks directory (honours core.hooksPath).
	HooksDir string `json:"hooksDir"`
	// CustomHooksPath: core.hooksPath is set (custom/framework-managed).
	CustomHooksPath bool `json:"customHooksPath"`
	// PreCommitFramework: a .pre-commit-config.yaml(.yml) is present.
	PreCommitFramework bool       `json:"preCommitFramework"`
	Hooks              []HookInfo `json:"hooks"`
}

// LfsFile is LfsFile in src/shared/types.ts.
type LfsFile struct {
	Path string `json:"path"`
	// Oid is the short object id.
	Oid string `json:"oid"`
	// Downloaded: real content present (false = pointer only).
	Downloaded bool `json:"downloaded"`
}

// LfsInfo is LfsInfo in src/shared/types.ts.
type LfsInfo struct {
	// Installed: the git-lfs binary is available.
	Installed bool `json:"installed"`
	// Enabled: the repo tracks anything via LFS.
	Enabled bool `json:"enabled"`
	// Patterns are the tracked glob patterns from .gitattributes.
	Patterns []string  `json:"patterns"`
	Files    []LfsFile `json:"files"`
}

// HooksInfo enumerates the repo's hooks and detects a custom hooksPath or the
// pre-commit framework.
//
// gitsvc: hooksInfo
func (s *GitService) HooksInfo(ctx context.Context, repoPath string) (HooksInfo, error) {
	return locked(s, ctx, "hooksInfo", repoPath, func(ctx context.Context) (HooksInfo, error) {
		dir, custom := gitx.ResolveHooksDir(ctx, repoPath)
		info := HooksInfo{
			HooksDir:        dir,
			CustomHooksPath: custom,
			PreCommitFramework: dExists(filepath.Join(repoPath, ".pre-commit-config.yaml")) ||
				dExists(filepath.Join(repoPath, ".pre-commit-config.yml")),
			Hooks: []HookInfo{},
		}
		for _, name := range gitx.KnownHooks {
			p := filepath.Join(dir, name)
			h := HookInfo{Name: name}
			if st, err := os.Stat(p); err == nil {
				h.Exists = st.Mode().IsRegular()
				h.Size = st.Size()
				h.Executable = st.Mode().Perm()&0o111 != 0
			}
			h.Sample = !h.Exists && dExists(p+".sample")
			info.Hooks = append(info.Hooks, h)
		}
		return info, nil
	})
}

// ReadHook reads a hook's contents for editing. Falls back to the shipped
// `.sample` template, then to a minimal shebang, so the editor is never blank.
//
// gitsvc: readHook
func (s *GitService) ReadHook(ctx context.Context, repoPath, name string) (string, error) {
	return locked(s, ctx, "readHook", repoPath, func(ctx context.Context) (string, error) {
		dir, _ := gitx.ResolveHooksDir(ctx, repoPath)
		p, err := dHookPath(dir, name)
		if err != nil {
			return "", err
		}
		if real, err := dReadUTF8(p); err == nil {
			return real, nil
		}
		if sample, err := dReadUTF8(p + ".sample"); err == nil {
			return sample, nil
		}
		return "#!/bin/sh\n", nil
	})
}

// WriteHook writes a hook and makes it executable (so git will run it).
//
// gitsvc: writeHook
func (s *GitService) WriteHook(ctx context.Context, repoPath, name, content string) error {
	return lockedErr(s, ctx, "writeHook", repoPath, func(ctx context.Context) error {
		dir, _ := gitx.ResolveHooksDir(ctx, repoPath)
		p, err := dHookPath(dir, name)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(dir, 0o777)
		if err := os.WriteFile(p, []byte(content), 0o666); err != nil {
			return dNodeFSError("open", p, "", err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			return dNodeFSError("chmod", p, "", err)
		}
		return nil
	})
}

// dModeBits are the permission bits chmod takes, special bits included.
const dModeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// SetHookEnabled toggles a hook's executable bit — git only runs hooks that
// are executable.
//
// gitsvc: setHookEnabled
func (s *GitService) SetHookEnabled(ctx context.Context, repoPath, name string, enabled bool) error {
	return lockedErr(s, ctx, "setHookEnabled", repoPath, func(ctx context.Context) error {
		dir, _ := gitx.ResolveHooksDir(ctx, repoPath)
		p, err := dHookPath(dir, name)
		if err != nil {
			return err
		}
		st, err := os.Stat(p)
		if err != nil {
			return dNodeFSError("stat", p, "", err)
		}
		mode := st.Mode() & dModeBits
		if enabled {
			mode |= 0o755
		} else {
			mode &^= 0o111
		}
		if err := os.Chmod(p, mode); err != nil {
			return dNodeFSError("chmod", p, "", err)
		}
		return nil
	})
}

// DeleteHook deletes a hook file. The shipped `.sample` template (if any) is
// left intact.
//
// gitsvc: deleteHook
func (s *GitService) DeleteHook(ctx context.Context, repoPath, name string) error {
	return lockedErr(s, ctx, "deleteHook", repoPath, func(ctx context.Context) error {
		dir, _ := gitx.ResolveHooksDir(ctx, repoPath)
		p, err := dHookPath(dir, name)
		if err != nil {
			return err
		}
		_ = os.Remove(p)
		return nil
	})
}

// dHookPath joins a hook name onto the hooks directory. The name arrives from the
// page, and every exported method is callable by any script running there, so a
// name must be one plain path segment: "../../.bashrc" would otherwise read,
// overwrite or chmod files anywhere the user can write. The Electron build had
// no such check (the IPC surface was the same); this closes it.
func dHookPath(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", dErrf("Invalid hook name: %s", name)
	}
	return filepath.Join(dir, name), nil
}

var (
	dLfsFilterRe = regexp.MustCompile(`filter=lfs`)
	// "<oid> <* or -> <path>"  (* = downloaded, - = pointer only)
	dLfsFileRe = regexp.MustCompile(`^([^` + dJSSpace + `]+)[` + dJSSpace + `]+([*-])[` + dJSSpace + `]+(` + dJSDot + `+)$`)
)

// LfsInfo reports whether git-lfs is installed, the tracked patterns, and the
// LFS files.
//
// gitsvc: lfsInfo
func (s *GitService) LfsInfo(ctx context.Context, repoPath string) (LfsInfo, error) {
	return locked(s, ctx, "lfsInfo", repoPath, func(ctx context.Context) (LfsInfo, error) {
		info := LfsInfo{Patterns: []string{}, Files: []LfsFile{}}
		if _, err := gitx.Raw(ctx, repoPath, []string{"lfs", "version"}, gitx.RunOpts{}); err != nil {
			return info, nil
		}
		info.Installed = true
		ga, err := dReadUTF8(filepath.Join(repoPath, ".gitattributes"))
		if err != nil {
			ga = ""
		}
		for _, l := range dLines(ga) {
			l = gitx.TrimJS(l)
			if l == "" || strings.HasPrefix(l, "#") || !dLfsFilterRe.MatchString(l) {
				continue
			}
			info.Patterns = append(info.Patterns, dSplitWS(l)[0])
		}
		out, err := gitx.Raw(ctx, repoPath, []string{"lfs", "ls-files"}, gitx.RunOpts{})
		if err != nil {
			out = ""
		}
		for _, l := range dLines(out) {
			l = gitx.TrimJS(l)
			if l == "" {
				continue
			}
			if m := dLfsFileRe.FindStringSubmatch(l); m != nil {
				info.Files = append(info.Files, LfsFile{Oid: m[1], Downloaded: m[2] == "*", Path: m[3]})
			}
		}
		info.Enabled = len(info.Patterns) > 0 || len(info.Files) > 0
		return info, nil
	})
}

func (s *GitService) dLfs(ctx context.Context, method, repoPath string, args ...string) error {
	return lockedErr(s, ctx, method, repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, append([]string{"lfs"}, args...), gitx.RunOpts{})
		return err
	})
}

// LfsTrack tracks a glob pattern with LFS (writes .gitattributes).
//
// gitsvc: lfsTrack
func (s *GitService) LfsTrack(ctx context.Context, repoPath, pattern string) error {
	return s.dLfs(ctx, "lfsTrack", repoPath, "track", pattern)
}

// LfsUntrack stops tracking a pattern with LFS.
//
// gitsvc: lfsUntrack
func (s *GitService) LfsUntrack(ctx context.Context, repoPath, pattern string) error {
	return s.dLfs(ctx, "lfsUntrack", repoPath, "untrack", pattern)
}

// LfsPull downloads LFS content for pointers in the working tree.
//
// gitsvc: lfsPull
func (s *GitService) LfsPull(ctx context.Context, repoPath string) error {
	return s.dLfs(ctx, "lfsPull", repoPath, "pull")
}

// LfsPrune prunes old/unreferenced LFS objects from local storage.
//
// gitsvc: lfsPrune
func (s *GitService) LfsPrune(ctx context.Context, repoPath string) error {
	return s.dLfs(ctx, "lfsPrune", repoPath, "prune")
}
