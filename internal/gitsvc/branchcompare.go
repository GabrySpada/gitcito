package gitsvc

// Branch comparison: what each side has that the other lacks, and the diff
// since their merge base.
//
// Renderer channels served (through the 'git' dispatcher): compareBranches.

import (
	"context"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// BranchCompareResult is BranchCompareResult in src/shared/types.ts.
type BranchCompareResult struct {
	AheadCommits  []GraphCommit `json:"aheadCommits"`
	BehindCommits []GraphCommit `json:"behindCommits"`
	Diff          string        `json:"diff"`
}

// bCompareLog lists `range` as graph commits; an unknown ref is an empty list.
func bCompareLog(ctx context.Context, repoPath, rng string) []GraphCommit {
	format := "--pretty=format:%H" + gitx.SEP + "%P" + gitx.SEP + "%an" + gitx.SEP + "%ae" + gitx.SEP +
		"%at" + gitx.SEP + "%D" + gitx.SEP + "%s" + gitx.REC
	out := bRawOr(ctx, repoPath, "", "log", rng, format)
	commits := []GraphCommit{}
	for _, rec := range strings.Split(out, gitx.REC) {
		rec = gitx.TrimJS(rec)
		if rec == "" {
			continue
		}
		f := bFields(rec, gitx.SEP, 7)
		parents := []string{}
		for _, p := range strings.Split(f[1], " ") {
			if p != "" {
				parents = append(parents, p)
			}
		}
		refs := []string{}
		if f[5] != "" {
			for _, r := range strings.Split(f[5], ",") {
				if r = gitx.TrimJS(r); r != "" {
					refs = append(refs, r)
				}
			}
		}
		commits = append(commits, GraphCommit{
			Hash: f[0], Parents: parents, Author: f[2], Email: f[3], Date: bJSInt(f[4]), Refs: refs, Subject: f[6],
		})
	}
	return commits
}

// gitsvc: compareBranches
func (s *GitService) CompareBranches(ctx context.Context, repoPath, a, b string) (BranchCompareResult, error) {
	return locked(s, ctx, "compareBranches", repoPath, func(ctx context.Context) (BranchCompareResult, error) {
		return BranchCompareResult{
			AheadCommits:  bCompareLog(ctx, repoPath, b+".."+a),
			BehindCommits: bCompareLog(ctx, repoPath, a+".."+b),
			Diff:          bRawOr(ctx, repoPath, "", "diff", b+"..."+a),
		}, nil
	})
}
