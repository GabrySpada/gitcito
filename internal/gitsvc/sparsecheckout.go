package gitsvc

// Sparse checkout (cone mode).
//
// Renderer channels served (through the 'git' dispatcher):
// sparseCheckoutInfo, sparseCheckoutSet, sparseCheckoutDisable.

import (
	"context"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// SparseCheckoutInfo is SparseCheckoutInfo in src/shared/types.ts.
type SparseCheckoutInfo struct {
	// Enabled: core.sparseCheckout is on.
	Enabled bool `json:"enabled"`
	// Cone: cone mode (directory-based).
	Cone bool `json:"cone"`
	// Dirs are the currently-included top-level directories.
	Dirs []string `json:"dirs"`
	// TopLevelDirs are all top-level directories in HEAD (toggle candidates).
	TopLevelDirs []string `json:"topLevelDirs"`
}

var bEdgeSlashesRe = regexp.MustCompile(`^/+|/+$`)

// bNormDirs is `.split('\n').map(norm).filter(Boolean)` with norm = trim and
// strip leading/trailing slashes.
func bNormDirs(out string) []string {
	dirs := []string{}
	for _, l := range strings.Split(out, "\n") {
		if d := bEdgeSlashesRe.ReplaceAllString(gitx.TrimJS(l), ""); d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// gitsvc: sparseCheckoutInfo
//
// Cone-mode sparse-checkout state + the top-level dirs available to toggle.
func (s *GitService) SparseCheckoutInfo(ctx context.Context, repoPath string) (SparseCheckoutInfo, error) {
	return locked(s, ctx, "sparseCheckoutInfo", repoPath, func(ctx context.Context) (SparseCheckoutInfo, error) {
		cfg := func(key string) string { return gitx.TrimJS(bRawOr(ctx, repoPath, "", "config", "--get", key)) }
		info := SparseCheckoutInfo{
			Enabled: cfg("core.sparseCheckout") == "true",
			Cone:    cfg("core.sparseCheckoutCone") == "true",
			Dirs:    []string{},
		}
		if info.Enabled {
			info.Dirs = bNormDirs(bRawOr(ctx, repoPath, "", "sparse-checkout", "list"))
		}
		info.TopLevelDirs = bNormDirs(bRawOr(ctx, repoPath, "", "ls-tree", "--name-only", "-d", "HEAD"))
		return info, nil
	})
}

// gitsvc: sparseCheckoutSet
//
// Enable cone-mode sparse-checkout and restrict the working tree to dirs.
func (s *GitService) SparseCheckoutSet(ctx context.Context, repoPath string, dirs []string) error {
	return lockedErr(s, ctx, "sparseCheckoutSet", repoPath, func(ctx context.Context) error {
		_, err := bRaw(ctx, repoPath, append([]string{"sparse-checkout", "set", "--cone"}, dirs...)...)
		return err
	})
}

// gitsvc: sparseCheckoutDisable
//
// Disable sparse-checkout — restore the full working tree.
func (s *GitService) SparseCheckoutDisable(ctx context.Context, repoPath string) error {
	return lockedErr(s, ctx, "sparseCheckoutDisable", repoPath, func(ctx context.Context) error {
		_, err := bRaw(ctx, repoPath, "sparse-checkout", "disable")
		return err
	})
}
