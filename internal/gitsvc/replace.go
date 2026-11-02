package gitsvc

// Object replacement (`git replace`).
//
// A replace ref tells git "wherever anyone asks for object A, read B instead".
// Nothing is rewritten and no sha changes — which makes it the honest way to
// shorten a clone's history without the violence of a filter-branch, and a
// genuinely dangerous thing to leave lying around unexplained, since the
// history everyone sees is no longer the history that is stored.

import (
	"context"
	"regexp"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// ReplaceRef is ReplaceRef in src/shared/types.ts.
type ReplaceRef struct {
	Original           string `json:"original"`
	OriginalSubject    string `json:"originalSubject"`
	Replacement        string `json:"replacement"`
	ReplacementSubject string `json:"replacementSubject"`
	// ReplacementParents: a graft's whole point is changing these.
	ReplacementParents []string `json:"replacementParents"`
}

// ReplaceStatus is ReplaceStatus in src/shared/types.ts.
type ReplaceStatus struct {
	Refs []ReplaceRef `json:"refs"`
	// Enabled is `core.useReplaceRefs` — false means git ignores every replacement here.
	Enabled bool `json:"enabled"`
}

// "<original> (<type>) -> <replacement> (<type>)"
var dReplaceLineRe = regexp.MustCompile(`^([0-9a-f]{40,64})[` + dJSSpace + `]+\([^)]*\)[` + dJSSpace + `]+->[` + dJSSpace + `]+([0-9a-f]{40,64})`)

// dGitTrim is `runGit(...).catch(() => ”)` trimmed.
func dGitTrim(ctx context.Context, repoPath string, args ...string) string {
	out, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
	if err != nil {
		return ""
	}
	return gitx.TrimJS(out)
}

// Replacements lists every replacement, with enough of each side to tell them
// apart.
//
// gitsvc: replacements
func (s *GitService) Replacements(ctx context.Context, repoPath string) (ReplaceStatus, error) {
	return locked(s, ctx, "replacements", repoPath, func(ctx context.Context) (ReplaceStatus, error) {
		listed, err := gitx.RunGit(ctx, repoPath, []string{"replace", "-l", "--format=long"}, gitx.RunOpts{})
		if err != nil {
			listed = ""
		}
		refs := []ReplaceRef{}
		for _, line := range dLines(listed) {
			m := dReplaceLineRe.FindStringSubmatch(gitx.TrimJS(line))
			if m == nil {
				continue
			}
			original, replacement := m[1], m[2]
			// The original is behind the replacement now, so ask for it with
			// replacements switched off — otherwise both sides read the same.
			parents := []string{}
			for _, p := range dSplitWS(dGitTrim(ctx, repoPath, "--no-replace-objects", "log", "-1", "--format=%P", replacement)) {
				if p != "" {
					parents = append(parents, p)
				}
			}
			refs = append(refs, ReplaceRef{
				Original:           original,
				OriginalSubject:    dGitTrim(ctx, repoPath, "--no-replace-objects", "log", "-1", "--format=%s", original),
				Replacement:        replacement,
				ReplacementSubject: dGitTrim(ctx, repoPath, "log", "-1", "--format=%s", replacement),
				ReplacementParents: parents,
			})
		}
		useReplace := dGitTrim(ctx, repoPath, "config", "--get", "core.useReplaceRefs")
		return ReplaceStatus{Refs: refs, Enabled: useReplace != "false"}, nil
	})
}

// ReplaceGraft grafts a commit onto different parents — or onto none at all —
// and returns the commit's full sha.
//
// `git replace --graft <commit>` with no parents makes that commit look like
// the beginning of history: everything before it becomes unreachable through
// normal walks, without a single object being rewritten. Point it at a commit
// in an archive repository instead, and the truncated clone gets its full
// history back.
//
// gitsvc: replaceGraft
func (s *GitService) ReplaceGraft(ctx context.Context, repoPath, commit string, parents []string) (string, error) {
	return locked(s, ctx, "replaceGraft", repoPath, func(ctx context.Context) (string, error) {
		target := gitx.TrimJS(commit)
		if target == "" {
			return "", dErr("Choose a commit to graft.")
		}
		args := []string{"replace", "--graft", target}
		for _, p := range parents {
			if p = gitx.TrimJS(p); p != "" {
				args = append(args, p)
			}
		}
		if _, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{}); err != nil {
			return "", err
		}
		out, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", target}, gitx.RunOpts{})
		if err != nil {
			return "", err
		}
		return gitx.TrimJS(out), nil
	})
}

// ReplaceObject replaces one object with another outright.
//
// gitsvc: replaceObject
func (s *GitService) ReplaceObject(ctx context.Context, repoPath, original, replacement string) error {
	return lockedErr(s, ctx, "replaceObject", repoPath, func(ctx context.Context) error {
		o, r := gitx.TrimJS(original), gitx.TrimJS(replacement)
		if o == "" || r == "" {
			return dErr("Both objects are needed.")
		}
		_, err := gitx.RunGit(ctx, repoPath, []string{"replace", "-f", o, r}, gitx.RunOpts{})
		return err
	})
}

// ReplaceDelete drops a replacement. The original was never touched, so this
// restores it.
//
// gitsvc: replaceDelete
func (s *GitService) ReplaceDelete(ctx context.Context, repoPath, original string) error {
	return lockedErr(s, ctx, "replaceDelete", repoPath, func(ctx context.Context) error {
		o := gitx.TrimJS(original)
		if o == "" {
			return dErr("Nothing to delete.")
		}
		_, err := gitx.RunGit(ctx, repoPath, []string{"replace", "-d", o}, gitx.RunOpts{})
		return err
	})
}

// SetUseReplaceRefs turns replacements on or off for this repository
// (`core.useReplaceRefs`). On means the setting is removed, not set to true.
//
// gitsvc: setUseReplaceRefs
func (s *GitService) SetUseReplaceRefs(ctx context.Context, repoPath string, enabled bool) error {
	return lockedErr(s, ctx, "setUseReplaceRefs", repoPath, func(ctx context.Context) error {
		if enabled {
			_, _ = gitx.RunGit(ctx, repoPath, []string{"config", "--local", "--unset", "core.useReplaceRefs"}, gitx.RunOpts{})
			return nil
		}
		_, err := gitx.RunGit(ctx, repoPath, []string{"config", "--local", "core.useReplaceRefs", "false"}, gitx.RunOpts{})
		return err
	})
}
