package gitsvc

// Slice A — remotes: list, add, remove, edit, fetch one. Renderer channel:
// the `'git'` dispatcher, methods remotes, addRemote, removeRemote,
// editRemote, fetchRemote.

import (
	"context"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: remotes
func (s *GitService) Remotes(ctx context.Context, repoPath string) ([]RemoteInfo, error) {
	return locked(s, ctx, "remotes", repoPath, func(ctx context.Context) ([]RemoteInfo, error) {
		out, err := gitx.Raw(ctx, repoPath, []string{"remote", "-v"}, gitx.RunOpts{})
		if err != nil {
			return nil, err
		}
		list := []RemoteInfo{}
		for _, r := range aParseRemotesVerbose(out) {
			list = append(list, RemoteInfo{Name: r.name, URL: aOr(r.fetch, r.push)})
		}
		return list, nil
	})
}

// gitsvc: addRemote
func (s *GitService) AddRemote(ctx context.Context, repoPath, name, url, pushURL string) error {
	return lockedErr(s, ctx, "addRemote", repoPath, func(ctx context.Context) error {
		if _, err := gitx.Raw(ctx, repoPath, []string{"remote", "add", name, url}, gitx.RunOpts{}); err != nil {
			return err
		}
		if pushURL != "" && pushURL != url {
			_, err := gitx.Raw(ctx, repoPath, []string{"remote", "set-url", "--push", name, pushURL}, gitx.RunOpts{})
			return err
		}
		return nil
	})
}

// gitsvc: removeRemote
func (s *GitService) RemoveRemote(ctx context.Context, repoPath, name string) error {
	return lockedErr(s, ctx, "removeRemote", repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, []string{"remote", "remove", name}, gitx.RunOpts{})
		return err
	})
}

// gitsvc: editRemote
//
// Rename a remote and/or update its fetch & push URLs in one shot.
func (s *GitService) EditRemote(ctx context.Context, repoPath, oldName, newName, url, pushURL string) error {
	return lockedErr(s, ctx, "editRemote", repoPath, func(ctx context.Context) error {
		raw := func(args ...string) error {
			_, err := gitx.Raw(ctx, repoPath, append([]string{"remote"}, args...), gitx.RunOpts{})
			return err
		}
		if newName != "" && newName != oldName {
			if err := raw("rename", oldName, newName); err != nil {
				return err
			}
		}
		name := aOr(newName, oldName)
		if url != "" {
			if err := raw("set-url", name, url); err != nil {
				return err
			}
		}
		// An empty pushUrl resets the push URL to mirror the fetch URL.
		if pushURL != "" && pushURL != url {
			return raw("set-url", "--push", name, pushURL)
		}
		_ = raw("set-url", "--push", name, aOr(url, pushURL))
		return nil
	})
}

// gitsvc: fetchRemote
func (s *GitService) FetchRemote(ctx context.Context, repoPath, name string) error {
	return lockedErr(s, ctx, "fetchRemote", repoPath, func(ctx context.Context) error {
		return gitx.WithRemoteAuth(ctx, repoPath, name, s.token, func(ctx context.Context) error {
			_, err := gitx.RunGit(ctx, repoPath, []string{"fetch", name, "--prune"}, gitx.RunOpts{})
			return err
		})
	})
}
