package shared

// `gitcito commit-check` — the same conventions the composer nudges about,
// evaluated on a whole commit-message file so a `commit-msg` hook or a CI job
// can fail on them. Mirrors src/shared/commitCheck.ts.
//
// Deliberately separate from renderer/src/lib/commitLint.ts, which returns
// dictionary keys for a UI in seventeen languages. A hook prints to a terminal:
// the text is English, the same way git's own is, and the value that matters is
// the exit code.
//
// What raises the level from `warn` to `error` is always something the
// repository itself asked for in `.gitcito.json` — an unknown scope is an error
// only because a scope list exists. Style alone never fails a build.

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// CommitCheckIssue mirrors CommitCheckIssue in src/shared/commitCheck.ts.
type CommitCheckIssue struct {
	// Level is 'error' | 'warn'.
	Level string `json:"level"`
	// Code is a stable identifier, so a caller can filter without matching prose.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CommitCheckConfig is the slice of RepoConfig the check reads: commit.scopes,
// commit.trailers and commit.ticketFromBranch. Kept local so shared does not
// import the repoconfig package.
type CommitCheckConfig struct {
	Scopes           []string
	Trailers         []string
	TicketFromBranch bool
}

// CommitCheckOptions mirrors CommitCheckOptions.
type CommitCheckOptions struct {
	// Config is the repository's own rules, when it ships a `.gitcito.json`.
	Config *CommitCheckConfig
	// Branch is the current branch, used to look for the ticket key it carries.
	Branch string
}

const (
	subjectIdeal = 50
	subjectMax   = 72
	bodyWrap     = 72
)

// JS `.` stops at every line terminator; RE2's only at \n. A subject split on
// \n can still carry a \r (CRLF messages), which must stop the match the way
// it does in the TypeScript.
const jsDot = `[^\n\r\x{2028}\x{2029}]`

// jsSpace is JavaScript's \s: Unicode whitespace plus the line terminators.
const jsSpace = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var (
	scissorsRe     = regexp.MustCompile(`^#` + jsSpace + `*-+` + jsSpace + `*>8` + jsSpace + `*-+`)
	conventionalRe = regexp.MustCompile(`^(\w+)(\(([^)]*)\))?!?:` + jsSpace + `?(` + jsDot + `*)$`)
	urlLineRe      = regexp.MustCompile(`^` + jsSpace + `*[^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+:\/\/`)
	// TicketRe matches ticket keys as they appear in a subject or a trailer: `ABC-123`.
	TicketRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)
)

// utf16Len is String.prototype.length.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 && r <= utf8.MaxRune {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// StripCommitComments strips what git itself strips: comment lines, and
// everything from a `# ------------------------ >8 ------------------------`
// scissors line on.
func StripCommitComments(raw string) string {
	lines := []string{}
	for _, line := range strings.Split(raw, "\n") {
		if scissorsRe.MatchString(line) {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return JSTrim(strings.Join(lines, "\n"))
}

// CheckCommitMessage checks a full commit-message file. It returns every issue
// found; the caller decides what to do with the levels (the CLI exits 1 on any
// `error`). Mirrors checkCommitMessage.
func CheckCommitMessage(raw string, opts CommitCheckOptions) []CommitCheckIssue {
	issues := []CommitCheckIssue{}
	text := StripCommitComments(raw)
	if text == "" {
		return []CommitCheckIssue{{Level: "error", Code: "empty", Message: "The commit message is empty."}}
	}

	lines := strings.Split(text, "\n")
	subject := JSTrim(lines[0])
	body := strings.Join(lines[1:], "\n")

	if n := utf16Len(subject); n > subjectMax {
		issues = append(issues, CommitCheckIssue{Level: "error", Code: "subject-too-long",
			Message: fmt.Sprintf("Subject is %d characters; keep it under %d.", n, subjectMax)})
	} else if n > subjectIdeal {
		issues = append(issues, CommitCheckIssue{Level: "warn", Code: "subject-long",
			Message: fmt.Sprintf("Subject is %d characters; %d or fewer reads better in a log.", n, subjectIdeal)})
	}

	if strings.HasSuffix(subject, ".") {
		issues = append(issues, CommitCheckIssue{Level: "warn", Code: "trailing-period", Message: "Subject ends with a period."})
	}

	if len(lines) > 1 && JSTrim(lines[1]) != "" {
		issues = append(issues, CommitCheckIssue{Level: "error", Code: "no-blank-line",
			Message: "A blank line must separate the subject from the body."})
	}

	for _, l := range strings.Split(body, "\n") {
		if utf16Len(l) > bodyWrap && !urlLineRe.MatchString(l) {
			issues = append(issues, CommitCheckIssue{Level: "warn", Code: "body-wrap",
				Message: fmt.Sprintf("Wrap the body at %d columns.", bodyWrap)})
			break
		}
	}

	cc := conventionalRe.FindStringSubmatchIndex(subject)
	var scopes []string
	if opts.Config != nil {
		scopes = opts.Config.Scopes
	}
	// A scope list is the repository stating it uses Conventional Commits, so a
	// subject that is not one stops being a style preference and becomes a break
	// of the stated rule.
	if len(scopes) > 0 && cc == nil {
		issues = append(issues, CommitCheckIssue{Level: "error", Code: "not-conventional",
			Message: "Subject must be `type(scope): summary` — this repository declares commit scopes."})
	}

	scope := ""
	if cc != nil && cc[6] >= 0 {
		scope = JSTrim(subject[cc[6]:cc[7]])
	}
	if len(scopes) > 0 && scope != "" && !containsString(scopes, scope) {
		issues = append(issues, CommitCheckIssue{Level: "error", Code: "unknown-scope",
			Message: fmt.Sprintf("Unknown scope \"%s\". Allowed: %s.", scope, strings.Join(scopes, ", "))})
	}

	if opts.Config != nil {
		bodyLines := strings.Split(body, "\n")
		for _, trailer := range opts.Config.Trailers {
			name := JSTrim(strings.Split(trailer, ":")[0])
			if name == "" {
				continue
			}
			prefix := strings.ToLower(name) + ":"
			present := false
			for _, l := range bodyLines {
				if strings.HasPrefix(strings.ToLower(JSTrim(l)), prefix) {
					present = true
					break
				}
			}
			if !present {
				issues = append(issues, CommitCheckIssue{Level: "warn", Code: "missing-trailer",
					Message: fmt.Sprintf("Missing trailer \"%s:\".", name)})
			}
		}

		if opts.Config.TicketFromBranch {
			fromBranch := ""
			if opts.Branch != "" {
				fromBranch = TicketRe.FindString(opts.Branch)
			}
			if fromBranch != "" && !strings.Contains(text, fromBranch) {
				issues = append(issues, CommitCheckIssue{Level: "warn", Code: "missing-ticket",
					Message: fmt.Sprintf("Branch mentions %s; the message does not.", fromBranch)})
			}
		}
	}

	return issues
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
