package ai

// V8 JSON.parse error messages, copied from internal/hosting/jsjson.go (unexported
// there). `res.json()` on a provider reply that is not JSON rejects with this
// text, and the renderer shows it.

import (
	"strconv"
	"unicode/utf16"
)

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
