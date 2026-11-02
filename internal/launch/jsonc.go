package launch

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// ─── JSONC parsing ──────────────────────────────────────────────────────────
// launch.json / tasks.json are JSON-with-comments and allow trailing commas.
// Strip both while respecting string literals, then JSON.parse.

var trailingComma = shared.MustJSRegexp(`,(\s*[}\]])`, false)

// stripJsonc works byte by byte: every character it reacts to is ASCII, and a
// UTF-8 continuation byte can never be mistaken for one.
func stripJsonc(input string) string {
	out := make([]byte, 0, len(input))
	inStr, inLine, inBlock := false, false, false
	var strQuote byte
	for i := 0; i < len(input); i++ {
		ch := input[i]
		var next byte
		hasNext := i+1 < len(input)
		if hasNext {
			next = input[i+1]
		}
		if inLine {
			if ch == '\n' {
				inLine = false
				out = append(out, ch)
			}
			continue
		}
		if inBlock {
			if ch == '*' && hasNext && next == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if inStr {
			out = append(out, ch)
			if ch == '\\' {
				if hasNext {
					out = append(out, next)
				}
				i++
			} else if ch == strQuote {
				inStr = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inStr = true
			strQuote = ch
			out = append(out, ch)
			continue
		}
		if ch == '/' && hasNext && next == '/' {
			inLine = true
			i++
			continue
		}
		if ch == '/' && hasNext && next == '*' {
			inBlock = true
			i++
			continue
		}
		out = append(out, ch)
	}
	// Remove trailing commas before } or ].
	return trailingComma.ReplaceAllString(string(out), "${1}")
}

// parseJsonc is `JSON.parse(stripJsonc(raw))`; ok is false where TS caught
// the parse error and returned null. Numbers decode as float64, which is what
// a JS number is and re-encodes the way JSON.stringify does.
func parseJsonc(raw string) (any, bool) {
	var v any
	if err := json.Unmarshal([]byte(stripJsonc(raw)), &v); err != nil {
		return nil, false
	}
	return v, true
}

// ─── loosely typed JSON access ─────────────────────────────────────────────

// truthy is JavaScript truthiness for a decoded JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	case int:
		return x != 0
	}
	return true
}

// jsString is `String(v)` for the scalars a config can hold.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e21 {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	case []any:
		s := ""
		for i, e := range x {
			if i > 0 {
				s += ","
			}
			if e != nil {
				s += jsString(e)
			}
		}
		return s
	}
	return "[object Object]"
}

// str returns m[k] when it is a string.
func str(m map[string]any, k string) (string, bool) {
	s, ok := m[k].(string)
	return s, ok
}

// obj returns v as an object (a JSON object, not an array).
func obj(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok && m != nil
}

// list returns v as an array.
func list(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}
