// Package todoscan ports src/main/todoScan.ts: every `// TODO`, `# FIXME` or
// `HACK` the repository carries, read straight out of the source.
//
// The search is `git grep`, not a directory walk: it already knows which files
// are tracked, which are ignored and which are binary, which is the whole
// difference between a scan that finishes and one that reads `node_modules`.
//
// Two passes on purpose. `git grep` runs a deliberately loose pattern — cheap
// for C, wrong often enough to matter — and every candidate line is then parsed
// here, where the comment leader, the owner and the message can be picked
// apart properly. Grep decides what to read; ParseTodoLine decides what counts.
package todoscan

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/shared"
)

const (
	// maxTodos: past this the panel is wallpaper and the renderer starts to hurt.
	maxTodos    = 5000
	grepTimeout = 60 * time.Second
	// maxMessage: a long marker is a paragraph someone pasted; the tooltip
	// still has all of it.
	maxMessage = 300
)

// CodeTodo is one TODO-style marker found in the repository's own source
// (src/shared/types.ts CodeTodo).
type CodeTodo struct {
	// File is repo-relative, forward slashes.
	File string `json:"file"`
	Line int    `json:"line"`
	// Col is the 1-based column (UTF-16) of the tag itself.
	Col int `json:"col"`
	// Tag is the normalised upper-case tag.
	Tag string `json:"tag"`
	// Owner from `TODO(cgm)`, `TODO (cgm)` or `TODO: @cgm`, lower-cased; empty
	// (absent on the wire) means unassigned.
	Owner string `json:"owner,omitempty"`
	// Message is the note itself, with the tag, the owner and any separator removed.
	Message string `json:"message"`
	// Text is the whole source line, trimmed.
	Text string `json:"text"`
}

// TodoScanResult is the todoscan:run result.
type TodoScanResult struct {
	Todos     []CodeTodo `json:"todos"`
	Truncated bool       `json:"truncated"`
	Ms        int64      `json:"ms"`
}

// TodoTags are the tags a marker can carry. Longest-first when they share a
// prefix, so the alternation cannot match `FIX` inside `FIXME` and leave `ME`
// as the message.
var TodoTags = []string{
	"FIXME", "TODO", "BUG", "HACK", "XXX", "NOTE", "OPTIMIZE", "REVIEW", "REFACTOR",
	"DEPRECATED", "QUESTION", "IDEA", "WIP", "TEMP",
}

var tagsAlt = func() string {
	tags := append([]string(nil), TodoTags...)
	sort.SliceStable(tags, func(i, j int) bool { return len(tags[i]) > len(tags[j]) })
	return strings.Join(tags, "|")
}()

// caseless spells an ASCII alternation case-insensitively with explicit
// classes. JS's `i` flag (without `u`) folds ASCII only; Go's (?i) would also
// let the Kelvin sign match K and the long s match S.
func caseless(alt string) string {
	var b strings.Builder
	for _, r := range alt {
		if r >= 'A' && r <= 'Z' {
			b.WriteString("[" + string(r) + string(r+'a'-'A') + "]")
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var (
	// leader is what has to sit in front of the tag for it to be a comment
	// rather than a word.
	//
	// Every line comment leader in wide use, plus `*` for the continuation lines
	// of a block comment, and the decoration people put after them (`//!`,
	// `# ---`).
	//
	// A leader is *required*. Without one, `todo = [l for l in lines if ...]`
	// reads as a marker, and a panel that lists a variable assignment as
	// technical debt is a panel nobody trusts twice. A TODO is something
	// written in a comment.
	leader = regexp.MustCompile(`(?:\/\/|\/\*|\*|#|<!--|--|;|%|"""|''')[ \t*!\-=>]*$`)
	// owner is a handle, not a sentence: `(cgm)` yes, `(see the issue)` no.
	owner  = regexp.MustCompile(`^[A-Za-z0-9_.@+-]{1,32}$`)
	tagAt  = regexp.MustCompile(`\b(` + caseless(tagsAlt) + `)\b`)
	marker = shared.MustJSRegexp(
		// tag, then an optional `(owner)` / `[owner]`, then an optional separator.
		`^(`+caseless(tagsAlt)+`)\b[ \t]*(?:\(([^)]*)\)|\[([^\]]*)\])?[ \t]*[:：\-–—]?[ \t]*(.*)$`, false)
	leadingSep = shared.MustJSRegexp(`^[:：\-–—]\s*`, false)
	atOwner    = regexp.MustCompile(`^@([A-Za-z0-9_.+-]{1,32})\b[ \t]*[:：\-–—]?[ \t]*`)
	closer     = shared.MustJSRegexp(`\s*(?:\*\/|-->|"""|''')\s*$`, false)
	grepLine   = shared.MustJSRegexp(`^(.+?):(\d+):(.*)$`, false)
)

// ParseTodoLine parses one source line into a marker, or nil when it holds none.
//
// `TODO`, `todo`, `//TODO`, `// TODO:` and a block-comment `TODO(cgm):` are the
// same thing said five ways, and they all land in the same bucket: the tag is
// upper-cased, the owner lower-cased, the separator thrown away.
func ParseTodoLine(file string, line int, text string) *CodeTodo {
	for _, hit := range tagAt.FindAllStringSubmatchIndex(text, -1) {
		at := hit[0]
		// A comment has to have opened before the tag. Code that merely uses the
		// word — a variable called `todo`, a function called `reviewNotes` — is no
		// marker, however it is indented.
		if !leader.MatchString(text[:at]) {
			continue
		}
		m := marker.FindStringSubmatch(text[at:])
		if m == nil {
			continue
		}
		tag, paren, bracket, rest := m[1], m[2], m[3], m[4]
		own := paren
		if own == "" {
			own = bracket
		}
		own = gitx.TrimJS(own)
		message := gitx.TrimJS(rest)
		// `(cgm)` is an owner; `(see below)` is the start of the sentence, and
		// swallowing it would lose the words and invent a group nobody wrote.
		if own != "" && !owner.MatchString(own) {
			message = leadingSep.ReplaceAllLiteralString(gitx.TrimJS(text[at+len(tag):]), "")
			own = ""
		}
		// `TODO: @cgm ship this` — the same claim, written the other common way.
		if own == "" {
			if a := atOwner.FindStringSubmatch(message); a != nil {
				own = a[1]
				message = message[len(a[0]):]
			}
		}
		// A block comment's closer is punctuation, not part of the note.
		message = gitx.TrimJS(closer.ReplaceAllLiteralString(message, ""))
		todo := &CodeTodo{
			File:    file,
			Line:    line,
			Col:     gitx.UTF16Len(text[:at]) + 1,
			Tag:     strings.ToUpper(tag),
			Message: gitx.SliceUTF16(message, maxMessage),
			Text:    gitx.SliceUTF16(gitx.TrimJS(text), maxMessage),
		}
		if own != "" {
			todo.Owner = strings.ToLower(strings.TrimPrefix(own, "@"))
		}
		return todo
	}
	return nil
}

// GrepPattern is the grep pattern: a comment leader followed by a tag.
//
// POSIX ERE, because `git grep -P` needs a PCRE build nobody can count on. It
// is looser than ParseTodoLine by design — grep is here to skip the 99% of the
// repository that mentions no tag at all, not to be right.
func GrepPattern() string {
	return `(//|/\*|\*|#|<!--|--|;|%|"""|''')[ ` + "\t" + `*!-]*(` + tagsAlt + `)`
}

// ParseGrepOutput reads `path:line:text` lines (non-greedy, so a path holding
// a colon still parses).
func ParseGrepOutput(stdout string) []CodeTodo {
	out := []CodeTodo{}
	for _, raw := range shared.SplitNL(stdout) {
		if raw == "" {
			continue
		}
		m := grepLine.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		file := strings.ReplaceAll(m[1], `\`, "/")
		// A dependency tree's TODOs are somebody else's, tracked or not.
		deps := false
		for _, seg := range strings.Split(file, "/") {
			if seg == "node_modules" {
				deps = true
				break
			}
		}
		if deps {
			continue
		}
		if todo := ParseTodoLine(file, atoi(m[2]), m[3]); todo != nil {
			out = append(out, *todo)
		}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// SortTodos orders by file, then line — the order the panel groups in,
// whatever it groups by.
func SortTodos(todos []CodeTodo) []CodeTodo {
	out := append([]CodeTodo{}, todos...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File == b.File {
			return a.Line < b.Line
		}
		return shared.CompareUTF16(a.File, b.File) < 0
	})
	return out
}

// Scanner owns the running scans, so a second click cancels rather than piling up.
type Scanner struct {
	mu      sync.Mutex
	running map[string]*scan
}

type scan struct{ cancel context.CancelFunc }

// NewScanner builds an idle Scanner.
func NewScanner() *Scanner { return &Scanner{running: map[string]*scan{}} }

// Scan runs the grep over repoPath and parses what it found.
func (s *Scanner) Scan(ctx context.Context, repoPath string) TodoScanResult {
	started := time.Now()
	s.Cancel(repoPath)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	me := &scan{cancel: cancel}
	s.mu.Lock()
	s.running[repoPath] = me
	s.mu.Unlock()

	res, err := gitx.Exec(runCtx, repoPath, "git", []string{
		"grep", "-n", "-I", "-E", "-i", "--no-color",
		// Untracked files count — a TODO written five minutes ago is the one
		// that matters most — but anything `.gitignore`d is build output.
		"--untracked", "--exclude-standard",
		"-e", GrepPattern(),
	}, gitx.RunOpts{PlainEnv: true, Timeout: grepTimeout, MaxBuffer: 64 * 1024 * 1024})
	stdout := strings.ToValidUTF8(res.Stdout, "�")
	// Exit 1 is "no matches", which is a result, not a failure. Anything else
	// (no git, not a repo) leaves us with nothing to say.
	if err != nil {
		if code, ok := gitx.ExitCode(err); !ok || (code != 0 && code != 1) {
			stdout = ""
		}
	}
	s.mu.Lock()
	if s.running[repoPath] == me {
		delete(s.running, repoPath)
	}
	s.mu.Unlock()

	todos := SortTodos(ParseGrepOutput(stdout))
	truncated := len(todos) > maxTodos
	if truncated {
		todos = todos[:maxTodos]
	}
	return TodoScanResult{Todos: todos, Truncated: truncated, Ms: time.Since(started).Milliseconds()}
}

// Cancel kills the repo's running scan.
func (s *Scanner) Cancel(repoPath string) {
	s.mu.Lock()
	sc := s.running[repoPath]
	delete(s.running, repoPath)
	s.mu.Unlock()
	if sc != nil {
		sc.cancel()
	}
}

// CancelAll stops every scan (app shutdown).
func (s *Scanner) CancelAll() {
	s.mu.Lock()
	all := s.running
	s.running = map[string]*scan{}
	s.mu.Unlock()
	for _, sc := range all {
		sc.cancel()
	}
}
