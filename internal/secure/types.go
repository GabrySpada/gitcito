package secure

import "encoding/json"

// Wire types of the `secure:*` channels, mirroring src/shared/types.ts. The
// TS results are unions with an `{ error }` arm; each Go result type holds
// every arm's fields and marshals to exactly one of them.

// Candidate is SecureShareCandidate: a repo file offered in the export picker.
type Candidate struct {
	// Path is relative to the repo root, forward slashes.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Secret matches the secret-file heuristics — preselected in the UI.
	Secret bool `json:"secret"`
}

// ExportSpec is SecureExportSpec: what the export UI asks to pack. Repo file
// contents, vault values and note bodies are read here — the renderer only
// names what to include (and, for a workspace, hands over the already-portable
// tab shape, kept verbatim).
type ExportSpec struct {
	Kind     string   `json:"kind"`
	RepoPath string   `json:"repoPath,omitempty"`
	Project  string   `json:"project,omitempty"`
	Folder   string   `json:"folder,omitempty"`
	Remote   string   `json:"remote,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	// Name and Tabs (workspace) stay raw: the TS only sanity-shaped them
	// (`typeof name === 'string' && Array.isArray(tabs)`), skipping anything else.
	Name json.RawMessage `json:"name,omitempty"`
	Tabs json.RawMessage `json:"tabs,omitempty"`
}

// ApplyPlan is SecureApplyPlan: how the import UI wants one section applied.
// Repo and notes sections target a chosen local repo; the vault section merges
// into the global vault. A workspace section is applied by the renderer.
type ApplyPlan struct {
	Kind           string   `json:"kind"`
	SectionIndex   float64  `json:"sectionIndex"`
	TargetRepoPath string   `json:"targetRepoPath,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	Keys           []string `json:"keys,omitempty"`
	Overwrite      bool     `json:"overwrite,omitempty"`
}

// PreviewEntry is SecureSharePreviewEntry: one decrypted bundle entry,
// previewed before writing into the repo.
type PreviewEntry struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	// Exists: a file already sits at this path in the target repo.
	Exists bool `json:"exists"`
	// Safe: the path cannot escape the repo (unsafe entries are never written).
	Safe bool `json:"safe"`
}

// NotePreviewEntry is SecureNotePreviewEntry.
type NotePreviewEntry struct {
	Sha          string `json:"sha"`
	CommitExists bool   `json:"commitExists"`
	// State: 'new' = no local note; 'same' = identical; 'different' = needs overwrite.
	State string `json:"state"`
}

// ApplyResult is SecureApplyResult (or `{ error }`).
type ApplyResult struct {
	FilesWritten   int    `json:"filesWritten"`
	SecretsWritten int    `json:"secretsWritten"`
	NotesWritten   int    `json:"notesWritten"`
	NotesSkipped   int    `json:"notesSkipped"`
	Error          string `json:"error,omitempty"`
}

// MarshalJSON writes `{ error }` alone when Error is set.
func (r ApplyResult) MarshalJSON() ([]byte, error) {
	if r.Error != "" {
		return errorJSON(r.Error)
	}
	type plain ApplyResult
	return json.Marshal(plain(r))
}

// ExportResult is `{ path } | { canceled: true } | { error }`.
type ExportResult struct {
	Path     string `json:"path,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
	Error    string `json:"error,omitempty"`
}

// PickResult is `{ path, header } | { error }`; secure:pick returns nil (null)
// when the dialog is cancelled.
type PickResult struct {
	Path   string        `json:"path,omitempty"`
	Header *BundleHeader `json:"header,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// PreviewResult is `{ files } | { error }`.
type PreviewResult struct {
	Files []PreviewEntry `json:"files"`
	Error string         `json:"error,omitempty"`
}

// MarshalJSON writes `{ error }` alone when Error is set.
func (r PreviewResult) MarshalJSON() ([]byte, error) {
	if r.Error != "" {
		return errorJSON(r.Error)
	}
	if r.Files == nil {
		r.Files = []PreviewEntry{}
	}
	type plain PreviewResult
	return json.Marshal(plain(r))
}

// NotesPreviewResult is `{ notes } | { error }`.
type NotesPreviewResult struct {
	Notes []NotePreviewEntry `json:"notes"`
	Error string             `json:"error,omitempty"`
}

// MarshalJSON writes `{ error }` alone when Error is set.
func (r NotesPreviewResult) MarshalJSON() ([]byte, error) {
	if r.Error != "" {
		return errorJSON(r.Error)
	}
	if r.Notes == nil {
		r.Notes = []NotePreviewEntry{}
	}
	type plain NotesPreviewResult
	return json.Marshal(plain(r))
}

// WrittenResult is `{ written } | { error }` (secure:apply).
type WrittenResult struct {
	Written []string `json:"written"`
	Error   string   `json:"error,omitempty"`
}

// MarshalJSON writes `{ error }` alone when Error is set.
func (r WrittenResult) MarshalJSON() ([]byte, error) {
	if r.Error != "" {
		return errorJSON(r.Error)
	}
	if r.Written == nil {
		r.Written = []string{}
	}
	type plain WrittenResult
	return json.Marshal(plain(r))
}

// OpenedBundle is SecureBundleOpened (or `{ error }`): a decrypted bundle
// sanitised for the renderer — file lists and vault keys, never file contents,
// secret values or note bodies. Sections are SecureOpenedSection objects,
// built with the TS's exact keys.
type OpenedBundle struct {
	Version  int    `json:"version"`
	Sections []any  `json:"sections"`
	Error    string `json:"error,omitempty"`
}

// MarshalJSON writes `{ error }` alone when Error is set.
func (r OpenedBundle) MarshalJSON() ([]byte, error) {
	if r.Error != "" {
		return errorJSON(r.Error)
	}
	sections := r.Sections
	if sections == nil {
		sections = []any{}
	}
	return stringifyJS(jsObject{{"version", r.Version}, {"sections", sections}}, ""), nil
}

func errorJSON(code string) ([]byte, error) {
	return stringifyJS(jsObject{{"error", code}}, ""), nil
}
