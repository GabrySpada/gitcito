package gitsvc

// Notes. `git notes` attaches text to a commit without touching the commit
// itself — the only way to annotate history that is already published.
// Gitcito uses the default `refs/notes/commits` ref, which is what `git log`
// and every other tool reads by default.
//
// Renderer channels served (through the 'git' dispatcher): note,
// notedCommits, setNote, removeNote, fetchNotes, pushNotes.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

var bTrailingNewlinesRe = regexp.MustCompile(`\n+$`)

// gitsvc: note
//
// The note attached to a commit, or "" when it has none.
func (s *GitService) Note(ctx context.Context, repoPath, sha string) (string, error) {
	return locked(s, ctx, "note", repoPath, func(ctx context.Context) (string, error) {
		// `notes show` exits 1 for "no note", which is an answer, not a failure.
		return bTrailingNewlinesRe.ReplaceAllString(bGitOr(ctx, repoPath, "", "notes", "show", sha), ""), nil
	})
}

// gitsvc: notedCommits
//
// Commits that carry a note. One cheap call, so the graph can mark them.
func (s *GitService) NotedCommits(ctx context.Context, repoPath string) ([]string, error) {
	return locked(s, ctx, "notedCommits", repoPath, func(ctx context.Context) ([]string, error) {
		out := []string{}
		for _, line := range strings.Split(bGitOr(ctx, repoPath, "", "notes", "list"), "\n") {
			// "<note blob> <annotated commit>"
			if parts := strings.Split(gitx.TrimJS(line), " "); len(parts) > 1 && parts[1] != "" {
				out = append(out, parts[1])
			}
		}
		return out, nil
	})
}

// bRemoveNote is removeNote without the lock.
func bRemoveNote(ctx context.Context, repoPath, sha string) error {
	_, err := bGit(ctx, repoPath, "notes", "remove", "--ignore-missing", sha)
	return err
}

// gitsvc: setNote
//
// Write (or overwrite) a commit's note. An empty text removes it instead.
func (s *GitService) SetNote(ctx context.Context, repoPath, sha, text string) error {
	return lockedErr(s, ctx, "setNote", repoPath, func(ctx context.Context) error {
		if gitx.TrimJS(text) == "" {
			return bRemoveNote(ctx, repoPath, sha)
		}
		// `-F -` would need stdin; a temp file keeps multi-line notes intact
		// without fighting the shell over quoting.
		dir, err := os.MkdirTemp("", "gitcito-note-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		file := filepath.Join(dir, "note.txt")
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			return err
		}
		_, err = bGit(ctx, repoPath, "notes", "add", "--force", "-F", file, sha)
		return err
	})
}

// gitsvc: removeNote
func (s *GitService) RemoveNote(ctx context.Context, repoPath, sha string) error {
	return lockedErr(s, ctx, "removeNote", repoPath, func(ctx context.Context) error {
		return bRemoveNote(ctx, repoPath, sha)
	})
}

// bRemoteOrOrigin applies a TS `remote = 'origin'` default. The bridge cannot
// tell an omitted argument from an empty string, so "" takes the default too.
func bRemoteOrOrigin(remote string) string {
	if remote == "" {
		return "origin"
	}
	return remote
}

// gitsvc: fetchNotes
//
// Notes live under `refs/notes/*`, which a normal fetch or push ignores
// entirely — the single most common reason someone's notes "disappear" on
// another machine. fetchNotes and pushNotes move them explicitly. Only the
// user-facing commit-notes ref travels: a wildcard would also publish the
// app's own refs/notes/gitcito-ci verdicts, which are promised to stay local.
func (s *GitService) FetchNotes(ctx context.Context, repoPath, remote string) error {
	return lockedErr(s, ctx, "fetchNotes", repoPath, func(ctx context.Context) error {
		remote := bRemoteOrOrigin(remote)
		return gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
			_, err := bGit(ctx, repoPath, "fetch", remote, "+refs/notes/commits:refs/notes/commits")
			return err
		})
	})
}

// gitsvc: pushNotes
func (s *GitService) PushNotes(ctx context.Context, repoPath, remote string) error {
	return lockedErr(s, ctx, "pushNotes", repoPath, func(ctx context.Context) error {
		remote := bRemoteOrOrigin(remote)
		return gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
			_, err := bGit(ctx, repoPath, "push", remote, "refs/notes/commits")
			return err
		})
	})
}
