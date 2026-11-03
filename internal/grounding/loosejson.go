package grounding

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kaptinlin/jsonrepair"

	"github.com/GabrySpada/gitcito/internal/shared"
)

const sp = shared.JSSpace

// lookahead is a JS regex with a trailing `(?=…)`, ported as a pattern whose
// last capture group is the lookahead text. Matching continues from the start
// of that group, so — as in JS — the lookahead text is not consumed and can
// begin the next match.
type lookahead struct {
	re   *regexp.Regexp
	repl func(groups []string) string
}

func (l lookahead) replaceAll(s string) string {
	var b strings.Builder
	pos := 0
	for pos <= len(s) {
		loc := l.re.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		groups := make([]string, len(loc)/2)
		for g := range groups {
			if loc[2*g] >= 0 {
				groups[g] = s[pos+loc[2*g] : pos+loc[2*g+1]]
			}
		}
		laStart := loc[len(loc)-2]
		b.WriteString(s[pos : pos+loc[0]])
		b.WriteString(l.repl(groups))
		if laStart == loc[0] {
			// Nothing consumed before the lookahead: step past one rune so the
			// scan cannot stall (the patterns here never do this).
			_, size := utf8.DecodeRuneInString(s[pos+laStart:])
			b.WriteString(s[pos+laStart : pos+laStart+size])
			pos += laStart + size
			continue
		}
		pos += laStart
	}
	if pos < len(s) {
		b.WriteString(s[pos:])
	}
	return b.String()
}

func fixed(text string) func([]string) string { return func([]string) string { return text } }

var reBraceQuoteBrace = regexp.MustCompile(`\{` + sp + `*"` + sp + `*\{`)

var quotedArrayRepairs = []func(string) string{
	// .replace(/,\s*""\s*,\s*(?="[^"]+"\s*:)/g, ',')
	lookahead{regexp.MustCompile(`,` + sp + `*""` + sp + `*,` + sp + `*("[^"]+"` + sp + `*:)`), fixed(",")}.replaceAll,
	// .replace(/"\s*:\s*"\s*:\s*(?=[{\[])/g, '":')
	lookahead{regexp.MustCompile(`"` + sp + `*:` + sp + `*"` + sp + `*:` + sp + `*([{\[])`), fixed(`":`)}.replaceAll,
	// .replace(/\{\s*"\s*\{/g, '{')
	func(s string) string { return reBraceQuoteBrace.ReplaceAllLiteralString(s, "{") },
	// .replace(/\{"(\s+)(?="[^"]+"\s*:)/g, '{$1')
	lookahead{regexp.MustCompile(`\{"(` + sp + `+)("[^"]+"` + sp + `*:)`), func(g []string) string { return "{" + g[1] }}.replaceAll,
	// .replace(/\}\s*"\s*(?=[,\]])/g, '}')
	lookahead{regexp.MustCompile(`\}` + sp + `*"` + sp + `*([,\]])`), fixed("}")}.replaceAll,
}

// repairQuotedArrayObjects repairs a narrow DiffusionGemma failure mode where
// array objects acquire an extra quote and brace (`{"{"text"...}`) plus a quote
// before the closing bracket. The normal parser always runs first, so valid
// JSON strings that happen to contain braces are left untouched.
func repairQuotedArrayObjects(value string) string {
	for _, repair := range quotedArrayRepairs {
		value = repair(value)
	}
	return value
}

// repairMismatchedClosers replaces a closing bracket with the delimiter
// required by the open JSON container. DiffusionGemma occasionally closes a
// section object with `]`, yielding `}]]]` where the schema requires `}]}]`.
func repairMismatchedClosers(value string) string {
	chars := []rune(value)
	var stack []rune
	inString, escaped := false, false

	for i := 0; i < len(chars); i++ {
		char := chars[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			continue
		}
		if char == '{' || char == '[' {
			stack = append(stack, char)
			continue
		}
		if (char != '}' && char != ']') || len(stack) == 0 {
			continue
		}

		open := stack[len(stack)-1]
		expected := ']'
		if open == '{' {
			expected = '}'
		}
		if char == expected {
			stack = stack[:len(stack)-1]
			continue
		}

		// A repeated closer means this one replaced the missing delimiter and
		// the next still closes its array. A lone mismatch is left for
		// jsonrepair, which can insert a missing delimiter without consuming
		// later fields.
		next := i + 1
		for next < len(chars) && shared.JSTrim(string(chars[next])) == "" {
			next++
		}
		if next < len(chars) && chars[next] == char {
			chars[i] = expected
			stack = stack[:len(stack)-1]
		}
	}
	return string(chars)
}

var (
	reSummaryFirst  = regexp.MustCompile(`^\{` + sp + `*"summary"` + sp + `*:`)
	reSectionsKey   = regexp.MustCompile(`"sections"` + sp + `*:`)
	reClaimsKey     = regexp.MustCompile(`"claims"` + sp + `*:`)
	reRelatedKey    = regexp.MustCompile(`"related"` + sp + `*:`)
	reHeadingKey    = regexp.MustCompile(`,` + sp + `*"heading"` + sp + `*:`)
	reNestedRelated = regexp.MustCompile(`,` + sp + `*\{` + sp + `*"related"` + sp + `*:` + sp + `*(\[[^\[\]]*\])` + sp + `*\}` + sp + `*$`)
	reEnvelope      = regexp.MustCompile(`^[{\[]` + sp + `*\\"`)
	reThinkBlock    = regexp.MustCompile(`(?is)<think>.*?</think>`)
	reThinkOpen     = regexp.MustCompile(`(?is)<think>.*$`)
	reContainerHead = regexp.MustCompile(`^[{\[]`)
)

// repairFlattenedWikiSections restores the page schema when a model omits
// `sections:[{` and writes the first section's heading directly after the root
// summary. The remaining section separators and final array closer are
// already present in this form.
func repairFlattenedWikiSections(value string) string {
	trimmed := trim(value)
	if !reSummaryFirst.MatchString(trimmed) || reSectionsKey.MatchString(trimmed) {
		return value
	}
	if !reClaimsKey.MatchString(trimmed) || !reRelatedKey.MatchString(trimmed) {
		return value
	}
	loc := reHeadingKey.FindStringIndex(value)
	if loc == nil {
		return value
	}
	return value[:loc[0]] + `,"sections":[{"heading":` + value[loc[1]:]
}

// repairNestedWikiRelated moves a trailing `{ "related": [...] }` out of the
// sections array.
func repairNestedWikiRelated(value string) string {
	if !reSectionsKey.MatchString(value) {
		return value
	}
	loc := reNestedRelated.FindStringSubmatchIndex(value)
	if loc == nil {
		return value
	}
	return value[:loc[0]] + `],"related":` + value[loc[2]:loc[3]] + "}" + value[loc[1]:]
}

// unescapeJSONEnvelope decodes providers that return the JSON object escaped
// as if it were a string.
func unescapeJSONEnvelope(value string) string {
	trimmed := trim(value)
	if !reEnvelope.MatchString(trimmed) {
		return value
	}
	literal := `"` + strings.ReplaceAll(strings.ReplaceAll(trimmed, "\r", `\r`), "\n", `\n`) + `"`
	var decoded string
	if err := json.Unmarshal([]byte(literal), &decoded); err != nil {
		return value
	}
	return decoded
}

// jsonContainerCandidates finds complete top-level JSON containers inside
// prose or repeated replies. Byte-wise is safe: every character it looks at is
// ASCII, which never occurs inside a multi-byte UTF-8 sequence.
func jsonContainerCandidates(value string) []string {
	var candidates []string
	var stack []byte
	start := -1
	inString, escaped := false, false

	for i := 0; i < len(value); i++ {
		char := value[i]
		if start < 0 {
			if char == '{' || char == '[' {
				start = i
				stack = append(stack, char)
			}
			continue
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			continue
		}
		if char == '{' || char == '[' {
			stack = append(stack, char)
			continue
		}
		if char != '}' && char != ']' {
			continue
		}
		var open byte
		if len(stack) > 0 {
			open = stack[len(stack)-1]
		}
		if (open == '{' && char == '}') || (open == '[' && char == ']') {
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				candidates = append(candidates, value[start:i+1])
				start = -1
			}
		}
	}
	return candidates
}

func isLineTerminator(r rune) bool {
	return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029
}

// cleanFences is `.replace(/^```(json)?/m, ”).replace(/```$/m, ”).trim()`:
// the first fence at a line start (with its "json" tag), then the first fence
// at a line end, where JS's multiline anchors count \r, U+2028 and U+2029 as
// line terminators too.
func cleanFences(value string) string {
	for i := 0; i+3 <= len(value); i++ {
		if value[i:i+3] != "```" {
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(value[:i])
			if !isLineTerminator(prev) {
				continue
			}
		}
		end := i + 3
		if strings.HasPrefix(value[end:], "json") {
			end += 4
		}
		value = value[:i] + value[end:]
		break
	}
	for i := 0; i+3 <= len(value); i++ {
		if value[i:i+3] != "```" {
			continue
		}
		next, _ := utf8.DecodeRuneInString(value[i+3:])
		if i+3 == len(value) || isLineTerminator(next) {
			value = value[:i] + value[i+3:]
			break
		}
	}
	return trim(value)
}

// parseJSON is the TS `parse` helper: JSON.parse of the cleaned text, where a
// parse error and a literal `null` both mean "no value".
func parseJSON(value string) (any, bool) {
	v, err := shared.DecodeJSON([]byte(cleanFences(value)))
	if err != nil || v == nil {
		return nil, false
	}
	return v, true
}

// lastIndexFoldThink is `text.toLowerCase().lastIndexOf('</think>')` on the
// original string's byte offsets.
func lastIndexFoldThink(text string) int {
	const tag = "</think>"
	for i := len(text) - len(tag); i >= 0; i-- {
		if strings.EqualFold(text[i:i+len(tag)], tag) {
			return i
		}
	}
	return -1
}

// ParseLooseJSON strips a stray ```json fence and parses; ok is false when the
// text cannot be read as JSON. Numbers decode as json.Number.
func ParseLooseJSON(text string) (any, bool) {
	if v, ok := parseJSON(text); ok {
		return v, true
	}

	// Some self-hosted reasoning models wrap an otherwise valid answer in a
	// visible <think> prelude. Never use that prose; remove the wrapper and
	// validate the JSON payload through the same semantic contract as usual.
	withoutThinking := reThinkBlock.ReplaceAllLiteralString(text, "")
	if lastClose := lastIndexFoldThink(withoutThinking); lastClose >= 0 {
		withoutThinking = withoutThinking[lastClose+len("</think>"):]
	}
	withoutThinking = reThinkOpen.ReplaceAllLiteralString(withoutThinking, "")
	if v, ok := parseJSON(withoutThinking); ok {
		return v, true
	}
	unescaped := unescapeJSONEnvelope(cleanFences(withoutThinking))
	if v, ok := parseJSON(unescaped); ok {
		return v, true
	}
	normalized := repairMismatchedClosers(
		repairNestedWikiRelated(repairFlattenedWikiSections(repairQuotedArrayObjects(unescaped))),
	)
	candidates := jsonContainerCandidates(normalized)
	for i := len(candidates) - 1; i >= 0; i-- {
		if v, ok := parseJSON(candidates[i]); ok {
			return v, true
		}
	}
	if !reContainerHead.MatchString(trim(normalized)) {
		return nil, false
	}
	repaired, err := jsonrepair.Repair(normalized)
	if err != nil {
		return nil, false
	}
	return parseJSON(repaired)
}
