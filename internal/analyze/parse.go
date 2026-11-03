package analyze

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Problem is one diagnostic from a project's own analyzer, normalised across
// tools (src/shared/types.ts Problem). Line and col are JS numbers: a tool
// that prints "2.5" really did produce 2.5 in TS, so they stay float64.
type Problem struct {
	// File is repo-relative, forward slashes — the key the UI groups on.
	File     string  `json:"file"`
	Line     float64 `json:"line"`
	Col      float64 `json:"col"`
	Severity string  `json:"severity"`
	Message  string  `json:"message"`
	// Code is the tool's own code (`TS2304`, an ESLint rule id).
	Code string `json:"code,omitempty"`
	// Source names the analyzer (`dart`, `tsc`, `eslint`, …).
	Source string `json:"source"`
}

// ─── paths ──────────────────────────────────────────────────────────────────

// Caches live for the process, as the TS module-level maps did: realpath is a
// syscall and rel runs per diagnostic.
var (
	cacheMu      sync.Mutex
	realPaths    = map[string]string{}
	physicalDirs = map[string]string{}
)

func realRepo(repoPath string) string {
	cacheMu.Lock()
	known, ok := realPaths[repoPath]
	cacheMu.Unlock()
	if ok {
		return known
	}
	real := repoPath
	if r, err := filepath.EvalSymlinks(repoPath); err == nil {
		real = r
	}
	// Gone or unreadable — the path as given is the best we have.
	cacheMu.Lock()
	realPaths[repoPath] = real
	cacheMu.Unlock()
	return real
}

// resolve is Node's path.resolve(base, p).
func resolve(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	abs, err := filepath.Abs(filepath.Join(base, p))
	if err != nil {
		return filepath.Join(base, p)
	}
	return abs
}

// relative is Node's path.relative(from, to): both resolved, "" when equal.
func relative(from, to string) string {
	f, t := resolve("", from), resolve("", to)
	if f == t {
		return ""
	}
	r, err := filepath.Rel(f, t)
	if err != nil {
		return t
	}
	return r
}

// rel makes file repo-relative with forward slashes — what the UI groups and
// filters on.
//
// A tool that resolved symlinks on its way (Node's `process.cwd()` does) hands
// back a path under the repo's *physical* location, which is not the path the
// user opened. Relative to the given root that produces a `../../..` chain, so
// when the first attempt escapes the repo, the physical root gets a turn.
func rel(repoPath, file string) string {
	abs := file
	if !filepath.IsAbs(file) {
		abs = resolve(repoPath, file)
	}
	r := relative(repoPath, abs)
	if strings.HasPrefix(r, "..") {
		if viaReal := relative(realRepo(repoPath), physical(abs)); !strings.HasPrefix(viaReal, "..") {
			r = viaReal
		}
	}
	if r == "" {
		r = file
	}
	return filepath.ToSlash(r)
}

// physical is the physical path of p, resolving symlinks in whatever part of
// it exists. A diagnostic can name a file that is gone by the time we look, so
// the deepest existing ancestor is resolved and the rest kept as written.
func physical(p string) string {
	dir := filepath.Dir(p)
	cacheMu.Lock()
	known, ok := physicalDirs[dir]
	cacheMu.Unlock()
	if ok {
		return filepath.Join(known, filepath.Base(p))
	}
	cursor := dir
	var tail []string
	for {
		if real, err := filepath.EvalSymlinks(cursor); err == nil {
			resolved := filepath.Join(append([]string{real}, tail...)...)
			cacheMu.Lock()
			physicalDirs[dir] = resolved
			cacheMu.Unlock()
			return filepath.Join(resolved, filepath.Base(p))
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			cacheMu.Lock()
			physicalDirs[dir] = dir
			cacheMu.Unlock()
			return p
		}
		tail = append([]string{filepath.Base(cursor)}, tail...)
		cursor = parent
	}
}

// ─── JS value helpers ───────────────────────────────────────────────────────

var reJSNumber = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// jsNumber is `Number(v)`.
func jsNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		s := gitx.TrimJS(x)
		if s == "" {
			return 0
		}
		if len(s) > 2 && s[0] == '0' {
			base := 0
			switch s[1] {
			case 'x', 'X':
				base = 16
			case 'o', 'O':
				base = 8
			case 'b', 'B':
				base = 2
			}
			if base != 0 {
				if n, err := strconv.ParseUint(s[2:], base, 64); err == nil {
					return float64(n)
				}
				return math.NaN()
			}
		}
		switch s {
		case "Infinity", "+Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		}
		if !reJSNumber.MatchString(s) {
			return math.NaN()
		}
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return math.NaN()
}

// numOr is `Number(v) || def`. An infinite value (unencodable as JSON) also
// falls back.
func numOr(v any, def float64) float64 {
	n := jsNumber(v)
	if n == 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return def
	}
	return n
}

// fmtNum is a JS number's string form, for dedupe keys.
func fmtNum(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// jsonSlice is `JSON.parse(s.slice(s.indexOf('['), s.lastIndexOf(']') + 1))`
// with String.prototype.slice's negative-index rule.
func jsonSlice(s string) ([]any, bool) {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]") + 1
	if start < 0 {
		start = len(s) - 1
		if start < 0 {
			start = 0
		}
	}
	if start >= end {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(s[start:end]), &v); err != nil {
		return nil, false
	}
	arr, ok := v.([]any)
	return arr, ok
}

// ─── Parsers (pure — covered by the test suite) ──────────────────────────────

var reDartUnescape = shared.MustJSRegexp(`\\(.)`, false)

// ParseDartAnalyze reads `dart analyze --format=machine`:
// `SEVERITY|TYPE|CODE|FILE|LINE|COL|LENGTH|MESSAGE`, message backslash-escaped.
func ParseDartAnalyze(stdout, repoPath string) []Problem {
	out := []Problem{}
	for _, line := range shared.SplitNL(stdout) {
		parts := strings.Split(line, "|")
		if len(parts) < 8 {
			continue
		}
		severity, code, file := parts[0], parts[2], parts[3]
		message := gitx.TrimJS(reDartUnescape.ReplaceAllString(strings.Join(parts[7:], "|"), "${1}"))
		if file == "" || message == "" {
			continue
		}
		sev := "info"
		switch severity {
		case "ERROR":
			sev = "error"
		case "WARNING":
			sev = "warning"
		}
		out = append(out, Problem{
			File: rel(repoPath, file), Line: numOr(parts[4], 1), Col: numOr(parts[5], 1),
			Severity: sev, Message: message, Code: code, Source: "dart",
		})
	}
	return out
}

var reTsc = shared.MustJSRegexp(`^(.+?)\((\d+),(\d+)\): (error|warning|info) ([A-Z]+\d+): (.*)$`, false)

// ParseTsc reads `tsc --noEmit --pretty false`: `file(line,col): error TS2304: message`.
func ParseTsc(stdout, repoPath string) []Problem {
	out := []Problem{}
	for _, line := range shared.SplitNL(stdout) {
		m := reTsc.FindStringSubmatch(gitx.TrimJS(line))
		if m == nil {
			continue
		}
		out = append(out, Problem{
			File: rel(repoPath, m[1]), Line: jsNumber(m[2]), Col: jsNumber(m[3]),
			Severity: m[4], Message: gitx.TrimJS(m[6]), Code: m[5], Source: "tsc",
		})
	}
	return out
}

// ParseEslint reads `eslint -f json`: one entry per file, `severity` 1 =
// warning, 2 = error.
func ParseEslint(stdout, repoPath string) []Problem {
	out := []Problem{}
	raw, ok := jsonSlice(stdout)
	if !ok {
		return out
	}
	for _, entry := range raw {
		f, ok := entry.(map[string]any)
		if !ok || f == nil {
			continue
		}
		filePath, isStr := f["filePath"].(string)
		messages, isArr := f["messages"].([]any)
		if !isStr || !isArr {
			continue
		}
		for _, msg := range messages {
			m, ok := msg.(map[string]any)
			if !ok || m == nil {
				continue
			}
			message, ok := m["message"].(string)
			if !ok {
				continue
			}
			sev := "warning"
			if n, isNum := m["severity"].(float64); isNum && n == 2 {
				sev = "error"
			}
			code, _ := m["ruleId"].(string)
			out = append(out, Problem{
				File: rel(repoPath, filePath), Line: numOr(m["line"], 1), Col: numOr(m["column"], 1),
				Severity: sev, Message: message, Code: code, Source: "eslint",
			})
		}
	}
	return out
}

var reLineDiag = shared.MustJSRegexp(`^(.+?):(\d+):(\d+):\s*(?:(error|warning|note|help)(?:\[([^\]]+)\])?:\s*)?(.+)$`, false)

// ParseLineDiagnostics reads the `file:line:col: level: msg` shape of `cargo
// clippy --message-format=short` and `go vet`. Both write to stderr, and
// neither marks the level the same way, so the level is read when present and
// assumed a warning when it is not.
func ParseLineDiagnostics(text, repoPath, source string) []Problem {
	out := []Problem{}
	for _, line := range shared.SplitNL(text) {
		m := reLineDiag.FindStringSubmatch(gitx.TrimJS(line))
		if m == nil {
			continue
		}
		level := m[4]
		// `note:` / `help:` lines are continuations of the diagnostic above them.
		if level == "note" || level == "help" {
			continue
		}
		sev := "warning"
		if level == "error" {
			sev = "error"
		}
		out = append(out, Problem{
			File: rel(repoPath, m[1]), Line: jsNumber(m[2]), Col: jsNumber(m[3]),
			Severity: sev, Message: gitx.TrimJS(m[6]), Code: m[5], Source: source,
		})
	}
	return out
}

// ParseRuff reads `ruff check --output-format=json`.
func ParseRuff(stdout, repoPath string) []Problem {
	out := []Problem{}
	raw, ok := jsonSlice(stdout)
	if !ok {
		return out
	}
	for _, entry := range raw {
		e, ok := entry.(map[string]any)
		if !ok || e == nil {
			continue
		}
		loc, _ := e["location"].(map[string]any)
		filename, isStr := e["filename"].(string)
		message, isMsg := e["message"].(string)
		if !isStr || !isMsg {
			continue
		}
		code, _ := e["code"].(string)
		out = append(out, Problem{
			File: rel(repoPath, filename), Line: numOr(loc["row"], 1), Col: numOr(loc["column"], 1),
			// Ruff reports lint findings, not compile errors.
			Severity: "warning", Message: message, Code: code, Source: "ruff",
		})
	}
	return out
}

var severityRank = map[string]int{"error": 0, "warning": 1, "info": 2}

// NormaliseProblems deduplicates (two tools can report the same line) and
// sorts for display.
func NormaliseProblems(problems []Problem) []Problem {
	seen := map[string]bool{}
	out := []Problem{}
	for _, p := range problems {
		key := p.File + ":" + fmtNum(p.Line) + ":" + fmtNum(p.Col) + ":" + p.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if c := shared.LocaleCompare(a.File, b.File); c != 0 {
			return c < 0
		}
		if ra, rb := severityRank[a.Severity], severityRank[b.Severity]; ra != rb {
			return ra < rb
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
