package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/GabrySpada/gitcito/internal/repoconfig"
)

// suggestedConfig is a proposed config in the key order suggestRepoConfig
// built it in (version, protect, requires, commit) — the order JSON.stringify
// wrote to the file. repoconfig.Serialize follows the validator's order
// instead, so `config init` serializes through this.
type suggestedConfig struct {
	Version   float64                         `json:"version"`
	Protect   []string                        `json:"protect,omitempty"`
	Requires  *repoconfig.RepoConfigRequires  `json:"requires,omitempty"`
	Commit    *repoconfig.RepoConfigCommit    `json:"commit,omitempty"`
	Links     *repoconfig.RepoConfigLinks     `json:"links,omitempty"`
	Checklist *repoconfig.RepoConfigChecklist `json:"checklist,omitempty"`
}

// serializeSuggested is serializeRepoConfig(suggested): JSON.stringify with
// two spaces and a trailing newline.
func serializeSuggested(c *repoconfig.RepoConfig) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(suggestedConfig{
		Version: c.Version, Protect: c.Protect, Requires: c.Requires, Commit: c.Commit, Links: c.Links, Checklist: c.Checklist,
	})
	if err != nil {
		return "", err
	}
	return unescapeLineSeparators(buf.String()), nil
}

// unescapeLineSeparators turns encoding/json's U+2028 / U+2029 escapes back
// into the raw characters JSON.stringify writes. A string value cannot hold a
// literal backslash-u sequence (it would be encoded as `\\u2028`), so an odd
// backslash run is always the encoder's own escape.
func unescapeLineSeparators(s string) string {
	if !strings.Contains(s, `\u202`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		run := 0
		for j := i; j < len(s) && s[j] == '\\'; j++ {
			run++
		}
		b.WriteString(s[i : i+run-1])
		i += run - 1
		if run%2 == 1 && i+5 < len(s) && (s[i+1:i+6] == "u2028" || s[i+1:i+6] == "u2029") {
			if s[i+5] == '8' {
				b.WriteString(" ")
			} else {
				b.WriteString(" ")
			}
			i += 5
			continue
		}
		b.WriteByte('\\')
	}
	return b.String()
}
