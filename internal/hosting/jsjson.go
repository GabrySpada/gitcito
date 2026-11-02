package hosting

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// The TS module parsed every response with `res.json()` / `JSON.parse` and
// read fields with JavaScript semantics (`??`, `||`, truthiness, template
// stringification). These helpers reproduce exactly the parts it relied on, so
// the Go port takes the same branches and produces the same error text.

// errJSONShape is returned when a well-formed JSON response does not fit the
// shape a caller decodes it into. TS would have thrown a TypeError further on
// ("Cannot read properties of …"); that text cannot be reproduced faithfully.
//
//lint:ignore ST1005 shown to the user verbatim, like the TS errors around it
var errJSONShape = errors.New("Unexpected response from the server")

// stripBOM drops a leading UTF-8 byte order mark, as `Response.text()` /
// `Response.json()` do before decoding.
func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
}

// parseJSON is `JSON.parse(text)` into v. A syntax error carries V8's message
// (what the renderer used to show); a shape mismatch is errJSONShape.
func parseJSON(text []byte, v any) error {
	if !json.Valid(text) {
		return errors.New(v8JSONError(string(text)))
	}
	if err := json.Unmarshal(text, v); err != nil {
		return errJSONShape
	}
	return nil
}

// decodeAny parses JSON into a generic tree (float64 numbers), or nil when the
// text is not JSON — `res.json().catch(() => null)`.
func decodeAny(text []byte) any {
	var v any
	if !json.Valid(text) {
		return nil
	}
	if err := json.Unmarshal(text, &v); err != nil {
		return nil
	}
	return v
}

// rawProp is the raw JSON of `body.key` when body is a JSON object holding key.
func rawProp(body []byte, key string) (json.RawMessage, bool) {
	if !json.Valid(body) {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}

// ── V8's JSON.parse error messages ──────────────────────────────────────────

const (
	v8MaxContext       = 10
	v8MinContextSource = v8MaxContext*2 + 1
)

// v8JSONError reproduces the message V8 (Node 22, Electron's main process)
// throws for invalid JSON. It mirrors V8's JsonParser: the same error classes,
// positions in UTF-16 code units, the context window around an unexpected
// token, and the line/column suffix.
func v8JSONError(src string) string {
	p := &v8Parser{s: utf16.Encode([]rune(src))}
	p.ws()
	if !p.value() {
		return p.msg
	}
	p.ws()
	if p.i < len(p.s) {
		return "Unexpected non-whitespace character after JSON" + p.where()
	}
	return "Unexpected end of JSON input" // valid JSON never reaches here
}

type v8Parser struct {
	s   []uint16
	i   int
	msg string
}

func (p *v8Parser) eof() bool { return p.i >= len(p.s) }

func (p *v8Parser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

// at fails with "<what> in JSON at position N (line L column C)".
func (p *v8Parser) at(what string) bool {
	p.msg = what + " in JSON" + p.where()
	return false
}

// where is V8's " at position N (line L column C)" suffix. Only \r, \n and
// \r\n count as line breaks, as in JSON whitespace.
func (p *v8Parser) where() string {
	line, last := 1, 0
	for c := 0; c < p.i && c < len(p.s); c++ {
		if p.s[c] == '\r' && c < p.i-1 && p.s[c+1] == '\n' {
			c++
		}
		if p.s[c] == '\r' || p.s[c] == '\n' {
			line++
			last = c + 1
		}
	}
	return " at position " + strconv.Itoa(p.i) +
		" (line " + strconv.Itoa(line) + " column " + strconv.Itoa(1+p.i-last) + ")"
}

// unexpected fails at the current position with V8's "Unexpected token" text,
// or "Unexpected end of JSON input" at the end.
func (p *v8Parser) unexpected() bool {
	if p.eof() {
		p.msg = "Unexpected end of JSON input"
		return false
	}
	src := string(utf16.Decode(p.s))
	switch src {
	case "NaN", "Infinity", "undefined", "[object Object]":
		p.msg = `"` + src + `" is not valid JSON`
		return false
	}
	tok := string(utf16.Decode(p.s[p.i : p.i+1]))
	n := len(p.s)
	if n < v8MinContextSource {
		p.msg = "Unexpected token '" + tok + "', \"" + src + "\" is not valid JSON"
		return false
	}
	start, end := 0, n
	prefix, suffix := "", ""
	switch {
	case p.i < v8MaxContext:
		end = p.i + v8MaxContext
		suffix = "..."
	case p.i < n-v8MaxContext:
		start, end = p.i-v8MaxContext, p.i+v8MaxContext
		prefix, suffix = "...", "..."
	default:
		start = p.i - v8MaxContext
		prefix = "..."
	}
	ctx := string(utf16.Decode(p.s[start:end]))
	p.msg = "Unexpected token '" + tok + "', " + prefix + `"` + ctx + `"` + suffix + " is not valid JSON"
	return false
}

func (p *v8Parser) value() bool {
	if p.eof() {
		return p.unexpected()
	}
	switch c := p.s[p.i]; {
	case c == '"':
		return p.str()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case c == 't':
		return p.literal("true")
	case c == 'f':
		return p.literal("false")
	case c == 'n':
		return p.literal("null")
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	}
	return p.unexpected()
}

func (p *v8Parser) literal(word string) bool {
	for k := 0; k < len(word); k++ {
		if p.eof() || p.s[p.i] != uint16(word[k]) {
			return p.unexpected()
		}
		p.i++
	}
	return true
}

func isDigit(c uint16) bool { return c >= '0' && c <= '9' }

func (p *v8Parser) number() bool {
	if p.s[p.i] == '-' {
		p.i++
		if p.eof() || !isDigit(p.s[p.i]) {
			return p.at("No number after minus sign")
		}
	}
	if p.s[p.i] == '0' {
		p.i++
		if !p.eof() && isDigit(p.s[p.i]) {
			return p.at("Unexpected number")
		}
	} else {
		for !p.eof() && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	if !p.eof() && p.s[p.i] == '.' {
		p.i++
		if p.eof() || !isDigit(p.s[p.i]) {
			return p.at("Unterminated fractional number")
		}
		for !p.eof() && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	if !p.eof() && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if !p.eof() && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if p.eof() || !isDigit(p.s[p.i]) {
			return p.at("Exponent part is missing a number")
		}
		for !p.eof() && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	return true
}

func isHex(c uint16) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (p *v8Parser) str() bool {
	p.i++ // opening quote
	for {
		if p.eof() {
			return p.at("Unterminated string")
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return true
		case c == '\\':
			p.i++
			if p.eof() {
				return p.unexpected()
			}
			switch p.s[p.i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.i++
			case 'u':
				p.i++
				for k := 0; k < 4; k++ {
					if p.eof() || !isHex(p.s[p.i]) {
						return p.at("Bad Unicode escape")
					}
					p.i++
				}
			default:
				return p.at("Bad escaped character")
			}
		case c < 0x20:
			return p.at("Bad control character in string literal")
		default:
			p.i++
		}
	}
}

func (p *v8Parser) object() bool {
	p.i++
	p.ws()
	if !p.eof() && p.s[p.i] == '}' {
		p.i++
		return true
	}
	if p.eof() || p.s[p.i] != '"' {
		return p.at("Expected property name or '}'")
	}
	for {
		if !p.str() {
			return false
		}
		p.ws()
		if p.eof() || p.s[p.i] != ':' {
			return p.at("Expected ':' after property name")
		}
		p.i++
		p.ws()
		if !p.value() {
			return false
		}
		p.ws()
		if p.eof() {
			return p.at("Expected ',' or '}' after property value")
		}
		switch p.s[p.i] {
		case '}':
			p.i++
			return true
		case ',':
			p.i++
			p.ws()
			if p.eof() || p.s[p.i] != '"' {
				return p.at("Expected double-quoted property name")
			}
		default:
			return p.at("Expected ',' or '}' after property value")
		}
	}
}

func (p *v8Parser) array() bool {
	p.i++
	p.ws()
	if !p.eof() && p.s[p.i] == ']' {
		p.i++
		return true
	}
	for {
		if !p.value() {
			return false
		}
		p.ws()
		if p.eof() {
			return p.at("Expected ',' or ']' after array element")
		}
		switch p.s[p.i] {
		case ']':
			p.i++
			return true
		case ',':
			p.i++
			p.ws()
		default:
			return p.at("Expected ',' or ']' after array element")
		}
	}
}

// ── JavaScript value semantics over a decoded JSON tree ─────────────────────

// jsTruthy is JavaScript truthiness for a decoded JSON value.
func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true // objects and arrays, even empty ones
}

// jsNumberString is `String(n)` for a finite or non-finite double.
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

// jsString is `String(v)` / template-literal interpolation of a decoded value.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
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

// jsJoin is `Array.prototype.join`: null and undefined become empty strings.
func jsJoin(items []any, sep string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		if it != nil {
			parts[i] = jsString(it)
		}
	}
	return strings.Join(parts, sep)
}

// prop reads `v?.key` from a decoded value: nil when v is not an object.
func prop(v any, key string) any {
	if m, ok := v.(map[string]any); ok {
		return m[key]
	}
	return nil
}

// jsonStringify is `JSON.stringify(v)` for values built by this package
// (ordered objects, strings, numbers, booleans, slices). JS escapes only
// quotes, backslashes and control characters — not <, >, & or U+2028/9.
func jsonStringify(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return unescapeLineSeparators(strings.TrimSuffix(buf.String(), "\n"))
}

// unescapeLineSeparators undoes encoding/json's unconditional escaping of
// U+2028 and U+2029, which JSON.stringify leaves as raw characters. Only
// escapes that are really escapes (an odd run of backslashes) are rewritten.
func unescapeLineSeparators(s string) string {
	if !strings.Contains(s, `\u202`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			run := 0
			for j := i; j < len(s) && s[j] == '\\'; j++ {
				run++
			}
			// Copy all but the last backslash of the run: pairs are literal.
			b.WriteString(s[i : i+run-1])
			i += run - 1
			if run%2 == 1 && i+5 < len(s) && (s[i+1:i+6] == "u2028" || s[i+1:i+6] == "u2029") {
				if s[i+5] == '8' {
					b.WriteString(" ")
				} else {
					b.WriteString(" ")
				}
				i += 5
				continue
			}
			b.WriteByte('\\')
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// stringifyRaw is `JSON.stringify(JSON.parse(raw))`: the same value
// re-serialized compactly, keys in JavaScript property order (integer-like
// keys ascending first, then insertion order; a repeated key keeps its first
// position and takes the last value), strings re-escaped the JS way.
func stringifyRaw(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := orderedValue(dec)
	if err != nil {
		return ""
	}
	return jsonStringify(v)
}

// orderedObject is a JSON object that keeps JavaScript's property order.
type orderedObject struct {
	keys []string
	vals map[string]any
}

func (o *orderedObject) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// arrayIndexKey reports whether k is a canonical array index ("0", "17"),
// which JavaScript orders numerically before every other key.
func arrayIndexKey(k string) (uint64, bool) {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 32)
	if err != nil || n == math.MaxUint32 {
		return 0, false
	}
	return n, true
}

func (o *orderedObject) MarshalJSON() ([]byte, error) {
	var ints, rest []string
	for _, k := range o.keys {
		if _, ok := arrayIndexKey(k); ok {
			ints = append(ints, k)
		} else {
			rest = append(rest, k)
		}
	}
	// Stable insertion sort: index keys are few.
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
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range append(ints, rest...) {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(jsonStringify(k))
		buf.WriteByte(':')
		buf.WriteString(jsonStringify(o.vals[k]))
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// jsNumber re-encodes a decoded number the way JSON.stringify prints a double.
type jsNumber float64

func (n jsNumber) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return []byte(jsNumberString(f)), nil
}

func orderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			o := &orderedObject{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := orderedValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(kt.(string), v)
			}
			_, err := dec.Token()
			return o, err
		}
		arr := []any{}
		for dec.More() {
			v, err := orderedValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token()
		return arr, err
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return jsNumber(math.Inf(1)), nil
		}
		return jsNumber(f), nil
	}
	return tok, nil // string, bool, nil
}

// jsObject is an object literal with keys in the order written. A value of
// `undefined` (the zero undefinedValue) drops the key, as JSON.stringify does.
type jsObject []jsField

type jsField struct {
	key string
	val any
}

type undefinedValue struct{}

// undefined marks a property JSON.stringify leaves out.
var undefined = undefinedValue{}

func (o jsObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, f := range o {
		if _, skip := f.val.(undefinedValue); skip {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.WriteString(jsonStringify(f.key))
		buf.WriteByte(':')
		buf.WriteString(jsonStringify(f.val))
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// orUndefined is `s || undefined`.
func orUndefined(s string) any {
	if s == "" {
		return undefined
	}
	return s
}

// jsToNumber is `Number(s)` for a string: whitespace-trimmed decimal, or a
// 0x/0o/0b integer; "" is 0 and anything else NaN.
func jsToNumber(s string) float64 {
	t := gitx.TrimJS(s)
	if t == "" {
		return 0
	}
	if len(t) > 2 && t[0] == '0' {
		base := 0
		switch t[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n, err := strconv.ParseUint(t[2:], base, 64)
			if err != nil || strings.Contains(t[2:], "_") {
				return math.NaN()
			}
			return float64(n)
		}
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	for _, r := range t {
		if !strings.ContainsRune("0123456789+-.eE", r) {
			return math.NaN()
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return f
		}
		return math.NaN()
	}
	return f
}
