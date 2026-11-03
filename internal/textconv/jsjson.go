package textconv

// JSON.parse and JSON.stringify(v, null, 2) with JavaScript's semantics, for
// jsonToStable. encoding/json is close but not the same: it turns a lone
// surrogate escape into U+FFFD (JS keeps the code unit and writes it back as
// `\udXXX`), keeps number literals verbatim (JS rounds them through a double
// and prints them with Number#toString) and escapes U+2028/U+2029 and HTML
// characters (JSON.stringify does not).
//
// Strings are held as Go strings in WTF-8: a lone surrogate is the 3-byte
// sequence its code unit would have if surrogates were encodable, which no
// valid UTF-8 input can produce.

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// jsObject is a parsed object: keys are unique (a repeated key keeps its last
// value, as JSON.parse does); order does not matter, the caller sorts.
type jsObject map[string]any

var errJSON = textErr("Unexpected token in JSON")

type jsonParser struct {
	s string
	i int
}

// parseJSON is JSON.parse: one value, surrounded only by JSON whitespace.
func parseJSON(s string) (any, error) {
	p := &jsonParser{s: s}
	p.ws()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, errJSON
	}
	return v, nil
}

func (p *jsonParser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jsonParser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, errJSON
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	}
	for _, lit := range []struct {
		word string
		v    any
	}{{"true", true}, {"false", false}, {"null", nil}} {
		if strings.HasPrefix(p.s[p.i:], lit.word) {
			p.i += len(lit.word)
			return lit.v, nil
		}
	}
	return nil, errJSON
}

func (p *jsonParser) object() (any, error) {
	p.i++ // {
	obj := jsObject{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return obj, nil
	}
	for {
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, errJSON
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, errJSON
		}
		p.i++
		p.ws()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		obj[key] = v
		p.ws()
		if p.i >= len(p.s) {
			return nil, errJSON
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return obj, nil
		default:
			return nil, errJSON
		}
	}
}

func (p *jsonParser) array() (any, error) {
	p.i++ // [
	arr := []any{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return arr, nil
	}
	for {
		p.ws()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, errJSON
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return arr, nil
		default:
			return nil, errJSON
		}
	}
}

// appendUnit appends one UTF-16 code unit sequence's worth of text: a valid
// pair or BMP code point as UTF-8, a lone surrogate as WTF-8.
func appendRuneWTF8(b *strings.Builder, r rune) {
	if r >= 0xD800 && r <= 0xDFFF {
		b.WriteByte(byte(0xE0 | r>>12))
		b.WriteByte(byte(0x80 | (r>>6)&0x3F))
		b.WriteByte(byte(0x80 | r&0x3F))
		return
	}
	b.WriteRune(r)
}

func (p *jsonParser) hex4() (rune, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	n, err := strconv.ParseUint(p.s[p.i:p.i+4], 16, 32)
	if err != nil {
		return 0, false
	}
	p.i += 4
	return rune(n), true
}

func (p *jsonParser) str() (string, error) {
	p.i++ // "
	var b strings.Builder
	for {
		if p.i >= len(p.s) {
			return "", errJSON
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), nil
		case c < 0x20:
			return "", errJSON
		case c != '\\':
			b.WriteByte(c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.s) {
			return "", errJSON
		}
		e := p.s[p.i]
		p.i++
		switch e {
		case '"', '\\', '/':
			b.WriteByte(e)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return "", errJSON
			}
			if utf16.IsSurrogate(r) && r < 0xDC00 && strings.HasPrefix(p.s[p.i:], `\u`) {
				save := p.i
				p.i += 2
				if lo, ok := p.hex4(); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					b.WriteRune(utf16.DecodeRune(r, lo))
					continue
				}
				p.i = save
			}
			appendRuneWTF8(&b, r)
		default:
			return "", errJSON
		}
	}
}

func (p *jsonParser) number() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		return nil, errJSON
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if digits() == 0 {
			return nil, errJSON
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			return nil, errJSON
		}
	}
	f, err := strconv.ParseFloat(p.s[start:p.i], 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, errJSON
	}
	return f, nil // ErrRange: ±Inf or 0, which is what JS rounds to as well
}

// numberString is Number#toString for a double.
func numberString(f float64) string {
	if f == 0 {
		return "0"
	}
	// Shortest round-trip digits and the decimal exponent, then ECMA-262's
	// Number::toString layout.
	e := strconv.FormatFloat(math.Abs(f), 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	k, n := len(digits), x+1
	var out string
	switch {
	case k <= n && n <= 21:
		out = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		out = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		out = "0." + strings.Repeat("0", -n) + digits
	default:
		sign := "+"
		if n-1 < 0 {
			sign = "-"
		}
		m := digits[:1]
		if k > 1 {
			m += "." + digits[1:]
		}
		out = m + "e" + sign + strconv.Itoa(absInt(n-1))
	}
	if f < 0 {
		return "-" + out
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// quoteJS is JSON.stringify for a (WTF-8) string.
func quoteJS(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		// A WTF-8 lone surrogate: ED A0..BF xx.
		if c == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF {
			r := rune(c&0x0F)<<12 | rune(s[i+1]&0x3F)<<6 | rune(s[i+2]&0x3F)
			b.WriteString(`\u` + strconv.FormatInt(int64(r), 16))
			i += 3
			continue
		}
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
			b.WriteString(strconv.FormatInt(int64(r)&0xF, 16))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// utf16Units is the string as JavaScript sees it, for code-unit ordering.
func utf16Units(s string) []uint16 {
	var out []uint16
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF {
			out = append(out, uint16(c&0x0F)<<12|uint16(s[i+1]&0x3F)<<6|uint16(s[i+2]&0x3F))
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x10000 {
			hi, lo := utf16.EncodeRune(r)
			out = append(out, uint16(hi), uint16(lo))
		} else {
			out = append(out, uint16(r))
		}
		i += size
	}
	return out
}

func lessUTF16(a, b string) bool {
	ua, ub := utf16Units(a), utf16Units(b)
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// stringifyIndent is JSON.stringify(v, null, 2) for parsed values whose
// object keys the caller wants sorted (Object.keys(v).sort()).
func stringifyIndent(b *strings.Builder, v any, indent string) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			b.WriteString("null")
		} else {
			b.WriteString(numberString(x))
		}
	case string:
		b.WriteString(quoteJS(x))
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		inner := indent + "  "
		b.WriteString("[\n")
		for i, item := range x {
			b.WriteString(inner)
			stringifyIndent(b, item, inner)
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case jsObject:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sortUTF16(keys)
		keys = propertyOrder(keys)
		inner := indent + "  "
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(inner + quoteJS(k) + ": ")
			stringifyIndent(b, x[k], inner)
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	}
}

// propertyOrder is how a JS object enumerates keys it was given in order:
// array-index keys ("0" … "4294967294") first in ascending numeric order,
// then the rest as inserted. So jsonToStable writes "9" before "10" even
// though it sorted them the other way round.
func propertyOrder(sorted []string) []string {
	var index, named []string
	for _, k := range sorted {
		if isArrayIndex(k) {
			index = append(index, k)
		} else {
			named = append(named, k)
		}
	}
	sort.SliceStable(index, func(i, j int) bool {
		a, _ := strconv.ParseUint(index[i], 10, 64)
		b, _ := strconv.ParseUint(index[j], 10, 64)
		return a < b
	})
	return append(index, named...)
}

// isArrayIndex: a canonical decimal integer below 2^32-1.
func isArrayIndex(k string) bool {
	if k == "" || len(k) > 1 && k[0] == '0' {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 1<<32-1
}

// sortUTF16 is Array#sort on strings: by UTF-16 code units, which differs from
// Go's byte order only for characters above U+FFFF.
func sortUTF16(keys []string) {
	sort.SliceStable(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
}
