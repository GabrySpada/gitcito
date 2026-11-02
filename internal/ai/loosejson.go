package ai

import (
	"regexp"
	"strings"
)

// parseLooseJson from src/main/grounding.ts: model replies wrapped in fences,
// <think> preludes, a provider's escaping layer or a self-hosted model's
// characteristic damage, repaired as far as is safe. JavaScript regexps are
// reproduced by hand where RE2 differs: `^`/`$` under the m flag also break on
// \r, U+2028 and U+2029, \s is JS whitespace, and RE2 has no lookahead.

// jsWS is the JS regexp class \s.
const jsWS = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

func isLineTerminatorBefore(s string, i int) bool {
	if i == 0 {
		return true
	}
	if s[i-1] == '\n' || s[i-1] == '\r' {
		return true
	}
	return i >= 3 && (s[i-3:i] == " " || s[i-3:i] == " ")
}

func isLineEndAt(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	if s[i] == '\n' || s[i] == '\r' {
		return true
	}
	return strings.HasPrefix(s[i:], " ") || strings.HasPrefix(s[i:], " ")
}

// cleanFence is `.replace(/^```(json)?/m, ”).replace(/```$/m, ”).trim()`.
func cleanFence(v string) string {
	for i := 0; i+3 <= len(v); i++ {
		if v[i] == '`' && strings.HasPrefix(v[i:], "```") && isLineTerminatorBefore(v, i) {
			end := i + 3
			if strings.HasPrefix(v[end:], "json") {
				end += 4
			}
			v = v[:i] + v[end:]
			break
		}
	}
	for i := 0; i+3 <= len(v); i++ {
		if v[i] == '`' && strings.HasPrefix(v[i:], "```") && isLineEndAt(v, i+3) {
			v = v[:i] + v[i+3:]
			break
		}
	}
	return jsTrim(v)
}

// parseClean is the inner `parse`: JSON.parse after cleanFence, failing (as a
// JS `null` result does) on anything unparseable or on a literal null.
func parseClean(v string) (any, bool) {
	out, err := jsonParse(cleanFence(v))
	if err != nil || out == nil {
		return nil, false
	}
	return out, true
}

// asciiLower lowers A–Z only, keeping byte offsets. A non-unicode JS regexp
// with the i flag matches an ASCII pattern exactly this way.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// stripThinking removes <think>…</think> blocks, anything up to a stray
// closing tag, and an unterminated block at the end.
func stripThinking(text string) string {
	lower := asciiLower(text)
	var b strings.Builder
	pos := 0
	for {
		open := strings.Index(lower[pos:], "<think>")
		if open < 0 {
			break
		}
		open += pos
		closeAt := strings.Index(lower[open+7:], "</think>")
		if closeAt < 0 {
			break
		}
		end := open + 7 + closeAt + len("</think>")
		b.WriteString(text[pos:open])
		pos = end
	}
	b.WriteString(text[pos:])
	out := b.String()
	if last := strings.LastIndex(asciiLower(out), "</think>"); last >= 0 {
		out = out[last+len("</think>"):]
	}
	if open := strings.Index(asciiLower(out), "<think>"); open >= 0 {
		out = out[:open]
	}
	return out
}

var envelopeRE = regexp.MustCompile(`^[{\[]` + jsWS + `*\\"`)

// unescapeJSONEnvelope decodes providers that return the JSON object escaped
// as if it were a string.
func unescapeJSONEnvelope(value string) string {
	trimmed := jsTrim(value)
	if !envelopeRE.MatchString(trimmed) {
		return value
	}
	quoted := `"` + strings.ReplaceAll(strings.ReplaceAll(trimmed, "\r", `\r`), "\n", `\n`) + `"`
	decoded, err := jsonParse(quoted)
	if err != nil {
		return value
	}
	if s, ok := decoded.(string); ok {
		return s
	}
	return value
}

// replaceLook is a global `replace` of a pattern that ends in a lookahead.
// head must be anchored with ^; every pattern here starts with an ASCII
// character, so trying each byte offset matches JS's scan.
func replaceLook(s string, head *regexp.Regexp, look *regexp.Regexp, repl func(m []string) string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		loc := head.FindStringSubmatchIndex(s[i:])
		if loc == nil || (look != nil && !look.MatchString(s[i+loc[1]:])) {
			i++
			continue
		}
		groups := make([]string, len(loc)/2)
		for g := range groups {
			if loc[2*g] >= 0 {
				groups[g] = s[i+loc[2*g] : i+loc[2*g+1]]
			}
		}
		b.WriteString(s[last:i])
		b.WriteString(repl(groups))
		i += loc[1]
		last = i
		if loc[1] == 0 {
			i++
		}
	}
	b.WriteString(s[last:])
	return b.String()
}

var (
	qaEmptyToken   = regexp.MustCompile(`^,` + jsWS + `*""` + jsWS + `*,` + jsWS + `*`)
	qaPropLook     = regexp.MustCompile(`^"[^"]+"` + jsWS + `*:`)
	qaDoubleColon  = regexp.MustCompile(`^"` + jsWS + `*:` + jsWS + `*"` + jsWS + `*:` + jsWS + `*`)
	qaOpenLook     = regexp.MustCompile(`^[{\[]`)
	qaQuotedBrace  = regexp.MustCompile(`\{` + jsWS + `*"` + jsWS + `*\{`)
	qaQuoteIndent  = regexp.MustCompile(`^\{"(` + jsWS + `+)`)
	qaCloseQuote   = regexp.MustCompile(`^\}` + jsWS + `*"` + jsWS + `*`)
	qaCloserLook   = regexp.MustCompile(`^[,\]]`)
	wikiSummary    = regexp.MustCompile(`^\{` + jsWS + `*"summary"` + jsWS + `*:`)
	wikiSections   = regexp.MustCompile(`"sections"` + jsWS + `*:`)
	wikiClaims     = regexp.MustCompile(`"claims"` + jsWS + `*:`)
	wikiRelated    = regexp.MustCompile(`"related"` + jsWS + `*:`)
	wikiHeading    = regexp.MustCompile(`,` + jsWS + `*"heading"` + jsWS + `*:`)
	wikiNestedRel  = regexp.MustCompile(`,` + jsWS + `*\{` + jsWS + `*"related"` + jsWS + `*:` + jsWS + `*(\[[^\[\]]*\])` + jsWS + `*\}` + jsWS + `*\z`)
	startsWithOpen = regexp.MustCompile(`^[{\[]`)
)

// repairQuotedArrayObjects repairs a narrow DiffusionGemma failure mode where
// array objects acquire an extra quote and brace (`{"{"text"...}`) plus a
// quote before the closing bracket. The normal parser always runs first, so
// valid JSON strings that happen to contain braces are left untouched.
func repairQuotedArrayObjects(v string) string {
	v = replaceLook(v, qaEmptyToken, qaPropLook, func([]string) string { return "," })
	v = replaceLook(v, qaDoubleColon, qaOpenLook, func([]string) string { return `":` })
	v = qaQuotedBrace.ReplaceAllLiteralString(v, "{")
	v = replaceLook(v, qaQuoteIndent, qaPropLook, func(m []string) string { return "{" + m[1] })
	return replaceLook(v, qaCloseQuote, qaCloserLook, func([]string) string { return "}" })
}

// repairMismatchedClosers replaces a closing bracket with the delimiter the
// open container requires. DiffusionGemma occasionally closes a section object
// with `]`, yielding `}]]]` where the schema requires `}]}]`.
func repairMismatchedClosers(v string) string {
	chars := []rune(v)
	var stack []rune
	inString, escaped := false, false
	for i := 0; i < len(chars); i++ {
		c := chars[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == '{' || c == '[' {
			stack = append(stack, c)
			continue
		}
		if (c != '}' && c != ']') || len(stack) == 0 {
			continue
		}
		open := stack[len(stack)-1]
		expected := ']'
		if open == '{' {
			expected = '}'
		}
		if c == expected {
			stack = stack[:len(stack)-1]
			continue
		}
		// A repeated closer means this one replaced the missing delimiter and
		// the next still closes its array. A lone mismatch is left for
		// jsonrepair, which can insert a missing delimiter without consuming
		// later fields.
		next := i + 1
		for next < len(chars) && isJSSpace(chars[next]) {
			next++
		}
		if next < len(chars) && chars[next] == c {
			chars[i] = expected
			stack = stack[:len(stack)-1]
		}
	}
	return string(chars)
}

// repairFlattenedWikiSections restores the page schema when a model omits
// `sections:[{` and writes the first section's heading directly after the
// root summary.
func repairFlattenedWikiSections(v string) string {
	trimmed := jsTrim(v)
	if !wikiSummary.MatchString(trimmed) || wikiSections.MatchString(trimmed) {
		return v
	}
	if !wikiClaims.MatchString(trimmed) || !wikiRelated.MatchString(trimmed) {
		return v
	}
	loc := wikiHeading.FindStringIndex(v)
	if loc == nil {
		return v
	}
	return v[:loc[0]] + `,"sections":[{"heading":` + v[loc[1]:]
}

// repairNestedWikiRelated moves a trailing `{ "related": [...] }` out of the
// sections array.
func repairNestedWikiRelated(v string) string {
	if !wikiSections.MatchString(v) {
		return v
	}
	m := wikiNestedRel.FindStringSubmatchIndex(v)
	if m == nil {
		return v
	}
	return v[:m[0]] + `],"related":` + v[m[2]:m[3]] + `}` + v[m[1]:]
}

// jsonContainerCandidates finds complete top-level JSON containers inside
// prose or repeated replies. Every delimiter is ASCII, so a byte scan finds
// the same boundaries as JS's code-unit scan.
func jsonContainerCandidates(v string) []string {
	var candidates []string
	var stack []byte
	start := -1
	inString, escaped := false, false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if start < 0 {
			if c == '{' || c == '[' {
				start = i
				stack = append(stack, c)
			}
			continue
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == '{' || c == '[' {
			stack = append(stack, c)
			continue
		}
		if c != '}' && c != ']' {
			continue
		}
		open := stack[len(stack)-1]
		if (open == '{' && c == '}') || (open == '[' && c == ']') {
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				candidates = append(candidates, v[start:i+1])
				start = -1
			}
		}
	}
	return candidates
}

// ParseLooseJSON strips a stray ```json fence and parses, repairing what it
// safely can; ok is false when nothing usable is left. The value is a decoded
// tree (nil, bool, float64, string, []any, *Obj).
func ParseLooseJSON(text string) (any, bool) {
	if v, ok := parseClean(text); ok {
		return v, true
	}
	// Some self-hosted reasoning models wrap an otherwise valid answer in a
	// visible <think> prelude. Never use that prose; remove the wrapper and
	// validate the JSON payload through the same semantic contract as usual.
	withoutThinking := stripThinking(text)
	if v, ok := parseClean(withoutThinking); ok {
		return v, true
	}
	unescaped := unescapeJSONEnvelope(cleanFence(withoutThinking))
	if v, ok := parseClean(unescaped); ok {
		return v, true
	}
	normalized := repairMismatchedClosers(repairNestedWikiRelated(repairFlattenedWikiSections(repairQuotedArrayObjects(unescaped))))
	candidates := jsonContainerCandidates(normalized)
	for i := len(candidates) - 1; i >= 0; i-- {
		if v, ok := parseClean(candidates[i]); ok {
			return v, true
		}
	}
	if !startsWithOpen.MatchString(jsTrim(normalized)) {
		return nil, false
	}
	repaired, err := jsonRepair(normalized)
	if err != nil {
		return nil, false
	}
	return parseClean(repaired)
}
