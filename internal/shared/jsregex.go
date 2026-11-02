package shared

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Pieces for porting JavaScript regular expressions and value coercions that
// RE2 / encoding/json read differently. Go's `\s` is ASCII-only and its `.`
// only excludes '\n'; JS's `\s` covers the Unicode spaces and its `.` excludes
// every line terminator. Patterns ported from TS splice these in so the same
// text matches the same way.
const (
	jsSpaceChars = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	// JSSpace is JS `\s`.
	JSSpace = `[` + jsSpaceChars + `]`
	// JSNotSpace is JS `\S`.
	JSNotSpace = `[^` + jsSpaceChars + `]`
	// JSDot is JS `.` without the `s` flag.
	JSDot = `[^\n\r\x{2028}\x{2029}]`
)

// JSString is `String(v)` for a decoded JSON value: numbers print the way JS
// prints them, arrays join with commas, objects become "[object Object]".
func JSString(v any) string { return jsKey(v) }

// OrderedEntries is `Object.entries(v)` for one raw JSON value, keeping the
// order JS would: integer-like keys ascending first, then the other keys in
// source order (a repeated key keeps its first position and its last value).
// Arrays give their indices; strings their characters; anything else nothing.
// Values decode as json.Number-carrying trees.
func OrderedEntries(raw json.RawMessage) [][2]any {
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, "{"):
		return orderedObject(raw)
	case strings.HasPrefix(trimmed, "["):
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return nil
		}
		out := make([][2]any, 0, len(arr))
		for i, item := range arr {
			v, _ := DecodeJSON(item)
			out = append(out, [2]any{strconv.Itoa(i), v})
		}
		return out
	case strings.HasPrefix(trimmed, `"`):
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		out := [][2]any{}
		i := 0
		for _, r := range s {
			out = append(out, [2]any{strconv.Itoa(i), string(r)})
			i++
		}
		return out
	}
	return nil
}

func orderedObject(raw json.RawMessage) [][2]any {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var keys []string
	vals := map[string]any{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, _ := tok.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		if _, seen := vals[key]; !seen {
			keys = append(keys, key)
		}
		vals[key] = v
	}
	var ints, rest []string
	for _, k := range keys {
		if isArrayIndex(k) {
			ints = append(ints, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(ints, func(i, j int) bool {
		a, _ := strconv.ParseUint(ints[i], 10, 64)
		b, _ := strconv.ParseUint(ints[j], 10, 64)
		return a < b
	})
	out := make([][2]any, 0, len(keys))
	for _, k := range append(ints, rest...) {
		out = append(out, [2]any{k, vals[k]})
	}
	return out
}

// isArrayIndex is the property-key rule that makes JS enumerate a key first:
// a canonical unsigned integer below 2^32-1.
func isArrayIndex(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 4294967295
}
