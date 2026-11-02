package gitsvc

// Slice A — the radars: Conflict Radar (mergePreview) and Teammate Radar.
// Renderer channel: the `'git'` dispatcher, methods mergePreview,
// teammateRadar.

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// gitsvc: mergePreview
//
// Conflict Radar — merge every ref into `base` inside the object database and
// report which ones would conflict. `git merge-tree --write-tree` writes only
// loose objects: no index, no working tree, no refs, no checkout, so this is
// safe to run over every branch while the user keeps working.
func (s *GitService) MergePreview(ctx context.Context, repoPath, base string, refs []string) (MergePreviewResult, error) {
	return locked(s, ctx, "mergePreview", repoPath, func(ctx context.Context) (MergePreviewResult, error) {
		return aMergePreview(ctx, repoPath, base, refs)
	})
}

func aMergePreview(ctx context.Context, repoPath, base string, refs []string) (MergePreviewResult, error) {
	baseOut, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", base}, gitx.RunOpts{})
	if err != nil {
		return MergePreviewResult{}, err
	}
	treeOut, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", base + "^{tree}"}, gitx.RunOpts{})
	if err != nil {
		return MergePreviewResult{}, err
	}
	baseSha, baseTree := gitx.TrimJS(baseOut), gitx.TrimJS(treeOut)
	// Refs never contain whitespace; anything that does would corrupt the
	// one-merge-per-line stdin protocol.
	wanted := []string{}
	seen := map[string]bool{}
	for _, r := range refs {
		if r != "" && !aHasJSSpace(r) && !seen[r] {
			seen[r] = true
			wanted = append(wanted, r)
		}
	}

	// One process for the whole batch. git aborts the stream on the first fatal
	// merge (unknown ref, unrelated histories), so short results are re-run one
	// ref at a time below rather than being reported as failures.
	records := []aMergeTreeRecord{}
	if len(wanted) > 0 {
		var stdin strings.Builder
		for _, r := range wanted {
			stdin.WriteString(baseSha + " " + r + "\n")
		}
		out := gitx.WithStdin(ctx, repoPath, []string{"merge-tree", "--stdin", "--name-only"}, stdin.String())
		records = aParseMergeTreeStdin(out)
	}

	entries := []MergePreviewEntry{}
	for i, ref := range wanted {
		var rec aMergeTreeRecord
		if i < len(records) {
			rec = records[i]
		} else {
			rec = aSingleMergeTree(ctx, repoPath, baseSha, ref)
		}
		// A merge whose result is the base tree changes nothing — the ref is
		// already contained in the base.
		status := rec.Status
		if rec.Status == "clean" && rec.Tree == baseTree {
			status = "merged"
		}
		e := MergePreviewEntry{Ref: ref, Status: status, Files: rec.Files}
		if rec.Status == "error" {
			e.Message = aOr(rec.Message, "merge-tree failed")
		}
		entries = append(entries, e)
	}
	return MergePreviewResult{Base: base, BaseSha: baseSha, Entries: entries, ScannedAt: time.Now().UnixMilli()}, nil
}

// aMergeTreeExec runs one `git merge-tree --write-tree` and reads it the way
// singleMergeTree/cherryPickTree did: exit 1 (conflict) is a result, not an
// error; a run that did not exit normally counts as exit 2.
func aMergeTreeExec(ctx context.Context, repoPath string, args []string) aMergeTreeRecord {
	res, err := gitx.Run(ctx, repoPath, args, gitx.RunOpts{MaxBuffer: 16 * 1024 * 1024})
	if err == nil {
		return aParseMergeTreeSingle(res.Stdout, 0, "")
	}
	code, ok := gitx.ExitCode(err)
	if !ok {
		code = 2
	}
	return aParseMergeTreeSingle(res.Stdout, code, aOr(res.Stderr, err.Error()))
}

// aSingleMergeTree is one in-memory merge of ref into base, used to fill gaps
// left by a batch abort.
func aSingleMergeTree(ctx context.Context, repoPath, base, ref string) aMergeTreeRecord {
	return aMergeTreeExec(ctx, repoPath, []string{"merge-tree", "--write-tree", "--name-only", "--messages", base, ref})
}

// gitsvc: teammateRadar
//
// Teammate radar: remote awareness computed entirely from the last fetch.
// For every recently-moved remote branch with commits HEAD does not have,
// report who moved it, which files those commits touch, which of them are
// dirty in the local working tree right now, and whether merging the branch
// into HEAD would conflict (batched in-memory merge-tree). No network.
func (s *GitService) TeammateRadar(ctx context.Context, repoPath string, opts *TeammateRadarOpts) (TeammateRadarResult, error) {
	return locked(s, ctx, "teammateRadar", repoPath, func(ctx context.Context) (TeammateRadarResult, error) {
		maxBranches, maxDays := 30, 45.0
		if opts != nil && opts.MaxBranches != nil {
			maxBranches = *opts.MaxBranches
		}
		if opts != nil && opts.MaxDays != nil {
			maxDays = *opts.MaxDays
		}
		maxAgeSec := maxDays * 86400
		empty := TeammateRadarResult{Entries: []TeammateRadarEntry{}, ScannedAt: time.Now().UnixMilli()}

		// Unborn HEAD → nothing to compare against.
		headOut, _ := gitx.RunGit(ctx, repoPath, []string{"rev-parse", "--verify", "HEAD"}, gitx.RunOpts{})
		head := gitx.TrimJS(headOut)
		if head == "" {
			return empty, nil
		}

		// One call: every remote tip with its last committer and date, newest first.
		raw, _ := gitx.RunGit(ctx, repoPath, []string{"for-each-ref", "--sort=-committerdate",
			"--format=%(refname:short)|%(objectname:short)|%(committerdate:unix)|%(committername)", "refs/remotes"},
			gitx.RunOpts{})
		now := float64(time.Now().Unix())
		type tip struct {
			ref, sha, author string
			time             float64
		}
		var tips []tip
		for _, line := range strings.Split(raw, "\n") {
			if gitx.TrimJS(line) == "" {
				continue
			}
			// committername may itself contain '|' in pathological configs —
			// keep it last and rejoin.
			f := strings.Split(line, "|")
			ref := f[0]
			// Symbolic origin/HEAD shortens to just "origin" — a real remote
			// branch short name always carries a slash.
			if strings.HasSuffix(ref, "/HEAD") || !strings.Contains(ref, "/") {
				continue
			}
			t := aJSNumber(aField(f, 2))
			if now-t > maxAgeSec {
				continue
			}
			author := ""
			if len(f) > 3 {
				author = strings.Join(f[3:], "|")
			}
			tips = append(tips, tip{ref: ref, sha: aField(f, 1), time: t, author: author})
			if len(tips) >= maxBranches {
				break
			}
		}
		if len(tips) == 0 {
			return empty, nil
		}

		dirty := map[string]bool{}
		dirtyCount := 0
		if st, err := aSimpleStatus(ctx, repoPath); err == nil {
			for _, f := range st.Files {
				if !dirty[f.Path] {
					dirty[f.Path] = true
					dirtyCount++
				}
			}
		}

		entries := []TeammateRadarEntry{}
		for _, t := range tips {
			countOut, err := gitx.RunGit(ctx, repoPath, []string{"rev-list", "--count", head + ".." + t.ref}, gitx.RunOpts{})
			if err != nil {
				countOut = "0"
			}
			ahead := aJSNumber(countOut)
			if ahead == 0 || math.IsNaN(ahead) {
				continue
			}
			// Three-dot diff = what the branch would bring in, measured from the merge base.
			diffOut, _ := gitx.RunGit(ctx, repoPath, []string{"diff", "--name-only", head + "..." + t.ref}, gitx.RunOpts{})
			files := gitx.SplitLines(diffOut)
			overlap := []string{}
			for _, f := range files {
				if dirty[f] {
					overlap = append(overlap, f)
				}
			}
			entries = append(entries, TeammateRadarEntry{
				Ref: t.ref, Sha: t.sha, Author: t.author, Time: int64(t.time), Ahead: int(ahead),
				FilesTouched: len(files), Overlap: overlap, Risk: "clean", ConflictFiles: []string{},
			})
		}

		// One batched merge-tree for the conflict forecast.
		if len(entries) > 0 {
			refs := make([]string, len(entries))
			for i, e := range entries {
				refs[i] = e.Ref
			}
			if preview, err := aMergePreview(ctx, repoPath, "HEAD", refs); err == nil {
				for _, p := range preview.Entries {
					for i := range entries {
						if entries[i].Ref != p.Ref {
							continue
						}
						entries[i].Risk = p.Status
						if p.Status == "conflict" {
							entries[i].ConflictFiles = p.Files
						} else {
							entries[i].ConflictFiles = []string{}
						}
						break
					}
				}
			}
		}

		// Most collision-prone first: overlap, then predicted conflicts, then recency.
		isConflict := func(e TeammateRadarEntry) int {
			if e.Risk == "conflict" {
				return 1
			}
			return 0
		}
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			if d := len(b.Overlap) - len(a.Overlap); d != 0 {
				return d < 0
			}
			if d := isConflict(b) - isConflict(a); d != 0 {
				return d < 0
			}
			return b.Time-a.Time < 0
		})
		return TeammateRadarResult{Entries: entries, DirtyCount: dirtyCount, ScannedAt: time.Now().UnixMilli()}, nil
	})
}
