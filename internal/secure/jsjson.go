package secure

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The .gitcito format is JSON written by JSON.stringify, and its ciphertext
// covers the JSON of the payload — so re-encrypting byte for byte means
// producing JSON byte for byte. encoding/json disagrees with JSON.stringify in
// three ways that matter here: it sorts map keys (JS keeps insertion order),
// it escapes <, >, & and U+2028/U+2029 (JS escapes none of them), and it
// formats numbers it holds as json.Number verbatim (JS re-formats them). The
// helpers below are the small subset of JSON.parse / JSON.stringify the format
// needs, with JS's rules.

// jsObject is a JSON object with its key order, as JS keeps it.
type jsObject []jsField

type jsField struct {
	Key string
	Val any
}

// get returns a property; a missing key and null both read as nil.
func (o jsObject) get(key string) any {
	for _, f := range o {
		if f.Key == key {
			return f.Val
		}
	}
	return nil
}

// MarshalJSON lets a parsed value travel to the renderer through Wails'
// encoder with its key order intact.
func (o jsObject) MarshalJSON() ([]byte, error) {
	return stringifyJS(o, ""), nil
}

// parseJS is JSON.parse: objects keep key order (a repeated key keeps its
// first position and its last value, as in V8), numbers stay json.Number so
// stringifyJS can format them the way JS would after the round trip.
func parseJS(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after JSON value")
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
		switch t {
		case '{':
			obj := jsObject{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				replaced := false
				for i := range obj {
					if obj[i].Key == key {
						obj[i].Val = val
						replaced = true
						break
					}
				}
				if !replaced {
					obj = append(obj, jsField{key, val})
				}
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, errors.New("unexpected delimiter")
	default:
		return tok, nil
	}
}

// stringifyJS is JSON.stringify(v) (indent "") or JSON.stringify(v, null, 2)
// (indent "  ") for the values parseJS and this package build: jsObject,
// []any, string, bool, nil, json.Number, float64 and the Go integer types.
func stringifyJS(v any, indent string) []byte {
	var b bytes.Buffer
	writeJS(&b, v, indent, "")
	return b.Bytes()
}

func writeJS(b *bytes.Buffer, v any, indent, prefix string) {
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
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			b.WriteString("null")
			return
		}
		b.WriteString(jsNumber(f))
	case float64:
		b.WriteString(jsNumber(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case jsObject:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		inner := prefix + indent
		b.WriteByte('{')
		for i, f := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if indent != "" {
				b.WriteByte('\n')
				b.WriteString(inner)
			}
			writeJSString(b, f.Key)
			b.WriteByte(':')
			if indent != "" {
				b.WriteByte(' ')
			}
			writeJS(b, f.Val, indent, inner)
		}
		if indent != "" {
			b.WriteByte('\n')
			b.WriteString(prefix)
		}
		b.WriteByte('}')
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		inner := prefix + indent
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if indent != "" {
				b.WriteByte('\n')
				b.WriteString(inner)
			}
			writeJS(b, item, indent, inner)
		}
		if indent != "" {
			b.WriteByte('\n')
			b.WriteString(prefix)
		}
		b.WriteByte(']')
	default:
		// Not produced by this package; encoding/json is the best effort.
		raw, err := json.Marshal(x)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(raw)
	}
}

// writeJSString quotes like JSON.stringify: only ", \ and the C0 controls are
// escaped (the five with short forms, the rest as lowercase \u00xx). Invalid
// UTF-8 cannot come out of a JS string; it is written as U+FFFD.
func writeJSString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && w == 1 {
			b.WriteString("�")
			i++
			continue
		}
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[r>>4])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteString(s[i : i+w])
			}
		}
		i += w
	}
	b.WriteByte('"')
}

// jsNumber is Number.prototype.toString() as JSON.stringify applies it:
// shortest round-trip digits, plain notation for exponents in [-7, 21),
// "1e+21" style otherwise; -0 is "0"; NaN and ±Infinity are null.
func jsNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	// d.ddde±x → digits and exponent.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k := len(digits)
	n := exp + 1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	ex := n - 1
	if ex < 0 {
		ex = -ex
	}
	if k == 1 {
		return sign + digits + "e" + es + strconv.Itoa(ex)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + es + strconv.Itoa(ex)
}

// jsTruthy is JavaScript truthiness for a parsed JSON value.
func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return false
		}
		return f != 0 && !math.IsNaN(f)
	case float64:
		return x != 0 && !math.IsNaN(x)
	default:
		return true // objects and arrays, even empty ones
	}
}

// jsNum returns a parsed JSON number as float64 (`typeof x === 'number'`).
func jsNum(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return f, true
}

// utf16Len is a JS string's .length.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// nodeBase64 is Buffer.from(s, 'base64'): forgiving — it accepts the URL-safe
// alphabet too, skips anything outside the alphabet (line breaks, spaces),
// stops at the first '=' and tolerates missing padding.
func nodeBase64(s string) []byte {
	clean := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '=':
			i = len(s)
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			clean = append(clean, c)
		case c == '-':
			clean = append(clean, '+')
		case c == '_':
			clean = append(clean, '/')
		}
	}
	// A lone trailing sextet carries no whole byte; Node drops it.
	if len(clean)%4 == 1 {
		clean = clean[:len(clean)-1]
	}
	out, err := base64.RawStdEncoding.DecodeString(string(clean))
	if err != nil {
		// Non-zero padding bits in the last sextet: Node ignores them, so do it
		// the long way.
		return decodeBase64Loose(clean)
	}
	return out
}

func decodeBase64Loose(clean []byte) []byte {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	out := make([]byte, 0, len(clean)*3/4)
	var acc uint32
	bits := 0
	for _, c := range clean {
		acc = acc<<6 | uint32(strings.IndexByte(alphabet, c))
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>uint(bits)))
		}
	}
	return out
}

// decodeUTF8 is Node's `buffer.toString('utf-8')`: invalid sequences become
// U+FFFD, one per maximal subpart (the WHATWG rule). Every file and every git
// output the TS read as text went through it.
func decodeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r != utf8.RuneError || w > 1 {
			b.WriteString(s[i : i+w])
			i += w
			continue
		}
		b.WriteRune(utf8.RuneError)
		i += maximalSubpart(s[i:])
	}
	return b.String()
}

// maximalSubpart is how many bytes of an invalid sequence one U+FFFD stands
// for: the lead byte plus every continuation byte that could still have
// completed it.
func maximalSubpart(s string) int {
	c := s[0]
	need, lo, hi := 0, byte(0x80), byte(0xBF)
	switch {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		need = 2
	case c == 0xED:
		need, hi = 2, 0x9F
	case c == 0xF0:
		need, lo = 3, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	case c == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(s); k++ {
		b := s[n]
		if k == 0 && (b < lo || b > hi) || k > 0 && (b < 0x80 || b > 0xBF) {
			break
		}
		n++
	}
	return n
}
