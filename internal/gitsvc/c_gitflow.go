package gitsvc

import (
	"context"
	"errors"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// ─── git-flow ──────────────────────────────────────────────────────────────
// Stored under the same `gitflow.*` keys the `git flow` CLI writes, so a repo
// set up by either tool reads correctly in the other. Gitcito runs plain git
// commands throughout — the CLI is never required to be installed.

// cGitflowKinds are the flow kinds, in the order a current branch is matched.
var cGitflowKinds = []string{"feature", "release", "hotfix"}

// cPrefixFor is the configured branch prefix for a git-flow kind.
func cPrefixFor(config GitflowConfig, kind string) string {
	switch kind {
	case "feature":
		return config.FeaturePrefix
	case "release":
		return config.ReleasePrefix
	}
	return config.HotfixPrefix
}

// GitflowStatus is what the dialog needs to know about the repository now.
// gitsvc: gitflowStatus
func (s *GitService) GitflowStatus(ctx context.Context, repoPath string) (GitflowStatus, error) {
	return locked(s, ctx, "gitflowStatus", repoPath, func(ctx context.Context) (GitflowStatus, error) {
		return cGitflowStatus(ctx, repoPath), nil
	})
}

func cGitflowStatus(ctx context.Context, repoPath string) GitflowStatus {
	read := func(key string) string {
		return gitx.TrimJS(cRawOr(ctx, repoPath, "", "config", "--get", key))
	}
	or := func(v, fallback string) string {
		if v == "" {
			return fallback
		}
		return v
	}
	master := read("gitflow.branch.master")
	develop := read("gitflow.branch.develop")
	locals := gitx.LocalBranches(ctx, repoPath)

	// Nothing configured yet: propose a layout that matches what is already
	// there rather than one the user then has to correct.
	main := "main"
	if !cContains(locals, "main") && cContains(locals, "master") {
		main = "master"
	}
	config := GitflowConfig{
		Main:             or(master, main),
		Develop:          or(develop, "develop"),
		FeaturePrefix:    or(read("gitflow.prefix.feature"), "feature/"),
		ReleasePrefix:    or(read("gitflow.prefix.release"), "release/"),
		HotfixPrefix:     or(read("gitflow.prefix.hotfix"), "hotfix/"),
		VersionTagPrefix: or(read("gitflow.prefix.versiontag"), "v"),
	}

	current := gitx.TrimJS(cRawOr(ctx, repoPath, "", "rev-parse", "--abbrev-ref", "HEAD"))
	var flow *GitflowFlow
	for _, kind := range cGitflowKinds {
		prefix := cPrefixFor(config, kind)
		if prefix != "" && strings.HasPrefix(current, prefix) {
			flow = &GitflowFlow{Kind: kind, Name: current[len(prefix):]}
			break
		}
	}
	return GitflowStatus{
		Config:      config,
		Initialized: master != "" && develop != "",
		HasMain:     cContains(locals, config.Main),
		HasDevelop:  cContains(locals, config.Develop),
		Current:     current,
		CurrentFlow: flow,
	}
}

// GitflowInit writes the layout and creates the develop branch if it does not
// exist yet.
// gitsvc: gitflowInit
func (s *GitService) GitflowInit(ctx context.Context, repoPath string, config GitflowConfig) error {
	return lockedErr(s, ctx, "gitflowInit", repoPath, func(ctx context.Context) error {
		entries := [][2]string{
			{"gitflow.branch.master", config.Main},
			{"gitflow.branch.develop", config.Develop},
			{"gitflow.prefix.feature", config.FeaturePrefix},
			{"gitflow.prefix.release", config.ReleasePrefix},
			{"gitflow.prefix.hotfix", config.HotfixPrefix},
			{"gitflow.prefix.versiontag", config.VersionTagPrefix},
		}
		for _, e := range entries {
			if _, err := cRaw(ctx, repoPath, "config", e[0], e[1]); err != nil {
				return err
			}
		}
		locals := gitx.LocalBranches(ctx, repoPath)
		if !cContains(locals, config.Main) {
			return errors.New(`Branch "` + config.Main + `" does not exist.`)
		}
		// develop is the one branch this can reasonably create: it starts at main.
		if !cContains(locals, config.Develop) {
			_, err := cRaw(ctx, repoPath, "branch", config.Develop, config.Main)
			return err
		}
		return nil
	})
}

// GitflowStart branches off the right base and checks the new branch out. It
// returns the branch name.
// gitsvc: gitflowStart
func (s *GitService) GitflowStart(ctx context.Context, repoPath, kind, name string) (string, error) {
	return locked(s, ctx, "gitflowStart", repoPath, func(ctx context.Context) (string, error) {
		status := cGitflowStatus(ctx, repoPath)
		branch := cPrefixFor(status.Config, kind) + gitx.TrimJS(name)
		if gitx.TrimJS(name) == "" {
			//lint:ignore ST1005 the message is shown to the user verbatim, as in TS
			return "", errors.New("A name is required.")
		}
		// Features integrate through develop; releases and hotfixes ship from main.
		base := status.Config.Develop
		if kind == "hotfix" {
			base = status.Config.Main
		}
		locals := gitx.LocalBranches(ctx, repoPath)
		if cContains(locals, branch) {
			return "", errors.New(`Branch "` + branch + `" already exists.`)
		}
		if !cContains(locals, base) {
			return "", errors.New(`Branch "` + base + `" does not exist.`)
		}
		if _, err := gitx.RunGit(ctx, repoPath, []string{"checkout", "-b", branch, base}, gitx.RunOpts{}); err != nil {
			return "", err
		}
		return branch, nil
	})
}

// GitflowFinish closes a flow branch: merges it where it belongs, tags a
// release/hotfix, and deletes the branch. It returns the refs as they were, so
// gitflowUndo can put everything back — a merge conflict rolls back and
// leaves nothing behind.
// gitsvc: gitflowFinish
func (s *GitService) GitflowFinish(ctx context.Context, repoPath, kind, name string, opts GitflowFinishOptions) (GitflowSnapshot, error) {
	return locked(s, ctx, "gitflowFinish", repoPath, func(ctx context.Context) (GitflowSnapshot, error) {
		status := cGitflowStatus(ctx, repoPath)
		bare := gitx.TrimJS(name)
		branch := cPrefixFor(status.Config, kind) + bare
		mainBranch, develop := status.Config.Main, status.Config.Develop

		dirty, err := gitx.IsDirty(ctx, repoPath)
		if err != nil {
			return GitflowSnapshot{}, err
		}
		if dirty {
			//lint:ignore ST1005 the message is shown to the user verbatim, as in TS
			return GitflowSnapshot{}, errors.New("Commit or stash your changes before finishing a branch.")
		}

		locals := gitx.LocalBranches(ctx, repoPath)
		if !cContains(locals, branch) {
			return GitflowSnapshot{}, errors.New(`Branch "` + branch + `" does not exist.`)
		}

		// Features land on develop only; releases and hotfixes land on main
		// first, are tagged there, and are then merged back so develop keeps the
		// fixes.
		targets := []string{develop}
		if kind != "feature" {
			targets = []string{mainBranch, develop}
		}
		for _, target := range targets {
			if !cContains(locals, target) {
				return GitflowSnapshot{}, errors.New(`Branch "` + target + `" does not exist.`)
			}
		}

		shaOf := func(ref string) (string, error) {
			out, err := cRaw(ctx, repoPath, "rev-parse", ref)
			return gitx.TrimJS(out), err
		}
		snapshot := GitflowSnapshot{Refs: []GitflowRef{}}
		for _, target := range targets {
			sha, err := shaOf(target)
			if err != nil {
				return GitflowSnapshot{}, err
			}
			snapshot.Refs = append(snapshot.Refs, GitflowRef{Name: target, Sha: sha})
		}
		branchSha, err := shaOf(branch)
		if err != nil {
			return GitflowSnapshot{}, err
		}
		snapshot.Branch = GitflowRef{Name: branch, Sha: branchSha}
		tagName := ""
		if (opts.Tag == nil || *opts.Tag) && kind != "feature" {
			tagName = status.Config.VersionTagPrefix + bare
		}

		run := func(args ...string) error {
			_, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
			return err
		}
		finish := func() error {
			for _, target := range targets {
				if err := run("checkout", target); err != nil {
					return err
				}
				// --no-ff keeps the branch visible in the graph, which is the whole
				// point of the layout: you can see where a feature began and ended.
				if err := run("merge", "--no-ff", "-m", "Merge branch '"+branch+"' into "+target, branch); err != nil {
					return err
				}
				if tagName != "" && target == mainBranch {
					message := gitx.TrimJS(opts.Message)
					if message == "" {
						message = "Release " + bare
					}
					if err := run("tag", "-a", tagName, "-m", message); err != nil {
						return err
					}
					tag := tagName
					snapshot.Tag = &tag
				}
			}
			if opts.DeleteBranch == nil || *opts.DeleteBranch {
				return run("branch", "-d", branch)
			}
			return nil
		}
		if err := finish(); err != nil {
			// A conflicted merge would strand the repo mid-flow. Roll every ref
			// back to where it started and hand the error on untouched (the
			// original failure is the one worth reporting).
			_ = cGitflowUndo(ctx, repoPath, snapshot)
			return GitflowSnapshot{}, err
		}

		// End where the work continues: develop for a feature or release, main
		// for a hotfix that has just shipped.
		end := develop
		if kind == "hotfix" {
			end = mainBranch
		}
		if err := run("checkout", end); err != nil {
			return GitflowSnapshot{}, err
		}
		return snapshot, nil
	})
}

// GitflowUndo puts every ref a finish moved back where it was, drops the tag
// and restores the branch.
// gitsvc: gitflowUndo
func (s *GitService) GitflowUndo(ctx context.Context, repoPath string, snapshot GitflowSnapshot) error {
	return lockedErr(s, ctx, "gitflowUndo", repoPath, func(ctx context.Context) error {
		return cGitflowUndo(ctx, repoPath, snapshot)
	})
}

func cGitflowUndo(ctx context.Context, repoPath string, snapshot GitflowSnapshot) error {
	// Move off anything about to be rewound so the reset below cannot fail.
	_ = cRunGitOr(ctx, repoPath, "", "checkout", "--detach")
	for _, ref := range snapshot.Refs {
		if _, err := cRaw(ctx, repoPath, "update-ref", "refs/heads/"+ref.Name, ref.Sha); err != nil {
			return err
		}
	}
	if snapshot.Tag != nil && *snapshot.Tag != "" {
		_, _ = cRaw(ctx, repoPath, "tag", "-d", *snapshot.Tag)
	}
	if !cContains(gitx.LocalBranches(ctx, repoPath), snapshot.Branch.Name) {
		if _, err := cRaw(ctx, repoPath, "branch", snapshot.Branch.Name, snapshot.Branch.Sha); err != nil {
			return err
		}
	}
	_, err := gitx.RunGit(ctx, repoPath, []string{"checkout", snapshot.Branch.Name}, gitx.RunOpts{})
	return err
}
