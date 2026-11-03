package repochat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// The prepare half of src/main/repoFileActions.ts. The apply half runs under
// the git write lock and lives in internal/gitsvc (ApplyRepoFileActions); this
// half validates a proposal against the current repository and prepares
// immutable hashes plus user-visible diffs. No repository file is written here.

const (
	repoFileMaxBytes      = 512 * 1024
	repoFileBatchMaxBytes = 2 * 1024 * 1024
)

// FileActionError is RepoFileActionError: a refusal with a renderer-facing
// code (RepoChatActionErrorCode) and the paths it concerns.
type FileActionError struct {
	Code    string
	Message string
	Paths   []string
}

func (e *FileActionError) Error() string { return e.Message }

func fail(code, message, path string) error {
	return &FileActionError{Code: code, Message: message, Paths: []string{path}}
}

// FileActionContext is RepoFileActionContext.
type FileActionContext struct {
	EvidencePaths map[string]bool
	CompletePaths map[string]bool
	IgnoredPaths  map[string]bool
}

func assertFileActionType(action map[string]any) error {
	t := action["type"]
	if t != "edit_file" && t != "write_file" && t != "delete_file" {
		path, ok := action["path"].(string)
		if !ok {
			path = "(unknown)"
		}
		return fail("unknown", "The file action type is not supported.", path)
	}
	return nil
}

func normalizedTarget(repoPath string, rawPath any) (path, absolute string, err error) {
	raw, _ := rawPath.(string)
	if _, isStr := rawPath.(string); !isStr || !shared.IsSafeRepoPath(raw) {
		return "", "", fail("unsafe_path", "The target must be a relative repository path.", raw)
	}
	path = strings.ReplaceAll(trim(raw), `\`, "/")
	segments := strings.Split(path, "/")
	for _, s := range segments {
		if strings.ToLower(s) == ".git" {
			return "", "", fail("git_internal_path", "Git internal files cannot be changed by repository chat.", path)
		}
	}
	if shared.IsSecretFile(path) {
		return "", "", fail("secret_file", "Secret-looking files cannot be changed by repository chat.", path)
	}
	if reGeneratedPath.MatchString(path) {
		return "", "", fail("generated_path", "Generated files cannot be changed by repository chat.", path)
	}
	root, _ := filepath.Abs(repoPath)
	absolute = filepath.Join(append([]string{root}, segments...)...)
	inside, relErr := filepath.Rel(root, absolute)
	if relErr != nil || inside == "." || strings.HasPrefix(inside, "..") || filepath.IsAbs(inside) {
		return "", "", fail("unsafe_path", "The target must stay inside the repository.", path)
	}
	return path, absolute, nil
}

// inspectTarget walks the target's segments with lstat: no symlinked target or
// parent, and every parent a directory.
func inspectTarget(repoPath, path string) (exists, isFile bool, err error) {
	current, _ := filepath.Abs(repoPath)
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		current = filepath.Join(current, seg)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				return false, false, nil
			}
			return false, false, statErr
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return false, false, fail("symlink_path", "Symlink targets and parents are not allowed.", path)
		}
		if i < len(segments)-1 && !info.IsDir() {
			return false, false, fail("not_found", "A parent of the target is not a directory.", path)
		}
		if i == len(segments)-1 {
			return true, info.Mode().IsRegular(), nil
		}
	}
	return false, false, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func occurrences(content, exact string) int {
	if exact == "" {
		return 0
	}
	return strings.Count(content, exact)
}

// decodeUTF8 is Buffer.toString('utf8'): each invalid byte becomes U+FFFD.
func decodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		sb.WriteRune(r)
		b = b[size:]
	}
	return sb.String()
}

// expandJSReplacement is String.prototype.replace's replacement-string
// language for a match without capture groups: $$, $&, $` and $' expand,
// everything else is literal.
func expandJSReplacement(repl, match, before, after string) string {
	if !strings.Contains(repl, "$") {
		return repl
	}
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		if repl[i] != '$' || i+1 >= len(repl) {
			b.WriteByte(repl[i])
			continue
		}
		switch repl[i+1] {
		case '$':
			b.WriteByte('$')
		case '&':
			b.WriteString(match)
		case '`':
			b.WriteString(before)
		case '\'':
			b.WriteString(after)
		default:
			b.WriteByte('$')
			continue
		}
		i++
	}
	return b.String()
}

// jsReplaceFirst is `s.replace(search, replacement)` with a string search.
func jsReplaceFirst(s, search, replacement string) string {
	i := strings.Index(s, search)
	if i < 0 {
		return s
	}
	return s[:i] + expandJSReplacement(replacement, search, s[:i], s[i+len(search):]) + s[i+len(search):]
}

// jsRegexReplaceFirst is `s.replace(re, replacement)` for a non-global regex
// without capture groups.
func jsRegexReplaceFirst(re *regexp.Regexp, s, replacement string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + expandJSReplacement(replacement, s[loc[0]:loc[1]], s[:loc[0]], s[loc[1]:]) + s[loc[1]:]
}

var (
	rePreviewDiff = regexp.MustCompile(`(?m)^diff --git ` + shared.JSDot + `*$`)
	rePreviewOld  = regexp.MustCompile(`(?m)^--- ` + shared.JSDot + `*$`)
	rePreviewNew  = regexp.MustCompile(`(?m)^\+\+\+ ` + shared.JSDot + `*$`)
)

// localDiff runs `git diff --no-index`; exit 1 (differences) is success.
func localDiff(ctx context.Context, beforePath, afterPath string) (string, error) {
	res, err := gitx.Exec(ctx, "", "git", []string{"diff", "--no-index", "--no-color", "--", beforePath, afterPath},
		gitx.RunOpts{MaxBuffer: repoFileBatchMaxBytes * 4, PlainEnv: true})
	if err != nil {
		if code, ok := gitx.ExitCode(err); !ok || code != 1 {
			return "", err
		}
		var ee *gitx.ExecError
		if errors.As(err, &ee) {
			return ee.Stdout, nil
		}
		return res.Stdout, nil
	}
	return res.Stdout, nil
}

func previewFor(ctx context.Context, path string, before, after *string) (string, error) {
	dir, err := os.MkdirTemp("", "gitcito-file-preview-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	beforePath, afterPath := filepath.Join(dir, "before"), filepath.Join(dir, "after")
	if before != nil {
		if err := os.WriteFile(beforePath, []byte(*before), 0o644); err != nil {
			return "", err
		}
	}
	if after != nil {
		if err := os.WriteFile(afterPath, []byte(*after), 0o644); err != nil {
			return "", err
		}
	}
	from, to := beforePath, afterPath
	if before == nil {
		from = os.DevNull
	}
	if after == nil {
		to = os.DevNull
	}
	preview, err := localDiff(ctx, from, to)
	if err != nil {
		return "", err
	}
	oldSide, newSide := "a/"+path, "b/"+path
	if before == nil {
		oldSide = "/dev/null"
	}
	if after == nil {
		newSide = "/dev/null"
	}
	preview = jsRegexReplaceFirst(rePreviewDiff, preview, "diff --git a/"+path+" b/"+path)
	preview = jsRegexReplaceFirst(rePreviewOld, preview, "--- "+oldSide)
	return jsRegexReplaceFirst(rePreviewNew, preview, "+++ "+newSide), nil
}

// PrepareFileActions is prepareRepoFileActions: validate file mutations
// against the current repository and prepare immutable hashes plus
// user-visible diffs. Each prepared action is the model's action with the
// normalized path, expectedHash, expectedOccurrences (edits) and preview.
func PrepareFileActions(ctx context.Context, repoPath string, actions []map[string]any, fc FileActionContext) ([]map[string]any, error) {
	simulated := map[string]*string{}
	prepared := []map[string]any{}
	batchBytes := 0

	for _, action := range actions {
		if err := assertFileActionType(action); err != nil {
			return nil, err
		}
		path, absolute, err := normalizedTarget(repoPath, action["path"])
		if err != nil {
			return nil, err
		}
		typ, _ := action["type"].(string)
		mode := action["mode"]
		if fc.IgnoredPaths[path] {
			return nil, fail("ignored_path", "Ignored files cannot be changed by repository chat.", path)
		}
		needsEvidence := typ != "write_file" || mode == "replace"
		if needsEvidence && !fc.EvidencePaths[path] {
			return nil, fail("evidence_required", "The target was not grounded in repository evidence.", path)
		}
		if typ == "write_file" && mode == "replace" && !fc.CompletePaths[path] {
			return nil, fail("incomplete_evidence", "Whole-file replacement requires complete file evidence.", path)
		}

		exists, isFile, err := inspectTarget(repoPath, path)
		if err != nil {
			return nil, err
		}
		if exists && !isFile {
			return nil, fail("unsafe_path", "The target is not a regular file.", path)
		}

		before, known := simulated[path]
		if !known {
			if exists {
				bytes, err := os.ReadFile(absolute)
				if err != nil {
					return nil, err
				}
				if strings.IndexByte(string(bytes), 0) >= 0 {
					return nil, fail("binary_file", "Binary files cannot be changed by repository chat.", path)
				}
				if len(bytes) > repoFileMaxBytes {
					return nil, fail("file_too_large", "The target file is too large.", path)
				}
				text := decodeUTF8(bytes)
				before = &text
			}
			simulated[path] = before
		}

		var after *string
		var expectedOccurrences *int
		switch typ {
		case "edit_file":
			if before == nil {
				return nil, fail("not_found", "The file to edit does not exist.", path)
			}
			oldText, _ := action["oldText"].(string)
			newText, _ := action["newText"].(string)
			n := occurrences(*before, oldText)
			expectedOccurrences = &n
			if n == 0 {
				return nil, fail("old_text_missing", "The exact text to edit was not found.", path)
			}
			replaceAll := shared.JSTruthy(action["replaceAll"])
			if !replaceAll && n != 1 {
				return nil, fail("ambiguous_edit", "The exact edit matches more than once.", path)
			}
			var next string
			if replaceAll {
				next = strings.Join(strings.Split(*before, oldText), newText)
			} else {
				next = jsReplaceFirst(*before, oldText, newText)
			}
			after = &next
		case "write_file":
			if mode == "create" && before != nil {
				return nil, fail("already_exists", "The file to create already exists.", path)
			}
			if mode == "replace" && before == nil {
				return nil, fail("not_found", "The file to replace does not exist.", path)
			}
			content, _ := action["content"].(string)
			after = &content
		default:
			if before == nil {
				return nil, fail("not_found", "The file to delete does not exist.", path)
			}
		}

		if after != nil && strings.IndexByte(*after, 0) >= 0 {
			return nil, fail("binary_file", "Binary content cannot be written by repository chat.", path)
		}
		finalBytes := 0
		if after != nil {
			finalBytes = len(*after)
		}
		if finalBytes > repoFileMaxBytes {
			return nil, fail("file_too_large", "The resulting file is too large.", path)
		}
		batchBytes += finalBytes
		if batchBytes > repoFileBatchMaxBytes {
			return nil, fail("batch_too_large", "The prepared file batch is too large.", path)
		}
		if (after == nil && before == nil) || (after != nil && before != nil && *after == *before) {
			return nil, fail("unknown", "The proposed file action would not change the file.", path)
		}

		preview, err := previewFor(ctx, path, before, after)
		if err != nil {
			return nil, &FileActionError{
				Code:    "unknown",
				Message: "Could not generate a local diff preview: " + err.Error(),
				Paths:   []string{path},
			}
		}

		out := make(map[string]any, len(action)+4)
		for k, v := range action {
			out[k] = v
		}
		out["path"] = path
		if before == nil {
			out["expectedHash"] = nil
		} else {
			out["expectedHash"] = sha256Hex(*before)
		}
		if expectedOccurrences != nil {
			out["expectedOccurrences"] = *expectedOccurrences
		}
		out["preview"] = preview
		prepared = append(prepared, out)
		simulated[path] = after
	}
	return prepared, nil
}
