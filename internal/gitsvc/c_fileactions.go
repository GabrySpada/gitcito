package gitsvc

import (
	"bytes"
	"context"
	"crypto/rand"
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

// The apply half of src/main/repoFileActions.ts: a repository-chat file batch
// that the main process already prepared (validated, hashed, previewed) is
// revalidated and installed under the git write lock, all or nothing.

// Limits shared with the prepare step.
const (
	cRepoFileMaxBytes      = 512 * 1024
	cRepoFileBatchMaxBytes = 2 * 1024 * 1024
)

// cFileActionError is RepoFileActionError: a refusal with a renderer-facing
// code and the paths it concerns.
type cFileActionError struct {
	code    string
	message string
	paths   []string
}

func (e *cFileActionError) Error() string { return e.message }

func cFail(code, message, path string) error {
	return &cFileActionError{code: code, message: message, paths: []string{path}}
}

// ApplyRepoFileActions applies a validated repository-chat file batch under
// the git write lock. A refusal is a result ({ ok: false, error }), not an
// error; anything unexpected is an error.
// gitsvc: applyRepoFileActions
func (s *GitService) ApplyRepoFileActions(ctx context.Context, repoPath string, actions []PreparedRepoChatFileAction) (RepoFileBatchResult, error) {
	return locked(s, ctx, "applyRepoFileActions", repoPath, func(ctx context.Context) (RepoFileBatchResult, error) {
		paths := []string{}
		for _, a := range actions {
			paths = append(paths, a.Path)
		}
		ignoredList, err := cIgnoredTrackedFiles(ctx, repoPath, cUnique(paths))
		if err != nil {
			return RepoFileBatchResult{}, err
		}
		ignored := map[string]bool{}
		for _, p := range ignoredList {
			ignored[p] = true
		}
		applied, err := cApplyPreparedRepoFileActions(repoPath, actions, ignored)
		if err != nil {
			var fe *cFileActionError
			if errors.As(err, &fe) {
				p := fe.paths
				if p == nil {
					p = []string{}
				}
				return RepoFileBatchResult{Ok: false, Error: &RepoFileBatchError{Code: fe.code, Detail: fe.message, Paths: p}}, nil
			}
			return RepoFileBatchResult{}, err
		}
		return RepoFileBatchResult{Ok: true, Applied: &applied}, nil
	})
}

var (
	cGeneratedPathRe = regexp.MustCompile(`(?i)(^|/)(generated|gen)(/|$)|\.generated\.[^/]+$`)
	// cSecretFilePatterns are the path heuristics of src/shared/secretFiles.ts.
	cSecretFilePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(^|/)\.env(\.[\w.-]+)?$`), // .env, .env.local, .env.production…
		regexp.MustCompile(`(?i)(^|/)\.envrc$`),
		regexp.MustCompile(`(?i)\.(pem|key|p12|pfx|keystore|jks)$`),
		regexp.MustCompile(`(?i)(^|/)id_(rsa|dsa|ecdsa|ed25519)$`),
		regexp.MustCompile(`(?i)(^|/)\.npmrc$`),
		regexp.MustCompile(`(?i)(^|/)\.pgpass$`),
		// Apple signing material: a .mobileprovision embeds the team's
		// certificates, a .p8 is an App Store Connect key. A .cer is public.
		regexp.MustCompile(`(?i)\.(mobileprovision|provisionprofile|p8)$`),
		regexp.MustCompile(`(?i)(^|/)\.netrc$`),
		regexp.MustCompile(`(?i)(^|/)credentials(\.json)?$`),
		regexp.MustCompile(`(?i)(^|/)secrets?\.(ya?ml|json|toml|ini)$`),
	}
	// Committed-by-design templates that merely share the .env shape.
	cSecretFileAllow = regexp.MustCompile(`(?i)(^|/)\.env\.(example|sample|template|dist|defaults?)$`)
)

// cIsSecretFile is isSecretFile in src/shared/secretFiles.ts: a path that
// looks like it holds credentials.
func cIsSecretFile(path string) bool {
	if cSecretFileAllow.MatchString(path) {
		return false
	}
	for _, re := range cSecretFilePatterns {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// cNormalizedTarget is normalizedTarget: the safety checks on a target path,
// and its normalized repo-relative and absolute forms.
func cNormalizedTarget(repoPath, rawPath string) (path, absolute string, err error) {
	if !shared.IsSafeRepoPath(rawPath) {
		return "", "", cFail("unsafe_path", "The target must be a relative repository path.", rawPath)
	}
	path = strings.ReplaceAll(gitx.TrimJS(rawPath), `\`, "/")
	segments := strings.Split(path, "/")
	for _, seg := range segments {
		if strings.ToLower(seg) == ".git" {
			return "", "", cFail("git_internal_path", "Git internal files cannot be changed by repository chat.", path)
		}
	}
	if cIsSecretFile(path) {
		return "", "", cFail("secret_file", "Secret-looking files cannot be changed by repository chat.", path)
	}
	if cGeneratedPathRe.MatchString(path) {
		return "", "", cFail("generated_path", "Generated files cannot be changed by repository chat.", path)
	}
	root, rerr := filepath.Abs(repoPath)
	if rerr != nil {
		return "", "", rerr
	}
	absolute = filepath.Join(append([]string{root}, segments...)...)
	inside, rerr := filepath.Rel(root, absolute)
	if rerr != nil || inside == "." || strings.HasPrefix(inside, "..") || filepath.IsAbs(inside) {
		return "", "", cFail("unsafe_path", "The target must stay inside the repository.", path)
	}
	return path, absolute, nil
}

// cInspectTarget is inspectTarget: walk the target's segments refusing
// symlinks and non-directory parents.
func cInspectTarget(repoPath, path string) (exists, isFile bool, mode fs.FileMode, err error) {
	current, err := filepath.Abs(repoPath)
	if err != nil {
		return false, false, 0, err
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		current = filepath.Join(current, seg)
		info, lerr := os.Lstat(current)
		if lerr != nil {
			if errors.Is(lerr, fs.ErrNotExist) {
				return false, false, 0, nil
			}
			return false, false, 0, cNodeFsErr(lerr)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return false, false, 0, cFail("symlink_path", "Symlink targets and parents are not allowed.", path)
		}
		if i < len(segments)-1 && !info.IsDir() {
			return false, false, 0, cFail("not_found", "A parent of the target is not a directory.", path)
		}
		if i == len(segments)-1 {
			return true, info.Mode().IsRegular(), info.Mode(), nil
		}
	}
	return false, false, 0, nil
}

func cSha256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// cOccurrences counts non-overlapping occurrences of exact in content.
func cOccurrences(content, exact string) int {
	if exact == "" {
		return 0
	}
	return strings.Count(content, exact)
}

// cDecodeUTF8 is `buffer.toString('utf8')`: invalid bytes become U+FFFD.
// (Node replaces each maximal invalid subsequence; this replaces per byte —
// they differ only on truncated multi-byte sequences.)
func cDecodeUTF8(b []byte) string {
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

// cJSReplaceFirst is `s.replace(search, replacement)` with a string pattern:
// the first occurrence only, and the replacement's `$` patterns expanded
// ($$, $&, $`, $'; a string pattern has no groups, so $1 stays literal).
func cJSReplaceFirst(s, search, replacement string) string {
	i := strings.Index(s, search)
	if i < 0 {
		return s
	}
	var sb strings.Builder
	sb.WriteString(s[:i])
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
			sb.WriteString(search)
			j++
		case '`':
			sb.WriteString(s[:i])
			j++
		case '\'':
			sb.WriteString(s[i+len(search):])
			j++
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteString(s[i+len(search):])
	return sb.String()
}

// cApplyState is one target path across the batch.
type cApplyState struct {
	path, absolute         string
	original, current      *string
	mode                   fs.FileMode
	hasMode                bool
	temp, backup           string
	backupMoved, installed bool
}

func cRandomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// cApplyPreparedRepoFileActions is applyPreparedRepoFileActions: revalidate
// and apply a prepared batch. Existing targets move to sibling backups before
// any final content is installed, allowing a reverse rollback.
func cApplyPreparedRepoFileActions(repoPath string, actions []PreparedRepoChatFileAction, ignored map[string]bool) (int, error) {
	states := map[string]*cApplyState{}
	var ordered []*cApplyState
	batchBytes := 0

	// Full-batch validation and in-memory simulation happen before the first
	// directory, temporary file, backup, or repository target is changed.
	for _, action := range actions {
		if action.Type != "edit_file" && action.Type != "write_file" && action.Type != "delete_file" {
			return 0, cFail("unknown", "The file action type is not supported.", action.Path)
		}
		path, absolute, err := cNormalizedTarget(repoPath, action.Path)
		if err != nil {
			return 0, err
		}
		if ignored[path] {
			return 0, cFail("ignored_path", "Ignored files cannot be changed by repository chat.", path)
		}

		state := states[path]
		if state == nil {
			exists, isFile, mode, err := cInspectTarget(repoPath, path)
			if err != nil {
				return 0, err
			}
			if exists && !isFile {
				return 0, cFail("unsafe_path", "The target is not a regular file.", path)
			}
			var original *string
			if exists {
				data, err := os.ReadFile(absolute)
				if err != nil {
					return 0, cNodeFsErr(err)
				}
				if bytes.IndexByte(data, 0) >= 0 {
					return 0, cFail("binary_file", "Binary files cannot be changed by repository chat.", path)
				}
				if len(data) > cRepoFileMaxBytes {
					return 0, cFail("file_too_large", "The target file is too large.", path)
				}
				text := cDecodeUTF8(data)
				original = &text
			}
			state = &cApplyState{path: path, absolute: absolute, original: original, current: original, mode: mode, hasMode: exists}
			states[path] = state
			ordered = append(ordered, state)
		} else if _, _, _, err := cInspectTarget(repoPath, path); err != nil {
			return 0, err
		}

		var actualHash *string
		if state.current != nil {
			h := cSha256(*state.current)
			actualHash = &h
		}
		if !cSameOptional(actualHash, action.ExpectedHash) {
			return 0, cFail("stale_file", "The file changed after its preview was prepared.", path)
		}

		var after *string
		switch action.Type {
		case "edit_file":
			if state.current == nil {
				return 0, cFail("stale_file", "The file to edit no longer exists.", path)
			}
			count := cOccurrences(*state.current, action.OldText)
			if action.ExpectedOccurrences == nil || count != *action.ExpectedOccurrences || count == 0 || (!action.ReplaceAll && count != 1) {
				return 0, cFail("stale_file", "The exact edit no longer matches its preview.", path)
			}
			var next string
			if action.ReplaceAll {
				next = strings.ReplaceAll(*state.current, action.OldText, action.NewText)
			} else {
				next = cJSReplaceFirst(*state.current, action.OldText, action.NewText)
			}
			after = &next
		case "write_file":
			if action.Mode == "create" && state.current != nil {
				return 0, cFail("stale_file", "The file to create now exists.", path)
			}
			if action.Mode == "replace" && state.current == nil {
				return 0, cFail("stale_file", "The file to replace no longer exists.", path)
			}
			content := action.Content
			after = &content
		default:
			if state.current == nil {
				return 0, cFail("stale_file", "The file to delete no longer exists.", path)
			}
		}

		if after != nil && strings.Contains(*after, "\x00") {
			return 0, cFail("binary_file", "Binary content cannot be written by repository chat.", path)
		}
		finalBytes := 0
		if after != nil {
			finalBytes = len(*after)
		}
		if finalBytes > cRepoFileMaxBytes {
			return 0, cFail("file_too_large", "The resulting file is too large.", path)
		}
		batchBytes += finalBytes
		if batchBytes > cRepoFileBatchMaxBytes {
			return 0, cFail("batch_too_large", "The file batch is too large.", path)
		}
		if cSameOptional(after, state.current) {
			return 0, cFail("unknown", "The file action would not change the file.", path)
		}
		state.current = after
	}

	var createdDirs []string
	if err := cInstallStates(repoPath, ordered, &createdDirs); err != nil {
		var rollbackPaths []string
		for i := len(ordered) - 1; i >= 0; i-- {
			st := ordered[i]
			failed := false
			if st.backupMoved && st.backup != "" {
				if rerr := os.RemoveAll(st.absolute); rerr != nil {
					failed = true
				} else if rerr := os.Rename(st.backup, st.absolute); rerr != nil {
					failed = true
				} else {
					st.backupMoved = false
				}
			} else if st.original == nil && st.installed {
				if rerr := os.RemoveAll(st.absolute); rerr != nil {
					failed = true
				}
			}
			if failed {
				rollbackPaths = append(rollbackPaths, st.path)
			}
			if st.temp != "" {
				if rerr := os.RemoveAll(st.temp); rerr != nil {
					if !cContains(rollbackPaths, st.path) {
						rollbackPaths = append(rollbackPaths, st.path)
					}
				} else {
					st.temp = ""
				}
			}
		}
		for i := len(createdDirs) - 1; i >= 0; i-- {
			_ = os.Remove(createdDirs[i])
		}
		if len(rollbackPaths) > 0 {
			return 0, &cFileActionError{code: "rollback_failed",
				message: "The file batch failed and one or more original paths could not be restored.", paths: rollbackPaths}
		}
		var fe *cFileActionError
		if errors.As(err, &fe) {
			return 0, err
		}
		return 0, &cFileActionError{code: "unknown", message: err.Error(), paths: []string{}}
	}

	// Backups are no longer needed after every final target is installed.
	for _, st := range ordered {
		if st.backup != "" {
			_ = os.RemoveAll(st.backup)
		}
		if st.temp != "" {
			_ = os.RemoveAll(st.temp)
		}
	}
	return len(actions), nil
}

// cInstallStates writes every new content to a temp sibling, moves every
// original aside, then renames the temps into place.
func cInstallStates(repoPath string, ordered []*cApplyState, createdDirs *[]string) error {
	for _, st := range ordered {
		if st.current == nil {
			continue
		}
		if err := cEnsureParentDirectories(repoPath, st.absolute, createdDirs); err != nil {
			return err
		}
		st.temp = filepath.Join(filepath.Dir(st.absolute), ".gitcito-"+filepath.Base(st.absolute)+".tmp-"+cRandomUUID())
		f, err := os.OpenFile(st.temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			st.temp = ""
			return cNodeFsErr(err)
		}
		_, werr := f.WriteString(*st.current)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return cNodeFsErr(werr)
		}
		if st.hasMode {
			if err := os.Chmod(st.temp, st.mode.Perm()); err != nil {
				return cNodeFsErr(err)
			}
		}
	}

	// Move every original aside first. If any later installation fails, all
	// original contents are still available for a complete reverse restore.
	for _, st := range ordered {
		if st.original == nil || cSameOptional(st.current, st.original) {
			continue
		}
		st.backup = filepath.Join(filepath.Dir(st.absolute), ".gitcito-"+filepath.Base(st.absolute)+".bak-"+cRandomUUID())
		if err := os.Rename(st.absolute, st.backup); err != nil {
			return cNodeFsErr(err)
		}
		st.backupMoved = true
	}

	for _, st := range ordered {
		if cSameOptional(st.current, st.original) || st.current == nil {
			continue
		}
		if st.temp == "" {
			return &cFileActionError{code: "unknown", message: "A prepared temporary file is missing.", paths: []string{st.path}}
		}
		if err := os.Rename(st.temp, st.absolute); err != nil {
			return cNodeFsErr(err)
		}
		st.temp = ""
		st.installed = true
	}
	return nil
}

// cEnsureParentDirectories creates target's missing parents, refusing a
// symlinked or non-directory one, and records what it created.
func cEnsureParentDirectories(repoPath, target string, created *[]string) error {
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if parent == root {
		return nil
	}
	relTarget, _ := filepath.Rel(root, target)
	rel, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	var missing []string
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		current = filepath.Join(current, part)
		info, lerr := os.Lstat(current)
		if lerr != nil {
			if !errors.Is(lerr, fs.ErrNotExist) {
				return cNodeFsErr(lerr)
			}
			missing = append(missing, current)
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return cFail("symlink_path", "Symlink targets and parents are not allowed.", relTarget)
		}
		if !info.IsDir() {
			return cFail("unsafe_path", "A target parent is not a directory.", relTarget)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(parent, 0o777); err != nil {
		return cNodeFsErr(err)
	}
	*created = append(*created, missing...)
	return nil
}

// cSameOptional is `a === b` for `string | null`.
func cSameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
