package gitsvc

import "github.com/GabrySpada/gitcito/internal/gitx"

// Types from src/shared/types.ts that slice B returns but other slices return
// too (status, log, stashes). Kept in one file so the lead can keep a single
// copy when the slices merge: if another slice declares the same name, the
// definitions are meant to be identical and one file's copy is deleted.

// FileEntry is FileEntry in src/shared/types.ts.
type FileEntry struct {
	Path string `json:"path"`
	// Status is a FileChangeKind: 'A' | 'M' | 'D' | 'R' | 'C' | 'U' | '?'.
	Status    string `json:"status"`
	Untracked bool   `json:"untracked,omitempty"`
}

// GraphCommit is GraphCommit in src/shared/types.ts.
type GraphCommit struct {
	Hash    string   `json:"hash"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	// Date is unix seconds.
	Date      int64               `json:"date"`
	Refs      []string            `json:"refs"`
	Subject   string              `json:"subject"`
	CoAuthors []gitx.CommitAuthor `json:"coAuthors,omitempty"`
	// Signature is a CommitSignature, omitted when unsigned.
	Signature string `json:"signature,omitempty"`
	// Signer is the signing identity (%GS), when known.
	Signer string `json:"signer,omitempty"`
}
