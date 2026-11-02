package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// HasBinary reports whether command is on PATH (or, for a path, executable).
// Used to say which diff converters and credential helpers are real here.
// TS spawned `which`/`where`; exec.LookPath answers the same question without
// a process.
func HasBinary(command string) bool {
	if command == "" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil
}

var converterRe = regexp.MustCompile(`^"([^"]+)"|^([^` + jsSpaceClass + `]+)`)

// ConverterAvailable reports whether a configured textconv command can
// actually run: a path (quoted or not) is checked on disk, a bare command on PATH.
func ConverterAvailable(textconv string) bool {
	m := converterRe.FindStringSubmatch(TrimJS(textconv))
	cmd := ""
	if m != nil {
		cmd = m[1]
		if cmd == "" {
			cmd = m[2]
		}
	}
	if cmd == "" {
		return false
	}
	if strings.ContainsAny(cmd, `/\`) {
		return CanExecute(cmd)
	}
	return HasBinary(cmd)
}

var quoteEdgesRe = regexp.MustCompile(`^"|"$`)

// CredentialHelperExists reports whether a configured credential helper
// actually exists.
//
// git resolves a bare name to `git-credential-<name>`, which lives beside git
// rather than on PATH — hence the exec-path probe. A `!`-prefixed value is a
// shell command git runs verbatim, so only its first word can be checked.
func CredentialHelperExists(ctx context.Context, value string) bool {
	raw := TrimJS(value)
	if raw == "" {
		return false
	}
	if strings.HasPrefix(raw, "!") {
		first := firstWord(TrimJS(raw[1:]))
		return HasBinary(quoteEdgesRe.ReplaceAllString(first, ""))
	}
	name := firstWord(raw)
	// Absolute path, or a command of its own.
	if strings.ContainsAny(name, `/\`) {
		if _, err := os.Stat(name); err == nil {
			return true
		}
		return HasBinary(name)
	}
	if name == "cache" || name == "store" {
		return true // built into git itself
	}
	if HasBinary("git-credential-" + name) {
		return true
	}
	res, _ := Exec(ctx, "", "git", []string{"--exec-path"}, RunOpts{PlainEnv: true, MaxBuffer: -1})
	execPath := TrimJS(res.Stdout)
	if execPath == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(execPath, "git-credential-"+name))
	return err == nil
}

func firstWord(s string) string {
	return whitespaceRunRe.Split(s, 2)[0]
}

// CredentialCandidate is CredentialCandidate in src/shared/types.ts.
type CredentialCandidate struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	// Recommended is true for the one that fits this OS best.
	Recommended bool `json:"recommended"`
}

// CredentialCandidates lists the helpers worth offering on this platform, and
// whether they are installed.
func CredentialCandidates(ctx context.Context) []CredentialCandidate {
	var names []string
	var best string
	switch runtime.GOOS {
	case "darwin":
		names, best = []string{"osxkeychain", "cache", "store"}, "osxkeychain"
	case "windows":
		names, best = []string{"manager", "wincred", "cache", "store"}, "manager"
	default:
		names, best = []string{"libsecret", "cache", "store"}, "libsecret"
	}
	out := make([]CredentialCandidate, 0, len(names))
	for _, n := range names {
		out = append(out, CredentialCandidate{Name: n, Available: CredentialHelperExists(ctx, n), Recommended: n == best})
	}
	return out
}
