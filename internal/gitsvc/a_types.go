package gitsvc

import "github.com/GabrySpada/gitcito/internal/gitx"

// Bridge types for slice A, mirroring src/shared/types.ts field for field.
// JSON tags are the TypeScript property names; `?:` fields are omitempty or
// pointers, `T | null` fields are pointers without omitempty, and every slice
// a method returns is non-nil so it crosses the bridge as `[]`.

// RepoSummary is RepoSummary in src/shared/types.ts.
type RepoSummary struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Current string `json:"current"`
}

// GraphCommit is GraphCommit in src/shared/types.ts.
type GraphCommit struct {
	Hash    string   `json:"hash"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    int64    `json:"date"` // unix seconds
	Refs    []string `json:"refs"`
	Subject string   `json:"subject"`
	// CoAuthors is optional in the TS type. log() always sets it (to [] when
	// there are none) and so sends it; other producers (compareBranches) leave
	// it out. omitzero mirrors both: a nil slice is omitted, an empty one is sent.
	CoAuthors []gitx.CommitAuthor `json:"coAuthors,omitzero"`
	// Signature is omitted when unsigned ('good' | 'unverified' | 'bad' | 'expired').
	Signature string `json:"signature,omitempty"`
	// Signer is the signing identity (%GS), when known.
	Signer string `json:"signer,omitempty"`
}

// BranchInfo is BranchInfo in src/shared/types.ts.
type BranchInfo struct {
	Name              string  `json:"name"`
	Sha               string  `json:"sha"`
	Upstream          *string `json:"upstream"`
	Ahead             int     `json:"ahead"`
	Behind            int     `json:"behind"`
	IsCurrent         bool    `json:"isCurrent"`
	MergedIntoCurrent bool    `json:"mergedIntoCurrent"`
}

// RemoteBranchInfo is RemoteBranchInfo in src/shared/types.ts.
type RemoteBranchInfo struct {
	Remote            string `json:"remote"`
	Name              string `json:"name"`
	FullName          string `json:"fullName"`
	Sha               string `json:"sha"`
	MergedIntoCurrent bool   `json:"mergedIntoCurrent"`
}

// TagInfo is TagInfo in src/shared/types.ts.
type TagInfo struct {
	Name string `json:"name"`
	Sha  string `json:"sha"`
}

// BranchesPayload is BranchesPayload in src/shared/types.ts.
type BranchesPayload struct {
	Current string             `json:"current"`
	Locals  []BranchInfo       `json:"locals"`
	Remotes []RemoteBranchInfo `json:"remotes"`
	Tags    []TagInfo          `json:"tags"`
}

// MergePreviewEntry is MergePreviewEntry in src/shared/types.ts: one
// branch's forecast in the Conflict Radar.
type MergePreviewEntry struct {
	Ref string `json:"ref"`
	// Status is a MergeRiskKind: 'merged' | 'clean' | 'conflict' | 'error'.
	Status string `json:"status"`
	// Files would conflict (only for `conflict`).
	Files []string `json:"files"`
	// Message is git's error text, when Status is `error`.
	Message string `json:"message,omitempty"`
}

// MergePreviewResult is MergePreviewResult in src/shared/types.ts.
type MergePreviewResult struct {
	Base    string              `json:"base"`
	BaseSha string              `json:"baseSha"`
	Entries []MergePreviewEntry `json:"entries"`
	// ScannedAt is unix ms of the scan, so the UI can show "scanned 2m ago".
	ScannedAt int64 `json:"scannedAt"`
}

// TeammateRadarOpts is teammateRadar's optional second argument. A nil field
// takes the default (`??` in TS: only an absent value does — 0 is honoured).
type TeammateRadarOpts struct {
	MaxBranches *int     `json:"maxBranches,omitempty"`
	MaxDays     *float64 `json:"maxDays,omitempty"`
}

// TeammateRadarEntry is TeammateRadarEntry in src/shared/types.ts.
type TeammateRadarEntry struct {
	Ref           string   `json:"ref"`
	Sha           string   `json:"sha"`
	Author        string   `json:"author"`
	Time          int64    `json:"time"`
	Ahead         int      `json:"ahead"`
	FilesTouched  int      `json:"filesTouched"`
	Overlap       []string `json:"overlap"`
	Risk          string   `json:"risk"`
	ConflictFiles []string `json:"conflictFiles"`
}

// TeammateRadarResult is TeammateRadarResult in src/shared/types.ts.
type TeammateRadarResult struct {
	Entries    []TeammateRadarEntry `json:"entries"`
	DirtyCount int                  `json:"dirtyCount"`
	ScannedAt  int64                `json:"scannedAt"` // unix ms
}

// RangeDiffEntry is RangeDiffEntry in src/shared/types.ts.
type RangeDiffEntry struct {
	// Kind is 'unchanged' | 'modified' | 'removed' | 'added'.
	Kind     string  `json:"kind"`
	OldIndex *int    `json:"oldIndex"`
	OldSha   *string `json:"oldSha"`
	NewIndex *int    `json:"newIndex"`
	NewSha   *string `json:"newSha"`
	Subject  string  `json:"subject"`
	Body     string  `json:"body"`
}

// RefTip is RefTip in src/shared/types.ts.
type RefTip struct {
	Sha      string `json:"sha"`
	Selector string `json:"selector"`
	Reason   string `json:"reason"`
	Date     int64  `json:"date"`
	Subject  string `json:"subject"`
}

// RepoPulse is RepoPulse in src/shared/types.ts.
type RepoPulse struct {
	Path         string  `json:"path"`
	Name         string  `json:"name"`
	Branch       string  `json:"branch"`
	Upstream     *string `json:"upstream"`
	Ahead        int     `json:"ahead"`
	Behind       int     `json:"behind"`
	Staged       int     `json:"staged"`
	Unstaged     int     `json:"unstaged"`
	Untracked    int     `json:"untracked"`
	Conflicted   int     `json:"conflicted"`
	Stashes      int     `json:"stashes"`
	LastCommitAt int64   `json:"lastCommitAt"`
	Operation    *string `json:"operation"`
	Activity     []int   `json:"activity"`
	Error        *string `json:"error"`
}

// RepoDetailFile is one entry of RepoDetail.files.
type RepoDetailFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// RepoDetailCommit is one entry of RepoDetail.commits.
type RepoDetailCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// RepoDetail is RepoDetail in src/shared/types.ts.
type RepoDetail struct {
	Files   []RepoDetailFile   `json:"files"`
	Commits []RepoDetailCommit `json:"commits"`
}

// FileEntry is FileEntry in src/shared/types.ts.
type FileEntry struct {
	Path string `json:"path"`
	// Status is a FileChangeKind: 'A' | 'M' | 'D' | 'R' | 'C' | 'U' | '?'.
	Status    string `json:"status"`
	Untracked bool   `json:"untracked,omitempty"`
}

// RepoStatus is RepoStatus in src/shared/types.ts.
type RepoStatus struct {
	Current    string      `json:"current"`
	Tracking   *string     `json:"tracking"`
	Ahead      int         `json:"ahead"`
	Behind     int         `json:"behind"`
	Staged     []FileEntry `json:"staged"`
	Unstaged   []FileEntry `json:"unstaged"`
	Conflicted []FileEntry `json:"conflicted"`
}

// ConflictVersions is ConflictVersions in src/shared/types.ts.
type ConflictVersions struct {
	Content string  `json:"content"`
	Ours    *string `json:"ours"`
	Theirs  *string `json:"theirs"`
	Base    *string `json:"base"`
}

// ConflictRefInfo is ConflictRefInfo in src/shared/types.ts.
type ConflictRefInfo struct {
	Sha     string `json:"sha"`
	Branch  string `json:"branch"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// ConflictContext is ConflictContext in src/shared/types.ts.
type ConflictContext struct {
	// Kind is a ConflictOpKind: 'merge' | 'cherry-pick' | 'rebase' | 'revert'.
	Kind   string           `json:"kind"`
	Source string           `json:"source"`
	Target string           `json:"target"`
	Ours   *ConflictRefInfo `json:"ours"`
	Theirs *ConflictRefInfo `json:"theirs"`
}

// StashInfo is StashInfo in src/shared/types.ts.
type StashInfo struct {
	Index        int     `json:"index"`
	Sha          string  `json:"sha"`
	ParentSha    string  `json:"parentSha"`
	UntrackedSha *string `json:"untrackedSha"`
	Message      string  `json:"message"`
	Branch       *string `json:"branch"`
	Date         int64   `json:"date"`
}

// RemoteInfo is RemoteInfo in src/shared/types.ts.
type RemoteInfo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// RebaseStep is RebaseStep in src/shared/types.ts.
type RebaseStep struct {
	// Action is 'pick' | 'squash' | 'fixup' | 'drop' | 'reword' | 'edit'.
	Action     string `json:"action"`
	Hash       string `json:"hash"`
	Subject    string `json:"subject"`
	NewMessage string `json:"newMessage,omitempty"`
}

// InteractiveRebaseStep is one commit offered to the interactive-rebase editor.
type InteractiveRebaseStep struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// AbsorbHunk is AbsorbHunk in src/shared/types.ts.
type AbsorbHunk struct {
	File    string `json:"file"`
	Header  string `json:"header"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// AbsorbTarget is AbsorbTarget in src/shared/types.ts.
type AbsorbTarget struct {
	Sha     string       `json:"sha"`
	Subject string       `json:"subject"`
	Hunks   []AbsorbHunk `json:"hunks"`
}

// AbsorbPlan is AbsorbPlan in src/shared/types.ts.
type AbsorbPlan struct {
	Base       string         `json:"base"`
	RangeLabel string         `json:"rangeLabel"`
	Targets    []AbsorbTarget `json:"targets"`
	Unmatched  []AbsorbHunk   `json:"unmatched"`
	// Reason is 'no-staged' | 'no-commits' | 'in-progress', when there is nothing to do.
	Reason string `json:"reason,omitempty"`
}

// AbsorbApplyOpts is absorbApply's optional second argument.
type AbsorbApplyOpts struct {
	Rebase bool `json:"rebase,omitempty"`
}

// AbsorbApplyResult is what absorbApply returns.
type AbsorbApplyResult struct {
	Created int  `json:"created"`
	Rebased bool `json:"rebased"`
}

// ReflogEntry is ReflogEntry in src/shared/types.ts.
type ReflogEntry struct {
	Sha      string `json:"sha"`
	Selector string `json:"selector"`
	Action   string `json:"action"`
	Date     int64  `json:"date"`
}

// CommitEditInfo is CommitEditInfo in src/shared/types.ts.
type CommitEditInfo struct {
	Linear      bool   `json:"linear"`
	Ancestor    bool   `json:"ancestor"`
	Merges      int    `json:"merges"`
	Descendants int    `json:"descendants"`
	Pushed      bool   `json:"pushed"`
	Message     string `json:"message"`
	AuthorName  string `json:"authorName"`
	AuthorDate  string `json:"authorDate"`
}

// BlobAtCommit is BlobAtCommit in src/shared/types.ts.
type BlobAtCommit struct {
	Content *string `json:"content"`
	Binary  bool    `json:"binary"`
	Size    int64   `json:"size"`
}

// CommitEditStep is CommitEditStep in src/shared/types.ts.
type CommitEditStep struct {
	Sha     string `json:"sha"`
	Subject string `json:"subject"`
	// Status is 'clean' | 'conflict' | 'blocked'.
	Status string   `json:"status"`
	Files  []string `json:"files"`
	Merge  bool     `json:"merge,omitempty"`
}

// CommitEditPreview is CommitEditPreview in src/shared/types.ts.
type CommitEditPreview struct {
	NewTip *string          `json:"newTip"`
	Steps  []CommitEditStep `json:"steps"`
}

// CommitEditResult is CommitEditResult in src/shared/types.ts.
type CommitEditResult struct {
	OldHead   string `json:"oldHead"`
	NewTip    string `json:"newTip"`
	Rewritten int    `json:"rewritten"`
}

// SnapshotInfo is SnapshotInfo in src/shared/types.ts.
type SnapshotInfo struct {
	Ref   string `json:"ref"`
	Sha   string `json:"sha"`
	Time  int64  `json:"time"`
	Files int    `json:"files"`
	// Kind is a SnapshotKind: 'auto' | 'manual' | 'guard'.
	Kind string `json:"kind"`
}
