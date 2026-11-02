package shared

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
)

// Helpers for porting TypeScript that works on loosely typed JSON. Settings are
// held as the decoded JSON tree (map[string]any / []any / string / json.Number /
// bool / nil) rather than structs: the renderer owns that shape and changes it
// often, and a struct would silently drop every field Go does not know about.
// These helpers reproduce the JavaScript semantics the TS code relies on —
// truthiness, `String.prototype.trim`, property-key coercion, `===` — so the
// Go port takes the same branches on the same data.

// Object is a decoded JSON object.
type Object = map[string]any

// DecodeJSON parses exactly one JSON value, the way JSON.parse does: trailing
// non-whitespace is an error. Numbers decode as json.Number so they re-encode
// byte for byte instead of drifting through float64.
func DecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON: unexpected data after top-level value")
	}
	return v, nil
}

// MarshalIndent is JSON.stringify(v, null, 2): two-space indent, no trailing
// newline, and no HTML escaping (JS never turns `<` into `<`).
func MarshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// asObject returns v as an object when it is one.
func asObject(v any) (Object, bool) {
	m, ok := v.(map[string]any)
	return m, ok && m != nil
}

// asArray returns v as an array when it is one.
func asArray(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

// spread is `{ ...v }` for the values JSON can hold: an object copies its own
// properties; null, numbers and booleans contribute nothing. (Strings and
// arrays would spread their indices in JS; settings never hold one where an
// object belongs, so they are treated like null — see the port report.)
func spread(v any) Object {
	out := Object{}
	if m, ok := asObject(v); ok {
		for k, x := range m {
			out[k] = x
		}
	}
	return out
}

// get reads a property; a missing key and an explicit null both come back nil,
// which is all `?.` and `??` can tell apart anyway.
func get(m Object, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

// JSTruthy is JavaScript truthiness for a decoded JSON value.
func JSTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return err == nil && f != 0 && !math.IsNaN(f)
	case float64:
		return x != 0 && !math.IsNaN(x)
	case int:
		return x != 0
	default:
		// Objects and arrays — even empty ones — are truthy.
		return true
	}
}

// jsIsSpace reports whether r is whitespace to String.prototype.trim(). It
// differs from unicode.IsSpace on U+FEFF (JS trims it) and U+0085 (JS keeps it).
func jsIsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// JSTrim is String.prototype.trim().
func JSTrim(s string) string { return strings.TrimFunc(s, jsIsSpace) }

// strOr returns v when it is a string, else def. Used where TS writes
// `(x ?? def)` and then calls a string method: a non-string there would throw
// in JS, and the port degrades to def instead.
func strOr(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

// jsKey is the property key JS derives from v in `obj[v]`: String(v).
func jsKey(v any) string {
	switch x := v.(type) {
	case nil:
		// A missing property and null coerce differently in JS ("undefined" vs
		// "null"); callers that care pass the raw lookup through keyOf instead.
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number:
		if f, err := strconv.ParseFloat(string(x), 64); err == nil {
			return jsNumberString(f)
		}
		return string(x)
	case float64:
		return jsNumberString(x)
	case int:
		return strconv.Itoa(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = jsKey(e)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// keyOf is jsKey for a property read that may be missing: `obj[p.id]` with no
// `id` coerces undefined, not null.
func keyOf(m Object, prop string) string {
	v, ok := m[prop]
	if !ok {
		return "undefined"
	}
	return jsKey(v)
}

func jsNumberString(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if f == 0 {
		return "0"
	}
	if math.Abs(f) < 1e21 && f == math.Trunc(f) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// jsStrictEqual is `a === b` for JSON primitives. Two objects or arrays are
// never equal here: decoded JSON never shares a reference between two places.
func jsStrictEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case json.Number, float64, int:
		fa, oka := toFloat(a)
		fb, okb := toFloat(b)
		return oka && okb && fa == fb
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}
