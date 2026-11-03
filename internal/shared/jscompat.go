package shared

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// JavaScript string and RegExp semantics the process-group ports (launch,
// devices, analyze, todoscan, localci) lean on: localeCompare ordering, JS
// regular-expression character classes, `String.prototype.replace` replacement
// patterns, UTF-16 slicing and a streaming UTF-8 decoder for process output.

// ─── localeCompare ─────────────────────────────────────────────────────────

// LocaleCompare approximates `a.localeCompare(b)` — ICU's root collation,
// which is what Electron's V8 used. It is not a full UCA, but it reproduces the
// orderings a file list and a device picker actually show:
//   - whitespace, then punctuation and symbols (in DUCET order), then emoji,
//     then currency signs, then digits, then letters; digits compare as
//     characters, not numbers ("10" < "9");
//   - letters compare case- and accent-insensitively first, then by accent,
//     then lower case before upper case;
//   - other scripts sort after Latin by code point.
//
// (Same algorithm as gitsvc's unexported copy, shared here for the non-git
// packages.)
func LocaleCompare(a, b string) int {
	ka, kb := collationKey(a), collationKey(b)
	for level := 0; level < 3; level++ {
		if c := compareInts(ka[level], kb[level]); c != 0 {
			return c
		}
	}
	return 0
}

func compareInts(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// punctOrder is DUCET's order for the ASCII variable characters.
const punctOrder = "\t\n\v\f\r _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~"

const (
	weightPunct    = 1_000
	weightSymbol   = 2_000
	weightCurrency = 2_500
	weightDigit    = 3_000
	weightLatin    = 4_000
	weightOther    = 10_000
)

var latinFold = map[rune]struct {
	base   string
	accent int
}{
	'à': {"a", 1}, 'á': {"a", 2}, 'â': {"a", 3}, 'ã': {"a", 4}, 'ä': {"a", 5}, 'å': {"a", 6},
	'ç': {"c", 7}, 'è': {"e", 1}, 'é': {"e", 2}, 'ê': {"e", 3}, 'ë': {"e", 5},
	'ì': {"i", 1}, 'í': {"i", 2}, 'î': {"i", 3}, 'ï': {"i", 5}, 'ñ': {"n", 4},
	'ò': {"o", 1}, 'ó': {"o", 2}, 'ô': {"o", 3}, 'õ': {"o", 4}, 'ö': {"o", 5}, 'ø': {"o", 8},
	'ù': {"u", 1}, 'ú': {"u", 2}, 'û': {"u", 3}, 'ü': {"u", 5}, 'ý': {"y", 2}, 'ÿ': {"y", 5},
	'ß': {"ss", 9}, 'æ': {"ae", 9}, 'œ': {"oe", 9},
}

func collationKey(s string) [3][]int {
	var k [3][]int
	add := func(p, sec, ter int) {
		k[0] = append(k[0], p)
		k[1] = append(k[1], sec)
		k[2] = append(k[2], ter)
	}
	for _, r := range s {
		ter := 0
		if unicode.IsUpper(r) {
			ter = 1
			r = unicode.ToLower(r)
		}
		if f, ok := latinFold[r]; ok {
			for i, c := range f.base {
				sec := 0
				if i == 0 {
					sec = f.accent
				}
				add(weightLatin+int(c-'a'), sec, ter)
			}
			continue
		}
		switch {
		case r >= 'a' && r <= 'z':
			add(weightLatin+int(r-'a'), 0, ter)
		case r >= '0' && r <= '9':
			add(weightDigit+int(r-'0'), 0, ter)
		case r < 0x80 && strings.ContainsRune(punctOrder, r):
			add(weightPunct+strings.IndexRune(punctOrder, r), 0, ter)
		case unicode.Is(unicode.Sc, r):
			add(weightCurrency+int(r)%500, 0, ter)
		case unicode.IsSpace(r) || unicode.IsPunct(r):
			add(weightPunct+len(punctOrder)+int(r)%500, 0, ter)
		case unicode.IsSymbol(r) || r >= 0x1F000 && r <= 0x1FAFF:
			add(weightSymbol+int(r)%500, 0, ter)
		case unicode.IsDigit(r):
			add(weightDigit+int(r)%10, 0, ter)
		default:
			add(weightOther+int(r), 0, ter)
		}
	}
	return k
}

// CompareUTF16 is JavaScript's `<` on strings: code-unit order, which differs
// from Go's byte order only between astral characters and U+E000–U+FFFF.
func CompareUTF16(a, b string) int {
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

// ─── JavaScript RegExp ─────────────────────────────────────────────────────

// JSSpaceClass is the body of JavaScript's `\s` character class.
const JSSpaceClass = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// JSDot is JavaScript's `.` without the `s` flag: anything but a line terminator.
const JSDot = `[^\n\r\x{2028}\x{2029}]`

// JSRegexp compiles a JavaScript regular expression source (no `u` flag) for
// Go. RE2 shares JS's syntax for everyday patterns; the translation covers the
// places where the same text means something different: `.` (JS excludes \r,
// U+2028, U+2029 too), `\s`/`\S` (Unicode spaces in JS) and `\uXXXX`. What RE2
// cannot express at all — lookaround, backreferences — fails to compile, and
// callers treat that like an invalid pattern.
func JSRegexp(src string, ignoreCase bool) (*regexp.Regexp, error) {
	var b strings.Builder
	if ignoreCase {
		b.WriteString("(?i)")
	}
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			n := src[i+1]
			switch {
			case n == 's' && inClass:
				b.WriteString(JSSpaceClass)
			case n == 's':
				b.WriteString("[" + JSSpaceClass + "]")
			case n == 'S' && !inClass:
				b.WriteString("[^" + JSSpaceClass + "]")
			case n == 'u' && i+5 < len(src) && isHex(src[i+2:i+6]):
				b.WriteString(`\x{` + src[i+2:i+6] + `}`)
				i += 4
			default:
				b.WriteByte(c)
				b.WriteByte(n)
			}
			i++
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
		case c == '.' && !inClass:
			b.WriteString(JSDot)
		default:
			b.WriteByte(c)
		}
	}
	return regexp.Compile(b.String())
}

// MustJSRegexp is JSRegexp for package-level patterns known to compile.
func MustJSRegexp(src string, ignoreCase bool) *regexp.Regexp {
	re, err := JSRegexp(src, ignoreCase)
	if err != nil {
		panic(err)
	}
	return re
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ExpandJSReplacement expands the `$` patterns of a `String.prototype.replace`
// replacement string for one match: `$$`, `$&`, “ $` “, `$'` and `$n`/`$nn`
// (only for groups the pattern has — otherwise the text stays literal). s is
// the subject and m the match's submatch byte indices.
func ExpandJSReplacement(repl, s string, m []int) string {
	if !strings.Contains(repl, "$") {
		return repl
	}
	groups := len(m)/2 - 1
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		c := repl[i]
		if c != '$' || i+1 >= len(repl) {
			b.WriteByte(c)
			continue
		}
		n := repl[i+1]
		switch {
		case n == '$':
			b.WriteByte('$')
			i++
		case n == '&':
			b.WriteString(s[m[0]:m[1]])
			i++
		case n == '`':
			b.WriteString(s[:m[0]])
			i++
		case n == '\'':
			b.WriteString(s[m[1]:])
			i++
		case n >= '0' && n <= '9':
			// Two digits win when they name an existing group.
			if i+2 < len(repl) && repl[i+2] >= '0' && repl[i+2] <= '9' {
				g := int(n-'0')*10 + int(repl[i+2]-'0')
				if g >= 1 && g <= groups {
					if m[2*g] >= 0 {
						b.WriteString(s[m[2*g]:m[2*g+1]])
					}
					i += 2
					continue
				}
			}
			g := int(n - '0')
			if g >= 1 && g <= groups {
				if m[2*g] >= 0 {
					b.WriteString(s[m[2*g]:m[2*g+1]])
				}
				i++
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// JSReplaceAll is `s.replace(/re/g, repl)` with a string replacement.
func JSReplaceAll(re *regexp.Regexp, s, repl string) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		b.WriteString(ExpandJSReplacement(repl, s, m))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// JSReplaceFirstString is `s.replace('literal', repl)`: the first occurrence
// only, with the replacement's `$` patterns expanded.
func JSReplaceFirstString(s, literal, repl string) string {
	i := strings.Index(s, literal)
	if i < 0 {
		return s
	}
	m := []int{i, i + len(literal)}
	return s[:i] + ExpandJSReplacement(repl, s, m) + s[i+len(literal):]
}

// TailUTF16 is `s.slice(-n)` counted in UTF-16 code units. Where JS would cut
// a surrogate pair in half, the whole pair is dropped.
func TailUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := 0
	for i := len(s); i > 0; {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		w := utf16.RuneLen(r)
		if w < 0 {
			w = 1
		}
		if units+w > n {
			return s[i:]
		}
		units += w
		i -= size
	}
	return s
}

// ─── streaming UTF-8 ───────────────────────────────────────────────────────

// UTF8Stream turns a byte stream into strings without splitting a multi-byte
// character across two chunks — Node's StringDecoder, which node-pty used.
// A sequence cut at a read boundary is held back for the next chunk; invalid
// bytes become U+FFFD as Node does. Not safe for concurrent use.
type UTF8Stream struct {
	carry []byte
}

// Decode returns the complete characters of carry+p and keeps the tail.
func (d *UTF8Stream) Decode(p []byte) string {
	buf := append(d.carry, p...)
	d.carry = nil
	// Hold back an incomplete sequence at the end (at most 3 bytes).
	cut := len(buf)
	for back := 1; back <= 3 && back <= len(buf); back++ {
		c := buf[len(buf)-back]
		if c&0xC0 != 0x80 { // a lead byte (or ASCII)
			if c >= 0xC0 {
				need := 2
				switch {
				case c >= 0xF0:
					need = 4
				case c >= 0xE0:
					need = 3
				}
				if need > back {
					cut = len(buf) - back
				}
			}
			break
		}
	}
	if cut < len(buf) {
		d.carry = append([]byte(nil), buf[cut:]...)
		buf = buf[:cut]
	}
	return strings.ToValidUTF8(string(buf), "�")
}

// Flush returns whatever is still held back (as U+FFFD), at end of stream.
func (d *UTF8Stream) Flush() string {
	if len(d.carry) == 0 {
		return ""
	}
	d.carry = nil
	return "�"
}
