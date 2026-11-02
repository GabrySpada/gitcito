package gitx

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Field and record separators git.ts puts into `--format` strings. Unit and
// record separators never appear in names, subjects or refs.
const (
	SEP = "\x1f"
	REC = "\x1e"
)

// CreationFactor is how eagerly `git range-diff` pairs a commit with its
// rewritten self. git's default (60) leaves small commits unpaired — they show
// up as a delete plus an add, which is exactly the "everything changed" noise
// range-diff exists to remove. Pairing slightly too eagerly is the better
// failure: the interdiff is right there to judge it by.
//
// 75 was not eager enough. A one-line change to a four-line file — the
// `force-push` playground's "validate password", and the shape of most review
// fixes — still scored as two unrelated commits; the pairing only holds from
// 80 up. 85 sits clear of that edge without pairing commits that merely touch
// the same file.
const CreationFactor = "--creation-factor=85"

// ActivityDays is how many days of commit activity a Mission Control
// sparkline summarises.
const ActivityDays = 14

// FileTooLargePrefix is FILE_TOO_LARGE_PREFIX from src/shared/types.ts. The
// renderer looks for it at the start of an error message to offer "load
// anyway"; it must stay byte-identical.
const FileTooLargePrefix = "GITCITO_FILE_TOO_LARGE:"

// In-memory caps for whole-file reads. Both are overridable per call with
// `force` — the renderer surfaces the refusal and offers "load anyway", so
// nothing becomes unreachable, but a stray click on a 2GB video no longer
// allocates 5× its size across the backend, the bridge and the renderer.
const (
	PreviewMaxBytes = 32 * 1024 * 1024
	TextMaxBytes    = 16 * 1024 * 1024
)

// KnownHooks are the common client-side git hooks, in the order git documents them.
var KnownHooks = []string{
	"applypatch-msg",
	"pre-applypatch",
	"post-applypatch",
	"pre-commit",
	"pre-merge-commit",
	"prepare-commit-msg",
	"commit-msg",
	"post-commit",
	"pre-rebase",
	"post-checkout",
	"post-merge",
	"pre-push",
	"post-rewrite",
	"pre-auto-gc",
}

// CommitAuthor is CommitAuthor in src/shared/types.ts.
type CommitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

var coAuthorRe = regexp.MustCompile(`^(` + jsDot + `*?)[` + jsSpaceClass + `]*<([^>]*)>[` + jsSpaceClass + `]*$`)

// ParseCoAuthors parses `Co-authored-by` trailer values ("Name <email>"),
// separated by \x1d as the log format asks git to, into authors. A value with
// no `<email>` becomes a name with an empty email. Never nil.
func ParseCoAuthors(raw string) []CommitAuthor {
	out := []CommitAuthor{}
	if raw == "" {
		return out
	}
	for _, part := range strings.Split(raw, "\x1d") {
		line := TrimJS(part)
		if line == "" {
			continue
		}
		if m := coAuthorRe.FindStringSubmatch(line); m != nil {
			out = append(out, CommitAuthor{Name: TrimJS(m[1]), Email: TrimJS(m[2])})
		} else {
			out = append(out, CommitAuthor{Name: line, Email: ""})
		}
	}
	return out
}

// MapSignature normalises git's `%G?` signature char into a CommitSignature
// ('good' | 'unverified' | 'bad' | 'expired' | 'none').
func MapSignature(char string) string {
	switch TrimJS(char) {
	case "G":
		return "good"
	case "U", // good signature, unknown validity
		"E": // signature present but cannot be checked (e.g. missing public key)
		return "unverified"
	case "X", // expired signature
		"Y": // signature made by an expired key
		return "expired"
	case "B", // bad signature
		"R": // good signature made by a revoked key
		return "bad"
	default: // 'N' or empty — no signature
		return "none"
	}
}

// MapStatusCode maps a porcelain status letter to a FileChangeKind; anything
// unrecognised is a modification.
func MapStatusCode(code string) string {
	switch code {
	case "A", "D", "R", "C", "U", "?":
		return code
	default:
		return "M"
	}
}

// StashSubject is a stash reflog subject split into the branch it was made on
// and its message.
type StashSubject struct {
	Branch  *string // nil when the subject names none
	Message string
}

var stashSubjectRe = regexp.MustCompile(`^(?:WIP on|On) ([^:]*):[` + jsSpaceClass + `]*(` + jsDot + `*)$`)

// ParseStashSubject splits git's `WIP on <branch>: …` / `On <branch>: <message>`
// stash subject into the originating branch and the meaningful message; the UI
// shows them separately so the redundant prefix isn't repeated inline.
func ParseStashSubject(subject string) StashSubject {
	m := stashSubjectRe.FindStringSubmatch(subject)
	if m == nil {
		return StashSubject{Message: subject}
	}
	var branch *string
	if m[1] != "" {
		b := m[1]
		branch = &b
	}
	msg := TrimJS(m[2])
	if msg == "" {
		msg = subject
	}
	return StashSubject{Branch: branch, Message: msg}
}

var mimeByExt = map[string]string{
	// images
	"svg": "image/svg+xml", "jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
	"gif": "image/gif", "webp": "image/webp", "bmp": "image/bmp", "ico": "image/x-icon", "avif": "image/avif",
	// documents
	"pdf": "application/pdf",
	// video
	"mp4": "video/mp4", "webm": "video/webm", "ogv": "video/ogg", "mov": "video/quicktime", "m4v": "video/mp4",
	// audio
	"mp3": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg", "m4a": "audio/mp4", "flac": "audio/flac", "aac": "audio/aac",
	// office
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"xls":  "application/vnd.ms-excel",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// FileMime is the MIME type by extension, covering images plus the binary
// formats the file previewer can render (pdf, video, audio, office docs).
// Unknown extensions fall back to a generic binary type so the data URL is
// still well-formed. Like `file.split('.').pop()`, a name without a dot is
// its own "extension".
func FileMime(file string) string {
	ext := file
	if i := strings.LastIndexByte(file, '.'); i >= 0 {
		ext = file[i+1:]
	}
	if m, ok := mimeByExt[strings.ToLower(ext)]; ok {
		return m
	}
	return "application/octet-stream"
}

// Track is ahead/behind counts.
type Track struct {
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
}

var (
	aheadRe  = regexp.MustCompile(`ahead (\d+)`)
	behindRe = regexp.MustCompile(`behind (\d+)`)
)

// ParseTrack reads `[ahead N, behind M]` style tracking text.
func ParseTrack(track string) Track {
	var t Track
	if m := aheadRe.FindStringSubmatch(track); m != nil {
		t.Ahead, _ = strconv.Atoi(m[1])
	}
	if m := behindRe.FindStringSubmatch(track); m != nil {
		t.Behind, _ = strconv.Atoi(m[1])
	}
	return t
}

// EncodeSubtreeKey: a git config key cannot hold a slash in its subsection
// when written this way, and a subtree prefix is a path — so it is encoded,
// and decoded on the way back.
func EncodeSubtreeKey(prefix string) string { return strings.ReplaceAll(prefix, "/", "%2F") }

// DecodeSubtreeKey reverses EncodeSubtreeKey.
func DecodeSubtreeKey(key string) string { return strings.ReplaceAll(key, "%2F", "/") }

// PrePurgeKey is the config key holding the paths a purge removed. Git config
// keys must begin with a letter, so the timestamp cannot stand alone.
func PrePurgeKey(at int64) string { return "gitcito.prepurge.at" + strconv.FormatInt(at, 10) }

// BackupStamp is a filesystem-safe local timestamp (no colons) for naming
// backup branches: YYYYMMDD-HHMMSS.
func BackupStamp(now time.Time) string {
	now = now.Local()
	return fmt.Sprintf("%04d%02d%02d-%02d%02d%02d", now.Year(), int(now.Month()), now.Day(),
		now.Hour(), now.Minute(), now.Second())
}

// ContiguousRuns collapses line numbers into sorted, de-duplicated [start,end]
// runs — one blame per run keeps the process count down on a long hunk.
func ContiguousRuns(lines []int) [][2]int {
	seen := map[int]bool{}
	sorted := make([]int, 0, len(lines))
	for _, n := range lines {
		if !seen[n] {
			seen[n] = true
			sorted = append(sorted, n)
		}
	}
	sort.Ints(sorted)
	runs := [][2]int{}
	for _, n := range sorted {
		if len(runs) > 0 && n == runs[len(runs)-1][1]+1 {
			runs[len(runs)-1][1] = n
		} else {
			runs = append(runs, [2]int{n, n})
		}
	}
	return runs
}

var rerereResolvedRe = regexp.MustCompile(`Resolved '(` + jsDot + `+?)' using previous resolution`)

// ParseRerereResolved lists the files git says it resolved from a memorised
// resolution ("Resolved 'x' using previous resolution.") in the output of the
// merge/rebase that hit a conflict. Git only says so once, and nothing in the
// repository records it afterwards, so the caller captures it when it is
// said (withRerereCapture in TS). Never nil.
func ParseRerereResolved(output string) []string {
	files := []string{}
	for _, m := range rerereResolvedRe.FindAllStringSubmatch(output, -1) {
		files = append(files, m[1])
	}
	return files
}

// MergeOptions is MergeOptions in src/shared/types.ts.
type MergeOptions struct {
	// NoFf: `--no-ff` — always record a merge commit, even when a fast-forward would do.
	NoFf bool `json:"noFf,omitempty"`
	// FfOnly: `--ff-only` — refuse anything that is not a fast-forward.
	FfOnly bool `json:"ffOnly,omitempty"`
	// Squash: `--squash` — take the changes, leave the history and the commit to you.
	Squash bool `json:"squash,omitempty"`
	// NoCommit: `--no-commit` — merge and stage, but stop before committing.
	NoCommit bool `json:"noCommit,omitempty"`
	// Strategy: `-s <strategy>` ('ort' | 'resolve' | 'subtree' | 'octopus').
	Strategy string `json:"strategy,omitempty"`
	// Favour: `-X ours` / `-X theirs` — resolve conflicting hunks one way automatically.
	Favour string `json:"favour,omitempty"`
	// IgnoreSpace: `-X ignore-space-change` and friends ('change' | 'all' | 'eol').
	IgnoreSpace string `json:"ignoreSpace,omitempty"`
}

// MergeArgs turns MergeOptions into git flags. (TS also accepted a bare
// boolean meaning `noFf`; Go callers pass MergeOptions{NoFf: b}.)
func MergeArgs(o MergeOptions) []string {
	args := []string{}
	if o.FfOnly {
		args = append(args, "--ff-only")
	} else if o.NoFf {
		args = append(args, "--no-ff")
	}
	if o.Squash {
		args = append(args, "--squash")
	}
	if o.NoCommit {
		args = append(args, "--no-commit")
	}
	if o.Strategy != "" && o.Strategy != "ort" {
		args = append(args, "-s", o.Strategy)
	}
	// -X ours/theirs resolves only the hunks that actually clash. It is not
	// `-s ours`, which throws the other side away wholesale — a difference
	// worth keeping straight, since one is routine and the other loses work.
	if o.Favour != "" {
		args = append(args, "-X", o.Favour)
	}
	if o.IgnoreSpace != "" {
		mode := o.IgnoreSpace
		if mode == "eol" {
			mode = "at-eol"
		}
		args = append(args, "-X", "ignore-space-"+mode)
	}
	return args
}

// SplitLines is `s.split('\n').filter(Boolean)`: the non-empty lines.
func SplitLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// SplitTrimmedLines is `s.split('\n').map(l => l.trim()).filter(Boolean)`.
func SplitTrimmedLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if t := TrimJS(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}
