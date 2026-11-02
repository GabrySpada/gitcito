package gitsvc

// Per-repo protected branches, stored in git config
// (gitcito.protectedbranches, comma-joined) so they travel with the repo.
//
// Renderer channels served (through the 'git' dispatcher): protectedBranches,
// setProtectedBranches.

import (
	"context"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: protectedBranches
//
// Unset → default main/master.
func (s *GitService) ProtectedBranches(ctx context.Context, repoPath string) ([]string, error) {
	return locked(s, ctx, "protectedBranches", repoPath, func(ctx context.Context) ([]string, error) {
		// RunGit, not Raw: `config --get` exits 1 for a key that is not there,
		// and simple-git handed that back as an empty string rather than an
		// error — which quietly turned "never configured" into "nothing is
		// protected", and took the force-push guard on main with it.
		raw, err := bGit(ctx, repoPath, "config", "--get", "gitcito.protectedbranches")
		if err != nil {
			return []string{"main", "master"}, nil // never configured → sensible default
		}
		out := []string{}
		for _, b := range strings.Split(gitx.TrimJS(raw), ",") {
			if b = gitx.TrimJS(b); b != "" {
				out = append(out, b)
			}
		}
		return out, nil
	})
}

// gitsvc: setProtectedBranches
func (s *GitService) SetProtectedBranches(ctx context.Context, repoPath string, branches []string) error {
	return lockedErr(s, ctx, "setProtectedBranches", repoPath, func(ctx context.Context) error {
		var kept []string
		for _, b := range branches {
			if b = gitx.TrimJS(b); b != "" {
				kept = append(kept, b)
			}
		}
		_, err := bRaw(ctx, repoPath, "config", "gitcito.protectedbranches", strings.Join(kept, ","))
		return err
	})
}
