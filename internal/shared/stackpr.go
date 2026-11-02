package shared

import "strings"

// Go port of the parts of src/shared/stackPr.ts the main process uses. Vector
// tested against testdata/vectors/stackPr.json.

// The markers around the stack-navigation section Gitcito maintains in a PR body.
const (
	StackSectionStart = "<!-- gitcito-stack -->"
	StackSectionEnd   = "<!-- /gitcito-stack -->"
)

// MergeStackSection inserts or replaces the marked stack section in a PR body,
// preserving the rest.
func MergeStackSection(body, section string) string {
	start := strings.Index(body, StackSectionStart)
	end := strings.Index(body, StackSectionEnd)
	if start != -1 && end != -1 && end > start {
		return body[:start] + section + body[end+len(StackSectionEnd):]
	}
	// trimEnd: the same whitespace set as String.prototype.trimEnd.
	trimmed := strings.TrimRightFunc(body, jsIsSpace)
	if trimmed != "" {
		return trimmed + "\n\n" + section
	}
	return section
}
