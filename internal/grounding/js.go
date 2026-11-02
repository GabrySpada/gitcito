package grounding

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Helpers for porting TypeScript that inspects untrusted, model-produced JSON.
// Values are the decoded tree ParseLooseJSON returns (map[string]any, []any,
// string, json.Number or float64, bool, nil). These reproduce the JavaScript
// checks the TS validators perform — typeof, `!== undefined`, JSON.stringify —
// so the Go port produces the same correction lines for the same reply.

// AsObject is the `value && typeof value === 'object' && !Array.isArray(value)`
// check: v as a JSON object.
func AsObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok && m != nil
}

// isObjectLike is `!!v && typeof v === 'object'`: objects and arrays.
func isObjectLike(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return x != nil
	case []any:
		return true
	}
	return false
}

// Prop reads `v.key` on an object; arrays and primitives have no such
// property. ok is false when the key is absent (undefined), true for an
// explicit null.
func Prop(v any, key string) (any, bool) {
	m, isObj := v.(map[string]any)
	if !isObj || m == nil {
		return nil, false
	}
	x, ok := m[key]
	return x, ok
}

// get is Prop without the presence flag.
func get(v any, key string) any {
	x, _ := Prop(v, key)
	return x
}

// str is `typeof v === 'string' ? v : ”` with a flag.
func str(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// arr is Array.isArray.
func arr(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

// isNumber is `typeof v === 'number'`.
func isNumber(v any) bool {
	switch v.(type) {
	case json.Number, float64, int:
		return true
	}
	return false
}

// toFloat reads a JSON number.
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

// trim is String.prototype.trim().
func trim(s string) string { return shared.JSTrim(s) }

// JSONStringify is JSON.stringify for a decoded JSON value (undefined is the
// caller's business: pass nil for `x ?? null`). Object keys come out sorted —
// a decoded map has lost its source order, the one place this can differ from
// the TS text.
func JSONStringify(v any) string {
	var b strings.Builder
	writeJS(&b, v)
	return b.String()
}

func writeJS(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeJSString(b, x)
	case json.Number, float64, int:
		f, _ := toFloat(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			b.WriteString("null")
		} else {
			b.WriteString(shared.JSString(f))
		}
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJS(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSString(b, k)
			b.WriteByte(':')
			writeJS(b, x[k])
		}
		b.WriteByte('}')
	case Obj:
		b.WriteByte('{')
		for i, kv := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSString(b, kv.K)
			b.WriteByte(':')
			writeJS(b, kv.V)
		}
		b.WriteByte('}')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSString(b, e)
		}
		b.WriteByte(']')
	case json.RawMessage:
		b.Write(x)
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(raw)
	}
}

// writeJSString quotes s the way JSON.stringify does: only `"`, `\` and the
// C0 controls are escaped; `<`, `>`, `&`, U+2028 and U+2029 stay literal
// (encoding/json would escape them).
func writeJSString(b *strings.Builder, s string) {
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
			b.WriteByte("0123456789abcdef"[r>>4])
			b.WriteByte("0123456789abcdef"[r&0xf])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// KV is one property of an Obj.
type KV struct {
	K string
	V any
}

// Obj is a JSON object that keeps its key order — how a JS object literal
// serializes. Schemas sent to the model are built from it so the bytes match
// the TS JSON.stringify output exactly.
type Obj []KV

// MarshalJSON writes the object in order, JS style.
func (o Obj) MarshalJSON() ([]byte, error) { return []byte(JSONStringify(o)), nil }

// With returns o with key set: an existing key keeps its position and takes the
// new value, a new key is appended — `{ ...o, [key]: v }`.
func (o Obj) With(key string, v any) Obj {
	out := append(Obj{}, o...)
	for i := range out {
		if out[i].K == key {
			out[i].V = v
			return out
		}
	}
	return append(out, KV{key, v})
}

// Raw serializes v (an Obj tree) JS style.
func Raw(v any) json.RawMessage { return json.RawMessage(JSONStringify(v)) }

// SliceJS is `s.slice(0, n)` counted in UTF-16 code units. Where JS would cut
// a surrogate pair in half, the lone high surrogate it keeps turns into U+FFFD
// as soon as the text is JSON-encoded and decoded again — which is what the
// provider and the renderer receive — so that is what this keeps too.
func SliceJS(s string, n int) string {
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
			if units+1 == n {
				return s[:i] + "�"
			}
			return s[:i]
		}
		units += w
	}
	return s
}

// Int returns a pointer to n, for the optional numeric options below.
func Int(n int) *int { return &n }
