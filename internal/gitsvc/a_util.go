package gitsvc

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Small JS-compat helpers for slice A. Each reproduces one Node/JS behaviour
// the TS code leaned on, so the Go port's strings match byte for byte.

// aBasename is Node's path.basename (POSIX): trailing slashes are ignored and
// an empty or root path has an empty basename (Go's filepath.Base says ".").
func aBasename(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// aAbsUnder is `p.startsWith('/') ? p : join(repoPath, p)` — the TS idiom for
// a path git printed relative to the work tree.
func aAbsUnder(repoPath, p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return filepath.Join(repoPath, p)
}

// aField is `parts[i]` where an out-of-range index is `undefined`, read
// through `?? ”`.
func aField(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// aJSNumber is JavaScript's Number(s) for the decimal strings git prints:
// surrounding whitespace is ignored, ” is 0, anything unparsable is NaN.
func aJSNumber(s string) float64 {
	s = gitx.TrimJS(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseUint(s[2:], 16, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(n)
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	// ParseFloat accepts forms Number() does not ("inf", "1_000", "0b1"…);
	// git never prints those, and a stray one reads as NaN here and there alike
	// for every input that matters (digits, sign, dot, exponent).
	for _, r := range s {
		if !strings.ContainsRune("0123456789+-.eE", r) {
			return math.NaN()
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return n
}

// aJSInt is `Number(s) || 0` truncated to an integer — NaN and 0 both read as 0.
func aJSInt(s string) int64 {
	n := aJSNumber(s)
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return int64(n)
}

// aHasJSSpace is `/\s/.test(s)`.
func aHasJSSpace(s string) bool {
	return strings.IndexFunc(s, gitx.IsJSSpace) >= 0
}

// aSliceEnd is the end index of `arr.slice(0, n)` on an array of length l:
// a negative n counts from the end.
func aSliceEnd(n, l int) int {
	if n < 0 {
		n += l
		if n < 0 {
			n = 0
		}
	}
	if n > l {
		n = l
	}
	return n
}

// aSliceFrom is the start index of `arr.slice(n)` on an array of length l.
func aSliceFrom(n, l int) int {
	return aSliceEnd(n, l)
}

// aJSONString is JSON.stringify(s): unlike encoding/json it leaves <, >, &
// and U+2028/U+2029 alone.
func aJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(strconv.FormatInt(int64(r)>>4, 16))
			b.WriteString(strconv.FormatInt(int64(r)&0xf, 16))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// nodeErrno maps errnos to the code and libuv description Node puts in an fs
// error message.
var nodeErrno = map[syscall.Errno][2]string{
	syscall.ENOENT:  {"ENOENT", "no such file or directory"},
	syscall.EACCES:  {"EACCES", "permission denied"},
	syscall.EPERM:   {"EPERM", "operation not permitted"},
	syscall.EISDIR:  {"EISDIR", "illegal operation on a directory"},
	syscall.ENOTDIR: {"ENOTDIR", "not a directory"},
	syscall.EEXIST:  {"EEXIST", "file already exists"},
	syscall.EROFS:   {"EROFS", "read-only file system"},
	syscall.ENOSPC:  {"ENOSPC", "no space left on device"},
	syscall.EBUSY:   {"EBUSY", "resource busy or locked"},
	syscall.EMFILE:  {"EMFILE", "too many open files"},
}

// aFsError renders an fs failure the way Node's fs/promises rejects:
// `ENOENT: no such file or directory, open '/x'`. The renderer and the logs
// show this text, so it is kept. Unknown errnos fall back to Go's message.
func aFsError(syscallName, path string, err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if m, ok := nodeErrno[errno]; ok {
			return &aNodeFsError{msg: m[0] + ": " + m[1] + ", " + syscallName + " '" + path + "'", err: err}
		}
	}
	return err
}

type aNodeFsError struct {
	msg string
	err error
}

func (e *aNodeFsError) Error() string { return e.msg }
func (e *aNodeFsError) Unwrap() error { return e.err }

// aReadFile is `readFile(path, 'utf-8')` with Node's error text.
func aReadFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", aFsError("open", path, err)
	}
	return string(b), nil
}

// aWriteFile is `writeFile(path, content, 'utf-8')` with Node's error text.
func aWriteFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		return aFsError("open", path, err)
	}
	return nil
}

// aExists is fs.existsSync: any stat failure reads as "not there".
func aExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func aStrPtr(s string) *string { return &s }

// aOr is `a || b` for strings.
func aOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
