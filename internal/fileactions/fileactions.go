// Package fileactions is src/main/repoFileActions.ts: the file mutations
// repository chat may propose (edit_file / write_file / delete_file), checked
// against the repository, previewed as a local diff, and later applied as an
// all-or-nothing batch.
//
// Prepare runs while the chat answer is built — it validates every action,
// simulates the batch in memory and records the hash each target had, but
// writes nothing to the repository. Apply runs under the git write lock
// (internal/gitsvc applyRepoFileActions): it revalidates the whole batch
// against those hashes before the first byte is written, then installs every
// file through sibling temp files and backups so a failure part-way rolls the
// repository back.
package fileactions

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/repoconfig"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Limits shared by Prepare and Apply.
const (
	MaxBytes      = 512 * 1024
	BatchMaxBytes = 2 * 1024 * 1024
)

// Action is RepoChatFileAction in src/shared/types.ts, flattened: Type says
// which fields apply (edit_file: OldText/NewText/ReplaceAll; write_file:
// Content/Mode; delete_file: neither).
type Action struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Description string `json:"description"`
	OldText     string `json:"oldText,omitempty"`
	NewText     string `json:"newText,omitempty"`
	ReplaceAll  bool   `json:"replaceAll,omitempty"`
	Content     string `json:"content,omitempty"`
	Mode        string `json:"mode,omitempty"` // 'create' | 'replace'
}

// PreparedRepoChatFileAction is PreparedRepoChatFileAction in
// src/shared/types.ts: an action after the main process verified it and built
// its diff. The union is flattened; Type says which fields apply.
type PreparedRepoChatFileAction struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Description string `json:"description"`
	// edit_file
	OldText    string `json:"oldText,omitempty"`
	NewText    string `json:"newText,omitempty"`
	ReplaceAll bool   `json:"replaceAll,omitempty"`
	// write_file
	Content string `json:"content,omitempty"`
	Mode    string `json:"mode,omitempty"` // 'create' | 'replace'
	// prepared
	ExpectedHash        *string `json:"expectedHash"`
	ExpectedOccurrences *int    `json:"expectedOccurrences,omitempty"`
	Preview             string  `json:"preview"`
}

// MarshalJSON writes the fields the action's variant carries even when they
// are empty: an edit that deletes text has `newText: ""`, a write of an empty
// file has `content: ""`, exactly as the TS spread them.
func (a PreparedRepoChatFileAction) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"type":         a.Type,
		"path":         a.Path,
		"description":  a.Description,
		"expectedHash": a.ExpectedHash,
		"preview":      a.Preview,
	}
	switch a.Type {
	case "edit_file":
		m["oldText"], m["newText"] = a.OldText, a.NewText
		if a.ReplaceAll {
			m["replaceAll"] = true
		}
	case "write_file":
		m["content"], m["mode"] = a.Content, a.Mode
	}
	if a.ExpectedOccurrences != nil {
		m["expectedOccurrences"] = *a.ExpectedOccurrences
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Context is RepoFileActionContext: which paths the chat answer is grounded
// in, which of those it saw whole, and which the repository ignores.
type Context struct {
	EvidencePaths map[string]bool
	CompletePaths map[string]bool
	IgnoredPaths  map[string]bool
}

// Error is RepoFileActionError: a refusal with a renderer-facing code
// (RepoChatActionErrorCode) and the paths it concerns.
type Error struct {
	Code    string
	Message string
	Paths   []string
}

func (e *Error) Error() string { return e.Message }

func fail(code, message, path string) error {
	return &Error{Code: code, Message: message, Paths: []string{path}}
}

var generatedPathRe = regexp.MustCompile(`(?i)(^|/)(generated|gen)(/|$)|\.generated\.[^/]+$`)

// IsGeneratedRepoPath reports whether a path looks generated (a `generated/`
// or `gen/` directory, or a `.generated.` file), which chat must not edit.
func IsGeneratedRepoPath(path string) bool {
	return generatedPathRe.MatchString(path)
}

func assertType(t, path string) error {
	if t != "edit_file" && t != "write_file" && t != "delete_file" {
		if path == "" {
			path = "(unknown)"
		}
		return fail("unknown", "The file action type is not supported.", path)
	}
	return nil
}

// normalizedTarget is the safety checks on a target path, and its normalized
// repo-relative and absolute forms.
func normalizedTarget(repoPath, rawPath string) (path, absolute string, err error) {
	if !shared.IsSafeRepoPath(rawPath) {
		return "", "", fail("unsafe_path", "The target must be a relative repository path.", rawPath)
	}
	path = strings.ReplaceAll(gitx.TrimJS(rawPath), `\`, "/")
	segments := strings.Split(path, "/")
	for _, seg := range segments {
		if strings.ToLower(seg) == ".git" {
			return "", "", fail("git_internal_path", "Git internal files cannot be changed by repository chat.", path)
		}
	}
	if shared.IsSecretFile(path) {
		return "", "", fail("secret_file", "Secret-looking files cannot be changed by repository chat.", path)
	}
	if IsGeneratedRepoPath(path) {
		return "", "", fail("generated_path", "Generated files cannot be changed by repository chat.", path)
	}
	root, rerr := filepath.Abs(repoPath)
	if rerr != nil {
		return "", "", rerr
	}
	absolute = filepath.Join(append([]string{root}, segments...)...)
	inside, rerr := filepath.Rel(root, absolute)
	if rerr != nil || inside == "." || strings.HasPrefix(inside, "..") || filepath.IsAbs(inside) {
		return "", "", fail("unsafe_path", "The target must stay inside the repository.", path)
	}
	return path, absolute, nil
}

// target is what inspectTarget learned about a path.
type target struct {
	exists, isFile bool
	mode           fs.FileMode
}

// inspectTarget walks the target's segments refusing symlinks and
// non-directory parents.
func inspectTarget(repoPath, path string, fsys FS) (target, error) {
	current, err := filepath.Abs(repoPath)
	if err != nil {
		return target{}, err
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		current = filepath.Join(current, seg)
		info, lerr := fsys.Lstat(current)
		if lerr != nil {
			if errors.Is(lerr, fs.ErrNotExist) {
				return target{}, nil
			}
			return target{}, nodeFsErr(lerr)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return target{}, fail("symlink_path", "Symlink targets and parents are not allowed.", path)
		}
		if i < len(segments)-1 && !info.IsDir() {
			return target{}, fail("not_found", "A parent of the target is not a directory.", path)
		}
		if i == len(segments)-1 {
			return target{exists: true, isFile: info.Mode().IsRegular(), mode: info.Mode()}, nil
		}
	}
	return target{}, nil
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// occurrences counts the non-overlapping occurrences of exact in content.
func occurrences(content, exact string) int {
	if exact == "" {
		return 0
	}
	return strings.Count(content, exact)
}

// readText reads an existing target, refusing binary and oversized files.
func readText(fsys FS, absolute, path string) (string, error) {
	data, err := fsys.ReadFile(absolute)
	if err != nil {
		return "", nodeFsErr(err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fail("binary_file", "Binary files cannot be changed by repository chat.", path)
	}
	if len(data) > MaxBytes {
		return "", fail("file_too_large", "The target file is too large.", path)
	}
	return repoconfig.DecodeUTF8(data), nil
}

// edited is the content after an edit_file: every occurrence for replaceAll
// (split/join), else the first through String.prototype.replace.
func edited(content string, a *PreparedRepoChatFileAction) string {
	if a.ReplaceAll {
		return strings.ReplaceAll(content, a.OldText, a.NewText)
	}
	return ReplaceFirst(content, a.OldText, a.NewText)
}

// ReplaceFirst is `s.replace(search, replacement)` with a string pattern: the
// first occurrence only, with the replacement's `$` patterns expanded.
func ReplaceFirst(s, search, replacement string) string {
	i := strings.Index(s, search)
	if i < 0 {
		return s
	}
	return s[:i] + expandReplacement(replacement, s, i, i+len(search)) + s[i+len(search):]
}

// expandReplacement expands String.prototype.replace's `$` patterns in a
// replacement for the match s[start:end] — `$$`, `$&`, “ $` “ and `$'`. A
// string or group-less pattern has no captures, so `$1` stays literal.
func expandReplacement(replacement, s string, start, end int) string {
	if !strings.Contains(replacement, "$") {
		return replacement
	}
	var sb strings.Builder
	for j := 0; j < len(replacement); j++ {
		c := replacement[j]
		if c != '$' || j+1 >= len(replacement) {
			sb.WriteByte(c)
			continue
		}
		switch replacement[j+1] {
		case '$':
			sb.WriteByte('$')
			j++
		case '&':
			sb.WriteString(s[start:end])
			j++
		case '`':
			sb.WriteString(s[:start])
			j++
		case '\'':
			sb.WriteString(s[end:])
			j++
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// replaceFirst is `s.replace(re, replacement)` for a group-less regexp.
func replaceFirst(re *regexp.Regexp, s, replacement string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + expandReplacement(replacement, s, loc[0], loc[1]) + s[loc[1]:]
}

// JS `.` stops at any line terminator, and `$` under the m flag matches before
// any of them, so `^x.*$` is `^x` plus a run of non-terminators.
var (
	diffHeaderRe = regexp.MustCompile(`(?m)^diff --git [^\n\r\x{2028}\x{2029}]*`)
	minusRe      = regexp.MustCompile(`(?m)^--- [^\n\r\x{2028}\x{2029}]*`)
	plusRe       = regexp.MustCompile(`(?m)^\+\+\+ [^\n\r\x{2028}\x{2029}]*`)
)

// localDiff is `git diff --no-index` between two files; exit 1 (they differ)
// is success.
func localDiff(ctx context.Context, beforePath, afterPath string) (string, error) {
	res, err := gitx.Exec(ctx, "", "git", []string{"diff", "--no-index", "--no-color", "--", beforePath, afterPath},
		gitx.RunOpts{PlainEnv: true, MaxBuffer: BatchMaxBytes * 4})
	if err != nil {
		if code, ok := gitx.ExitCode(err); !ok || code != 1 {
			return "", err
		}
	}
	return res.Stdout, nil
}

// previewFor diffs before → after through temp files and relabels the
// headers with the repository path.
func previewFor(ctx context.Context, path string, before, after *string) (string, error) {
	dir, err := os.MkdirTemp("", "gitcito-file-preview-")
	if err != nil {
		return "", nodeFsErr(err)
	}
	defer os.RemoveAll(dir)
	beforePath, afterPath := filepath.Join(dir, "before"), filepath.Join(dir, "after")
	if before != nil {
		if err := os.WriteFile(beforePath, []byte(*before), 0o666); err != nil {
			return "", nodeFsErr(err)
		}
	} else {
		beforePath = os.DevNull
	}
	if after != nil {
		if err := os.WriteFile(afterPath, []byte(*after), 0o666); err != nil {
			return "", nodeFsErr(err)
		}
	} else {
		afterPath = os.DevNull
	}
	preview, err := localDiff(ctx, beforePath, afterPath)
	if err != nil {
		return "", err
	}
	minus, plus := "--- a/"+path, "+++ b/"+path
	if before == nil {
		minus = "--- /dev/null"
	}
	if after == nil {
		plus = "+++ /dev/null"
	}
	preview = replaceFirst(diffHeaderRe, preview, "diff --git a/"+path+" b/"+path)
	preview = replaceFirst(minusRe, preview, minus)
	return replaceFirst(plusRe, preview, plus), nil
}

// Prepare validates file mutations against the current repository and
// prepares immutable hashes plus user-visible diffs. No repository file is
// written here.
func Prepare(ctx context.Context, repoPath string, actions []Action, c Context) ([]PreparedRepoChatFileAction, error) {
	simulated := map[string]*string{}
	prepared := []PreparedRepoChatFileAction{}
	batchBytes := 0
	fsys := OS()

	for _, action := range actions {
		if err := assertType(action.Type, action.Path); err != nil {
			return nil, err
		}
		path, absolute, err := normalizedTarget(repoPath, action.Path)
		if err != nil {
			return nil, err
		}
		if c.IgnoredPaths[path] {
			return nil, fail("ignored_path", "Ignored files cannot be changed by repository chat.", path)
		}
		needsEvidence := action.Type != "write_file" || action.Mode == "replace"
		if needsEvidence && !c.EvidencePaths[path] {
			return nil, fail("evidence_required", "The target was not grounded in repository evidence.", path)
		}
		if action.Type == "write_file" && action.Mode == "replace" && !c.CompletePaths[path] {
			return nil, fail("incomplete_evidence", "Whole-file replacement requires complete file evidence.", path)
		}

		inspected, err := inspectTarget(repoPath, path, fsys)
		if err != nil {
			return nil, err
		}
		if inspected.exists && !inspected.isFile {
			return nil, fail("unsafe_path", "The target is not a regular file.", path)
		}

		before, seen := simulated[path]
		if !seen {
			if inspected.exists {
				text, err := readText(fsys, absolute, path)
				if err != nil {
					return nil, err
				}
				before = &text
			}
			simulated[path] = before
		}

		out := PreparedRepoChatFileAction{Type: action.Type, Path: path, Description: action.Description,
			OldText: action.OldText, NewText: action.NewText, ReplaceAll: action.ReplaceAll,
			Content: action.Content, Mode: action.Mode}
		var after *string
		switch action.Type {
		case "edit_file":
			if before == nil {
				return nil, fail("not_found", "The file to edit does not exist.", path)
			}
			n := occurrences(*before, action.OldText)
			out.ExpectedOccurrences = &n
			if n == 0 {
				return nil, fail("old_text_missing", "The exact text to edit was not found.", path)
			}
			if !action.ReplaceAll && n != 1 {
				return nil, fail("ambiguous_edit", "The exact edit matches more than once.", path)
			}
			next := edited(*before, &out)
			after = &next
		case "write_file":
			if action.Mode == "create" && before != nil {
				return nil, fail("already_exists", "The file to create already exists.", path)
			}
			if action.Mode == "replace" && before == nil {
				return nil, fail("not_found", "The file to replace does not exist.", path)
			}
			content := action.Content
			after = &content
		default:
			if before == nil {
				return nil, fail("not_found", "The file to delete does not exist.", path)
			}
		}

		if after != nil && strings.Contains(*after, "\x00") {
			return nil, fail("binary_file", "Binary content cannot be written by repository chat.", path)
		}
		finalBytes := 0
		if after != nil {
			finalBytes = len(*after)
		}
		if finalBytes > MaxBytes {
			return nil, fail("file_too_large", "The resulting file is too large.", path)
		}
		batchBytes += finalBytes
		if batchBytes > BatchMaxBytes {
			return nil, fail("batch_too_large", "The prepared file batch is too large.", path)
		}
		if sameOptional(after, before) {
			return nil, fail("unknown", "The proposed file action would not change the file.", path)
		}

		preview, err := previewFor(ctx, path, before, after)
		if err != nil {
			return nil, &Error{Code: "unknown", Message: "Could not generate a local diff preview: " + err.Error(), Paths: []string{path}}
		}
		if before != nil {
			h := sha(*before)
			out.ExpectedHash = &h
		}
		out.Preview = preview
		prepared = append(prepared, out)
		simulated[path] = after
	}
	return prepared, nil
}

// applyState is one target path across the batch.
type applyState struct {
	path, absolute         string
	original, current      *string
	mode                   fs.FileMode
	hasMode                bool
	temp, backup           string
	backupMoved, installed bool
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Apply revalidates and applies a prepared batch and returns how many actions
// it applied. Existing targets move to sibling backups before any final
// content is installed, allowing a reverse rollback. fsys nil is the real
// filesystem; tests inject failures through it.
func Apply(repoPath string, actions []PreparedRepoChatFileAction, ignored map[string]bool, fsys FS) (int, error) {
	if fsys == nil {
		fsys = OS()
	}
	states := map[string]*applyState{}
	var ordered []*applyState
	batchBytes := 0

	// Full-batch validation and in-memory simulation happen before the first
	// directory, temporary file, backup, or repository target is changed.
	for i := range actions {
		action := &actions[i]
		if err := assertType(action.Type, action.Path); err != nil {
			return 0, err
		}
		path, absolute, err := normalizedTarget(repoPath, action.Path)
		if err != nil {
			return 0, err
		}
		if ignored[path] {
			return 0, fail("ignored_path", "Ignored files cannot be changed by repository chat.", path)
		}

		state := states[path]
		if state == nil {
			inspected, err := inspectTarget(repoPath, path, fsys)
			if err != nil {
				return 0, err
			}
			if inspected.exists && !inspected.isFile {
				return 0, fail("unsafe_path", "The target is not a regular file.", path)
			}
			var original *string
			if inspected.exists {
				text, err := readText(fsys, absolute, path)
				if err != nil {
					return 0, err
				}
				original = &text
			}
			state = &applyState{path: path, absolute: absolute, original: original, current: original,
				mode: inspected.mode, hasMode: inspected.exists}
			states[path] = state
			ordered = append(ordered, state)
		} else if _, err := inspectTarget(repoPath, path, fsys); err != nil {
			return 0, err
		}

		var actualHash *string
		if state.current != nil {
			h := sha(*state.current)
			actualHash = &h
		}
		if !sameOptional(actualHash, action.ExpectedHash) {
			return 0, fail("stale_file", "The file changed after its preview was prepared.", path)
		}

		var after *string
		switch action.Type {
		case "edit_file":
			if state.current == nil {
				return 0, fail("stale_file", "The file to edit no longer exists.", path)
			}
			count := occurrences(*state.current, action.OldText)
			if action.ExpectedOccurrences == nil || count != *action.ExpectedOccurrences || count == 0 || (!action.ReplaceAll && count != 1) {
				return 0, fail("stale_file", "The exact edit no longer matches its preview.", path)
			}
			next := edited(*state.current, action)
			after = &next
		case "write_file":
			if action.Mode == "create" && state.current != nil {
				return 0, fail("stale_file", "The file to create now exists.", path)
			}
			if action.Mode == "replace" && state.current == nil {
				return 0, fail("stale_file", "The file to replace no longer exists.", path)
			}
			content := action.Content
			after = &content
		default:
			if state.current == nil {
				return 0, fail("stale_file", "The file to delete no longer exists.", path)
			}
		}

		if after != nil && strings.Contains(*after, "\x00") {
			return 0, fail("binary_file", "Binary content cannot be written by repository chat.", path)
		}
		finalBytes := 0
		if after != nil {
			finalBytes = len(*after)
		}
		if finalBytes > MaxBytes {
			return 0, fail("file_too_large", "The resulting file is too large.", path)
		}
		batchBytes += finalBytes
		if batchBytes > BatchMaxBytes {
			return 0, fail("batch_too_large", "The file batch is too large.", path)
		}
		if sameOptional(after, state.current) {
			return 0, fail("unknown", "The file action would not change the file.", path)
		}
		state.current = after
	}

	var createdDirs []string
	if err := install(repoPath, ordered, fsys, &createdDirs); err != nil {
		var rollbackPaths []string
		for i := len(ordered) - 1; i >= 0; i-- {
			st := ordered[i]
			failed := false
			if st.backupMoved && st.backup != "" {
				if rerr := fsys.RemoveAll(st.absolute); rerr != nil {
					failed = true
				} else if rerr := fsys.Rename(st.backup, st.absolute); rerr != nil {
					failed = true
				} else {
					st.backupMoved = false
				}
			} else if st.original == nil && st.installed {
				if rerr := fsys.RemoveAll(st.absolute); rerr != nil {
					failed = true
				}
			}
			if failed {
				rollbackPaths = append(rollbackPaths, st.path)
			}
			if st.temp != "" {
				if rerr := fsys.RemoveAll(st.temp); rerr != nil {
					if !contains(rollbackPaths, st.path) {
						rollbackPaths = append(rollbackPaths, st.path)
					}
				} else {
					st.temp = ""
				}
			}
		}
		// Only empty directories go: anything else in one was not ours. (The TS
		// called rm() without `recursive`, which refuses every directory, so it
		// left them all behind; removing the empty ones is what it meant.)
		for i := len(createdDirs) - 1; i >= 0; i-- {
			_ = fsys.Remove(createdDirs[i])
		}
		if len(rollbackPaths) > 0 {
			return 0, &Error{Code: "rollback_failed",
				Message: "The file batch failed and one or more original paths could not be restored.", Paths: rollbackPaths}
		}
		var fe *Error
		if errors.As(err, &fe) {
			return 0, err
		}
		return 0, &Error{Code: "unknown", Message: err.Error(), Paths: []string{}}
	}

	// Backups are no longer needed after every final target is installed.
	for _, st := range ordered {
		if st.backup != "" {
			_ = fsys.RemoveAll(st.backup)
		}
		if st.temp != "" {
			_ = fsys.RemoveAll(st.temp)
		}
	}
	return len(actions), nil
}

// install writes every new content to a temp sibling, moves every original
// aside, then renames the temps into place.
func install(repoPath string, ordered []*applyState, fsys FS, createdDirs *[]string) error {
	for _, st := range ordered {
		if st.current == nil {
			continue
		}
		if err := ensureParentDirectories(repoPath, st.absolute, fsys, createdDirs); err != nil {
			return err
		}
		temp := filepath.Join(filepath.Dir(st.absolute), ".gitcito-"+filepath.Base(st.absolute)+".tmp-"+randomUUID())
		if err := fsys.WriteFileExcl(temp, []byte(*st.current)); err != nil {
			return nodeFsErr(err)
		}
		st.temp = temp
		if st.hasMode {
			if err := fsys.Chmod(st.temp, st.mode.Perm()); err != nil {
				return nodeFsErr(err)
			}
		}
	}

	// Move every original aside first. If any later installation fails, all
	// original contents are still available for a complete reverse restore.
	for _, st := range ordered {
		if st.original == nil || sameOptional(st.current, st.original) {
			continue
		}
		st.backup = filepath.Join(filepath.Dir(st.absolute), ".gitcito-"+filepath.Base(st.absolute)+".bak-"+randomUUID())
		if err := fsys.Rename(st.absolute, st.backup); err != nil {
			return nodeFsErr(err)
		}
		st.backupMoved = true
	}

	for _, st := range ordered {
		if sameOptional(st.current, st.original) || st.current == nil {
			continue
		}
		if st.temp == "" {
			return &Error{Code: "unknown", Message: "A prepared temporary file is missing.", Paths: []string{st.path}}
		}
		if err := fsys.Rename(st.temp, st.absolute); err != nil {
			return nodeFsErr(err)
		}
		st.temp = ""
		st.installed = true
	}
	return nil
}

// ensureParentDirectories creates target's missing parents, refusing a
// symlinked or non-directory one, and records what it created.
func ensureParentDirectories(repoPath, target string, fsys FS, created *[]string) error {
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if parent == root {
		return nil
	}
	relTarget, _ := filepath.Rel(root, target)
	relTarget = filepath.ToSlash(relTarget)
	rel, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	var missing []string
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		current = filepath.Join(current, part)
		info, lerr := fsys.Lstat(current)
		if lerr != nil {
			if !errors.Is(lerr, fs.ErrNotExist) {
				return nodeFsErr(lerr)
			}
			missing = append(missing, current)
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fail("symlink_path", "Symlink targets and parents are not allowed.", relTarget)
		}
		if !info.IsDir() {
			return fail("unsafe_path", "A target parent is not a directory.", relTarget)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := fsys.MkdirAll(parent); err != nil {
		return nodeFsErr(err)
	}
	*created = append(*created, missing...)
	return nil
}

// sameOptional is `a === b` for `string | null`.
func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
