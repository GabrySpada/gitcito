package gitsvc

// Helpers shared by slice D's files (sync, clone, submodules, worktrees,
// subtrees, hooks, LFS, attributes, filters, credentials, replace refs,
// maintenance, bundles, clean, .gitcito.json). Slice-prefixed because other
// slices write into this package in parallel; the ones worth hoisting into
// gitx are listed in the port report.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// dJSSpace is the character class JavaScript's `\s` matches (gitx keeps the
// same constant unexported).
const dJSSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// dJSDot is JavaScript's `.`: anything but a line terminator.
const dJSDot = `[^\n\r\x{2028}\x{2029}]`

var dWhitespaceRe = regexp.MustCompile(`[` + dJSSpace + `]+`)

// dSplitWS is `s.split(/\s+/)`: empty leading/trailing fields kept, as in JS.
func dSplitWS(s string) []string {
	return dWhitespaceRe.Split(s, -1)
}

// dLines is `s.split('\n')`.
func dLines(s string) []string {
	return strings.Split(s, "\n")
}

// dResolve is Node's path.resolve(base, p) for an absolute base: p wins when
// it is absolute, and the result is always cleaned.
func dResolve(base string, parts ...string) string {
	out := base
	for _, p := range parts {
		if filepath.IsAbs(p) {
			out = p
		} else {
			out = filepath.Join(out, p)
		}
	}
	if !filepath.IsAbs(out) {
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
	}
	return filepath.Clean(out)
}

// dExists is fs.existsSync.
func dExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// dGitDir is the absolute git directory the way several git.ts methods
// resolve it: `rev-parse --git-dir` (relative to the repo), '.git' when that
// fails.
func dGitDir(ctx context.Context, repoPath string) string {
	out, err := gitx.RunGit(ctx, repoPath, []string{"rev-parse", "--git-dir"}, gitx.RunOpts{})
	if err != nil {
		out = ".git"
	}
	return dResolve(repoPath, gitx.TrimJS(out))
}

// dMtimeMs is Node's `stat.mtimeMs`: milliseconds, with the sub-millisecond
// part kept as a fraction.
func dMtimeMs(fi fs.FileInfo) float64 {
	return float64(fi.ModTime().UnixNano()) / 1e6
}

// dErrno names an errno the way Node does in its error messages.
var dErrno = map[syscall.Errno][2]string{
	syscall.ENOENT:    {"ENOENT", "no such file or directory"},
	syscall.EACCES:    {"EACCES", "permission denied"},
	syscall.EEXIST:    {"EEXIST", "file already exists"},
	syscall.EISDIR:    {"EISDIR", "illegal operation on a directory"},
	syscall.ENOTDIR:   {"ENOTDIR", "not a directory"},
	syscall.EPERM:     {"EPERM", "operation not permitted"},
	syscall.ENOTEMPTY: {"ENOTEMPTY", "directory not empty"},
}

// dNodeFSError renders a filesystem error as Node's fs module words it
// (`ENOENT: no such file or directory, stat '/x'`), which is the text the TS
// methods let reach the renderer. dest is the second path of a two-path
// syscall (copyfile, rename), or "".
func dNodeFSError(syscallName, path, dest string, err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	name, ok := dErrno[errno]
	if !ok {
		name = [2]string{"E" + strings.ToUpper(errno.Error()), errno.Error()}
	}
	msg := name[0] + ": " + name[1] + ", " + syscallName + " '" + path + "'"
	if dest != "" {
		msg += " -> '" + dest + "'"
	}
	return errors.New(msg)
}

// dCompareUTF16 orders strings the way JavaScript's default sort does: by
// UTF-16 code units, which differs from Go's byte order only above U+FFFF.
func dCompareUTF16(a, b string) int {
	if a == b {
		return 0
	}
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}

// dSortJS is `array.sort()` on strings.
func dSortJS(xs []string) {
	sort.SliceStable(xs, func(i, j int) bool { return dCompareUTF16(xs[i], xs[j]) < 0 })
}

// dPunctOrder is CLDR root collation's order for ASCII punctuation and
// symbols, all of which sort before digits and letters.
const dPunctOrder = "_-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

// dLocaleCompare approximates `a.localeCompare(b)` (ICU root collation) for
// the strings it is used on here — repository paths. Primary strength:
// whitespace, then punctuation in CLDR order, then digits, then letters
// case-insensitively; ties go to lowercase-first, then to code-unit order.
// It is not a full UCA: non-ASCII letters compare by lowercased code point.
func dLocaleCompare(a, b string) int {
	ka, kb := dCollationKey(a), dCollationKey(b)
	for i := 0; i < len(ka) && i < len(kb); i++ {
		if ka[i] != kb[i] {
			if ka[i] < kb[i] {
				return -1
			}
			return 1
		}
	}
	if len(ka) != len(kb) {
		if len(ka) < len(kb) {
			return -1
		}
		return 1
	}
	// Tertiary: lowercase before uppercase at the first case difference.
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if ra[i] != rb[i] {
			la, lb := dIsLower(ra[i]), dIsLower(rb[i])
			if la != lb {
				if la {
					return -1
				}
				return 1
			}
		}
	}
	return dCompareUTF16(a, b)
}

func dIsLower(r rune) bool { return strings.ToLower(string(r)) == string(r) }

func dCollationKey(s string) []int {
	out := make([]int, 0, len(s))
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			out = append(out, 1)
		case r < 0x80 && strings.ContainsRune(dPunctOrder, r):
			out = append(out, 10+strings.IndexRune(dPunctOrder, r))
		case r >= '0' && r <= '9':
			out = append(out, 100+int(r-'0'))
		default:
			lr := []rune(strings.ToLower(string(r)))[0]
			out = append(out, 1000+int(lr))
		}
	}
	return out
}

// dDecodeUTF8 is Node's `buffer.toString('utf-8')` / `readFile(p, 'utf-8')`:
// invalid sequences become U+FFFD, one per maximal invalid subpart (the
// WHATWG decoder), where a plain Go string would carry the raw bytes and the
// JSON encoder would then replace them byte by byte.
func dDecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			sb.WriteRune(r)
			i += size
			continue
		}
		// Invalid: consume the lead byte plus every continuation byte that is
		// still a valid continuation of it, and emit one replacement.
		lead := b[i]
		need, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case lead >= 0xC2 && lead <= 0xDF:
			need = 1
		case lead == 0xE0:
			need, lo = 2, 0xA0
		case lead >= 0xE1 && lead <= 0xEC, lead == 0xEE, lead == 0xEF:
			need = 2
		case lead == 0xED:
			need, hi = 2, 0x9F
		case lead == 0xF0:
			need, lo = 3, 0x90
		case lead >= 0xF1 && lead <= 0xF3:
			need = 3
		case lead == 0xF4:
			need, hi = 3, 0x8F
		}
		j := i + 1
		for k := 0; k < need && j < len(b); k++ {
			c := b[j]
			if k == 0 && (c < lo || c > hi) || k > 0 && (c < 0x80 || c > 0xBF) {
				break
			}
			j++
		}
		sb.WriteRune(utf8.RuneError)
		i = j
	}
	return sb.String()
}

// dReadUTF8 is `readFile(p, 'utf-8')`.
func dReadUTF8(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return dDecodeUTF8(b), nil
}

// dErr builds an error whose text is shown to the user verbatim, exactly as
// the TS threw it — capitalised and punctuated, which is why it does not go
// through errors.New with a literal (staticcheck ST1005).
func dErr(msg string) error { return errors.New(msg) }

// dErrf is dErr with formatting.
func dErrf(format string, args ...any) error { return fmt.Errorf(format, args...) }
