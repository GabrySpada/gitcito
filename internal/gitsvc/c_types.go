package gitsvc

import (
	"bytes"
	"encoding/json"

	"github.com/GabrySpada/gitcito/internal/fileactions"
	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Types of wave-1 slice C (commit, branches, merge/rebase, stacks, git-flow,
// rerere, bisect, patches, file-system actions). JSON tags are the property
// names of src/shared/types.ts.

// MergeOptionsArg is the `options: MergeOptions | boolean` argument of
// `merge` and `mergeInto`. A bare boolean is the pre-options signature,
// meaning `noFf` — the renderer's mergeInto adapter still sends one — so it
// decodes from either shape (null/absent → no options).
type MergeOptionsArg struct {
	gitx.MergeOptions
}

// UnmarshalJSON accepts `true` / `false` (→ noFf) as well as an object.
func (m *MergeOptionsArg) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	switch string(trimmed) {
	case "true":
		*m = MergeOptionsArg{gitx.MergeOptions{NoFf: true}}
		return nil
	case "false", "null":
		*m = MergeOptionsArg{}
		return nil
	}
	var o gitx.MergeOptions
	if err := json.Unmarshal(trimmed, &o); err != nil {
		return err
	}
	*m = MergeOptionsArg{o}
	return nil
}

// CheckoutRemoteResult is CheckoutRemoteResult in src/shared/types.ts: what
// checkoutRemote found when a local branch of the same name already exists.
type CheckoutRemoteResult struct {
	Diverged  bool `json:"diverged"`
	AheadOnly bool `json:"aheadOnly"`
	Ahead     int  `json:"ahead"`
	Behind    int  `json:"behind"`
}

// DivergedCheckoutResult is resolveDivergedCheckout's `{ backupRef?, previousRef }`.
type DivergedCheckoutResult struct {
	BackupRef   string `json:"backupRef,omitempty"`
	PreviousRef string `json:"previousRef"`
}

// SquashResult is squashCommits' `{ before, after }`: HEAD before and after the fold.
type SquashResult struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// CommitSummary is commitSummary's `{ hash, author, date, subject, body }`.
type CommitSummary struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// SigningConfig is SigningConfig in src/shared/types.ts.
type SigningConfig struct {
	Sign   bool   `json:"sign"`   // commit.gpgsign
	Format string `json:"format"` // gpg.format: openpgp | ssh | x509
	Key    string `json:"key"`    // user.signingkey ('' if unset)
}

// SigningConfigUpdate is setSigningConfig's `{ sign?, format?, key? }`: only
// the fields present are written.
type SigningConfigUpdate struct {
	Sign   *bool   `json:"sign,omitempty"`
	Format *string `json:"format,omitempty"`
	Key    *string `json:"key,omitempty"`
}

// ConflictCommit is ConflictCommit in src/shared/types.ts.
type ConflictCommit struct {
	Sha     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"` // ISO
	Side    string `json:"side"` // 'ours' | 'theirs'
}

// FileSizeInfo is one value of fileSizes' `Record<string, { size, binary }>`.
type FileSizeInfo struct {
	Size   int64 `json:"size"`
	Binary bool  `json:"binary"`
}

// StackBranch is StackBranch in src/shared/types.ts.
type StackBranch struct {
	Name         string  `json:"name"`
	Parent       *string `json:"parent"` // null for the trunk base
	IsCurrent    bool    `json:"isCurrent"`
	Ahead        int     `json:"ahead"`
	NeedsRestack bool    `json:"needsRestack"`
}

// StackInfo is StackInfo in src/shared/types.ts, ordered bottom → top.
type StackInfo struct {
	Trunk    string        `json:"trunk"`
	Branches []StackBranch `json:"branches"`
}

// GitflowConfig is GitflowConfig in src/shared/types.ts.
type GitflowConfig struct {
	Main             string `json:"main"`
	Develop          string `json:"develop"`
	FeaturePrefix    string `json:"featurePrefix"`
	ReleasePrefix    string `json:"releasePrefix"`
	HotfixPrefix     string `json:"hotfixPrefix"`
	VersionTagPrefix string `json:"versionTagPrefix"`
}

// GitflowFlow is GitflowStatus.currentFlow: a flow branch's kind and bare name.
type GitflowFlow struct {
	Kind string `json:"kind"` // GitflowKind: 'feature' | 'release' | 'hotfix'
	Name string `json:"name"`
}

// GitflowStatus is GitflowStatus in src/shared/types.ts.
type GitflowStatus struct {
	Config      GitflowConfig `json:"config"`
	Initialized bool          `json:"initialized"`
	HasMain     bool          `json:"hasMain"`
	HasDevelop  bool          `json:"hasDevelop"`
	Current     string        `json:"current"`
	CurrentFlow *GitflowFlow  `json:"currentFlow"`
}

// GitflowRef is one `{ name, sha }` of a GitflowSnapshot.
type GitflowRef struct {
	Name string `json:"name"`
	Sha  string `json:"sha"`
}

// GitflowSnapshot is GitflowSnapshot in src/shared/types.ts: refs as they
// stood before a finish, so the whole thing can be undone.
type GitflowSnapshot struct {
	Refs   []GitflowRef `json:"refs"`
	Tag    *string      `json:"tag"`
	Branch GitflowRef   `json:"branch"`
}

// GitflowFinishOptions is gitflowFinish's `{ tag?, deleteBranch?, message? }`.
// Tag and DeleteBranch default to true (TS tested `!== false`).
type GitflowFinishOptions struct {
	Tag          *bool  `json:"tag,omitempty"`
	DeleteBranch *bool  `json:"deleteBranch,omitempty"`
	Message      string `json:"message,omitempty"`
}

// RerereStatus is RerereStatus in src/shared/types.ts.
type RerereStatus struct {
	Enabled    bool     `json:"enabled"`
	AutoUpdate bool     `json:"autoUpdate"`
	PerRepo    bool     `json:"perRepo"`
	Recorded   int      `json:"recorded"`
	Replayed   []string `json:"replayed"`
}

// RerereSettings is setRerere's `{ enabled?, autoUpdate? }`.
type RerereSettings struct {
	Enabled    *bool `json:"enabled,omitempty"`
	AutoUpdate *bool `json:"autoUpdate,omitempty"`
}

// BisectStatus is BisectStatus in src/shared/types.ts.
type BisectStatus struct {
	InProgress      bool   `json:"inProgress"`
	NeedGood        bool   `json:"needGood"`
	NeedBad         bool   `json:"needBad"`
	CurrentSha      string `json:"currentSha"`
	CurrentSubject  string `json:"currentSubject"`
	RemainingSteps  int    `json:"remainingSteps"`
	Finished        bool   `json:"finished"`
	FirstBadSha     string `json:"firstBadSha"`
	FirstBadSubject string `json:"firstBadSubject"`
}

// PreparedRepoChatFileAction is PreparedRepoChatFileAction in
// src/shared/types.ts: a repository-chat file mutation (edit_file /
// write_file / delete_file) after the main process verified it and built its
// diff. It lives in internal/fileactions, which prepares and applies it.
type PreparedRepoChatFileAction = fileactions.PreparedRepoChatFileAction

// RepoFileBatchError is the `error` of a failed RepoFileBatchResult.
type RepoFileBatchError struct {
	Code   string   `json:"code"` // RepoChatActionErrorCode
	Detail string   `json:"detail"`
	Paths  []string `json:"paths"`
}

// RepoFileBatchResult is RepoFileBatchResult in src/shared/types.ts:
// `{ ok: true, applied }` or `{ ok: false, error }`.
type RepoFileBatchResult struct {
	Ok      bool                `json:"ok"`
	Applied *int                `json:"applied,omitempty"`
	Error   *RepoFileBatchError `json:"error,omitempty"`
}
