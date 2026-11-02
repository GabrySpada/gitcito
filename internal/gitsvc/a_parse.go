package gitsvc

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Go ports of the src/shared decoders slice A needs: mergeTree.ts,
// rangeDiff.ts and patchHunks.ts. They are pure, so they would sit better in
// internal/gitx or internal/shared (listed for the lead to hoist).

// aJSSpace / aJSDot are the regexp bodies for JavaScript's `\s` and `.`
// (Go's are ASCII-only / newline-only).
const (
	aJSSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	aJSDot   = `[^\n\r\x{2028}\x{2029}]`
)

// ─── mergeTree.ts ───────────────────────────────────────────────────────────
//
// merge-tree performs a real merge entirely in the object database: it never
// touches the index, the working tree or any ref, so it is safe to run against
// dozens of branches in the background while the user keeps working. That is
// what powers the Conflict Radar — knowing which branches will bite *before*
// checking anything out.

// aMergeTreeRecord is the outcome of one merge-tree run.
type aMergeTreeRecord struct {
	// Status: 'clean' = merges without conflicts, 'conflict' = needs hands, 'error' = git refused.
	Status string
	// Tree is the OID of the merged toplevel tree (empty on error).
	Tree string
	// Files are the conflicting paths (only populated for 'conflict').
	Files []string
	// Message is informational / error text, newline-joined.
	Message string
}

// aParseMergeTreeStdin parses `git merge-tree --stdin --name-only`.
//
// Per merge, git emits: status NUL tree NUL [conflicted paths NUL…] NUL
// [message blocks NUL…] NUL — where the conflicted-path section is present
// only when the merge conflicted, and each message block is
// `<n> NUL <path>×n NUL <type> NUL <text>`.
//
// A fatal error (unrelated histories, unknown ref) makes git abort the whole
// batch, so the caller must treat a short result as "the rest is unknown" and
// retry those refs one at a time.
func aParseMergeTreeStdin(out string) []aMergeTreeRecord {
	f := strings.Split(out, "\x00")
	at := func(i int) (string, bool) {
		if i < len(f) {
			return f[i], true
		}
		return "", false
	}
	records := []aMergeTreeRecord{}
	i := 0
	for i < len(f) {
		// Skip padding between records; the stream ends with a trailing NUL.
		for i < len(f) && f[i] == "" {
			i++
		}
		if i >= len(f) {
			break
		}
		status := aJSNumber(f[i])
		i++
		if math.IsNaN(status) || math.IsInf(status, 0) {
			break
		}
		tree, _ := at(i)
		i++
		files := []string{}
		if status == 0 {
			for i < len(f) && f[i] != "" {
				files = append(files, f[i])
				i++
			}
			i++ // section terminator
		}
		messages := []string{}
		for i < len(f) && f[i] != "" {
			count := aJSInt(f[i])
			i++
			i += int(count) // the paths the message is about — already covered by `files`
			i++             // message type ('CONFLICT (content)', 'Auto-merging'…)
			if text, ok := at(i); ok && text != "" {
				messages = append(messages, gitx.TrimJS(text))
			}
			i++
		}
		i++ // record terminator
		st := "error"
		if status == 0 {
			st = "conflict"
		} else if status > 0 {
			st = "clean"
		}
		records = append(records, aMergeTreeRecord{Status: st, Tree: tree, Files: files, Message: strings.Join(messages, "\n")})
	}
	return records
}

// aParseMergeTreeSingle parses one `git merge-tree --write-tree --name-only
// --messages` run. Exit 0 = clean, 1 = conflicts, anything else = git refused.
func aParseMergeTreeSingle(stdout string, exitCode int, stderr string) aMergeTreeRecord {
	if exitCode != 0 && exitCode != 1 {
		return aMergeTreeRecord{Status: "error", Files: []string{}, Message: gitx.TrimJS(aOr(stderr, stdout))}
	}
	lines := strings.Split(stdout, "\n")
	tree := gitx.TrimJS(lines[0])
	lines = lines[1:]
	files := []string{}
	for len(lines) > 0 && gitx.TrimJS(lines[0]) != "" {
		files = append(files, gitx.TrimJS(lines[0]))
		lines = lines[1:]
	}
	status := "clean"
	if exitCode == 1 {
		status = "conflict"
	}
	return aMergeTreeRecord{Status: status, Tree: tree, Files: files, Message: gitx.TrimJS(strings.Join(lines, "\n"))}
}

// ─── rangeDiff.ts ───────────────────────────────────────────────────────────
//
// Decoder for `git range-diff`, the "what changed since you last looked"
// command: it pairs the commits of two versions of a branch and shows how each
// one was rewritten, instead of the useless "everything changed" a plain diff
// reports after a rebase or a force-push.
//
// Each pair is one header line, optionally followed by an indented interdiff:
//
//	1:  890dbf2 = 1:  890dbf2 add x          (identical)
//	2:  eb1d14d ! 3:  0da280d add y          (rewritten — interdiff follows)
//	3:  ea2e850 < -:  ------- add z          (dropped)
//	-:  ------- > 2:  eb6c239 add w          (new)

var aRangeHeaderRe = regexp.MustCompile(`^(\d+|-):[` + aJSSpace + `]+([0-9a-f]+|-+)[` + aJSSpace + `]+([=!<>])[` +
	aJSSpace + `]+(\d+|-):[` + aJSSpace + `]+([0-9a-f]+|-+)[` + aJSSpace + `]+(` + aJSDot + `*)$`)

var aRangeKind = map[string]string{"=": "unchanged", "!": "modified", "<": "removed", ">": "added"}

func aRangeIndex(raw string) *int {
	if raw == "-" {
		return nil
	}
	n, _ := strconv.Atoi(raw)
	return &n
}

func aRangeSha(raw string) *string {
	if strings.Trim(raw, "-") == "" {
		return nil
	}
	return &raw
}

// aParseRangeDiff decodes `git range-diff` output.
func aParseRangeDiff(out string) []RangeDiffEntry {
	entries := []RangeDiffEntry{}
	var body []string
	flush := func() {
		if len(entries) == 0 {
			return
		}
		// Interdiff lines are indented by four spaces; strip that so the body
		// reads like an ordinary patch.
		lines := make([]string, len(body))
		for i, l := range body {
			lines[i] = strings.TrimPrefix(l, "    ")
		}
		entries[len(entries)-1].Body = gitx.TrimEndJS(strings.Join(lines, "\n"))
		body = nil
	}
	for _, line := range strings.Split(out, "\n") {
		if m := aRangeHeaderRe.FindStringSubmatch(line); m != nil {
			flush()
			kind, ok := aRangeKind[m[3]]
			if !ok {
				kind = "modified"
			}
			entries = append(entries, RangeDiffEntry{
				Kind:     kind,
				OldIndex: aRangeIndex(m[1]),
				OldSha:   aRangeSha(m[2]),
				NewIndex: aRangeIndex(m[4]),
				NewSha:   aRangeSha(m[5]),
				Subject:  gitx.TrimJS(m[6]),
			})
		} else if len(entries) > 0 {
			body = append(body, line)
		}
	}
	flush()
	return entries
}

// ─── patchHunks.ts ──────────────────────────────────────────────────────────
//
// Splitting a unified diff into per-file hunks, and putting chosen hunks back
// together into an applicable patch. Absorb needs both directions: it takes
// the staged diff apart to ask blame who owns each hunk, then rebuilds one
// patch per target commit so each `fixup!` carries only its own changes.

// aPatchHunk is one hunk of a file's diff.
type aPatchHunk struct {
	// Header is the `@@ -a,b +c,d @@ …` line, verbatim.
	Header string
	// Lines are the body lines (context, additions, deletions), verbatim.
	Lines []string
	// OldStart / OldCount: first old-side line and how many lines it spans.
	OldStart int
	OldCount int
}

// aPatchFile is one file of a unified diff.
type aPatchFile struct {
	// OldPath is the path on the old side (`a/…` stripped); equals NewPath unless renamed.
	OldPath string
	NewPath string
	// HeaderLines are the `diff --git` line plus the mode/index/±++ lines, verbatim.
	HeaderLines []string
	Hunks       []*aPatchHunk
	// Binary: a binary file, a pure rename, a mode change — nothing to attribute.
	Binary bool
}

var (
	aHunkRe    = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	aDiffGitRe = regexp.MustCompile(`^diff --git (?:"?a/)?(` + aJSDot + `+?)"? (?:"?b/)?(` + aJSDot + `+?)"?$`)
	aBinaryRe  = regexp.MustCompile(`^(GIT binary patch|Binary files )`)
)

// aPathsFrom reads both paths from a `diff --git a/foo b/foo` line. Quoted
// paths keep their quotes, which git also accepts back when the patch is applied.
func aPathsFrom(line string) (oldPath, newPath string) {
	m := aDiffGitRe.FindStringSubmatch(line)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// aParsePatch splits a unified diff (as produced by `git diff`) into files and hunks.
func aParsePatch(diff string) []*aPatchFile {
	files := []*aPatchFile{}
	var file *aPatchFile
	var hunk *aPatchHunk
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			o, n := aPathsFrom(line)
			file = &aPatchFile{OldPath: o, NewPath: n, HeaderLines: []string{line}}
			hunk = nil
			files = append(files, file)
			continue
		}
		if file == nil {
			continue
		}
		if m := aHunkRe.FindStringSubmatch(line); m != nil {
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			hunk = &aPatchHunk{Header: line, Lines: []string{}, OldStart: start, OldCount: count}
			file.Hunks = append(file.Hunks, hunk)
			continue
		}
		if hunk != nil {
			// "\ No newline at end of file" belongs to the hunk it follows.
			hunk.Lines = append(hunk.Lines, line)
		} else {
			if aBinaryRe.MatchString(line) {
				file.Binary = true
			}
			file.HeaderLines = append(file.HeaderLines, line)
		}
	}
	// A trailing newline in the input leaves one empty line on the last hunk.
	for _, f := range files {
		if n := len(f.Hunks); n > 0 {
			last := f.Hunks[n-1]
			if k := len(last.Lines); k > 0 && last.Lines[k-1] == "" {
				last.Lines = last.Lines[:k-1]
			}
		}
	}
	return files
}

// aBuildPatch rebuilds an applicable patch containing only hunks of file.
// Hunk headers are reused untouched: every hunk here comes from one diff
// against one base, so their old-side line numbers are still correct.
func aBuildPatch(file *aPatchFile, hunks []*aPatchHunk) string {
	if len(hunks) == 0 {
		return ""
	}
	out := append([]string{}, file.HeaderLines...)
	for _, h := range hunks {
		out = append(out, h.Header)
		out = append(out, h.Lines...)
	}
	return strings.Join(out, "\n") + "\n"
}

// aTouchedOldLines lists the old-side line numbers a hunk touches — the lines
// that already existed and were deleted or used as context. Those are the ones
// blame can attribute; a hunk that only adds lines has none of its own, so its
// surrounding context is used instead.
func aTouchedOldLines(h *aPatchHunk) (deleted, context []int) {
	deleted, context = []int{}, []int{}
	lineNo := h.OldStart
	for _, line := range h.Lines {
		switch {
		case strings.HasPrefix(line, "-"):
			deleted = append(deleted, lineNo)
			lineNo++
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, `\`):
			// additions and "\ No newline" don't consume an old-side line
		default:
			context = append(context, lineNo)
			lineNo++
		}
	}
	return deleted, context
}
