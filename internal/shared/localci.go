package shared

import "regexp"

// Pure helpers for the local-CI (act) integration — src/shared/localCi.ts.

var (
	workflowNameLine = MustJSRegexp(`^name:\s*(.+?)\s*$`, false)
	workflowQuotes   = regexp.MustCompile(`^['"]|['"]$`)
	workflowBodyLine = regexp.MustCompile(`^(on|jobs):`)
)

// ParseWorkflowName is the top-level `name:` of a workflow YAML, without a
// YAML parser: the first unindented `name:` line wins. Good enough for a
// picker label — a wrong guess still runs the right file. ok is false when the
// document has no name (TS: null).
func ParseWorkflowName(text string) (name string, ok bool) {
	for _, line := range SplitNL(text) {
		if m := workflowNameLine.FindStringSubmatch(line); m != nil {
			return workflowQuotes.ReplaceAllString(m[1], ""), true
		}
		// Stop scanning once real content starts — `on:`/`jobs:` mean the top of
		// the document has passed without a name.
		if workflowBodyLine.MatchString(line) {
			break
		}
	}
	return "", false
}

// SplitNL is `text.split('\n')`: always at least one element.
func SplitNL(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
