package ai

import (
	"strconv"
	"strings"
)

// A port of jsonrepair 3.15.0 (josdejong/jsonrepair, ISC), the regular
// (non-streaming) repairer that parseLooseJson falls back to. It works on
// UTF-16 code units exactly like the JavaScript original so that every index,
// slice and lookahead lands where V8's does; testdata/vectors/aiJson.json pins
// its output against the npm package.
//
// A character is an int: a code unit, or chUndef for `text[i]` past the end
// (undefined) and chEmpty for `text.charAt(i)` past the end (''), which the
// original's string helpers treat differently ('' is "included" in every
// string, so isDelimiter('') is true).

const (
	chUndef = -1
	chEmpty = -2
)

type jsonRepairError struct{ msg string }

func (e jsonRepairError) Error() string { return e.msg }

type repairer struct {
	text []uint16
	i    int
	out  []uint16
}

// jsonRepair repairs a broken JSON document, or fails with jsonrepair's error.
func jsonRepair(input string) (result string, err error) {
	r := &repairer{text: utf16Units(input)}
	defer func() {
		if rec := recover(); rec != nil {
			if e, ok := rec.(jsonRepairError); ok {
				err = e
				return
			}
			panic(rec)
		}
	}()
	return r.run(), nil
}

func (r *repairer) at(i int) int {
	if i < 0 || i >= len(r.text) {
		return chUndef
	}
	return int(r.text[i])
}

func (r *repairer) charAt(i int) int {
	if i < 0 || i >= len(r.text) {
		return chEmpty
	}
	return int(r.text[i])
}

func u16(s string) []uint16 { return utf16Units(s) }

func (r *repairer) emit(s string)     { r.out = append(r.out, u16(s)...) }
func (r *repairer) emitUnit(c uint16) { r.out = append(r.out, c) }
func (r *repairer) slice(a, b int) []uint16 {
	if a < 0 {
		a = 0
	}
	if b > len(r.text) {
		b = len(r.text)
	}
	if a >= b {
		return nil
	}
	return r.text[a:b]
}

// ─── string helpers (utils/stringUtils.js) ──────────────────────────────────

func includesChar(set string, c int) bool {
	if c == chEmpty {
		return true
	}
	if c == chUndef {
		return false
	}
	return strings.ContainsRune(set, rune(c))
}

func jrIsHex(c int) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')
}
func jrIsDigit(c int) bool              { return c >= '0' && c <= '9' }
func isValidStringCharacter(c int) bool { return c >= 0x20 }
func isDelimiterChar(c int) bool        { return includesChar(",:[]/{}()\n+", c) }
func isFunctionNameCharStart(c int) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '$'
}
func isFunctionNameChar(c int) bool { return isFunctionNameCharStart(c) || (c >= '0' && c <= '9') }

func isUnquotedStringDelimiter(c int) bool { return includesChar(",[]/{}\n+", c) }

// regexStartOfValue is /^[[{\w-]$/.
func isStartOfValue(c int) bool {
	if isQuote(c) {
		return true
	}
	return c == '[' || c == '{' || c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isControlCharacter(c int) bool {
	return c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f'
}

func isWhitespaceUnit(c int) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' }
func isWhitespaceExceptNewline(c int) bool {
	return c == ' ' || c == '\t' || c == '\r'
}
func isSpecialWhitespace(c int) bool {
	return c == 0xa0 || c == 0x180e || (c >= 0x2000 && c <= 0x200b) || c == 0x202f || c == 0x205f || c == 0x3000 || c == 0xfeff
}

func isQuote(c int) bool           { return isDoubleQuoteLike(c) || isSingleQuoteLike(c) }
func isDoubleQuoteLike(c int) bool { return c == '"' || c == 0x201c || c == 0x201d }
func isDoubleQuote(c int) bool     { return c == '"' }
func isSingleQuoteLike(c int) bool {
	return c == '\'' || c == 0x2018 || c == 0x2019 || c == 0x60 || c == 0xb4
}
func isSingleQuote(c int) bool { return c == '\'' }

func lastIndexUnit(s []uint16, c uint16) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func stripLastOccurrence(text []uint16, c uint16, stripRemaining bool) []uint16 {
	idx := lastIndexUnit(text, c)
	if idx == -1 {
		return text
	}
	out := append([]uint16(nil), text[:idx]...)
	if !stripRemaining {
		out = append(out, text[idx+1:]...)
	}
	return out
}

func insertBeforeLastWhitespace(text []uint16, insert string) []uint16 {
	index := len(text)
	if index == 0 || !isWhitespaceUnit(int(text[index-1])) {
		return append(text, u16(insert)...)
	}
	for index > 0 && isWhitespaceUnit(int(text[index-1])) {
		index--
	}
	out := append([]uint16(nil), text[:index]...)
	out = append(out, u16(insert)...)
	return append(out, text[index:]...)
}

func removeAtIndex(text []uint16, start, count int) []uint16 {
	out := append([]uint16(nil), text[:start]...)
	return append(out, text[start+count:]...)
}

// endsWithCommaOrNewline is /[,\n][ \t\r]*$/.
func endsWithCommaOrNewline(text []uint16) bool {
	i := len(text) - 1
	for i >= 0 && (text[i] == ' ' || text[i] == '\t' || text[i] == '\r') {
		i--
	}
	return i >= 0 && (text[i] == ',' || text[i] == '\n')
}

type htmlEntity struct {
	char   string
	length int
}

var namedHTMLEntities = map[string]string{"&quot;": `"`, "&amp;": "&", "&lt;": "<", "&gt;": ">", "&apos;": "'"}

const maxHTMLEntityLength = 12

func matchHTMLEntity(fragment []uint16) *htmlEntity {
	if len(fragment) == 0 || fragment[0] != '&' {
		return nil
	}
	semicolon := -1
	for k, c := range fragment {
		if c == ';' {
			semicolon = k
			break
		}
	}
	if semicolon == -1 {
		return nil
	}
	entity := fragment[:semicolon+1]
	if named, ok := namedHTMLEntities[fromUTF16(entity)]; ok {
		return &htmlEntity{char: named, length: len(entity)}
	}
	if len(fragment) > 1 && fragment[1] == '#' {
		body := fromUTF16(fragment[2:semicolon])
		hex := strings.HasPrefix(body, "x") || strings.HasPrefix(body, "X")
		digits := body
		if hex {
			digits = body[1:]
		}
		if len(digits) > 0 {
			code, ok := jsParseInt(digits, hex)
			if ok && code >= 0 && code <= 0x10ffff {
				return &htmlEntity{char: fromCodePoint(code), length: len(entity)}
			}
		}
	}
	return nil
}

// jsParseInt is Number.parseInt(digits, 16|10): the longest valid prefix.
func jsParseInt(s string, hex bool) (int64, bool) {
	s = jsTrim(s)
	neg := false
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		neg = s[0] == '-'
		s = s[1:]
	}
	base := 10
	if hex {
		base = 16
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			s = s[2:]
		}
	}
	end := 0
	for end < len(s) {
		c := int(s[end])
		if base == 16 && jrIsHex(c) || base == 10 && jrIsDigit(c) {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(s[:end], base, 64)
	if err != nil {
		// Larger than any code point anyway.
		return 1 << 40, true
	}
	if neg {
		n = -n
	}
	return n, true
}

// fromCodePoint is String.fromCodePoint; a surrogate code point cannot live
// in a Go string, so it becomes U+FFFD (as it would on the way out of JS).
func fromCodePoint(code int64) string {
	return string(rune(code))
}

func countOccurrences(text []uint16, c uint16) int {
	n := 0
	for _, x := range text {
		if x == c {
			n++
		}
	}
	return n
}

func isInsideUnclosedBracket(text []uint16, closeChar int) bool {
	switch closeChar {
	case ')':
		return countOccurrences(text, '(') > countOccurrences(text, ')')
	case ']':
		return countOccurrences(text, '[') > countOccurrences(text, ']')
	case '}':
		return countOccurrences(text, '{') > countOccurrences(text, '}')
	}
	return false
}

// regexURLStart is /^(http|https|ftp|mailto|file|data|irc):\/\/$/.
func isURLStart(s []uint16) bool {
	switch fromUTF16(s) {
	case "http://", "https://", "ftp://", "mailto://", "file://", "data://", "irc://":
		return true
	}
	return false
}

// regexUrlChar is /^[A-Za-z0-9-._~:/?#@!$&'()*+;=]$/.
func isURLChar(c int) bool {
	if c < 0 {
		return false
	}
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		strings.ContainsRune("-._~:/?#@!$&'()*+;=", rune(c))
}

// ─── the parser (regular/jsonrepair.js) ─────────────────────────────────────

func (r *repairer) run() string {
	r.parseMarkdownCodeBlock([]string{"```", "[```", "{```"})
	if !r.parseValue() {
		r.throwUnexpectedEnd()
	}
	r.parseMarkdownCodeBlock([]string{"```", "```]", "```}"})
	processedComma := r.parseCharacter(',')
	if processedComma {
		r.parseWhitespaceAndSkipComments(true)
	}
	if isStartOfValue(r.at(r.i)) && endsWithCommaOrNewline(r.out) {
		// start of a new value after end of the root level object: looks like
		// newline delimited JSON -> turn into a root level array
		if !processedComma {
			r.out = insertBeforeLastWhitespace(r.out, ",")
		}
		r.parseNewlineDelimitedJSON()
	} else if processedComma {
		r.out = stripLastOccurrence(r.out, ',', false)
	}
	// repair redundant end quotes
	for r.at(r.i) == '}' || r.at(r.i) == ']' {
		r.i++
		r.parseWhitespaceAndSkipComments(true)
	}
	if r.i >= len(r.text) {
		return fromUTF16(r.out)
	}
	r.throwUnexpectedCharacter()
	return ""
}

func (r *repairer) parseValue() bool {
	r.parseWhitespaceAndSkipComments(true)
	processed := r.parseObject() || r.parseArray() || r.parseString(false, -1) || r.parseNumber() ||
		r.parseKeywords() || r.parseUnquotedString(false) || r.parseRegex()
	r.parseWhitespaceAndSkipComments(true)
	return processed
}

func (r *repairer) parseWhitespaceAndSkipComments(skipNewline bool) bool {
	start := r.i
	r.parseWhitespace(skipNewline)
	for {
		changed := r.parseComment()
		if changed {
			changed = r.parseWhitespace(skipNewline)
		}
		if !changed {
			break
		}
	}
	return r.i > start
}

func (r *repairer) parseWhitespace(skipNewline bool) bool {
	var ws []uint16
	for {
		c := r.at(r.i)
		isWS := isWhitespaceUnit(c)
		if !skipNewline {
			isWS = isWhitespaceExceptNewline(c)
		}
		if isWS {
			ws = append(ws, uint16(c))
			r.i++
		} else if isSpecialWhitespace(c) {
			// repair special whitespace
			ws = append(ws, ' ')
			r.i++
		} else {
			break
		}
	}
	if len(ws) > 0 {
		r.out = append(r.out, ws...)
		return true
	}
	return false
}

func (r *repairer) parseComment() bool {
	if r.at(r.i) == '/' && r.at(r.i+1) == '*' {
		for r.i < len(r.text) && !(r.at(r.i) == '*' && r.at(r.i+1) == '/') {
			r.i++
		}
		r.i += 2
		return true
	}
	if r.at(r.i) == '/' && r.at(r.i+1) == '/' {
		for r.i < len(r.text) && r.at(r.i) != '\n' {
			r.i++
		}
		return true
	}
	return false
}

func (r *repairer) parseMarkdownCodeBlock(blocks []string) bool {
	if r.skipMarkdownCodeBlock(blocks) {
		if isFunctionNameCharStart(r.at(r.i)) {
			for r.i < len(r.text) && isFunctionNameChar(r.at(r.i)) {
				r.i++
			}
		}
		r.parseWhitespaceAndSkipComments(true)
		return true
	}
	return false
}

func (r *repairer) skipMarkdownCodeBlock(blocks []string) bool {
	r.parseWhitespace(true)
	for _, block := range blocks {
		end := r.i + len(block)
		if fromUTF16(r.slice(r.i, end)) == block {
			r.i = end
			return true
		}
	}
	return false
}

func (r *repairer) parseCharacter(c int) bool {
	if r.at(r.i) == c {
		r.emitUnit(uint16(c))
		r.i++
		return true
	}
	return false
}

func (r *repairer) skipCharacter(c int) bool {
	if r.at(r.i) == c {
		r.i++
		return true
	}
	return false
}

func (r *repairer) skipEllipsis() bool {
	r.parseWhitespaceAndSkipComments(true)
	if r.at(r.i) == '.' && r.at(r.i+1) == '.' && r.at(r.i+2) == '.' {
		r.i += 3
		r.parseWhitespaceAndSkipComments(true)
		r.skipCharacter(',')
		return true
	}
	return false
}

func (r *repairer) parseObject() bool {
	if r.at(r.i) != '{' {
		return false
	}
	r.emit("{")
	r.i++
	r.parseWhitespaceAndSkipComments(true)
	if r.skipCharacter(',') {
		r.parseWhitespaceAndSkipComments(true)
	}
	initial := true
	for r.i < len(r.text) && r.at(r.i) != '}' {
		if !initial {
			if !r.parseCharacter(',') {
				r.out = insertBeforeLastWhitespace(r.out, ",")
			}
			r.parseWhitespaceAndSkipComments(true)
		}
		r.skipEllipsis()
		processedKey := r.parseString(false, -1) || r.parseUnquotedString(true)
		if !processedKey {
			c := r.at(r.i)
			if c == '}' || c == '{' || c == ']' || c == '[' || c == chUndef {
				if !initial {
					r.out = stripLastOccurrence(r.out, ',', false)
				}
			} else {
				r.throwObjectKeyExpected()
			}
			break
		}
		r.parseWhitespaceAndSkipComments(true)
		processedColon := r.parseCharacter(':')
		truncatedText := r.i >= len(r.text)
		if !processedColon {
			if isStartOfValue(r.at(r.i)) || truncatedText {
				r.out = insertBeforeLastWhitespace(r.out, ":")
			} else {
				r.throwColonExpected()
			}
		}
		if !r.parseValue() {
			if processedColon || truncatedText {
				r.emit("null")
			} else {
				r.throwColonExpected()
			}
		}
		initial = false
	}
	if r.at(r.i) == '}' {
		r.emit("}")
		r.i++
	} else {
		r.out = insertBeforeLastWhitespace(r.out, "}")
	}
	return true
}

func (r *repairer) parseArray() bool {
	if r.at(r.i) != '[' {
		return false
	}
	r.emit("[")
	r.i++
	r.parseWhitespaceAndSkipComments(true)
	if r.skipCharacter(',') {
		r.parseWhitespaceAndSkipComments(true)
	}
	initial := true
	for r.i < len(r.text) && r.at(r.i) != ']' {
		if !initial {
			if !r.parseCharacter(',') {
				r.out = insertBeforeLastWhitespace(r.out, ",")
			}
		}
		r.skipEllipsis()
		if !r.parseValue() {
			if !initial {
				r.out = stripLastOccurrence(r.out, ',', false)
			}
			break
		}
		initial = false
	}
	if r.at(r.i) == ']' {
		r.emit("]")
		r.i++
	} else {
		r.out = insertBeforeLastWhitespace(r.out, "]")
	}
	return true
}

func (r *repairer) parseNewlineDelimitedJSON() {
	initial := true
	processedValue := true
	for processedValue {
		if !initial {
			if !r.parseCharacter(',') {
				r.out = insertBeforeLastWhitespace(r.out, ",")
			}
		} else {
			initial = false
		}
		processedValue = r.parseValue()
	}
	r.out = stripLastOccurrence(r.out, ',', false)
	wrapped := append(u16("[\n"), r.out...)
	r.out = append(wrapped, u16("\n]")...)
}

func (r *repairer) entityAt(i int) *htmlEntity {
	return matchHTMLEntity(r.slice(i, i+maxHTMLEntityLength))
}

func entityIsQuote(e *htmlEntity, q string) bool { return e != nil && e.char == q }

func (r *repairer) parseString(stopAtDelimiter bool, stopAtIndex int) bool {
	skipEscapeChars := r.at(r.i) == '\\'
	if skipEscapeChars {
		r.i++
		if !isQuote(r.at(r.i)) {
			r.throwUnexpectedCharacter()
		}
	}
	var openEntity *htmlEntity
	if r.at(r.i) == '&' {
		openEntity = r.entityAt(r.i)
	}
	openedByEntity := entityIsQuote(openEntity, `"`) || entityIsQuote(openEntity, "'")
	if !isQuote(r.at(r.i)) && !openedByEntity {
		return false
	}
	var isEndQuote func(int) bool
	switch c := r.at(r.i); {
	case isDoubleQuote(c):
		isEndQuote = isDoubleQuote
	case isSingleQuote(c):
		isEndQuote = isSingleQuote
	case isSingleQuoteLike(c):
		isEndQuote = isSingleQuoteLike
	default:
		isEndQuote = isDoubleQuoteLike
	}
	iBefore := r.i
	oBefore := len(r.out)
	str := u16(`"`)
	if openedByEntity && openEntity != nil {
		r.i += openEntity.length
	} else {
		r.i++
	}
	for {
		if r.i >= len(r.text) {
			iPrev := r.prevNonWhitespaceIndex(r.i - 1)
			if !stopAtDelimiter && isDelimiterChar(r.charAt(iPrev)) {
				r.i = iBefore
				r.out = r.out[:oBefore]
				return r.parseString(true, -1)
			}
			str = insertBeforeLastWhitespace(str, `"`)
			r.out = append(r.out, str...)
			return true
		}
		if r.i == stopAtIndex {
			str = insertBeforeLastWhitespace(str, `"`)
			r.out = append(r.out, str...)
			return true
		}
		var entity *htmlEntity
		if openedByEntity && r.at(r.i) == '&' {
			entity = r.entityAt(r.i)
		}
		var isEnd bool
		if entity != nil && openEntity != nil {
			isEnd = entity.char == openEntity.char
		} else {
			isEnd = isEndQuote(r.at(r.i))
		}
		switch {
		case isEnd:
			iQuote := r.i
			oQuote := len(str)
			str = append(str, '"')
			if entity != nil {
				r.i += entity.length
			} else {
				r.i++
			}
			r.out = append(r.out, str...)
			r.parseWhitespaceAndSkipComments(false)
			c := r.at(r.i)
			if stopAtDelimiter || r.i >= len(r.text) ||
				(isDelimiterChar(c) && !isInsideUnclosedBracket(str, c)) ||
				(isQuote(c) && !r.nextQuoteIsEndQuote(r.i)) || jrIsDigit(c) {
				r.parseConcatenatedString()
				return true
			}
			if c == '\\' {
				r.throwUnexpectedCharacter()
			}
			iPrevChar := r.prevNonWhitespaceIndex(iQuote - 1)
			prevChar := r.charAt(iPrevChar)
			if prevChar == ',' {
				r.i = iBefore
				r.out = r.out[:oBefore]
				return r.parseString(false, iPrevChar)
			}
			if isDelimiterChar(prevChar) {
				r.i = iBefore
				r.out = r.out[:oBefore]
				return r.parseString(true, -1)
			}
			// revert to right after the quote but before any whitespace, and
			// continue parsing the string
			r.out = r.out[:oBefore]
			if entity != nil {
				r.i = iQuote + entity.length
			} else {
				r.i = iQuote + 1
			}
			// repair unescaped quote
			fixed := append([]uint16(nil), str[:oQuote]...)
			fixed = append(fixed, '\\')
			str = append(fixed, str[oQuote:]...)
		case stopAtDelimiter && isUnquotedStringDelimiter(r.at(r.i)):
			// test start of an url like "https://..." (this would be parsed as a comment)
			if r.at(r.i-1) == ':' && isURLStart(r.slice(iBefore+1, r.i+2)) {
				for r.i < len(r.text) && isURLChar(r.at(r.i)) {
					str = append(str, r.text[r.i])
					r.i++
				}
			}
			str = insertBeforeLastWhitespace(str, `"`)
			r.out = append(r.out, str...)
			r.parseConcatenatedString()
			return true
		case entity != nil:
			ch := entity.char
			switch {
			case ch == `"`:
				str = append(str, u16(`\"`)...)
			case len(ch) == 1 && isControlCharacter(int(ch[0])):
				str = append(str, u16(controlEscape(int(ch[0])))...)
			default:
				str = append(str, u16(ch)...)
			}
			r.i += entity.length
		case r.at(r.i) == '\\':
			ch := r.charAt(r.i + 1)
			switch {
			case ch == '"' || ch == '\\' || ch == '/' || ch == 'b' || ch == 'f' || ch == 'n' || ch == 'r' || ch == 't':
				str = append(str, r.slice(r.i, r.i+2)...)
				r.i += 2
			case ch == 'u':
				j := 2
				for j < 6 && jrIsHex(r.at(r.i+j)) {
					j++
				}
				if j == 6 {
					str = append(str, r.slice(r.i, r.i+6)...)
					r.i += 6
				} else if r.i+j >= len(r.text) {
					// repair invalid or truncated unicode char at the end of
					// the text by removing it and ending the string here
					r.i = len(r.text)
				} else {
					r.throwInvalidUnicodeCharacter()
				}
			case ch == '\n':
				str = append(str, u16(`\n`)...)
				r.i += 2
			default:
				// repair invalid escape character: remove it
				if ch >= 0 {
					str = append(str, uint16(ch))
				}
				r.i += 2
			}
		default:
			ch := r.charAt(r.i)
			switch {
			case ch == '"' && r.at(r.i-1) != '\\':
				str = append(str, '\\', '"')
				r.i++
			case isControlCharacter(ch):
				str = append(str, u16(controlEscape(ch))...)
				r.i++
			default:
				if !isValidStringCharacter(ch) {
					r.throwInvalidCharacter(ch)
				}
				str = append(str, uint16(ch))
				r.i++
			}
		}
		if skipEscapeChars {
			r.skipCharacter('\\')
		}
	}
}

func controlEscape(c int) string {
	switch c {
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	}
	return `\t`
}

func (r *repairer) parseConcatenatedString() bool {
	processed := false
	r.parseWhitespaceAndSkipComments(true)
	for r.at(r.i) == '+' {
		processed = true
		r.i++
		r.parseWhitespaceAndSkipComments(true)
		r.out = stripLastOccurrence(r.out, '"', true)
		start := len(r.out)
		if r.parseString(false, -1) {
			r.out = removeAtIndex(r.out, start, 1)
		} else {
			r.out = insertBeforeLastWhitespace(r.out, `"`)
		}
	}
	return processed
}

func (r *repairer) parseNumber() bool {
	start := r.i
	var num []uint16
	invalid := false
	if r.at(r.i) == '-' {
		num = append(num, '-')
		r.i++
		if !jrIsDigit(r.at(r.i)) && r.atEndOfNumber() {
			num = append(num, '0')
		}
	}
	if r.at(r.i) == '0' && jrIsDigit(r.at(r.i+1)) {
		invalid = true
	}
	for jrIsDigit(r.at(r.i)) {
		num = append(num, r.text[r.i])
		r.i++
	}
	if r.at(r.i) == '.' {
		if len(num) == 0 || (len(num) == 1 && num[0] == '-') {
			num = append(num, '0')
		}
		num = append(num, '.')
		r.i++
		if !jrIsDigit(r.at(r.i)) {
			num = append(num, '0')
		}
		for jrIsDigit(r.at(r.i)) {
			num = append(num, r.text[r.i])
			r.i++
		}
	}
	if r.i > start {
		if r.at(r.i) == 'e' || r.at(r.i) == 'E' {
			if len(num) == 1 && num[0] == '-' {
				invalid = true
			}
			num = append(num, r.text[r.i])
			r.i++
			if r.at(r.i) == '-' || r.at(r.i) == '+' {
				num = append(num, r.text[r.i])
				r.i++
			}
			if !jrIsDigit(r.at(r.i)) {
				num = append(num, '0')
			}
			for jrIsDigit(r.at(r.i)) {
				num = append(num, r.text[r.i])
				r.i++
			}
		}
		if !r.atEndOfNumber() {
			r.i = start
			return false
		}
		if invalid {
			r.emit(`"`)
			r.out = append(r.out, r.text[start:r.i]...)
			r.emit(`"`)
		} else {
			r.out = append(r.out, num...)
		}
		return true
	}
	return false
}

func (r *repairer) parseKeywords() bool {
	return r.parseKeyword("true", "true") || r.parseKeyword("false", "false") || r.parseKeyword("null", "null") ||
		r.parseKeyword("True", "true") || r.parseKeyword("False", "false") || r.parseKeyword("None", "null")
}

func (r *repairer) parseKeyword(name, value string) bool {
	if fromUTF16(r.slice(r.i, r.i+len(name))) == name && !isFunctionNameChar(r.at(r.i+len(name))) {
		r.emit(value)
		r.i += len(name)
		return true
	}
	return false
}

func (r *repairer) parseUnquotedString(isKey bool) bool {
	start := r.i
	if isFunctionNameCharStart(r.at(r.i)) {
		for r.i < len(r.text) && isFunctionNameChar(r.at(r.i)) {
			r.i++
		}
		j := r.i
		for isWhitespaceUnit(r.at(j)) {
			j++
		}
		if r.at(j) == '(' {
			// repair a MongoDB function call like NumberLong("2") or a JSONP
			// call like callback({...});
			r.i = j + 1
			r.parseValue()
			if r.at(r.i) == ')' {
				r.i++
				if r.at(r.i) == ';' {
					r.i++
				}
			}
			return true
		}
	}
	for r.i < len(r.text) && !isUnquotedStringDelimiter(r.at(r.i)) && !isQuote(r.at(r.i)) && (!isKey || r.at(r.i) != ':') {
		r.i++
	}
	if r.at(r.i-1) == ':' && isURLStart(r.slice(start, r.i+2)) {
		for r.i < len(r.text) && isURLChar(r.at(r.i)) {
			r.i++
		}
	}
	if r.i > start {
		for isWhitespaceUnit(r.at(r.i-1)) && r.i > 0 {
			r.i--
		}
		symbol := fromUTF16(r.text[start:r.i])
		if symbol == "undefined" {
			r.emit("null")
		} else {
			r.emit(jsonStringify(symbol))
		}
		if r.at(r.i) == '"' {
			r.i++
		}
		return true
	}
	return false
}

func (r *repairer) parseRegex() bool {
	if r.at(r.i) != '/' {
		return false
	}
	start := r.i
	r.i++
	for r.i < len(r.text) && (r.at(r.i) != '/' || r.at(r.i-1) == '\\') {
		r.i++
	}
	r.i++
	r.emit(jsonStringify(fromUTF16(r.slice(start, r.i))))
	return true
}

func (r *repairer) prevNonWhitespaceIndex(start int) int {
	prev := start
	for prev > 0 && isWhitespaceUnit(r.at(prev)) {
		prev--
	}
	return prev
}

func (r *repairer) nextQuoteIsEndQuote(index int) bool {
	next := index + 1
	for next < len(r.text) && isWhitespaceUnit(r.at(next)) {
		next++
	}
	return next >= len(r.text) || isDelimiterChar(r.at(next))
}

func (r *repairer) atEndOfNumber() bool {
	return r.i >= len(r.text) || isDelimiterChar(r.at(r.i)) || isWhitespaceUnit(r.at(r.i))
}

func (r *repairer) fail(msg string, pos int) {
	panic(jsonRepairError{msg: msg + " at position " + strconv.Itoa(pos)})
}

func (r *repairer) charJSON(c int) string {
	if c < 0 {
		return "undefined"
	}
	return jsonStringify(fromUTF16([]uint16{uint16(c)}))
}

func (r *repairer) throwInvalidCharacter(c int) {
	r.fail("Invalid character "+r.charJSON(c), r.i)
}
func (r *repairer) throwUnexpectedCharacter() {
	r.fail("Unexpected character "+r.charJSON(r.at(r.i)), r.i)
}
func (r *repairer) throwUnexpectedEnd()     { r.fail("Unexpected end of json string", len(r.text)) }
func (r *repairer) throwObjectKeyExpected() { r.fail("Object key expected", r.i) }
func (r *repairer) throwColonExpected()     { r.fail("Colon expected", r.i) }
func (r *repairer) throwInvalidUnicodeCharacter() {
	r.fail(`Invalid unicode character "`+fromUTF16(r.slice(r.i, r.i+6))+`"`, r.i)
}
