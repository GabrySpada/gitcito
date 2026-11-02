// Package logbook is the machine-local, append-only log of git operations —
// src/main/log.ts. It serves the renderer channels `log:get` and `log:clear`
// (through whichever service binds Entries/Clear) and is fed by the git
// dispatcher (internal/gitsvc).
//
// Same file name (gitcito-log.json), same cap (1000 entries, oldest dropped),
// same JSON shape (LogEntry in src/shared/types.ts, pretty-printed with two
// spaces), same 400ms debounced save — under the new data dir.
package logbook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// FileName is the log's file name inside the data dir.
const FileName = "gitcito-log.json"

// MaxEntries is the hard cap on retained entries; the oldest are dropped once exceeded.
const MaxEntries = 1000

// MaxErrorLen caps a stored error message, in UTF-16 code units like the TS
// `error.slice(0, 500)`.
const MaxErrorLen = 500

// saveDelay debounces writes: a burst of operations costs one write.
const saveDelay = 400 * time.Millisecond

// ActivityEvent is ActivityEvent in src/shared/types.ts: which activity an
// operation maps to. It lives here (the lowest package that needs it) so the
// git service and analytics can share it without an import cycle.
type ActivityEvent string

// The ActivityEvent values, spelled exactly as the TS union.
const (
	EventCommit           ActivityEvent = "commit"
	EventAmend            ActivityEvent = "amend"
	EventPush             ActivityEvent = "push"
	EventPull             ActivityEvent = "pull"
	EventFetch            ActivityEvent = "fetch"
	EventBranchCreate     ActivityEvent = "branchCreate"
	EventBranchDelete     ActivityEvent = "branchDelete"
	EventMerge            ActivityEvent = "merge"
	EventRebase           ActivityEvent = "rebase"
	EventStash            ActivityEvent = "stash"
	EventStashPop         ActivityEvent = "stashPop"
	EventConflictResolved ActivityEvent = "conflictResolved"
	EventTagCreate        ActivityEvent = "tagCreate"
	EventCherryPick       ActivityEvent = "cherryPick"
	EventRevert           ActivityEvent = "revert"
	EventRepoOpen         ActivityEvent = "repoOpen"
	EventClone            ActivityEvent = "clone"
	EventInit             ActivityEvent = "init"
)

// LogEntry is LogEntry in src/shared/types.ts: one recorded git operation.
type LogEntry struct {
	// Ts is Unix ms when the operation finished.
	Ts int64 `json:"ts"`
	// RepoPath is the repository the operation ran against ('' for app-level ops).
	RepoPath string `json:"repoPath"`
	// RepoName is the basename of RepoPath, '' for app-level ops.
	RepoName string `json:"repoName"`
	// Event is which activity the operation maps to.
	Event ActivityEvent `json:"event"`
	// Ok is whether the operation completed successfully.
	Ok bool `json:"ok"`
	// Error is the truncated error message when Ok is false (absent otherwise).
	Error string `json:"error,omitempty"`
}

// Logbook holds the log in memory and persists it to <dir>/gitcito-log.json.
// Safe for concurrent use.
type Logbook struct {
	dir string
	now func() time.Time

	mu     sync.Mutex
	cache  []LogEntry
	loaded bool
	timer  *time.Timer
	gen    uint64 // bumps on every change; a save writes only the newest snapshot
}

// New opens the log stored in dir (the app data dir). Nothing is read until
// first use.
func New(dir string) *Logbook {
	return &Logbook{dir: dir, now: time.Now}
}

// Path is the log file's location.
func (l *Logbook) Path() string { return filepath.Join(l.dir, FileName) }

// load reads the file once; an unreadable or non-array file is an empty log.
// Callers hold l.mu.
func (l *Logbook) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	l.cache = []LogEntry{}
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		return
	}
	var parsed []json.RawMessage
	if json.Unmarshal(raw, &parsed) != nil || parsed == nil {
		return
	}
	for _, item := range parsed {
		var e LogEntry
		if json.Unmarshal(item, &e) == nil {
			l.cache = append(l.cache, e)
		}
	}
}

// Record appends one operation to the log. Best-effort: it never fails the
// caller; persistence happens on a debounce.
//
// Never logs secrets: the error message has any `user:token@` URL credentials
// redacted before it is stored (the dispatcher already does this; doing it
// here too means no other caller can forget). Redaction runs before the
// 500-unit truncation so a cut can never leave half a token behind.
func (l *Logbook) Record(event ActivityEvent, repoPath string, ok bool, errMsg string) {
	entry := LogEntry{
		Ts:       l.now().UnixMilli(),
		RepoPath: repoPath,
		Event:    event,
		Ok:       ok,
	}
	if repoPath != "" {
		entry.RepoName = basename(repoPath)
	}
	if errMsg != "" {
		entry.Error = gitx.SliceUTF16(gitx.RedactCredentials(errMsg), MaxErrorLen)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	l.cache = append(l.cache, entry)
	if over := len(l.cache) - MaxEntries; over > 0 {
		l.cache = append([]LogEntry{}, l.cache[over:]...)
	}
	l.scheduleSave()
}

// Entries returns a copy of the log, oldest first (`log:get`). Never nil.
func (l *Logbook) Entries() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	return append([]LogEntry{}, l.cache...)
}

// Clear empties the log and returns the (empty) result (`log:clear`).
func (l *Logbook) Clear() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loaded = true
	l.cache = []LogEntry{}
	l.scheduleSave()
	return []LogEntry{}
}

// scheduleSave restarts the debounce timer. Callers hold l.mu.
func (l *Logbook) scheduleSave() {
	l.gen++
	if l.timer != nil {
		l.timer.Stop()
	}
	l.timer = time.AfterFunc(saveDelay, func() { _ = l.Flush() })
}

// Flush writes the log now, cancelling any pending debounced save. Call it on
// shutdown so the last 400ms of operations are not lost.
func (l *Logbook) Flush() error {
	l.mu.Lock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	if !l.loaded {
		l.mu.Unlock()
		return nil // nothing was ever read or changed
	}
	snapshot := append([]LogEntry{}, l.cache...)
	gen := l.gen
	l.mu.Unlock()

	data, err := marshal(snapshot)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if gen != l.gen && l.timer != nil {
		return nil // a newer change is already scheduled to write
	}
	return writeFileAtomic(l.dir, l.Path(), data)
}

// marshal is JSON.stringify(v, null, 2): two-space indent, no trailing
// newline, and — unlike Go's default — no HTML escaping of <, > and &.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// writeFileAtomic writes via a temp file + rename so a crash mid-write never
// leaves a truncated log. The data dir is created 0700 and the file 0600:
// operation logs name private repositories.
func writeFileAtomic(dir, path string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gitcito-log-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// basename is Node's path.basename for a repo path: the last segment, trailing
// separators ignored.
func basename(p string) string {
	trimmed := p
	for len(trimmed) > 1 && os.IsPathSeparator(trimmed[len(trimmed)-1]) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	b := filepath.Base(trimmed)
	if b == "." || (len(b) == 1 && os.IsPathSeparator(b[0])) {
		return ""
	}
	return b
}
