package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The TS module works on JSON with JavaScript semantics: JSON.parse into a
// value tree, `typeof` checks, `String(v)`, and JSON.stringify for the request
// bodies a provider receives. Those bodies are product behaviour (prompt
// wording, field order and escaping are what the model sees), so they are
// built and serialized here byte-for-byte the way V8 does it — encoding/json
// sorts map keys and escapes <, >, & and U+2028/9, which JSON.stringify never
// does.
//
// A decoded value is one of: nil, bool, float64, string, []any, *Obj.

// Obj is a JSON object with JavaScript property order: canonical array-index
// keys ("0", "17") first in ascending order, then the rest in insertion order;
// setting an existing key keeps its position.
type Obj struct {
	keys []string
	vals map[string]any
}

// newObj builds an object from alternating key, value pairs.
func newObj(kv ...any) *Obj {
	o := &Obj{vals: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Set writes key, keeping the position of an existing key.
func (o *Obj) Set(k string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Get reads key; ok is false when it is absent (JS `undefined`).
func (o *Obj) Get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

// V is `o?.[k]`: nil when o is nil or k is absent.
func (o *Obj) V(k string) any {
	if o == nil {
		return nil
	}
	return o.vals[k]
}

// Has is the `in` operator for an own property.
func (o *Obj) Has(k string) bool {
	_, ok := o.Get(k)
	return ok
}

// Delete removes key.
func (o *Obj) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// Keys returns the keys in JavaScript property order.
func (o *Obj) Keys() []string {
	if o == nil {
		return nil
	}
	var ints, rest []string
	for _, k := range o.keys {
		if _, ok := arrayIndexKey(k); ok {
			ints = append(ints, k)
		} else {
			rest = append(rest, k)
		}
	}
	for i := 1; i < len(ints); i++ {
		for j := i; j > 0; j-- {
			a, _ := arrayIndexKey(ints[j-1])
			b, _ := arrayIndexKey(ints[j])
			if a <= b {
				break
			}
			ints[j-1], ints[j] = ints[j], ints[j-1]
		}
	}
	return append(ints, rest...)
}

// Clone is `{ ...o }`.
func (o *Obj) Clone() *Obj {
	c := &Obj{vals: map[string]any{}}
	if o == nil {
		return c
	}
	c.keys = append([]string(nil), o.keys...)
	for k, v := range o.vals {
		c.vals[k] = v
	}
	return c
}

// MarshalJSON lets an Obj cross the bridge in property order.
func (o *Obj) MarshalJSON() ([]byte, error) { return []byte(jsonStringify(o)), nil }

// UnmarshalJSON decodes with JSON.parse semantics.
func (o *Obj) UnmarshalJSON(b []byte) error {
	v, err := jsonParse(string(b))
	if err != nil {
		return err
	}
	m, ok := v.(*Obj)
	if !ok {
		return errors.New("not a JSON object")
	}
	*o = *m
	return nil
}

// arrayIndexKey reports whether k is a canonical array index.
func arrayIndexKey(k string) (uint64, bool) {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(k, 10, 32)
	if err != nil || n == math.MaxUint32 {
		return 0, false
	}
	return n, true
}

// undefinedT marks a property JSON.stringify leaves out of an object.
type undefinedT struct{}

// ─── JSON.parse ─────────────────────────────────────────────────────────────

// jsonParse is JSON.parse: exactly one value, numbers as doubles, a repeated
// key keeps its first position and takes the last value. The error carries
// V8's message.
func jsonParse(text string) (any, error) {
	if !json.Valid([]byte(text)) {
		return nil, errors.New(v8JSONError(text))
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New(v8JSONError(text))
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			o := &Obj{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(kt.(string), v)
			}
			_, err := dec.Token()
			return o, err
		}
		arr := []any{}
		for dec.More() {
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token()
		return arr, err
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil {
			// Out of range: V8 yields ±Infinity (or 0 for an underflow, which
			// ParseFloat already returns without an error).
			if f > 0 || (f == 0 && !strings.HasPrefix(string(t), "-")) {
				return math.Inf(1), nil
			}
			return math.Inf(-1), nil
		}
		return f, nil
	}
	return tok, nil // string, bool, nil
}

// ─── JSON.stringify ─────────────────────────────────────────────────────────

// jsonStringify is JSON.stringify(v) for the values this package builds.
func jsonStringify(v any) string {
	var b strings.Builder
	writeJSON(&b, v)
	return b.String()
}

func writeJSON(b *strings.Builder, v any) {
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
		if math.IsNaN(x) || math.IsInf(x, 0) {
			b.WriteString("null")
		} else {
			b.WriteString(jsNumberString(x))
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case string:
		writeJSONString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if _, u := e.(undefinedT); u {
				b.WriteString("null")
				continue
			}
			writeJSON(b, e)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, e)
		}
		b.WriteByte(']')
	case *Obj:
		if x == nil {
			b.WriteString("null")
			return
		}
		b.WriteByte('{')
		first := true
		for _, k := range x.Keys() {
			val := x.vals[k]
			if _, u := val.(undefinedT); u {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			writeJSONString(b, k)
			b.WriteByte(':')
			writeJSON(b, val)
		}
		b.WriteByte('}')
	case json.Marshaler:
		raw, err := x.MarshalJSON()
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(raw)
	default:
		// Structs from this package: encoding/json without HTML escaping is
		// only used for values whose strings are plain (no U+2028/9 concerns
		// matter outside request bodies, which never take this path).
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(x)
		b.WriteString(strings.TrimSuffix(buf.String(), "\n"))
	}
}

const hexDigits = "0123456789abcdef"

// writeJSONString is V8's QuoteJSONString: only quotes, backslashes and C0
// controls are escaped (lowercase \u00xx), and lone surrogates — which a Go
// string cannot hold — are not a concern.
func writeJSONString(b *strings.Builder, s string) {
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
			b.WriteByte(hexDigits[r>>4])
			b.WriteByte(hexDigits[r&0xf])
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// jsNumberString is `String(n)` for a double.
func jsNumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		// Go writes e+21 / e-07; JS writes e+21 / e-7.
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[:1]
		digits := strings.TrimLeft(exp[1:], "0")
		return mant + "e" + sign + digits
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ─── JavaScript value semantics ─────────────────────────────────────────────

// jsString is `String(v)`.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case undefinedT:
		return "undefined"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberString(x)
	case string:
		return x
	case []any:
		return jsJoin(x, ",")
	}
	return "[object Object]"
}

// jsJoin is Array.prototype.join: null and undefined become empty strings.
func jsJoin(items []any, sep string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		if it != nil {
			parts[i] = jsString(it)
		}
	}
	return strings.Join(parts, sep)
}

// asObj is `value && typeof value === 'object' && !Array.isArray(value)`.
func asObj(v any) *Obj {
	o, _ := v.(*Obj)
	return o
}

// isJSSpace is a whitespace character to String.prototype.trim and to the
// regexp class \s.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// jsTrimEnd is String.prototype.trimEnd.
func jsTrimEnd(s string) string { return strings.TrimRightFunc(s, isJSSpace) }

// nonEmptyString is `typeof v === 'string' && v.trim().length > 0`.
func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && jsTrim(s) != ""
}

// utf16Len is `s.length`.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// sliceUTF16 is `s.slice(0, n)`. A surrogate pair cut in half keeps no half:
// Go strings cannot hold a lone surrogate, so the cut moves before the pair.
func sliceUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// utf16Units and fromUTF16 convert to and from JavaScript's string model.
func utf16Units(s string) []uint16 { return utf16.Encode([]rune(s)) }
func fromUTF16(u []uint16) string  { return string(utf16.Decode(u)) }

// decodeText is `Response.text()`: a leading BOM dropped and every invalid
// byte replaced with U+FFFD.
func decodeText(b []byte) string {
	b = bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
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
