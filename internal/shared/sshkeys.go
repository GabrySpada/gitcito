package shared

import (
	"sort"
	"strconv"
	"strings"
)

// Reading what `ssh-keygen` and `ssh-add` print — port of src/shared/sshKeys.ts
// (testdata/vectors/sshKeys.json holds both to the same answers). Pure string
// work, kept out of the ssh package so it can be tested without an agent or a
// `~/.ssh` to poke at — the parsing is the part that breaks between OpenSSH
// versions.
//
// Nothing here ever handles a private key. Fingerprints and public halves only.

// SshKeyType is the TS SshKeyType union: ed25519, rsa, ecdsa, dsa, ed25519-sk,
// ecdsa-sk or unknown.
type SshKeyType = string

// SshKey is one public key found in the ssh directory.
type SshKey struct {
	// File is the file name of the public key, e.g. `id_ed25519.pub`.
	File string `json:"file"`
	// Path is the absolute path of the public key.
	Path string     `json:"path"`
	Type SshKeyType `json:"type"`
	Bits int        `json:"bits"`
	// Fingerprint is `SHA256:…`, the form `ssh-add -l` prints too.
	Fingerprint string `json:"fingerprint"`
	// Comment is the trailing comment, usually an email or machine name. Often empty.
	Comment string `json:"comment"`
	// PublicKey is the whole public key line, which is what a host wants pasted.
	PublicKey string `json:"publicKey"`
	// InAgent is true when the agent is holding the matching private key.
	InAgent bool `json:"inAgent"`
	// HasPrivate is true when the matching private key file sits next to it.
	HasPrivate bool `json:"hasPrivate"`
}

// SshTestResult is what `ssh -T git@<host>` meant: ok, denied, unreachable or
// unknown. OpenSSH says "successfully authenticated" and then exits 1 for
// GitHub, which looks like a failure and is not — so the exit code alone
// cannot be trusted.
type SshTestResult = string

// SshTest is a connection test's verdict.
type SshTest struct {
	Result SshTestResult `json:"result"`
	// Output is the raw ssh output, shown verbatim under the verdict — it is often specific.
	Output string `json:"output"`
}

// SshStatus is what the SSH section of Settings renders.
type SshStatus struct {
	// Dir is the absolute path of ~/.ssh, shown so the user knows where these live.
	Dir  string   `json:"dir"`
	Keys []SshKey `json:"keys"`
	// AgentRunning is false when no agent is reachable at all (SSH_AUTH_SOCK unset, or dead).
	AgentRunning bool `json:"agentRunning"`
}

// KeygenLine is one parsed `ssh-keygen -l` / `ssh-add -l` line.
type KeygenLine struct {
	Bits        int        `json:"bits"`
	Fingerprint string     `json:"fingerprint"`
	Comment     string     `json:"comment"`
	Type        SshKeyType `json:"type"`
}

// notAKey are the files in ~/.ssh that are configuration, not keys.
var notAKey = map[string]bool{
	"config": true, "known_hosts": true, "known_hosts.old": true, "authorized_keys": true, "environment": true, "rc": true,
}

// PublicKeyFiles picks the public-key files out of a `~/.ssh` listing, in a
// stable order.
func PublicKeyFiles(entries []string) []string {
	out := []string{}
	for _, name := range entries {
		if strings.HasSuffix(name, ".pub") && !notAKey[name] {
			out = append(out, name)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return LocaleCompare(out[i], out[j]) < 0 })
	return out
}

var (
	typeParensRe = MustJSRegexp(`^\(|\)$`, false)
	knownTypes   = []SshKeyType{"ed25519-sk", "ecdsa-sk", "ed25519", "rsa", "ecdsa", "dsa"}
	keygenLineRe = MustJSRegexp(`^(\d+)\s+(\S+)\s*(.*?)\s*(?:\(([^)]+)\))?$`, false)
	fingerprRe   = MustJSRegexp(`^(SHA256:|MD5:|[0-9a-f]{2}:)`, true)
)

func normalizeType(raw string) SshKeyType {
	value := jsLower(typeParensRe.ReplaceAllString(raw, ""))
	for _, k := range knownTypes {
		if value == k {
			return k
		}
	}
	return "unknown"
}

// ParseKeygenLine reads one line of `ssh-keygen -lf <file>` or `ssh-add -l`:
//
//	256 SHA256:abc123 you@example.com (ED25519)
//
// The comment is the middle, and may contain spaces — or be missing entirely,
// which older OpenSSH prints as `no comment`.
func ParseKeygenLine(line string) *KeygenLine {
	trimmed := JSTrim(line)
	if trimmed == "" {
		return nil
	}
	m := keygenLineRe.FindStringSubmatch(trimmed)
	if m == nil {
		return nil
	}
	bits, fingerprint, rawComment, rawType := m[1], m[2], m[3], m[4]
	if !fingerprRe.MatchString(fingerprint) {
		return nil
	}
	comment := rawComment
	if rawComment == "no comment" {
		comment = ""
	}
	return &KeygenLine{Bits: jsNumberInt(bits), Fingerprint: fingerprint, Comment: comment, Type: normalizeType(rawType)}
}

// jsNumberInt is Number(digits) for a run of ASCII digits, saturating where
// Number would leave the safe-integer range (no key has that many bits).
func jsNumberInt(digits string) int {
	f, err := strconv.ParseFloat(digits, 64)
	if err != nil || f > 1<<53 {
		return 1 << 53
	}
	return int(f)
}

// AgentFingerprints lists the fingerprints the agent is holding, once each, in
// the order `ssh-add -l` printed them (a JS Set). `ssh-add -l` exits non-zero
// with a human sentence when the agent is empty or unreachable, so anything
// that does not parse as a key line is simply not a fingerprint.
func AgentFingerprints(stdout string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(stdout, "\n") {
		if parsed := ParseKeygenLine(line); parsed != nil && !seen[parsed.Fingerprint] {
			seen[parsed.Fingerprint] = true
			out = append(out, parsed.Fingerprint)
		}
	}
	return out
}

// PrivateKeyName is the private key file a public key belongs to:
// `id_ed25519.pub` → `id_ed25519`.
func PrivateKeyName(publicFile string) string {
	return strings.TrimSuffix(publicFile, ".pub")
}

var (
	sshOkRe          = MustJSRegexp(`successfully authenticated|welcome to gitlab|logged in as|you can use git`, false)
	sshDeniedRe      = MustJSRegexp(`permission denied|publickey|authentication failed|access denied`, false)
	sshUnreachableRe = MustJSRegexp(`could not resolve|connection timed out|connection refused|network is unreachable|no route to host`, false)
)

// ReadSshTest reads the verdict out of `ssh -T` output.
func ReadSshTest(output string) SshTestResult {
	text := jsLower(output)
	// GitHub: "Hi user! You've successfully authenticated, but GitHub does not
	// provide shell access." GitLab: "Welcome to GitLab, @user!"
	if sshOkRe.MatchString(text) {
		return "ok"
	}
	if sshDeniedRe.MatchString(text) {
		return "denied"
	}
	if sshUnreachableRe.MatchString(text) {
		return "unreachable"
	}
	return "unknown"
}

// jsLower is toLowerCase() where it differs from strings.ToLower on text that
// can reach a pattern: JS lowers U+0130 to "i" plus a combining dot.
func jsLower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307"))
}
