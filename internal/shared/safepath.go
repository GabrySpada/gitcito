// Package shared holds Go ports of src/shared and small cross-domain rules that
// several services need. Each function mirrors a TypeScript original; vector
// files under testdata/vectors keep the two from drifting apart.
package shared

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

var driveLetter = regexp.MustCompile(`^[a-zA-Z]:`)

// IsSafeRepoPath reports whether a model- or CLI-supplied path stays inside the
// repo. Paths come back from the LLM and are joined onto the repo root before
// writing, so `..` segments, absolute paths and Windows drive letters have to be
// refused. Mirrors isSafeRepoPath in src/main/aiSchemas.ts.
func IsSafeRepoPath(path string) bool {
	p := strings.TrimSpace(path)
	// JS measures length in UTF-16 code units; count the same way so a path the
	// TS side accepted is not refused here (or the reverse).
	if p == "" || len(utf16.Encode([]rune(p))) > 400 {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || driveLetter.MatchString(p) {
		return false
	}
	if strings.Contains(p, "\x00") {
		return false
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}
