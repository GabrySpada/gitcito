package gitsvc

// Slice A — opening a repo, working-tree status, staging (whole files and
// patches), Mission Control's pulse/detail rows, the CHANGELOG writer and the
// git version. Renderer channel: the `'git'` dispatcher, methods open, status,
// stage, stageAll, unstage, unstageAll, stagePatch, repoPulse, repoDetail,
// writeChangelogFile, version.

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: open
func (s *GitService) Open(ctx context.Context, repoPath string) (RepoSummary, error) {
	return locked(s, ctx, "open", repoPath, func(ctx context.Context) (RepoSummary, error) {
		isRepo, err := aCheckIsRepo(ctx, repoPath)
		if err != nil {
			return RepoSummary{}, err
		}
		if !isRepo {
			return RepoSummary{}, &aMessageError{"Not a git repository: " + repoPath}
		}
		current := "HEAD"
		if out, err := gitx.Raw(ctx, repoPath, []string{"rev-parse", "--abbrev-ref", "HEAD"}, gitx.RunOpts{}); err == nil {
			current = gitx.TrimJS(out)
		} // else: empty repo
		return RepoSummary{Path: repoPath, Name: aBasename(repoPath), Current: current}, nil
	})
}

// aMessageError is `new Error(message)`.
type aMessageError struct{ msg string }

func (e *aMessageError) Error() string { return e.msg }

// gitsvc: status
func (s *GitService) Status(ctx context.Context, repoPath string) (RepoStatus, error) {
	return locked(s, ctx, "status", repoPath, func(ctx context.Context) (RepoStatus, error) {
		return aStatus(ctx, repoPath)
	})
}

func aStatus(ctx context.Context, repoPath string) (RepoStatus, error) {
	st, err := aStatusScan(ctx, repoPath)
	if err != nil {
		return RepoStatus{}, err
	}
	conflictPaths := map[string]bool{}
	for _, p := range st.Conflicted {
		conflictPaths[p] = true
	}
	out := RepoStatus{Tracking: st.Tracking, Ahead: st.Ahead, Behind: st.Behind,
		Staged: []FileEntry{}, Unstaged: []FileEntry{}, Conflicted: []FileEntry{}}
	if st.Current != nil {
		out.Current = *st.Current
	}
	for _, f := range st.Files {
		if conflictPaths[f.Path] {
			out.Conflicted = append(out.Conflicted, FileEntry{Path: f.Path, Status: "U"})
			continue
		}
		index := gitx.TrimJS(f.Index)
		work := gitx.TrimJS(f.WorkingDir)
		if f.Index == "?" || f.WorkingDir == "?" {
			out.Unstaged = append(out.Unstaged, FileEntry{Path: f.Path, Status: "?", Untracked: true})
			continue
		}
		if index != "" && index != "?" {
			out.Staged = append(out.Staged, FileEntry{Path: f.Path, Status: gitx.MapStatusCode(index)})
		}
		if work != "" && work != "?" {
			out.Unstaged = append(out.Unstaged, FileEntry{Path: f.Path, Status: gitx.MapStatusCode(work)})
		}
	}
	return out, nil
}

// gitsvc: stage
func (s *GitService) Stage(ctx context.Context, repoPath string, files []string) error {
	return lockedErr(s, ctx, "stage", repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, append([]string{"add"}, files...), gitx.RunOpts{})
		return err
	})
}

// gitsvc: stageAll
func (s *GitService) StageAll(ctx context.Context, repoPath string) error {
	return lockedErr(s, ctx, "stageAll", repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, []string{"add", "-A"}, gitx.RunOpts{})
		return err
	})
}

// gitsvc: unstage
func (s *GitService) Unstage(ctx context.Context, repoPath string, files []string) error {
	return lockedErr(s, ctx, "unstage", repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, append([]string{"restore", "--staged", "--"}, files...), gitx.RunOpts{})
		return err
	})
}

// gitsvc: unstageAll
func (s *GitService) UnstageAll(ctx context.Context, repoPath string) error {
	return lockedErr(s, ctx, "unstageAll", repoPath, func(ctx context.Context) error {
		_, err := gitx.Raw(ctx, repoPath, []string{"reset", "HEAD", "--", "."}, gitx.RunOpts{})
		return err
	})
}

// gitsvc: stagePatch
//
// Apply a patch to the index only. `reverse` takes it back out — how a line or
// hunk of the staged diff is unstaged, and how a stage is undone.
func (s *GitService) StagePatch(ctx context.Context, repoPath, patch string, reverse bool) error {
	return lockedErr(s, ctx, "stagePatch", repoPath, func(ctx context.Context) error {
		return aStagePatch(ctx, repoPath, patch, reverse)
	})
}

func aStagePatch(ctx context.Context, repoPath, patch string, reverse bool) error {
	// TS named the file gitcito-patch-<Date.now()>.patch; CreateTemp keeps the
	// prefix and cannot collide between two repos patching in the same ms.
	f, err := os.CreateTemp("", "gitcito-patch-*.patch")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(patch); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	args := []string{"apply", "--cached"}
	if reverse {
		args = append(args, "--reverse")
	}
	_, err = gitx.Raw(ctx, repoPath, append(args, tmp), gitx.RunOpts{})
	return err
}

var aBranchABRe = regexp.MustCompile(`\+(\d+) -(\d+)`)

// gitsvc: repoPulse
//
// A cheap, purely local health check for one repository — the row Mission
// Control shows for it.
//
// `status --porcelain=v2 --branch` answers branch, upstream, ahead/behind and
// every dirty path in a single process, so a dashboard over twenty repos
// costs twenty git calls rather than a hundred. Nothing here touches the
// network: no fetch, no host API.
func (s *GitService) RepoPulse(ctx context.Context, repoPath string) (RepoPulse, error) {
	return locked(s, ctx, "repoPulse", repoPath, func(ctx context.Context) (RepoPulse, error) {
		return aRepoPulse(ctx, repoPath), nil
	})
}

func aRepoPulse(ctx context.Context, repoPath string) RepoPulse {
	pulse := RepoPulse{Path: repoPath, Name: aBasename(repoPath), Activity: []int{}}

	out, err := gitx.RunGit(ctx, repoPath, []string{"status", "--porcelain=v2", "--branch", "-z"}, gitx.RunOpts{})
	if err != nil {
		pulse.Error = aStrPtr(err.Error())
		return pulse
	}
	for _, line := range strings.Split(out, "\x00") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "# branch.head "):
			head := gitx.TrimJS(strings.TrimPrefix(line, "# branch.head "))
			if head != "(detached)" {
				pulse.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			pulse.Upstream = aStrPtr(gitx.TrimJS(strings.TrimPrefix(line, "# branch.upstream ")))
		case strings.HasPrefix(line, "# branch.ab "):
			if m := aBranchABRe.FindStringSubmatch(line); m != nil {
				pulse.Ahead, _ = strconv.Atoi(m[1])
				pulse.Behind, _ = strconv.Atoi(m[2])
			}
		case strings.HasPrefix(line, "u "):
			pulse.Conflicted++
		case strings.HasPrefix(line, "? "):
			pulse.Untracked++
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			// `<XY>` staged/worktree status pair, e.g. "1 .M N... ".
			if len(line) <= 2 || line[2] != '.' {
				pulse.Staged++
			}
			if len(line) <= 3 || line[3] != '.' {
				pulse.Unstaged++
			}
		}
	}

	var wg sync.WaitGroup
	var stashes, lastCommit string
	var operation *string
	wg.Add(3)
	go func() {
		defer wg.Done()
		stashes, _ = gitx.RunGit(ctx, repoPath, []string{"stash", "list"}, gitx.RunOpts{})
	}()
	go func() {
		defer wg.Done()
		lastCommit, _ = gitx.RunGit(ctx, repoPath, []string{"log", "-1", "--format=%ct"}, gitx.RunOpts{})
	}()
	go func() {
		defer wg.Done()
		if op, err := aMergeState(ctx, repoPath); err == nil {
			operation = op
		}
	}()
	wg.Wait()
	pulse.Stashes = len(gitx.SplitLines(stashes))
	pulse.LastCommitAt = aJSInt(lastCommit)
	pulse.Operation = operation

	// Commits per day for the last fortnight, oldest bucket first — the row's
	// sparkline. One extra cheap `log`, and only timestamps come back.
	since := time.Now().Unix() - gitx.ActivityDays*86400
	stamps, _ := gitx.RunGit(ctx, repoPath, []string{"log", "--since=@" + strconv.FormatInt(since, 10), "--format=%ct"}, gitx.RunOpts{})
	buckets := make([]int, gitx.ActivityDays)
	for _, line := range strings.Split(stamps, "\n") {
		at := aJSNumber(line)
		if at == 0 || math.IsNaN(at) {
			continue
		}
		nowSec := float64(time.Now().UnixMilli()) / 1000
		day := int(math.Floor((nowSec - at) / 86400))
		if day >= 0 && day < gitx.ActivityDays {
			buckets[gitx.ActivityDays-1-day]++
		}
	}
	pulse.Activity = buckets
	return pulse
}

// gitsvc: repoDetail
//
// The extra detail one row expands to show: what is dirty and what is waiting
// to be pushed. Only fetched for the row the user actually opens, so a
// dashboard of twenty repos doesn't pay for twenty of these. max defaults to 8.
func (s *GitService) RepoDetail(ctx context.Context, repoPath string, max *int) (RepoDetail, error) {
	return locked(s, ctx, "repoDetail", repoPath, func(ctx context.Context) (RepoDetail, error) {
		n := 8
		if max != nil {
			n = *max
		}
		var status *RepoStatus
		var unpushed string
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if st, err := aStatus(ctx, repoPath); err == nil {
				status = &st
			}
		}()
		go func() {
			defer wg.Done()
			// No shell involved, so the revspec goes through verbatim; a branch
			// with no upstream simply yields nothing.
			unpushed, _ = gitx.RunGit(ctx, repoPath,
				[]string{"log", "--format=%h%x1f%s", "--max-count=" + strconv.Itoa(n), "@{upstream}..HEAD"}, gitx.RunOpts{})
		}()
		wg.Wait()
		all := []FileEntry{}
		if status != nil {
			all = append(append(append(all, status.Conflicted...), status.Staged...), status.Unstaged...)
		}
		detail := RepoDetail{Files: []RepoDetailFile{}, Commits: []RepoDetailCommit{}}
		for _, f := range all[:aSliceEnd(n, len(all))] {
			detail.Files = append(detail.Files, RepoDetailFile{Path: f.Path, Status: f.Status})
		}
		for _, line := range gitx.SplitLines(unpushed) {
			parts := strings.Split(line, gitx.SEP)
			detail.Commits = append(detail.Commits, RepoDetailCommit{Hash: parts[0], Subject: aField(parts, 1)})
		}
		return detail, nil
	})
}

// gitsvc: writeChangelogFile
//
// Prepend a changelog block to CHANGELOG.md (created if absent).
func (s *GitService) WriteChangelogFile(ctx context.Context, repoPath, markdown string) error {
	return lockedErr(s, ctx, "writeChangelogFile", repoPath, func(ctx context.Context) error {
		file := filepath.Join(repoPath, "CHANGELOG.md")
		existing, err := aReadFile(file)
		if err != nil {
			existing = ""
		}
		const header = "# Changelog\n\n"
		body := strings.TrimPrefix(existing, header)
		return aWriteFile(file, gitx.TrimEndJS(header+gitx.TrimEndJS(markdown)+"\n\n"+body)+"\n")
	})
}

// gitsvc: version
func (s *GitService) Version(ctx context.Context) (string, error) {
	return lockedNoRepo(s, ctx, "version", func(ctx context.Context) (string, error) {
		// simpleGit() with no base dir: the process cwd.
		out, err := gitx.Raw(ctx, "", []string{"--version"}, gitx.RunOpts{})
		if err != nil {
			var ge *gitx.GitError
			if errors.As(err, &ge) && ge.Exec != nil && !ge.Exec.Started() {
				return "0.0.0", nil // git not installed
			}
			return "", err
		}
		return aParseVersion(out), nil
	})
}
