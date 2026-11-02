package gitsvc

// Slice B helpers: JavaScript semantics the TS gitService leaned on without
// saying so (UTF-8 decoding, Number(), localeCompare, RegExp), and the bits of
// per-repo plumbing several slices share (merge state, guard snapshots, rerere
// capture). Everything here is unexported and b-prefixed; the ones worth
// hoisting into gitx are listed in the slice report.

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// ─── Text decoding ─────────────────────────────────────────────────────────

// bDecodeUTF8 is Node's `buffer.toString('utf-8')`: invalid sequences become
// U+FFFD, one per *maximal subpart* (the WHATWG rule). Every string TS read
// from git or the disk went through this decoder, so a binary or Latin-1 file
// has to come out with the same replacement characters here. (encoding/json
// would also replace them, but byte by byte, which disagrees on a truncated
// multi-byte sequence.)
func bDecodeUTF8(s string) string {
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
		i += bMaximalSubpart(s[i:])
	}
	return b.String()
}

// bMaximalSubpart is how many bytes of an invalid sequence one U+FFFD stands
// for: the lead byte plus every continuation byte that could still have
// completed it.
func bMaximalSubpart(s string) int {
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

// bReadText is `readFile(path, 'utf-8')`.
func bReadText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return bDecodeUTF8(string(data)), nil
}

// bRaw is gitx.Raw with its output decoded the way simple-git decoded it.
func bRaw(ctx context.Context, repoPath string, args ...string) (string, error) {
	out, err := gitx.Raw(ctx, repoPath, args, gitx.RunOpts{})
	if err != nil {
		return "", bDecodeErr(err)
	}
	return bDecodeUTF8(out), nil
}

// bRawOr is `gitFor(repo).raw(args).catch(() => fallback)`.
func bRawOr(ctx context.Context, repoPath, fallback string, args ...string) string {
	out, err := bRaw(ctx, repoPath, args...)
	if err != nil {
		return fallback
	}
	return out
}

// bGit is runGit(): stdout decoded, the error message already shaped by gitx.
func bGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	out, err := gitx.RunGit(ctx, repoPath, args, gitx.RunOpts{})
	if err != nil {
		return "", bDecodeErr(err)
	}
	return bDecodeUTF8(out), nil
}

// bGitOr is `runGit(repo, args).catch(() => fallback)`.
func bGitOr(ctx context.Context, repoPath, fallback string, args ...string) string {
	out, err := bGit(ctx, repoPath, args...)
	if err != nil {
		return fallback
	}
	return out
}

// bDecodeErr decodes a git error message as TS did (it was a JS string too),
// keeping the original error reachable.
func bDecodeErr(err error) error {
	var ge *gitx.GitError
	if errors.As(err, &ge) && !utf8.ValidString(ge.Message) {
		return &gitx.GitError{Message: bDecodeUTF8(ge.Message), Exec: ge.Exec}
	}
	return err
}

// ─── JavaScript value semantics ────────────────────────────────────────────

var bJSDecimalRe = regexp.MustCompile(`^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$`)

// bJSNumber is JavaScript's `Number(string)`: surrounding whitespace ignored,
// "" is 0, hex/octal/binary literals and Infinity accepted, anything else NaN.
func bJSNumber(s string) float64 {
	t := gitx.TrimJS(s)
	if t == "" {
		return 0
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
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
			if n, err := strconv.ParseUint(t[2:], base, 64); err == nil {
				return float64(n)
			}
			return math.NaN()
		}
	}
	if !bJSDecimalRe.MatchString(t) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN()
	}
	return f
}

// bJSInt is `Number(s) || 0` narrowed to the integers git prints (timestamps,
// counts, line numbers): NaN and ±Infinity become 0.
func bJSInt(s string) int64 {
	f := bJSNumber(s)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int64(f)
}

// bJSNumberString is `${Number(s)}` for the values purge stamps take.
func bJSNumberString(s string) string {
	f := bJSNumber(s)
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == math.Trunc(f) && math.Abs(f) < 1e21:
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// bSplitWS is `s.split(/\s+/).filter(Boolean)` with JavaScript's \s.
func bSplitWS(s string) []string {
	return strings.FieldsFunc(s, gitx.IsJSSpace)
}

// bSliceEnd is the end index of `arr.slice(0, end)` on an array of length n:
// a negative end counts back from the end, as in JS.
func bSliceEnd(n, end int) int {
	if end < 0 {
		end += n
		if end < 0 {
			end = 0
		}
	}
	if end > n {
		end = n
	}
	return end
}

// bIntOr resolves an optional numeric argument: nil (undefined in TS) takes
// the default, anything else is used as given — `max = 400` semantics.
func bIntOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

// bNonNil turns a nil slice into an empty one, so it crosses the bridge as [].
func bNonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ─── localeCompare ─────────────────────────────────────────────────────────

// bLocaleCompare approximates `a.localeCompare(b)` — ICU's root collation,
// which is what Electron's V8 used. It is not a full UCA, but it reproduces the
// orderings a file tree and a branch list actually show:
//   - whitespace, then punctuation and symbols (in DUCET order), then emoji,
//     then currency signs, then digits, then letters; digits compare as
//     characters, not numbers ("10" < "9");
//   - letters compare case- and accent-insensitively first, then by accent,
//     then lower case before upper case;
//   - emoji and other symbols sort with the symbols, other scripts after Latin
//     by code point.
func bLocaleCompare(a, b string) int {
	ka, kb := bCollationKey(a), bCollationKey(b)
	for level := 0; level < 3; level++ {
		if c := bCompareInts(ka[level], kb[level]); c != 0 {
			return c
		}
	}
	return 0
}

func bCompareInts(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// bPunctOrder is DUCET's order for the ASCII variable characters.
const bPunctOrder = "\t\n\v\f\r _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~"

// Primary weight bands.
const (
	bWeightPunct    = 1_000
	bWeightSymbol   = 2_000
	bWeightCurrency = 2_500
	bWeightDigit    = 3_000
	bWeightLatin    = 4_000
	bWeightOther    = 10_000
)

var bLatinFold = map[rune]struct {
	base   string
	accent int
}{
	'à': {"a", 1}, 'á': {"a", 2}, 'â': {"a", 3}, 'ã': {"a", 4}, 'ä': {"a", 5}, 'å': {"a", 6},
	'ç': {"c", 7}, 'è': {"e", 1}, 'é': {"e", 2}, 'ê': {"e", 3}, 'ë': {"e", 5},
	'ì': {"i", 1}, 'í': {"i", 2}, 'î': {"i", 3}, 'ï': {"i", 5}, 'ñ': {"n", 4},
	'ò': {"o", 1}, 'ó': {"o", 2}, 'ô': {"o", 3}, 'õ': {"o", 4}, 'ö': {"o", 5}, 'ø': {"o", 8},
	'ù': {"u", 1}, 'ú': {"u", 2}, 'û': {"u", 3}, 'ü': {"u", 5}, 'ý': {"y", 2}, 'ÿ': {"y", 5},
	'ß': {"ss", 9}, 'æ': {"ae", 9}, 'œ': {"oe", 9},
}

func bCollationKey(s string) [3][]int {
	var k [3][]int
	add := func(p, sec, ter int) {
		k[0] = append(k[0], p)
		k[1] = append(k[1], sec)
		k[2] = append(k[2], ter)
	}
	for _, r := range s {
		ter := 0
		if unicode.IsUpper(r) {
			ter = 1
			r = unicode.ToLower(r)
		}
		if f, ok := bLatinFold[r]; ok {
			for i, c := range f.base {
				sec := 0
				if i == 0 {
					sec = f.accent
				}
				add(bWeightLatin+int(c-'a'), sec, ter)
			}
			continue
		}
		switch {
		case r >= 'a' && r <= 'z':
			add(bWeightLatin+int(r-'a'), 0, ter)
		case r >= '0' && r <= '9':
			add(bWeightDigit+int(r-'0'), 0, ter)
		case r < 0x80 && strings.ContainsRune(bPunctOrder, r):
			add(bWeightPunct+strings.IndexRune(bPunctOrder, r), 0, ter)
		case unicode.Is(unicode.Sc, r):
			// Currency symbols come after every other symbol, emoji included.
			add(bWeightCurrency+int(r)%500, 0, ter)
		case unicode.IsSpace(r) || unicode.IsPunct(r):
			add(bWeightPunct+len(bPunctOrder)+int(r)%500, 0, ter)
		case unicode.IsSymbol(r) || r >= 0x1F000 && r <= 0x1FAFF:
			add(bWeightSymbol+int(r)%500, 0, ter)
		case unicode.IsDigit(r):
			add(bWeightDigit+int(r)%10, 0, ter)
		default:
			add(bWeightOther+int(r), 0, ter)
		}
	}
	return k
}

// ─── JavaScript RegExp ─────────────────────────────────────────────────────

// bJSRegexp compiles a JavaScript regular expression source (no `u` flag) for
// Go. RE2 shares JS's syntax for the patterns people type into a search box;
// the translation covers the places where the same text means something
// different: `.` (JS excludes \r,  ,   too), `\s`/`\S` (Unicode
// spaces in JS) and `\uXXXX`. What RE2 cannot express at all — lookaround,
// backreferences — fails to compile, and the callers treat that like the
// invalid pattern it is for them ([] / no match).
func bJSRegexp(src string, ignoreCase bool) (*regexp.Regexp, error) {
	var b strings.Builder
	if ignoreCase {
		b.WriteString("(?i)")
	}
	const dot = `[^\n\r\x{2028}\x{2029}]`
	const space = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			n := src[i+1]
			switch {
			case n == 's' && inClass:
				b.WriteString(space)
			case n == 's':
				b.WriteString("[" + space + "]")
			case n == 'S' && !inClass:
				b.WriteString("[^" + space + "]")
			case n == 'u' && i+5 < len(src) && bIsHex(src[i+2:i+6]):
				b.WriteString(`\x{` + src[i+2:i+6] + `}`)
				i += 4
			default:
				b.WriteByte(c)
				b.WriteByte(n)
			}
			i++
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
			// A leading ']' or '^]' is literal in Go but closes an empty class
			// in JS; let RE2 judge the rest.
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
		case c == '.' && !inClass:
			b.WriteString(dot)
		default:
			b.WriteByte(c)
		}
	}
	return regexp.Compile(b.String())
}

func bIsHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// bSearchPattern builds the search-bar pattern searchFileContents and
// searchFileMatches share: literal text escaped, `\b…\b` for whole words,
// case-insensitive unless asked otherwise.
func bSearchPattern(query string, caseSensitive, wholeWord, isRegex bool) (*regexp.Regexp, error) {
	src := query
	if !isRegex {
		src = bEscapeRegExp(query)
	}
	if wholeWord {
		src = `\b` + src + `\b`
	}
	return bJSRegexp(src, !caseSensitive)
}

// bEscapeRegExp is `s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')`.
func bEscapeRegExp(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.*+?^${}()|[]\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// bJSDot is JavaScript's `.` (no `s` flag) for the parsers ported from TS.
const bJSDot = `[^\n\r\x{2028}\x{2029}]`

// bReplaceFirst is `s.replace(re, repl)` without the `g` flag.
func bReplaceFirst(re *regexp.Regexp, s, repl string) string {
	loc := re.FindStringSubmatchIndex(s)
	if loc == nil {
		return s
	}
	var dst []byte
	dst = re.ExpandString(dst, repl, s, loc)
	return s[:loc[0]] + string(dst) + s[loc[1]:]
}

// ─── Shared per-repo plumbing ──────────────────────────────────────────────

// bMergeState is gitService.mergeState(): which multi-step operation is in
// progress, "" for none. TS cached the absolute git dir per repo; resolving it
// costs one rev-parse, so this slice resolves it each time.
func bMergeState(ctx context.Context, repoPath string) (string, error) {
	out, err := bRaw(ctx, repoPath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := gitx.TrimJS(out)
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(gitDir, name))
		return err == nil
	}
	switch {
	case has("rebase-merge") || has("rebase-apply"):
		return "rebase", nil
	case has("MERGE_HEAD"):
		return "merge", nil
	case has("CHERRY_PICK_HEAD"):
		return "cherry-pick", nil
	case has("REVERT_HEAD"):
		return "revert", nil
	}
	return "", nil
}

// bBisectInProgress is buildBisectStatus(repo).inProgress: BISECT_START exists.
func bBisectInProgress(ctx context.Context, repoPath string) bool {
	out, err := bRaw(ctx, repoPath, "rev-parse", "--git-path", "BISECT_START")
	if err != nil {
		return false
	}
	p := gitx.TrimJS(out)
	if !strings.HasPrefix(p, "/") {
		p = filepath.Join(repoPath, p)
	}
	_, err = os.Stat(p)
	return err == nil
}

// bRerereReplayed holds, per repository, the files git said it resolved from
// a memorised resolution during the last conflicting cherry-pick. Session
// state on purpose (see withRerereCapture in git.ts). rerereStatus — slice C —
// reads the same map in TS; the lead hoists one copy.
var bRerereReplayed = struct {
	sync.Mutex
	m map[string][]string
}{m: map[string][]string{}}

// bWithRerereCapture runs an operation that may conflict, noting any rerere
// replays it announces in its error output.
func bWithRerereCapture(repoPath string, op func() error) error {
	err := op()
	if err == nil {
		return nil
	}
	if files := gitx.ParseRerereResolved(err.Error()); len(files) > 0 {
		bRerereReplayed.Lock()
		bRerereReplayed.m[repoPath] = files
		bRerereReplayed.Unlock()
	}
	return err
}

// bErr is a user-facing error whose text the renderer shows verbatim — the TS
// `new Error('…')` messages, capitals and full stops included.
func bErr(msg string) error { return errors.New(msg) }

// bStableSortBy sorts xs in place, stably, by less — Array.prototype.sort
// is stable, so ties keep their insertion order in both languages.
func bStableSortBy[T any](xs []T, less func(a, b T) bool) {
	sort.SliceStable(xs, func(i, j int) bool { return less(xs[i], xs[j]) })
}
