package gitsvc

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Commits and their messages, squash/fixup/autosquash, and commit signing.

// Commit commits the staged changes.
//
// `resetAuthor` only means something with `amend`: an amend otherwise keeps
// the amended commit's author and author date, so new work folded into a
// colleague's commit would be credited to them.
//
// Like simple-git's commit, a commit with nothing staged is not an error: git
// exits 1 with a silent stderr, which simple-git reads as success.
// gitsvc: commit
func (s *GitService) Commit(ctx context.Context, repoPath, message string, amend, resetAuthor bool) error {
	return lockedErr(s, ctx, "commit", repoPath, func(ctx context.Context) error {
		return cCommit(ctx, repoPath, message, amend, resetAuthor)
	}, repoPath, message, amend, resetAuthor)
}

func cCommit(ctx context.Context, repoPath, message string, amend, resetAuthor bool) error {
	// simple-git's commitTask: `-c core.abbrev=40 commit -m <message> …options`.
	args := []string{"-c", "core.abbrev=40", "commit", "-m", message}
	if amend {
		args = append(args, "--amend")
		if resetAuthor {
			args = append(args, "--reset-author")
		}
	}
	_, err := cRaw(ctx, repoPath, args...)
	return err
}

// GetCommitMessage is the full message (%B) of one commit.
// gitsvc: getCommitMessage
func (s *GitService) GetCommitMessage(ctx context.Context, repoPath, hash string) (string, error) {
	return locked(s, ctx, "getCommitMessage", repoPath, func(ctx context.Context) (string, error) {
		return cRaw(ctx, repoPath, "log", "-1", "--format=%B", hash)
	})
}

// CommitAuthor is the author of one commit — nil when `rev` names nothing
// (an unborn branch).
// gitsvc: commitAuthor
func (s *GitService) CommitAuthor(ctx context.Context, repoPath, rev string) (*gitx.CommitAuthor, error) {
	return locked(s, ctx, "commitAuthor", repoPath, func(ctx context.Context) (*gitx.CommitAuthor, error) {
		raw := cRawOr(ctx, repoPath, "", "log", "-1", "--format=%an%x00%ae", rev)
		parts := strings.Split(gitx.TrimJS(raw), "\x00")
		name := parts[0]
		email := ""
		if len(parts) > 1 {
			email = parts[1]
		}
		if name == "" && email == "" {
			return nil, nil
		}
		return &gitx.CommitAuthor{Name: name, Email: email}, nil
	})
}

var cFullShaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ResolveRev is the full SHA for any revision expression — `HEAD~2`, a tag,
// a short hash. It returns nil rather than failing when the expression names
// nothing: the caller is usually turning user input into a selection, and
// "no such commit" is an answer, not a failure.
// gitsvc: resolveRev
func (s *GitService) ResolveRev(ctx context.Context, repoPath, rev string) (*string, error) {
	return locked(s, ctx, "resolveRev", repoPath, func(ctx context.Context) (*string, error) {
		// `^{commit}` peels an annotated tag down to the commit it points at, so
		// a tag name selects the commit rather than the tag object.
		sha := gitx.TrimJS(cRawOr(ctx, repoPath, "", "rev-parse", "--verify", "--quiet", rev+"^{commit}"))
		if !cFullShaRe.MatchString(sha) {
			return nil, nil
		}
		return &sha, nil
	})
}

// AmendCommitMessage rewrites HEAD's message only, whatever is staged.
// gitsvc: amendCommitMessage
func (s *GitService) AmendCommitMessage(ctx context.Context, repoPath, message string) error {
	return lockedErr(s, ctx, "amendCommitMessage", repoPath, func(ctx context.Context) error {
		_, err := cRaw(ctx, repoPath, "commit", "--amend", "--only", "-m", message)
		return err
	})
}

// CommitTemplate is the contents of the repo's `commit.template`
// (.gitmessage), or ” if none is configured or the file is missing. The path
// is resolved against ~ and the repo root, so both absolute and relative
// settings work.
// gitsvc: commitTemplate
func (s *GitService) CommitTemplate(ctx context.Context, repoPath string) (string, error) {
	return locked(s, ctx, "commitTemplate", repoPath, func(ctx context.Context) (string, error) {
		tpl := gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", "commit.template"))
		if tpl == "" {
			return "", nil
		}
		p := tpl
		if p == "~" || strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[1:])
		} else if !strings.HasPrefix(p, "/") {
			p = filepath.Join(repoPath, p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", nil
		}
		return string(data), nil
	})
}

// CommitSummary is the author, date and message of one commit — for pinned
// chat context.
// gitsvc: commitSummary
func (s *GitService) CommitSummary(ctx context.Context, repoPath, hash string) (CommitSummary, error) {
	return locked(s, ctx, "commitSummary", repoPath, func(ctx context.Context) (CommitSummary, error) {
		raw, err := cRaw(ctx, repoPath, "show", "-s", "--format=%H%x00%an%x00%aI%x00%s%x00%b", hash)
		if err != nil {
			return CommitSummary{}, err
		}
		parts := strings.Split(raw, "\x00")
		get := func(i int) string {
			if i < len(parts) {
				return parts[i]
			}
			return ""
		}
		return CommitSummary{
			Hash:    gitx.TrimJS(get(0)),
			Author:  get(1),
			Date:    get(2),
			Subject: get(3),
			Body:    gitx.TrimJS(get(4)),
		}, nil
	})
}

// CommitFixup commits the staged changes as a `fixup!` of targetSha (for
// autosquash).
// gitsvc: commitFixup
func (s *GitService) CommitFixup(ctx context.Context, repoPath, targetSha string) error {
	return lockedErr(s, ctx, "commitFixup", repoPath, func(ctx context.Context) error {
		_, err := cRaw(ctx, repoPath, "commit", "--fixup="+targetSha)
		return err
	})
}

// Autosquash rebases onto base, auto-ordering and folding any fixup!/squash!
// commits. It runs non-interactively (the generated todo is accepted as is).
// gitsvc: autosquash
func (s *GitService) Autosquash(ctx context.Context, repoPath, base string, autostash bool) error {
	return lockedErr(s, ctx, "autosquash", repoPath, func(ctx context.Context) error {
		// Run as a plain process, not through the simple-git rules: those refuse
		// both a PAGER env and `-c core.editor`. Real git with these *_EDITOR
		// vars set to true accepts the auto-generated todo without opening an
		// editor. `--autostash` lets the rebase run with a dirty tree (absorb
		// leaves the unattributed changes in place), restoring them afterwards.
		args := []string{"-C", repoPath, "rebase", "-i", "--autosquash"}
		if autostash {
			args = append(args, "--autostash")
		}
		args = append(args, base)
		_, err := gitx.Exec(ctx, "", "git", args, gitx.RunOpts{
			PlainEnv: true,
			Env:      []string{"GIT_SEQUENCE_EDITOR=true", "GIT_EDITOR=true"},
		})
		return err
	})
}

// SquashCommits folds HEAD back through oldestSha into one commit with
// message.
//
// It is built with plumbing and landed with a single compare-and-swap ref
// update, so it either happens or leaves HEAD where it was: `reset --soft` +
// `commit` stranded the branch whenever a commit hook refused, and folded in
// whatever was already staged. Hooks are skipped on purpose — like an
// interactive rebase, this re-packages commits that already exist rather than
// recording new work. `commit-tree` still honours `commit.gpgSign`.
// gitsvc: squashCommits
func (s *GitService) SquashCommits(ctx context.Context, repoPath, oldestSha, message string) (SquashResult, error) {
	return locked(s, ctx, "squashCommits", repoPath, func(ctx context.Context) (SquashResult, error) {
		run := func(args ...string) (string, error) {
			out, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
			return gitx.TrimJS(out), err
		}
		before, err := run("rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return SquashResult{}, err
		}
		parent, err := run("rev-parse", "--verify", oldestSha+"^{commit}^")
		if err != nil {
			return SquashResult{}, err
		}
		after, err := run("commit-tree", before+"^{tree}", "-p", parent, "-m", message)
		if err != nil {
			return SquashResult{}, err
		}
		subject, _, _ := strings.Cut(message, "\n")
		if _, err := run("update-ref", "-m", "squash: "+subject, "HEAD", after, before); err != nil {
			return SquashResult{}, err
		}
		return SquashResult{Before: before, After: after}, nil
	})
}

// SigningConfig reads this repo's commit-signing configuration.
// gitsvc: signingConfig
func (s *GitService) SigningConfig(ctx context.Context, repoPath string) (SigningConfig, error) {
	return locked(s, ctx, "signingConfig", repoPath, func(ctx context.Context) (SigningConfig, error) {
		get := func(key string) string {
			return gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", key))
		}
		format := get("gpg.format")
		if format == "" {
			format = "openpgp"
		}
		return SigningConfig{Sign: get("commit.gpgsign") == "true", Format: format, Key: get("user.signingkey")}, nil
	})
}

// SetSigningConfig updates this repo's commit-signing configuration; only the
// fields present are written, and an empty key unsets user.signingkey.
// gitsvc: setSigningConfig
func (s *GitService) SetSigningConfig(ctx context.Context, repoPath string, opts SigningConfigUpdate) error {
	return lockedErr(s, ctx, "setSigningConfig", repoPath, func(ctx context.Context) error {
		if opts.Sign != nil {
			if _, err := cRaw(ctx, repoPath, "config", "commit.gpgsign", cJSBool(*opts.Sign)); err != nil {
				return err
			}
		}
		if opts.Format != nil {
			if _, err := cRaw(ctx, repoPath, "config", "gpg.format", *opts.Format); err != nil {
				return err
			}
		}
		if opts.Key != nil {
			if *opts.Key != "" {
				if _, err := cRaw(ctx, repoPath, "config", "user.signingkey", *opts.Key); err != nil {
					return err
				}
			} else {
				_, _ = cRaw(ctx, repoPath, "config", "--unset", "user.signingkey")
			}
		}
		return nil
	})
}

// cJSBool is String(b).
func cJSBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
