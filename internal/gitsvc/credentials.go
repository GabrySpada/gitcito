package gitsvc

// Credential helpers.
//
// Git has its own credential store, and it is neither Gitcito's keychain nor
// your SSH agent. Misconfiguring it is the usual reason a push asks for a
// password every single time — or, worse, the reason a password is sitting in
// a plain file. Nothing here ever reads a secret: `git credential fill` would
// print one to stdout, so it is not used at all.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// CredentialHelperInfo is CredentialHelperInfo in src/shared/types.ts.
type CredentialHelperInfo struct {
	// Value as git stores it: `osxkeychain`, `store`, `!gh auth git-credential`…
	Value string `json:"value"`
	// Scope: 'system' | 'global' | 'repo'.
	Scope string `json:"scope"`
	// Available is true when the helper program exists — a typo here is
	// invisible until a push fails.
	Available bool `json:"available"`
	// Plaintext marks the `store` helper: passwords in a plain file.
	Plaintext bool `json:"plaintext"`
}

// CredentialURLRule is CredentialUrlRule in src/shared/types.ts: a
// `credential.<url>.*` section, which beats the plain setting for its URLs.
type CredentialURLRule struct {
	URL      string `json:"url"`
	Helper   string `json:"helper"`
	Username string `json:"username"`
	Scope    string `json:"scope"`
}

// PlaintextCredentialsFile describes `~/.git-credentials`: counted, never read
// out — every line of it is a live password.
type PlaintextCredentialsFile struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Entries int    `json:"entries"`
}

// CredentialStatus is CredentialStatus in src/shared/types.ts.
type CredentialStatus struct {
	Helpers       []CredentialHelperInfo     `json:"helpers"`
	URLRules      []CredentialURLRule        `json:"urlRules"`
	Candidates    []gitx.CredentialCandidate `json:"candidates"`
	PlaintextFile PlaintextCredentialsFile   `json:"plaintextFile"`
	// HTTPSHosts are the hosts this repository would ask about: its https remotes.
	HTTPSHosts []string `json:"httpsHosts"`
}

var (
	dCredentialKeyRe = regexp.MustCompile(`^credential\.(.*)\.(helper|username)$`)
	dHTTPSHostRe     = regexp.MustCompile(`^https?://(?:[^@/]*@)?([^/]+)`)
)

var dCredentialScopes = []string{"system", "global", "repo"}

func dScopeFlag(scope string) string {
	if scope == "repo" {
		return "--local"
	}
	return "--" + scope
}

// CredentialStatus says how git will answer the next password prompt, and
// what it would use.
//
// gitsvc: credentialStatus
func (s *GitService) CredentialStatus(ctx context.Context, repoPath string) (CredentialStatus, error) {
	return locked(s, ctx, "credentialStatus", repoPath, func(ctx context.Context) (CredentialStatus, error) {
		st := CredentialStatus{Helpers: []CredentialHelperInfo{}, URLRules: []CredentialURLRule{}, HTTPSHosts: []string{}}
		for _, scope := range dCredentialScopes {
			out, err := gitx.RunGit(ctx, repoPath, []string{"config", dScopeFlag(scope), "--get-all", "credential.helper"}, gitx.RunOpts{})
			if err != nil {
				out = ""
			}
			for _, l := range dLines(out) {
				value := gitx.TrimJS(l)
				if value == "" {
					continue
				}
				st.Helpers = append(st.Helpers, CredentialHelperInfo{
					Value:     value,
					Scope:     scope,
					Available: gitx.CredentialHelperExists(ctx, value),
					Plaintext: value == "store" || strings.HasPrefix(value, "store "),
				})
			}
		}

		// Per-URL sections: `credential.https://github.com.helper`. These beat the
		// plain setting for the URLs they match, which is exactly the kind of
		// thing that is invisible until it bites.
		for _, scope := range dCredentialScopes {
			out, err := gitx.RunGit(ctx, repoPath, []string{"config", dScopeFlag(scope), "--get-regexp", `^credential\..*\.(helper|username)$`}, gitx.RunOpts{})
			if err != nil {
				out = ""
			}
			for _, line := range dLines(out) {
				space := strings.IndexByte(line, ' ')
				if space < 0 {
					continue
				}
				m := dCredentialKeyRe.FindStringSubmatch(line[:space])
				if m == nil {
					continue
				}
				url, key, value := m[1], m[2], gitx.TrimJS(line[space+1:])
				idx := -1
				for i, r := range st.URLRules {
					if r.URL == url && r.Scope == scope {
						idx = i
						break
					}
				}
				if idx < 0 {
					st.URLRules = append(st.URLRules, CredentialURLRule{URL: url, Scope: scope})
					idx = len(st.URLRules) - 1
				}
				if key == "helper" {
					st.URLRules[idx].Helper = value
				} else {
					st.URLRules[idx].Username = value
				}
			}
		}

		home, _ := os.UserHomeDir()
		plaintextPath := filepath.Join(home, ".git-credentials")
		st.PlaintextFile.Path = plaintextPath
		if raw, err := os.ReadFile(plaintextPath); err == nil {
			st.PlaintextFile.Exists = true
			for _, l := range dLines(string(raw)) {
				if gitx.TrimJS(l) != "" {
					st.PlaintextFile.Entries++
				}
			}
		}

		remotes, err := gitx.RunGit(ctx, repoPath, []string{"remote", "-v"}, gitx.RunOpts{})
		if err != nil {
			remotes = ""
		}
		seen := map[string]bool{}
		for _, line := range dLines(remotes) {
			fields := dSplitWS(line)
			url := ""
			if len(fields) > 1 {
				url = fields[1]
			}
			if m := dHTTPSHostRe.FindStringSubmatch(url); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				st.HTTPSHosts = append(st.HTTPSHosts, m[1])
			}
		}
		st.Candidates = gitx.CredentialCandidates(ctx)
		return st, nil
	})
}

// SetCredentialHelper sets or clears `credential.helper` in scope ('global' |
// 'repo'). An empty value removes the setting.
//
// gitsvc: setCredentialHelper
func (s *GitService) SetCredentialHelper(ctx context.Context, repoPath, value, scope string) error {
	return lockedErr(s, ctx, "setCredentialHelper", repoPath, func(ctx context.Context) error {
		flag := "--global"
		if scope == "repo" {
			flag = "--local"
		}
		// `--unset-all`, not `--unset`: helpers stack, and leaving one behind is
		// how "I turned it off and it still remembers" happens.
		_, _ = gitx.RunGit(ctx, repoPath, []string{"config", flag, "--unset-all", "credential.helper"}, gitx.RunOpts{})
		if v := gitx.TrimJS(value); v != "" {
			_, err := gitx.RunGit(ctx, repoPath, []string{"config", flag, "--add", "credential.helper", v}, gitx.RunOpts{})
			return err
		}
		return nil
	})
}

// ForgetCredential makes git forget what it has stored for a host (protocol
// defaults to https).
//
// `git credential reject` is the documented way in: it hands the request to
// whichever helper is configured and asks it to erase. Nothing is read back,
// so no secret passes through here.
//
// gitsvc: forgetCredential
func (s *GitService) ForgetCredential(ctx context.Context, repoPath, host string, protocol *string) error {
	return lockedErr(s, ctx, "forgetCredential", repoPath, func(ctx context.Context) error {
		target := gitx.TrimJS(host)
		if target == "" {
			return dErr("Give a host to forget.")
		}
		proto := "https"
		if protocol != nil {
			proto = *protocol
		}
		res, err := gitx.Run(ctx, repoPath, []string{"credential", "reject"}, gitx.RunOpts{
			Stdin:     []byte("protocol=" + proto + "\nhost=" + target + "\n\n"),
			MaxBuffer: -1,
		})
		if err == nil {
			return nil
		}
		var ee *gitx.ExecError
		if errors.As(err, &ee) && !ee.Started() {
			return ee
		}
		if msg := gitx.TrimJS(res.Stderr); msg != "" {
			return errors.New(msg)
		}
		return errors.New("git credential reject exited " + dExitLabel(ee))
	})
}

// dExitLabel renders a close-event code the way `${code}` did: the number, or
// "null" when a signal ended the process.
func dExitLabel(ee *gitx.ExecError) string {
	if ee == nil {
		return "0"
	}
	if ee.Signal != "" || ee.Killed {
		return "null"
	}
	return strconv.Itoa(ee.Code)
}
