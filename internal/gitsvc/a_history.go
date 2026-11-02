package gitsvc

// Slice A — history reads: the commit graph (log, stashBaseShas), branches,
// range-diff and the reflog readers. Renderer channel: the `'git'` dispatcher,
// methods stashBaseShas, log, branches, rangeDiff, refTips, reflog.

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: stashBaseShas
//
// First-parent SHAs of every stash entry — the commits each stash was saved
// on top of. Used to seed `log()` so unreachable stash bases still appear in
// the graph and their stashes have something to connect to. Best-effort: a
// repo with no stashes (or a failure listing them) simply yields none.
func (s *GitService) StashBaseShas(ctx context.Context, repoPath string) ([]string, error) {
	return locked(s, ctx, "stashBaseShas", repoPath, func(ctx context.Context) ([]string, error) {
		return aStashBaseShas(ctx, repoPath), nil
	})
}

func aStashBaseShas(ctx context.Context, repoPath string) []string {
	out, err := gitx.RunGit(ctx, repoPath, []string{"stash", "list", "--pretty=format:%P"}, gitx.RunOpts{})
	bases := []string{}
	if err != nil {
		return bases
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		first := strings.Split(gitx.TrimJS(line), " ")[0]
		if first != "" && !seen[first] {
			seen[first] = true
			bases = append(bases, first)
		}
	}
	return bases
}

// gitsvc: log
//
// `skip` pages deeper into history so "load more" fetches only the next
// window instead of re-reading everything already loaded. Ordering is stable
// (--date-order over the same revs), and the store dedupes by hash, so a
// page boundary shifting under new commits cannot duplicate rows.
// maxCount defaults to 400 when nil.
func (s *GitService) Log(ctx context.Context, repoPath string, maxCount *int, skip int) ([]GraphCommit, error) {
	return locked(s, ctx, "log", repoPath, func(ctx context.Context) ([]GraphCommit, error) {
		n := 400
		if maxCount != nil {
			n = *maxCount
		}
		return aLog(ctx, repoPath, n, skip), nil
	})
}

func aLog(ctx context.Context, repoPath string, maxCount, skip int) []GraphCommit {
	// Stash base commits are frequently unreachable from any branch (a stash is
	// the only thing pointing at them). Feed those bases in as explicit revs so
	// the graph can show where each stash was taken from, instead of leaving the
	// stash floating with no parent. `--ignore-missing` keeps a stale/pruned
	// base from failing the whole log.
	stashBases := aStashBaseShas(ctx, repoPath)
	sep := gitx.SEP
	args := []string{
		"log",
		// Real refs only — excludes `refs/original/*` filter-branch backups and
		// other internal refs that `--all` would surface as ghost lanes.
		"--branches", "--tags", "--remotes", "HEAD",
	}
	args = append(args, stashBases...)
	args = append(args, "--ignore-missing", "--date-order", "--max-count="+strconv.Itoa(maxCount))
	if skip > 0 {
		args = append(args, "--skip="+strconv.Itoa(skip))
	}
	args = append(args, "--pretty=format:%H"+sep+"%P"+sep+"%an"+sep+"%ae"+sep+"%at"+sep+"%D"+sep+"%s"+sep+
		"%(trailers:key=Co-authored-by,valueonly,separator=%x1d)"+sep+"%G?"+sep+"%GS"+gitx.REC)
	// The `%G?`/`%GS` signature placeholders force git to load the gpg config to
	// verify each commit. A malformed value (e.g. an invalid `gpg.format` in the
	// user's global gitconfig) makes `git log` exit non-zero *after* it has
	// already written the commit records to stdout. Salvage that stdout so the
	// whole graph isn't blanked to "No commits yet" over a signing-config quirk;
	// a genuinely empty repository simply yields no output here.
	res, _ := gitx.Run(ctx, repoPath, args, gitx.RunOpts{PlainEnv: true, MaxBuffer: 64 * 1024 * 1024})
	raw := res.Stdout
	commits := []GraphCommit{}
	if gitx.TrimJS(raw) == "" {
		return commits // empty repository
	}
	for _, r := range strings.Split(raw, gitx.REC) {
		rec := gitx.TrimJS(r)
		if rec == "" {
			continue
		}
		f := strings.Split(rec, sep)
		c := GraphCommit{
			Hash:      f[0],
			Parents:   []string{},
			Author:    aField(f, 2),
			Email:     aField(f, 3),
			Date:      aJSInt(aField(f, 4)),
			Refs:      []string{},
			Subject:   aField(f, 6),
			CoAuthors: gitx.ParseCoAuthors(aField(f, 7)),
		}
		if p := aField(f, 1); p != "" {
			for _, x := range strings.Split(p, " ") {
				if x != "" {
					c.Parents = append(c.Parents, x)
				}
			}
		}
		if refs := aField(f, 5); refs != "" {
			for _, x := range strings.Split(refs, ",") {
				if x = gitx.TrimJS(x); x != "" {
					c.Refs = append(c.Refs, x)
				}
			}
		}
		if sig := gitx.MapSignature(aField(f, 8)); sig != "none" {
			c.Signature = sig
		}
		c.Signer = gitx.TrimJS(aField(f, 9))
		commits = append(commits, c)
	}
	return commits
}

// gitsvc: branches
func (s *GitService) Branches(ctx context.Context, repoPath string) (BranchesPayload, error) {
	return locked(s, ctx, "branches", repoPath, func(ctx context.Context) (BranchesPayload, error) {
		return aBranches(ctx, repoPath), nil
	})
}

func aBranches(ctx context.Context, repoPath string) BranchesPayload {
	sep := gitx.SEP
	// Independent reads, so run them together rather than one after another —
	// the per-repo lock is already held by branches() as a whole, and these are
	// the only git processes in flight for this repo.
	read := func(args []string, maxBuf int) string {
		out, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{MaxBuffer: maxBuf})
		if err != nil {
			return ""
		}
		return out
	}
	var current, headSha, localsOut, remotesOut, tagsOut string
	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); current = gitx.TrimJS(read([]string{"rev-parse", "--abbrev-ref", "HEAD"}, 0)) }()
	go func() { defer wg.Done(); headSha = gitx.TrimJS(read([]string{"rev-parse", "HEAD"}, 0)) }()
	go func() {
		defer wg.Done()
		localsOut = read([]string{"for-each-ref",
			"--format=%(refname:short)" + sep + "%(objectname:short)" + sep + "%(upstream:short)" + sep + "%(upstream:track)",
			"refs/heads"}, 0)
	}()
	go func() {
		defer wg.Done()
		remotesOut = read([]string{"for-each-ref", "--format=%(refname)" + sep + "%(objectname:short)" + sep + "%(symref)",
			"refs/remotes"}, 16*1024*1024)
	}()
	go func() {
		defer wg.Done()
		tagsOut = read([]string{"for-each-ref", "--sort=-version:refname",
			"--format=%(refname:short)" + sep + "%(objectname:short)", "refs/tags"}, 16*1024*1024)
	}()
	wg.Wait()

	merged := aMergedIntoHead(ctx, repoPath, headSha, localsOut+remotesOut)

	out := BranchesPayload{Current: current, Locals: []BranchInfo{}, Remotes: []RemoteBranchInfo{}, Tags: []TagInfo{}}
	for _, line := range gitx.SplitLines(localsOut) {
		f := strings.Split(line, sep)
		name := f[0]
		track := gitx.ParseTrack(aField(f, 3))
		var upstream *string
		if u := aField(f, 2); u != "" {
			upstream = &u
		}
		out.Locals = append(out.Locals, BranchInfo{
			Name: name, Sha: aField(f, 1), Upstream: upstream, Ahead: track.Ahead, Behind: track.Behind,
			IsCurrent: name == current, MergedIntoCurrent: merged["refs/heads/"+name],
		})
	}
	for _, line := range gitx.SplitLines(remotesOut) {
		f := strings.Split(line, sep)
		refName := f[0]
		if aField(f, 2) != "" || !strings.HasPrefix(refName, "refs/remotes/") {
			continue
		}
		fullName := strings.TrimPrefix(refName, "refs/remotes/")
		if strings.HasSuffix(fullName, "/HEAD") {
			continue
		}
		slash := strings.IndexByte(fullName, '/')
		if slash <= 0 {
			continue
		}
		out.Remotes = append(out.Remotes, RemoteBranchInfo{
			Remote: fullName[:slash], Name: fullName[slash+1:], FullName: fullName, Sha: aField(f, 1),
			MergedIntoCurrent: merged[refName],
		})
	}
	for _, line := range gitx.SplitLines(tagsOut) {
		f := strings.Split(line, sep)
		out.Tags = append(out.Tags, TagInfo{Name: f[0], Sha: aField(f, 1)})
	}
	return out
}

// gitsvc: rangeDiff
//
// `git range-diff` — pair up the commits of two versions of a branch and show
// how each one was rewritten. This is the answer to "someone force-pushed,
// what actually changed since I reviewed it?", which a plain diff cannot give
// because every commit after a rebase looks brand new.
//
// With `base`, both ranges are taken from it (`base..old` vs `base..new`);
// without it the two versions' merge base is used.
func (s *GitService) RangeDiff(ctx context.Context, repoPath, oldRev, newRev, base string) ([]RangeDiffEntry, error) {
	return locked(s, ctx, "rangeDiff", repoPath, func(ctx context.Context) ([]RangeDiffEntry, error) {
		// Resolve first: git rejects reflog selectors inside the `old...new`
		// form ("need two commit ranges"), and `origin/feature@{1}` is exactly
		// what the reflog picker hands us. (TS resolved both at once and failed
		// with whichever rejected first; the old side's error wins here.)
		oldOut, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", oldRev}, gitx.RunOpts{})
		if err != nil {
			return nil, err
		}
		newOut, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", newRev}, gitx.RunOpts{})
		if err != nil {
			return nil, err
		}
		oldSha, newSha := gitx.TrimJS(oldOut), gitx.TrimJS(newOut)
		// Without an explicit base, take the two versions' common ancestor: it
		// keeps the comparison to the commits that actually differ, and (unlike
		// the `old...new` form) still lists identical commits as unchanged when
		// the branch was rebased onto something newer.
		effectiveBase := gitx.TrimJS(base)
		if effectiveBase == "" {
			if mb, err := gitx.RunGit(ctx, repoPath, []string{"merge-base", oldSha, newSha}, gitx.RunOpts{}); err == nil {
				effectiveBase = gitx.TrimJS(mb)
			}
		}
		args := []string{"range-diff", gitx.CreationFactor, oldSha + "..." + newSha}
		if effectiveBase != "" {
			args = []string{"range-diff", gitx.CreationFactor, effectiveBase, oldSha, newSha}
		}
		out, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
		if err != nil {
			return nil, err
		}
		return aParseRangeDiff(out), nil
	})
}

// gitsvc: refTips
//
// Where a ref has been, newest first, from its reflog — the free record of
// every rebase, reset and forced fetch. Entry 0 is the current tip, so the
// "what changed since…" picker starts at index 1. max defaults to 20.
func (s *GitService) RefTips(ctx context.Context, repoPath, ref string, max *int) ([]RefTip, error) {
	return locked(s, ctx, "refTips", repoPath, func(ctx context.Context) ([]RefTip, error) {
		n := 20
		if max != nil {
			n = *max
		}
		sep := gitx.SEP
		tips := []RefTip{}
		out, err := gitx.RunGit(ctx, repoPath, []string{"reflog", "show", "--max-count=" + strconv.Itoa(n),
			"--format=%H" + sep + "%gd" + sep + "%gs" + sep + "%ct" + sep + "%s", ref}, gitx.RunOpts{})
		if err != nil {
			return tips, nil // no reflog for this ref (a fresh clone's remote refs, say)
		}
		for _, line := range gitx.SplitLines(out) {
			f := strings.Split(line, sep)
			if f[0] == "" {
				continue
			}
			tips = append(tips, RefTip{Sha: f[0], Selector: aField(f, 1), Reason: aField(f, 2),
				Date: aJSInt(aField(f, 3)), Subject: aField(f, 4)})
		}
		// Consecutive entries pointing at the same commit (a checkout, a no-op
		// fetch) would offer the user a comparison against itself.
		kept := []RefTip{}
		for i, t := range tips {
			if i == 0 || t.Sha != tips[i-1].Sha {
				kept = append(kept, t)
			}
		}
		return kept, nil
	})
}

// gitsvc: reflog
//
// Read `git reflog` for a ref (default HEAD). Each entry is a point history
// passed through — checkout/reset/amend/rebase all leave a trace, so this is
// the net for recovering "lost" commits. Restore by checking out, resetting,
// or branching from an entry's sha via the existing reset/checkout/branch ops.
// ref defaults to "HEAD" and max to 200 when nil.
func (s *GitService) Reflog(ctx context.Context, repoPath string, ref *string, max *int) ([]ReflogEntry, error) {
	return locked(s, ctx, "reflog", repoPath, func(ctx context.Context) ([]ReflogEntry, error) {
		r, n := "HEAD", 200
		if ref != nil {
			r = *ref
		}
		if max != nil {
			n = *max
		}
		sep := gitx.SEP
		out, err := gitx.Raw(ctx, repoPath, []string{"reflog", "show", "--max-count=" + strconv.Itoa(n),
			"--format=%H" + sep + "%gD" + sep + "%gs" + sep + "%ct" + gitx.REC, r}, gitx.RunOpts{})
		if err != nil {
			out = ""
		}
		entries := []ReflogEntry{}
		for _, rec := range strings.Split(out, gitx.REC) {
			rec = gitx.TrimJS(rec)
			if rec == "" {
				continue
			}
			f := strings.Split(rec, sep)
			entries = append(entries, ReflogEntry{Sha: f[0], Selector: aField(f, 1), Action: aField(f, 2),
				Date: aJSInt(aField(f, 3))})
		}
		return entries, nil
	})
}
