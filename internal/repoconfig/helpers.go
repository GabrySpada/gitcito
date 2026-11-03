package repoconfig

// Node-shaped helpers the ported code leans on: path.resolve, existsSync,
// readFile(p, 'utf8'), fs error wording and Array.prototype.sort on strings.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
)

// jsSpace is the character class JavaScript's `\s` matches.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// lines is `s.split('\n')`.
func lines(s string) []string {
	return strings.Split(s, "\n")
}

// resolve is Node's path.resolve(base, ...parts): a later absolute part wins,
// and the result is always absolute and cleaned.
func resolve(base string, parts ...string) string {
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

// exists is fs.existsSync.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// errnoNames names an errno the way Node does in its error messages.
var errnoNames = map[syscall.Errno][2]string{
	syscall.ENOENT:    {"ENOENT", "no such file or directory"},
	syscall.EACCES:    {"EACCES", "permission denied"},
	syscall.EEXIST:    {"EEXIST", "file already exists"},
	syscall.EISDIR:    {"EISDIR", "illegal operation on a directory"},
	syscall.ENOTDIR:   {"ENOTDIR", "not a directory"},
	syscall.EPERM:     {"EPERM", "operation not permitted"},
	syscall.ENOTEMPTY: {"ENOTEMPTY", "directory not empty"},
}

// nodeFSError renders a filesystem error as Node's fs module words it
// (`ENOENT: no such file or directory, open '/x'`), the text the TS let reach
// the renderer. dest is the second path of a two-path syscall, or "".
func nodeFSError(syscallName, path, dest string, err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	name, ok := errnoNames[errno]
	if !ok {
		name = [2]string{"E" + strings.ToUpper(errno.Error()), errno.Error()}
	}
	msg := name[0] + ": " + name[1] + ", " + syscallName + " '" + path + "'"
	if dest != "" {
		msg += " -> '" + dest + "'"
	}
	return errors.New(msg)
}

// compareUTF16 orders strings the way JavaScript's default sort does: by
// UTF-16 code units, which differs from Go's byte order only above U+FFFF.
func compareUTF16(a, b string) int {
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

// sortJS is `array.sort()` on strings.
func sortJS(xs []string) {
	sort.SliceStable(xs, func(i, j int) bool { return compareUTF16(xs[i], xs[j]) < 0 })
}

// DecodeUTF8 is Node's `buffer.toString('utf-8')` / `readFile(p, 'utf-8')`:
// invalid sequences become U+FFFD, one per maximal invalid subpart (the
// WHATWG decoder), where a plain Go string would carry the raw bytes.
func DecodeUTF8(b []byte) string {
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

// readUTF8 is `readFile(p, 'utf8')`.
func readUTF8(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return DecodeUTF8(b), nil
}

// errorf builds an error whose text is shown to the user verbatim, exactly as
// the TS threw it — capitalised and punctuated, which is why it does not go
// through errors.New with a literal (staticcheck ST1005).
func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
