package gitsvc

// Pull request preview without a token: find the ref a forge publishes a PR's
// head under, bring it down, and apply it locally without writing a commit.
//
// Renderer channels served (through the 'git' dispatcher): resolvePrRef,
// previewRef.

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// PrRefCandidate is PrRefCandidate in src/shared/types.ts.
type PrRefCandidate struct {
	// Flavor is a PrRefFlavor: 'github' | 'gitlab' | 'bitbucket' | 'azure'.
	Flavor string `json:"flavor"`
	Ref    string `json:"ref"`
}

// PrRefProbe is PrRefProbe in src/shared/types.ts: a pull request ref that
// actually exists on the remote, found by `ls-remote`.
type PrRefProbe struct {
	Flavor string `json:"flavor"`
	Ref    string `json:"ref"`
	Sha    string `json:"sha"`
}

// bPrConventions is where each forge publishes a pull/merge request's head as
// a plain git ref (src/shared/prRefs.ts). This is what makes previewing a PR
// work without an API token, without a second remote and without knowing the
// fork's URL: the PR head is mirrored on the *target* repository under a
// well-known ref. Hosts that copy GitHub's layout (Gitea, Forgejo, Gogs,
// GitHub Enterprise) fall out for free.
var bPrConventions = []struct {
	flavor string
	ref    func(n int) string
}{
	// GitHub, Gitea, Forgejo, Gogs, GHES.
	{"github", func(n int) string { return "refs/pull/" + strconv.Itoa(n) + "/head" }},
	// GitLab, cloud and self-hosted.
	{"gitlab", func(n int) string { return "refs/merge-requests/" + strconv.Itoa(n) + "/head" }},
	// Bitbucket Cloud and Bitbucket Server / Data Center.
	{"bitbucket", func(n int) string { return "refs/pull-requests/" + strconv.Itoa(n) + "/from" }},
	// Azure DevOps publishes only the pre-merged result, never a bare head.
	{"azure", func(n int) string { return "refs/pull/" + strconv.Itoa(n) + "/merge" }},
}

var (
	bFlavorGitlabRe    = regexp.MustCompile(`(?i)gitlab`)
	bFlavorBitbucketRe = regexp.MustCompile(`(?i)bitbucket`)
	bFlavorAzureRe     = regexp.MustCompile(`(?i)dev\.azure\.com|visualstudio\.com`)
	bFlavorGithubRe    = regexp.MustCompile(`(?i)github|gitea|forgejo|codeberg`)
)

// bFlavorForRemoteURL is flavorForRemoteUrl: the best guess at a remote's
// forge from its URL alone, "" when unknown.
func bFlavorForRemoteURL(url string) string {
	switch {
	case url == "":
		return ""
	case bFlavorGitlabRe.MatchString(url):
		return "gitlab"
	case bFlavorBitbucketRe.MatchString(url):
		return "bitbucket"
	case bFlavorAzureRe.MatchString(url):
		return "azure"
	case bFlavorGithubRe.MatchString(url):
		return "github"
	}
	return ""
}

// bPrRefCandidates is prRefCandidates: every ref a PR numbered `number` could
// live under, most likely first. All four are always returned — the URL hint
// only reorders them. A self-hosted GitLab on a vanity domain looks like
// nothing in particular, and probing the whole set costs one `ls-remote`
// either way. (Pure logic shared with the renderer: worth hoisting into
// internal/shared with its vitest vectors.)
func bPrRefCandidates(number int, remoteURL string) []PrRefCandidate {
	out := make([]PrRefCandidate, 0, len(bPrConventions))
	for _, c := range bPrConventions {
		out = append(out, PrRefCandidate{Flavor: c.flavor, Ref: c.ref(number)})
	}
	if hint := bFlavorForRemoteURL(remoteURL); hint != "" {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Flavor == hint && out[j].Flavor != hint })
	}
	return out
}

// gitsvc: resolvePrRef
//
// Find which ref a pull request's head lives under on remote. null when none
// of the conventions has it (the remote does not mirror PR heads, or the
// number does not exist).
//
// Deliberately **not** a read: WithRemoteAuth rewrites `.git/config` to
// inject the PAT, so two of these under the shared read lock would race for
// `.git/config.lock` and one would lose.
func (s *GitService) ResolvePrRef(ctx context.Context, repoPath, remote string, number int) (*PrRefProbe, error) {
	return locked(s, ctx, "resolvePrRef", repoPath, func(ctx context.Context) (*PrRefProbe, error) {
		candidates := bPrRefCandidates(number, gitx.GetRemoteURL(ctx, repoPath, remote))
		var out string
		err := gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
			args := []string{"ls-remote", remote}
			for _, c := range candidates {
				args = append(args, c.Ref)
			}
			var err error
			out, err = bGit(ctx, repoPath, args...)
			return err
		})
		if err != nil {
			return nil, err
		}
		found := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(line, "\t")
			if len(f) > 1 && f[0] != "" && f[1] != "" {
				found[gitx.TrimJS(f[1])] = gitx.TrimJS(f[0])
			}
		}
		// Candidate order is priority order, so the first hit is the best guess.
		for _, c := range candidates {
			if sha := found[c.Ref]; sha != "" {
				return &PrRefProbe{Flavor: c.Flavor, Ref: c.Ref, Sha: sha}, nil
			}
		}
		return nil, nil
	}, repoPath, remote, number)
}

// PrPreviewResult is PrPreviewResult in src/shared/types.ts.
type PrPreviewResult struct {
	Ref  string `json:"ref"`
	Sha  string `json:"sha"`
	Mode string `json:"mode"`
	// LocalBranch is set for `checkout` — the local branch the ref was fetched into.
	LocalBranch string `json:"localBranch,omitempty"`
	// Conflicts are the paths a `merge` preview left conflicted; empty when it applied cleanly.
	Conflicts []string `json:"conflicts"`
}

// gitsvc: previewRef
//
// Bring `ref` down from `remote` and apply it locally without writing a
// commit, so the work can be run and then dropped without a trace:
//
//   - `checkout` fetches it onto localBranch (reset with `-B`, so previewing
//     the same PR twice reuses the branch) and switches to it.
//   - `merge` merges it into the current branch with `--no-commit --no-ff`,
//     leaving the merged tree staged. Conflicts are reported rather than
//     returned as an error, because a conflicted preview is a useful answer.
//
// The fetch is deliberately `--no-tags`: a preview should not drag the
// remote's tag namespace into the local repository. localBranch "" is
// undefined.
func (s *GitService) PreviewRef(ctx context.Context, repoPath, remote, ref, mode, localBranch string) (PrPreviewResult, error) {
	return locked(s, ctx, "previewRef", repoPath, func(ctx context.Context) (PrPreviewResult, error) {
		err := gitx.WithRemoteAuth(ctx, repoPath, remote, s.token, func(ctx context.Context) error {
			_, err := bGit(ctx, repoPath, "fetch", "--no-tags", remote, ref)
			return err
		})
		if err != nil {
			return PrPreviewResult{}, err
		}
		shaOut, err := bGit(ctx, repoPath, "rev-parse", "FETCH_HEAD")
		if err != nil {
			return PrPreviewResult{}, err
		}
		sha := gitx.TrimJS(shaOut)

		if mode == "checkout" {
			if localBranch == "" {
				return PrPreviewResult{}, bErr("A local branch name is required to check out a preview")
			}
			if _, err := bGit(ctx, repoPath, "checkout", "-B", localBranch, sha); err != nil {
				return PrPreviewResult{}, err
			}
			return PrPreviewResult{Ref: ref, Sha: sha, Mode: mode, LocalBranch: localBranch, Conflicts: []string{}}, nil
		}

		// Run the merge through RunGit, not Raw: Raw *succeeds* on a conflicted
		// merge whose stderr is empty, which would report a clean preview over a
		// conflicted tree. A non-zero exit here is expected — the conflicted
		// tree it leaves behind is exactly what the user asked to see, so read
		// the conflicts back instead of surfacing git's error.
		if _, mergeErr := bGit(ctx, repoPath, "merge", "--no-commit", "--no-ff", sha); mergeErr != nil {
			out, err := bGit(ctx, repoPath, "diff", "--name-only", "--diff-filter=U")
			if err != nil {
				return PrPreviewResult{}, err
			}
			conflicts := gitx.SplitTrimmedLines(out)
			if len(conflicts) == 0 {
				return PrPreviewResult{}, mergeErr
			}
			return PrPreviewResult{Ref: ref, Sha: sha, Mode: mode, Conflicts: conflicts}, nil
		}
		return PrPreviewResult{Ref: ref, Sha: sha, Mode: mode, Conflicts: []string{}}, nil
	}, repoPath, remote, ref, mode)
}
