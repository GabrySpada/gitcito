package textconv

// Number formatting for spreadsheet cells — the part of SheetJS's SSF that
// sheet_to_csv output depends on. "General" is ported exactly
// (SSF_general_num, with JavaScript's toFixed/toPrecision/toExponential
// rounding). Explicit formats cover what workbooks use in practice: the
// built-in table, sections (`pos;neg;zero;text`), literal text, `0 # ?`
// placeholders with thousands separators, decimals, percent, scaling commas
// and scientific notation, and every date/time token (y m d h s, AM/PM, A/P,
// [h] [m] [s] elapsed time, fractional seconds, day and month names).
// Fractions (`# ?/?`) and conditional sections (`[>100]`) fall back to
// General.

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ─── JavaScript number → string ─────────────────────────────────────────────

// exactDigits is the exact decimal expansion of |x| as digits and the
// position of the decimal point (value = 0.digits × 10^point).
func exactDigits(x float64) (string, int) {
	s := new(big.Float).SetFloat64(math.Abs(x)).Text('f', 1100)
	intPart, frac, _ := strings.Cut(s, ".")
	frac = strings.TrimRight(frac, "0")
	digits := strings.TrimLeft(intPart, "0")
	point := len(digits)
	if digits == "" {
		trimmed := strings.TrimLeft(frac, "0")
		point = -(len(frac) - len(trimmed))
		digits = trimmed
	} else {
		digits += frac
	}
	if digits == "" {
		return "0", 1
	}
	return digits, point
}

// roundDigits rounds a digit string to n digits, half up (ECMA-262 picks the
// larger n on a tie), and reports whether a carry added a leading digit.
func roundDigits(digits string, n int) (string, bool) {
	if n < 0 {
		return "", false
	}
	if len(digits) <= n {
		return digits + strings.Repeat("0", n-len(digits)), false
	}
	out := []byte(digits[:n])
	if digits[n] >= '5' {
		i := n - 1
		for ; i >= 0; i-- {
			if out[i] == '9' {
				out[i] = '0'
				continue
			}
			out[i]++
			break
		}
		if i < 0 {
			return "1" + string(out), true
		}
	}
	return string(out), false
}

// toFixed is Number#toFixed(f).
func toFixed(x float64, f int) string {
	if math.IsNaN(x) {
		return "NaN"
	}
	if math.Abs(x) >= 1e21 || math.IsInf(x, 0) {
		return numberString(x)
	}
	sign := ""
	if x < 0 {
		sign = "-"
	}
	digits, point := exactDigits(x)
	// Digits up to f places after the point.
	n := point + f
	var m string
	if n < 0 {
		m = ""
	} else {
		var carry bool
		m, carry = roundDigits(digits, n)
		if carry {
			point++
		}
	}
	// Left-pad so there is at least one integer digit.
	whole := m
	intLen := point
	if intLen <= 0 {
		whole = strings.Repeat("0", -intLen+1) + whole
		intLen = 1
	}
	if len(whole) < intLen+f {
		whole += strings.Repeat("0", intLen+f-len(whole))
	}
	if f == 0 {
		return sign + whole[:intLen]
	}
	return sign + whole[:intLen] + "." + whole[intLen:intLen+f]
}

// sigDigits is |x| rounded to p significant digits, with its exponent.
func sigDigits(x float64, p int) (string, int) {
	digits, point := exactDigits(x)
	m, carry := roundDigits(digits, p)
	e := point - 1
	if carry {
		e++
		m = m[:p]
	}
	return m, e
}

// toPrecision is Number#toPrecision(p).
func toPrecision(x float64, p int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return numberString(x)
	}
	sign := ""
	if x < 0 {
		sign = "-"
	}
	if x == 0 {
		if p > 1 {
			return "0." + strings.Repeat("0", p-1)
		}
		return "0"
	}
	m, e := sigDigits(x, p)
	if e < -6 || e >= p {
		out := m[:1]
		if p > 1 {
			out += "." + m[1:]
		}
		es := "+"
		if e < 0 {
			es = "-"
		}
		return sign + out + "e" + es + strconv.Itoa(absInt(e))
	}
	if e >= 0 {
		if e+1 == p {
			return sign + m
		}
		return sign + m[:e+1] + "." + m[e+1:]
	}
	return sign + "0." + strings.Repeat("0", -e-1) + m
}

// toExponential is Number#toExponential(f).
func toExponential(x float64, f int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return numberString(x)
	}
	sign := ""
	if x < 0 {
		sign = "-"
	}
	m, e := "0", 0
	if x != 0 {
		m, e = sigDigits(x, f+1)
	} else {
		m = strings.Repeat("0", f+1)
	}
	out := m[:1]
	if f > 0 {
		out += "." + m[1:]
	}
	es := "+"
	if e < 0 {
		es = "-"
	}
	return sign + out + "e" + es + strconv.Itoa(absInt(e))
}

var (
	stripDecimalRe = regexp.MustCompile(`(?:\.0*|(\.\d*[1-9])0+)$`)
	normExpRe1     = regexp.MustCompile(`(?:\.0*|(\.\d*[1-9])0+)[Ee]`)
	normExpRe2     = regexp.MustCompile(`(E[+-])(\d)$`)
)

func ssfStripDecimal(o string) string {
	if !strings.Contains(o, ".") {
		return o
	}
	return stripDecimalRe.ReplaceAllString(o, "${1}")
}

func ssfNormalizeExp(o string) string {
	if !strings.Contains(o, "E") {
		return o
	}
	o = normExpRe1.ReplaceAllString(o, "${1}E")
	return normExpRe2.ReplaceAllString(o, "${1}0${2}")
}

func ssfSmallExp(v float64) string {
	w := 11
	if v < 0 {
		w = 12
	}
	if o := ssfStripDecimal(toFixed(v, 12)); len(o) <= w {
		return o
	}
	if o := toPrecision(v, 10); len(o) <= w {
		return o
	}
	return toExponential(v, 5)
}

func ssfLargeExp(v float64) string {
	w := 11
	if v < 0 {
		w = 12
	}
	o := ssfStripDecimal(toFixed(v, 11))
	if len(o) > w || o == "0" || o == "-0" {
		return toPrecision(v, 6)
	}
	return o
}

// generalNumber is how "General" shows a number: an int32 as itself, anything
// else in at most 11 characters (SSF_general_num).
func generalNumber(v float64) string {
	if v == math.Trunc(v) && v >= math.MinInt32 && v <= math.MaxInt32 {
		return numberString(v)
	}
	V := math.Floor(math.Log(math.Abs(v)) * math.Log10E)
	var o string
	switch {
	case V >= -4 && V <= -1:
		o = toPrecision(v, 10+int(V))
	case math.Abs(V) <= 9:
		o = ssfSmallExp(v)
	case V == 10:
		o = toFixed(v, 10)
		if len(o) > 12 {
			o = o[:12]
		}
	default:
		o = ssfLargeExp(v)
	}
	return ssfStripDecimal(ssfNormalizeExp(strings.ToUpper(o)))
}

// ─── formats ────────────────────────────────────────────────────────────────

var builtinFormats = map[int]string{
	0: "General", 1: "0", 2: "0.00", 3: "#,##0", 4: "#,##0.00", 9: "0%", 10: "0.00%", 11: "0.00E+00",
	12: "# ?/?", 13: "# ??/??", 14: "m/d/yy", 15: "d-mmm-yy", 16: "d-mmm", 17: "mmm-yy",
	18: "h:mm AM/PM", 19: "h:mm:ss AM/PM", 20: "h:mm", 21: "h:mm:ss", 22: "m/d/yy h:mm",
	37: "#,##0 ;(#,##0)", 38: "#,##0 ;[Red](#,##0)", 39: "#,##0.00;(#,##0.00)", 40: "#,##0.00;[Red](#,##0.00)",
	45: "mm:ss", 46: "[h]:mm:ss", 47: "mmss.0", 48: "##0.0E+0", 49: "@",
	56: `"上午/下午 "hh"時"mm"分"ss"秒 "`,
	// SSFImplicit: ids a workbook may use without defining.
	5: `"$"#,##0_);\("$"#,##0\)`, 6: `"$"#,##0_);[Red]\("$"#,##0\)`,
	7: `"$"#,##0.00_);\("$"#,##0.00\)`, 8: `"$"#,##0.00_);[Red]\("$"#,##0.00\)`,
	23: "General", 24: "General", 25: "General", 26: "General",
	27: "m/d/yy", 28: "m/d/yy", 29: "m/d/yy", 30: "m/d/yy", 31: "m/d/yy",
	32: "h:mm:ss", 33: "h:mm:ss", 34: "h:mm:ss", 35: "h:mm:ss", 36: "m/d/yy",
	41: `_(* #,##0_);_(* \(#,##0\);_(* "-"_);_(@_)`,
	42: `_("$"* #,##0_);_("$"* \(#,##0\);_("$"* "-"_);_(@_)`,
	43: `_(* #,##0.00_);_(* \(#,##0.00\);_(* "-"??_);_(@_)`,
	44: `_("$"* #,##0.00_);_("$"* \(#,##0.00\);_("$"* "-"??_);_(@_)`,
	50: "m/d/yy", 51: "m/d/yy", 52: "m/d/yy", 53: "m/d/yy", 54: "m/d/yy", 55: "m/d/yy", 57: "m/d/yy", 58: "m/d/yy",
	59: "0", 60: "0.00", 61: "#,##0", 62: "#,##0.00",
	63: `"$"#,##0_);\("$"#,##0\)`, 64: `"$"#,##0_);[Red]\("$"#,##0\)`,
	65: `"$"#,##0.00_);\("$"#,##0.00\)`, 66: `"$"#,##0.00_);[Red]\("$"#,##0.00\)`,
	67: "0%", 68: "0.00%", 69: "# ?/?", 70: "# ??/??", 71: "m/d/yy", 72: "m/d/yy", 73: "d-mmm-yy",
	74: "d-mmm", 75: "mmm-yy", 76: "h:mm", 77: "h:mm:ss", 78: "m/d/yy h:mm", 79: "mm:ss", 80: "[h]:mm:ss", 81: "mmss.0",
}

func builtinFormat(id int) string {
	if f, ok := builtinFormats[id]; ok {
		return f
	}
	return "General"
}

func isGeneral(s string) bool {
	return len(s) >= 7 && strings.EqualFold(s[:7], "general")
}

// splitFmt splits a format into its `;` sections, outside quotes and escapes.
func splitFmt(f string) []string {
	var out []string
	inStr := false
	j := 0
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case '"':
			inStr = !inStr
		case '_', '*', '\\':
			i++
		case ';':
			if !inStr {
				out = append(out, f[j:i])
				j = i + 1
			}
		}
	}
	return append(out, f[j:])
}

var (
	abstimeRe  = regexp.MustCompile(`^\[[HhMmSs]*\]$`)
	condRe     = regexp.MustCompile(`\[[=<>]`)
	currencyRe = regexp.MustCompile(`\$([^-\[\]]*)`)
	subsecRe   = regexp.MustCompile(`\.0+$`)
)

// fmtIsDate is whether a section formats a date or time.
func fmtIsDate(f string) bool {
	for i := 0; i < len(f); {
		c := f[i]
		switch c {
		case 'G':
			if isGeneral(f[i:]) {
				i += 6
			}
			i++
		case '"':
			i++
			for i < len(f) && f[i] != '"' {
				i++
			}
			i++
		case '\\', '_':
			i += 2
		case 'B', 'b':
			if i+1 < len(f) && (f[i+1] == '1' || f[i+1] == '2') {
				return true
			}
			return true
		case 'M', 'D', 'Y', 'H', 'S', 'E', 'm', 'd', 'y', 'h', 's', 'e', 'g':
			return true
		case 'A', 'a':
			if strings.HasPrefix(strings.ToUpper(f[i:]), "A/P") || strings.HasPrefix(strings.ToUpper(f[i:]), "AM/PM") {
				return true
			}
			i++
		case '[':
			end := strings.IndexByte(f[i:], ']')
			if end < 0 {
				return false
			}
			if abstimeRe.MatchString(f[i : i+end+1]) {
				return true
			}
			i += end + 1
		case '.', '0', '#':
			i++
			for i < len(f) && strings.IndexByte("0#?.,E+-%", f[i]) >= 0 {
				i++
			}
		case '*':
			i += 2
		default:
			i++
		}
	}
	return false
}

// chooseFmt is choose_fmt for a number: the section and the section count.
func chooseFmt(f string, v float64) (string, int, bool) {
	secs := splitFmt(f)
	l := len(secs)
	if l > 4 {
		return "", 0, false
	}
	lat := strings.Contains(secs[l-1], "@")
	if l < 4 && lat {
		l--
	}
	var s [4]string
	switch len(secs) {
	case 1:
		if lat {
			s = [4]string{"General", "General", "General", secs[0]}
		} else {
			s = [4]string{secs[0], secs[0], secs[0], "@"}
		}
	case 2:
		if lat {
			s = [4]string{secs[0], secs[0], secs[0], secs[1]}
		} else {
			s = [4]string{secs[0], secs[1], secs[0], "@"}
		}
	case 3:
		if lat {
			s = [4]string{secs[0], secs[1], secs[0], secs[2]}
		} else {
			s = [4]string{secs[0], secs[1], secs[2], "@"}
		}
	case 4:
		copy(s[:], secs)
	}
	if condRe.MatchString(s[0] + s[1]) {
		return "", 0, false // conditional sections: not supported, General instead
	}
	switch {
	case v > 0:
		return s[0], l, true
	case v < 0:
		return s[1], l, true
	}
	return s[2], l, true
}

// ssfFormat formats a number through a format string.
func ssfFormat(format string, v float64) string {
	if isGeneral(format) {
		return generalNumber(v)
	}
	sec, flen, ok := chooseFmt(format, v)
	if !ok || isGeneral(sec) || strings.Contains(sec, "/") && strings.Contains(sec, "?") {
		return generalNumber(v)
	}
	if fmtIsDate(sec) {
		if v < 0 {
			return ""
		}
		return evalDate(sec, v)
	}
	return evalNumber(sec, v, flen)
}

// ssfFormatText formats a string through a format string: the text section
// when there is one, the string as-is otherwise.
func ssfFormatText(format, s string) string {
	secs := splitFmt(format)
	if len(secs) > 4 {
		return s
	}
	last := secs[len(secs)-1]
	sec := "@"
	if len(secs) == 4 || strings.Contains(last, "@") {
		sec = last
	}
	var b strings.Builder
	for _, tok := range tokenize(sec) {
		switch tok.t {
		case '@':
			b.WriteString(s)
		case 't':
			b.WriteString(tok.v)
		}
	}
	return b.String()
}

type fmtToken struct {
	t byte // t literal, @ text, n number pattern, G general, date letters, T am/pm, Z elapsed, s subseconds
	v string
}

// tokenize is eval_fmt's first pass.
func tokenize(f string) []fmtToken {
	var out []fmtToken
	isDate := fmtIsDate(f)
	lst := byte('t')
	for i := 0; i < len(f); {
		c := f[i]
		switch {
		case c == 'G' && isGeneral(f[i:]):
			out = append(out, fmtToken{'G', "General"})
			i += 7
		case c == '"':
			j := i + 1
			for j < len(f) && f[j] != '"' {
				j++
			}
			out = append(out, fmtToken{'t', f[i+1 : min(j, len(f))]})
			i = j + 1
		case c == '\\':
			if i+1 < len(f) {
				out = append(out, fmtToken{'t', f[i+1 : i+2]})
			}
			i += 2
		case c == '_':
			out = append(out, fmtToken{'t', " "})
			i += 2
		case c == '@':
			out = append(out, fmtToken{'@', ""})
			i++
		case strings.IndexByte("BMDYHSEbmdyhseg", c) >= 0:
			lc := c | 0x20
			if c == 'B' || c == 'b' {
				lc = 'b'
			}
			j := i + 1
			for j < len(f) && f[j]|0x20 == lc {
				j++
			}
			t := lc
			if lc == 'm' && lst|0x20 == 'h' {
				t = 'M'
			}
			out = append(out, fmtToken{t, strings.Repeat(string(lc), j-i)})
			lst = t
			i = j
		case c == 'A' || c == 'a':
			up := strings.ToUpper(f[i:])
			switch {
			case strings.HasPrefix(up, "A/P"):
				out = append(out, fmtToken{'T', "A/P"})
				i += 3
			case strings.HasPrefix(up, "AM/PM"):
				out = append(out, fmtToken{'T', "AM/PM"})
				i += 5
			default:
				out = append(out, fmtToken{'t', string(c)})
				i++
			}
			lst = c
		case c == '[':
			end := strings.IndexByte(f[i:], ']')
			if end < 0 {
				i = len(f)
				break
			}
			o := f[i : i+end+1]
			if abstimeRe.MatchString(o) {
				out = append(out, fmtToken{'Z', strings.ToLower(o)})
				lst = o[1]
			} else if strings.Contains(o, "$") && !isDate {
				m := currencyRe.FindStringSubmatch(o)
				cur := "$"
				if m != nil && m[1] != "" {
					cur = m[1]
				}
				out = append(out, fmtToken{'t', cur})
			}
			i += end + 1
		case c == '.' && isDate:
			j := i + 1
			for j < len(f) && f[j] == '0' {
				j++
			}
			out = append(out, fmtToken{'s', f[i:j]})
			i = j
		case c == '.' || c == '0' || c == '#' || c == '?':
			j := i + 1
			for j < len(f) && strings.IndexByte("0#?.,E+-%", f[j]) >= 0 {
				j++
			}
			out = append(out, fmtToken{'n', f[i:j]})
			i = j
		case c == '*':
			i += 2
		case c >= '1' && c <= '9':
			j := i + 1
			for j < len(f) && f[j] >= '0' && f[j] <= '9' {
				j++
			}
			out = append(out, fmtToken{'t', f[i:j]})
			i = j
		default:
			// Every other character is literal; multi-byte runes stay whole.
			r := []rune(f[i:])[0]
			out = append(out, fmtToken{'t', string(r)})
			i += len(string(r))
		}
	}
	return out
}

// ─── numbers ────────────────────────────────────────────────────────────────

// jsRound is Math.round.
func jsRound(x float64) float64 { return math.Floor(x + 0.5) }

func commaify(s string) string {
	if len(s) <= 3 {
		return s
	}
	j := len(s) % 3
	o := s[:j]
	for ; j < len(s); j += 3 {
		if o != "" {
			o += ","
		}
		o += s[j : j+3]
	}
	return o
}

// writeNum formats a non-negative number through one number pattern
// (`#,##0.00`, `0%`, `0.00E+00`, `#,##0,`).
func writeNum(pat string, v float64) string {
	if v < 0 {
		return "-" + writeNum(pat, -v)
	}
	if pct := strings.Count(pat, "%"); pct > 0 {
		return writeNum(strings.ReplaceAll(pat, "%", ""), v*math.Pow(100, float64(pct))) + strings.Repeat("%", pct)
	}
	if strings.HasSuffix(pat, ",") && !strings.Contains(pat, "E") {
		trimmed := strings.TrimRight(pat, ",")
		return writeNum(trimmed, v/math.Pow(1000, float64(len(pat)-len(trimmed))))
	}
	if i := strings.IndexByte(pat, '-'); i > 0 && !strings.ContainsAny(pat, ".Ee") {
		// `000-0000`: the digits fill the placeholders right to left and the
		// dashes stay where the pattern puts them.
		digits := writeNum(strings.ReplaceAll(pat, "-", ""), v)
		var out []byte
		j := len(digits) - 1
		for k := len(pat) - 1; k >= 0; k-- {
			if pat[k] == '-' {
				out = append(out, '-')
				continue
			}
			if j >= 0 {
				out = append(out, digits[j])
				j--
			}
		}
		for ; j >= 0; j-- {
			out = append(out, digits[j])
		}
		for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
			out[a], out[b] = out[b], out[a]
		}
		return string(out)
	}
	if e := strings.IndexAny(pat, "Ee"); e >= 0 {
		return writeExp(pat[:e], pat[e:], v)
	}
	intPat, decPat, hasDot := strings.Cut(pat, ".")
	decimals := 0
	if hasDot {
		decimals = len(decPat)
	}
	p := math.Pow(10, float64(decimals))
	r := numberString(jsRound(v*p) / p)
	intDigits, fracDigits, _ := strings.Cut(r, ".")
	if strings.Contains(r, "e") {
		intDigits, fracDigits = toFixed(v, decimals), ""
		intDigits, fracDigits, _ = strings.Cut(intDigits, ".")
	}
	// Integer part: zero-padded to the `0` count, empty when it is 0 and the
	// pattern has no `0`; grouped by thousands when the pattern has a comma.
	minInt := strings.Count(intPat, "0")
	if intDigits == "0" {
		intDigits = ""
	}
	if len(intDigits) < minInt {
		intDigits = strings.Repeat("0", minInt-len(intDigits)) + intDigits
	}
	if strings.Contains(intPat, ",") {
		intDigits = commaify(intDigits)
	}
	if !hasDot {
		return intDigits
	}
	// Decimal part: `0` always shows, `#` drops a trailing zero, `?` pads with
	// a space.
	frac := []byte(fracDigits + strings.Repeat("0", max(0, decimals-len(fracDigits))))
	end := len(frac)
	for end > 0 && frac[end-1] == '0' && decPat[end-1] != '0' {
		if decPat[end-1] == '?' {
			frac[end-1] = ' '
		}
		end--
	}
	if decPat == "" {
		return intDigits + "."
	}
	return intDigits + "." + string(frac[:max(end, strings.LastIndexAny(decPat, "0?")+1)])
}

func writeExp(mantPat, expPat string, v float64) string {
	intPat, decPat, _ := strings.Cut(mantPat, ".")
	decimals := len(decPat)
	expDigits := strings.Count(expPat, "0")
	if v == 0 {
		return writeNum(mantPat, 0) + expSuffix(expPat, 0, expDigits)
	}
	e := int(math.Floor(math.Log10(v)))
	period := len(intPat)
	if period > 1 && strings.Contains(intPat, "#") {
		// Engineering notation: the exponent is a multiple of the integer width.
		e = int(math.Floor(float64(e)/float64(period))) * period
	} else if n := strings.Count(intPat, "0"); n > 1 {
		e -= n - 1
	}
	m := v / math.Pow(10, float64(e))
	s := toFixed(m, decimals)
	if f, _ := strconv.ParseFloat(s, 64); f >= math.Pow(10, float64(max(1, period))) && period <= 1 {
		e++
		s = toFixed(v/math.Pow(10, float64(e)), decimals)
	}
	return s + expSuffix(expPat, e, expDigits)
}

func expSuffix(expPat string, e, digits int) string {
	sign := ""
	switch {
	case e < 0:
		sign = "-"
	case strings.Contains(expPat, "+"):
		sign = "+"
	}
	d := strconv.Itoa(absInt(e))
	if len(d) < digits {
		d = strings.Repeat("0", digits-len(d)) + d
	}
	return expPat[:1] + sign + d
}

// evalNumber renders a number section: its literals around one number
// pattern (several patterns, as in `000-0000`, share the digits right to left).
func evalNumber(sec string, v float64, flen int) string {
	toks := tokenize(sec)
	myv := v
	if v < 0 && flen > 1 {
		myv = -v
	}
	var pat string
	var idx []int
	for i, t := range toks {
		if t.t == 'n' {
			pat += t.v
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		var b strings.Builder
		for _, t := range toks {
			switch t.t {
			case 't':
				b.WriteString(t.v)
			case 'G':
				b.WriteString(generalNumber(v))
			}
		}
		return b.String()
	}
	ostr := writeNum(pat, myv)
	if myv < 0 && toks[0].t == 't' {
		ostr = ostr[1:]
		toks[0].v = "-" + toks[0].v
	}
	if len(idx) == 1 || strings.ContainsAny(pat, ".Ee") {
		for k, i := range idx {
			toks[i].v = ""
			if k == 0 {
				toks[i].v = ostr
			}
		}
	} else {
		jj := len(ostr) - 1
		lasti := -1
		for k := len(idx) - 1; k >= 0; k-- {
			t := &toks[idx[k]]
			n := len(t.v)
			switch {
			case jj >= n-1:
				jj -= n
				t.v = ostr[jj+1 : jj+1+n]
			case jj < 0:
				t.v = ""
			default:
				t.v = ostr[:jj+1]
				jj = -1
			}
			lasti = idx[k]
		}
		if jj >= 0 && lasti >= 0 {
			toks[lasti].v = ostr[:jj+1] + toks[lasti].v
		}
	}
	var b strings.Builder
	for _, t := range toks {
		switch t.t {
		case 't', 'n':
			b.WriteString(t.v)
		case 'G':
			b.WriteString(generalNumber(v))
		}
	}
	return b.String()
}

// ─── dates ──────────────────────────────────────────────────────────────────

type dateCode struct {
	D, T       int
	u          float64
	y, m, d    int
	H, M, S, q int
}

// parseDateCode is SSF_parse_date_code for the 1900 date system, including
// Excel's phantom 29 February 1900.
func parseDateCode(v float64) (dateCode, bool) {
	if v > 2958465 || v < 0 {
		return dateCode{}, false
	}
	date := int(v)
	tm := int(math.Floor(86400 * (v - float64(date))))
	out := dateCode{D: date, T: tm, u: 86400*(v-float64(date)) - float64(tm)}
	if math.Abs(out.u) < 1e-6 {
		out.u = 0
	}
	if out.u > 0.9999 {
		out.u = 0
		tm++
		if tm == 86400 {
			out.T, tm = 0, 0
			date++
			out.D++
		}
	}
	switch {
	case date == 60:
		out.y, out.m, out.d, out.q = 1900, 2, 29, 3
	case date == 0:
		out.y, out.m, out.d, out.q = 1900, 1, 0, 6
	default:
		if date > 60 {
			date--
		}
		t := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, date-1)
		out.y, out.m, out.d = t.Year(), int(t.Month()), t.Day()
		out.q = int(t.Weekday())
		if date < 60 {
			out.q = (out.q + 6) % 7
		}
	}
	out.S = tm % 60
	tm /= 60
	out.M = tm % 60
	tm /= 60
	out.H = tm
	return out, true
}

var (
	monthNames = [12][3]string{
		{"J", "Jan", "January"}, {"F", "Feb", "February"}, {"M", "Mar", "March"}, {"A", "Apr", "April"},
		{"M", "May", "May"}, {"J", "Jun", "June"}, {"J", "Jul", "July"}, {"A", "Aug", "August"},
		{"S", "Sep", "September"}, {"O", "Oct", "October"}, {"N", "Nov", "November"}, {"D", "Dec", "December"},
	}
	dayNames = [7][2]string{
		{"Sun", "Sunday"}, {"Mon", "Monday"}, {"Tue", "Tuesday"}, {"Wed", "Wednesday"},
		{"Thu", "Thursday"}, {"Fri", "Friday"}, {"Sat", "Saturday"},
	}
)

func pad0(n, width int) string {
	s := strconv.Itoa(n)
	if len(s) < width {
		s = strings.Repeat("0", width-len(s)) + s
	}
	return s
}

// evalDate renders a date/time section.
func evalDate(sec string, v float64) string {
	dt, ok := parseDateCode(v)
	if !ok {
		return ""
	}
	toks := tokenize(sec)
	hr := byte('H')
	for _, t := range toks {
		if t.t == 'T' {
			hr = 'h'
		}
	}
	// Scan right to left: `m` right before seconds is minutes.
	bt, ss0 := 0, 0
	lst := byte('t')
	for i := len(toks) - 1; i >= 0; i-- {
		switch toks[i].t {
		case 'h', 'H':
			toks[i].t = hr
			lst = 'h'
			bt = max(bt, 1)
		case 's':
			if m := subsecRe.FindString(toks[i].v); m != "" {
				ss0 = max(ss0, len(m)-1)
			}
			bt = max(bt, 3)
			lst = 's'
		case 'd', 'y', 'M', 'e':
			lst = toks[i].t
		case 'm':
			if lst == 's' {
				toks[i].t = 'M'
				bt = max(bt, 2)
			}
		case 'Z':
			v := toks[i].v
			if bt < 1 && strings.ContainsAny(v, "Hh") {
				bt = 1
			}
			if bt < 2 && strings.ContainsAny(v, "Mm") {
				bt = 2
			}
			if bt < 3 && strings.ContainsAny(v, "Ss") {
				bt = 3
			}
		}
	}
	switch bt {
	case 1:
		if dt.u >= 0.5 {
			dt.u = 0
			dt.S++
		}
		if dt.S >= 60 {
			dt.S = 0
			dt.M++
		}
		if dt.M >= 60 {
			dt.M = 0
			dt.H++
		}
	case 2:
		if dt.u >= 0.5 {
			dt.u = 0
			dt.S++
		}
		if dt.S >= 60 {
			dt.S = 0
			dt.M++
		}
	}
	var b strings.Builder
	for _, t := range toks {
		switch t.t {
		case 't':
			b.WriteString(t.v)
		case 'T':
			switch {
			case t.v == "A/P" && dt.H >= 12:
				b.WriteString("P")
			case t.v == "A/P":
				b.WriteString("A")
			case dt.H >= 12:
				b.WriteString("PM")
			default:
				b.WriteString("AM")
			}
		case 'G':
			b.WriteString(generalNumber(v))
		case 'n':
			b.WriteString(writeNum(t.v, v))
		default:
			b.WriteString(writeDate(t.t, t.v, dt, ss0))
		}
	}
	return b.String()
}

// writeDate is SSF_write_date for one token.
func writeDate(typ byte, f string, val dateCode, ss0 int) string {
	switch typ {
	case 'y', 'b':
		y := val.y
		if typ == 'b' {
			y += 543
		}
		if len(f) <= 2 {
			return pad0(y%100, 2)
		}
		return pad0(y%10000, 4)
	case 'm':
		switch len(f) {
		case 1, 2:
			return pad0(val.m, len(f))
		case 3:
			return monthNames[val.m-1][1]
		case 5:
			return monthNames[val.m-1][0]
		}
		return monthNames[val.m-1][2]
	case 'd':
		switch len(f) {
		case 1, 2:
			return pad0(val.d, len(f))
		case 3:
			return dayNames[val.q][0]
		}
		return dayNames[val.q][1]
	case 'h':
		return pad0(1+(val.H+11)%12, min(len(f), 2))
	case 'H':
		return pad0(val.H, min(len(f), 2))
	case 'M':
		return pad0(val.M, min(len(f), 2))
	case 's':
		if val.u == 0 && (f == "s" || f == "ss") {
			return pad0(val.S, len(f))
		}
		tt := 1
		switch {
		case ss0 >= 2 && ss0 == 3:
			tt = 1000
		case ss0 >= 2:
			tt = 100
		case ss0 == 1:
			tt = 10
		}
		ss := int(jsRound(float64(tt) * (float64(val.S) + val.u)))
		if ss >= 60*tt {
			ss = 0
		}
		if f == "s" {
			if ss == 0 {
				return "0"
			}
			return numberString(float64(ss) / float64(tt))
		}
		o := pad0(ss, 2+ss0)
		if f == "ss" {
			return o[:2]
		}
		return "." + o[2:min(len(o), 2+len(f)-1)]
	case 'Z':
		var out int
		switch f {
		case "[h]", "[hh]":
			out = val.D*24 + val.H
		case "[m]", "[mm]":
			out = (val.D*24+val.H)*60 + val.M
		case "[s]", "[ss]":
			out = ((val.D*24+val.H)*60+val.M)*60 + int(jsRound(float64(val.S)+val.u))
		default:
			return ""
		}
		if len(f) == 3 {
			return pad0(out, 1)
		}
		return pad0(out, 2)
	case 'e':
		return pad0(val.y, 1)
	}
	return ""
}

// isoToSerial is datenum(parseDate(v)) for an ISO 8601 date cell (t="d"):
// days since 1899-12-30, the time as a fraction of a day.
func isoToSerial(v string) float64 {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
			return float64(t.Sub(base).Milliseconds()) / 86400000
		}
	}
	return math.NaN()
}
