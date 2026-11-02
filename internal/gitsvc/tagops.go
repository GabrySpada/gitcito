package gitsvc

// Tags: create, delete, publish, unpublish, and list what a remote has.
//
// Renderer channels served (through the 'git' dispatcher): createTag,
// deleteTag, pushTag, deleteRemoteTag, getRemoteTags.

import (
	"context"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// CreateTagOptions is createTag's `opts`.
type CreateTagOptions struct {
	Message string `json:"message,omitempty"`
	Sign    bool   `json:"sign,omitempty"`
}

// bTag is simple-git's `tag(args)`: it prepends `tag` unless the first
// argument already is `tag` — so a lightweight tag literally named "tag" runs
// `git tag` and lists tags instead, exactly as it did in TS.
func bTag(ctx context.Context, repoPath string, args []string) error {
	if len(args) == 0 || args[0] != "tag" {
		args = append([]string{"tag"}, args...)
	}
	_, err := bRaw(ctx, repoPath, args...)
	return err
}

// gitsvc: createTag
//
// hash "" (undefined) tags HEAD.
func (s *GitService) CreateTag(ctx context.Context, repoPath, name, hash string, opts *CreateTagOptions) error {
	return lockedErr(s, ctx, "createTag", repoPath, func(ctx context.Context) error {
		var o CreateTagOptions
		if opts != nil {
			o = *opts
		}
		var args []string
		// A message (or signing) makes it an annotated/signed tag object;
		// otherwise it stays a lightweight tag (just a ref).
		if o.Sign {
			args = append(args, "-s")
		} else if o.Message != "" {
			args = append(args, "-a")
		}
		if o.Message != "" {
			args = append(args, "-m", o.Message)
		}
		args = append(args, name)
		if hash != "" {
			args = append(args, hash)
		}
		return bTag(ctx, repoPath, args)
	})
}

// gitsvc: deleteTag
func (s *GitService) DeleteTag(ctx context.Context, repoPath, name string) error {
	return lockedErr(s, ctx, "deleteTag", repoPath, func(ctx context.Context) error {
		return bTag(ctx, repoPath, []string{"-d", name})
	})
}

// bPush is simple-git's `push([...args])`: `--verbose --porcelain` appended,
// and an error carries stdout (the porcelain report) followed by stderr.
func bPush(ctx context.Context, repoPath string, args ...string) error {
	_, err := bRaw(ctx, repoPath, append(append([]string{"push"}, args...), "--verbose", "--porcelain")...)
	return err
}

// gitsvc: pushTag
func (s *GitService) PushTag(ctx context.Context, repoPath, name, remote string) error {
	return lockedErr(s, ctx, "pushTag", repoPath, func(ctx context.Context) error {
		return bPush(ctx, repoPath, bRemoteOrOrigin(remote), "refs/tags/"+name)
	})
}

// gitsvc: deleteRemoteTag
func (s *GitService) DeleteRemoteTag(ctx context.Context, repoPath, name, remote string) error {
	return lockedErr(s, ctx, "deleteRemoteTag", repoPath, func(ctx context.Context) error {
		return bPush(ctx, repoPath, bRemoteOrOrigin(remote), "--delete", "refs/tags/"+name)
	})
}

// gitsvc: getRemoteTags
//
// The tags a remote advertises; [] when it cannot be reached.
func (s *GitService) GetRemoteTags(ctx context.Context, repoPath, remote string) ([]string, error) {
	return locked(s, ctx, "getRemoteTags", repoPath, func(ctx context.Context) ([]string, error) {
		out, err := bRaw(ctx, repoPath, "ls-remote", "--tags", "--refs", bRemoteOrOrigin(remote))
		if err != nil {
			return []string{}, nil
		}
		tags := []string{}
		for _, line := range gitx.SplitLines(out) {
			ref := ""
			if parts := strings.Split(line, "\t"); len(parts) > 1 {
				ref = parts[1]
			}
			tags = append(tags, strings.Replace(ref, "refs/tags/", "", 1))
		}
		return tags, nil
	})
}
