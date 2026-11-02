package gitx

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// jsSpaceClass is the body of a regexp character class matching exactly what
// JavaScript's `\s` matches. Go's `\s` is ASCII-only, so every regex ported
// from TS that used `\s` uses this instead to stay byte-identical.
const jsSpaceClass = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// jsDot matches what JavaScript's `.` matches without the `s` flag: anything
// but a line terminator. Go's `.` only excludes '\n'.
const jsDot = `[^\n\r\x{2028}\x{2029}]`

// IsJSSpace reports whether r is whitespace to String.prototype.trim().
func IsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// TrimJS is String.prototype.trim(). It differs from strings.TrimSpace on
// U+FEFF (JS trims it) and U+0085 (JS keeps it); git output rarely carries
// either, but the error strings the renderer parses must match TS exactly.
func TrimJS(s string) string {
	return strings.TrimFunc(s, IsJSSpace)
}

// TrimEndJS is String.prototype.trimEnd().
func TrimEndJS(s string) string {
	return strings.TrimRightFunc(s, IsJSSpace)
}

// UTF16Len is a JS string's `.length`: UTF-16 code units, not bytes or runes.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 && r <= utf8.MaxRune {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// SliceUTF16 is `s.slice(0, n)` for n >= 0, counted in UTF-16 code units.
// Where JS would cut a surrogate pair in half (leaving a lone surrogate Go
// cannot represent), the whole pair is dropped instead.
func SliceUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if w < 0 {
			w = 1
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}
