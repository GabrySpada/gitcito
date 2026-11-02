package hosting

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Credentials borrowed from git's own credential helper.
//
// Every machine that can `git clone` a private repo already has a working
// credential for it — stored by osxkeychain, or minted on demand by Git
// Credential Manager (which does the Entra ID / OAuth dance for Azure DevOps
// and GitHub). Gitcito used to ignore all of that and demand its own PAT per
// provider per profile, which is why multi-org Azure DevOps was painful: one
// PAT field cannot hold four organizations' tokens.
//
// Asking `git credential fill` instead makes the credential *per URL*, so every
// organization resolves independently with no configuration at all. A PAT typed
// into Settings still wins — this is the fallback for when none is set.

// GitCredential is what a helper answered for a URL.
type GitCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// FillOpts says how the helper may behave when it has nothing cached.
type FillOpts struct {
	// Interactive allows the helper to open a login window / prompt. Off for
	// background polling.
	Interactive bool
	// RepoPath runs the helper inside this repo so repo-local `credential.*`
	// config applies.
	RepoPath string
}

// GitHubCLIAuthStatus is what `gh auth status` says about the local login.
type GitHubCLIAuthStatus string

// The GitHubCLIAuthStatus values.
const (
	GHMissing       GitHubCLIAuthStatus = "missing"
	GHSignedOut     GitHubCLIAuthStatus = "signed-out"
	GHAuthenticated GitHubCLIAuthStatus = "authenticated"
	GHUnknown       GitHubCLIAuthStatus = "unknown"
)

const (
	// credentialTTL: lookups are cached per key this long, to avoid spawning git
	// per API call.
	credentialTTL = 5 * time.Minute
	// A helper that opens a GUI gets this long; a silent one should answer immediately.
	timeoutInteractive = 120 * time.Second
	timeoutSilent      = 10 * time.Second
	// ghStatusTTL: how long a `gh auth status` verdict may be reused. Far shorter
	// than the credential TTL: someone who reads "run `gh auth login`", does it
	// and retries must not be told the same thing again from a stale answer.
	ghStatusTTL = 30 * time.Second
)

// Command is one subprocess the credential code runs.
type Command struct {
	Name string
	Args []string
	// Stdin is written to the process and then closed ("" = no input).
	Stdin string
	// Env holds variables set on top of the process environment.
	Env map[string]string
	// Timeout kills the process after this long.
	Timeout time.Duration
}

// CommandResult is how a Command ended.
type CommandResult struct {
	Stdout string
	// Err is non-nil for any failure: non-zero exit, kill, or start failure.
	Err error
	// NotFound: the binary does not exist (Node's ENOENT).
	NotFound bool
	// TimedOut: the process was killed on its timeout (Node's `killed`).
	TimedOut bool
}

// CommandRunner runs a Command. Tests substitute a fake; production uses ExecRunner.
type CommandRunner func(ctx context.Context, cmd Command) CommandResult

// ExecRunner runs cmd as a real process (execFile in TS): the process
// environment plus cmd.Env, killed with its process group on timeout.
func ExecRunner(ctx context.Context, cmd Command) CommandResult {
	env := make([]string, 0, len(cmd.Env))
	for k, v := range cmd.Env {
		env = append(env, k+"="+v)
	}
	opts := gitx.RunOpts{PlainEnv: true, Env: env, Timeout: cmd.Timeout}
	if cmd.Stdin != "" {
		opts.Stdin = []byte(cmd.Stdin)
	}
	res, err := gitx.Exec(ctx, "", cmd.Name, cmd.Args, opts)
	out := CommandResult{Stdout: res.Stdout, Err: err}
	var ee *gitx.ExecError
	if errors.As(err, &ee) {
		out.NotFound = !ee.Started()
		out.TimedOut = ee.Killed
	}
	return out
}

type credEntry struct {
	cred *GitCredential
	at   time.Time
}

type ghEntry struct {
	status GitHubCLIAuthStatus
	at     time.Time
}

type ghFlight struct {
	done   chan struct{}
	status GitHubCLIAuthStatus
}

// Credentials resolves API tokens from git's credential helper, with the TS
// module's caches. Safe for concurrent use.
type Credentials struct {
	run CommandRunner
	now func() time.Time

	mu       sync.Mutex
	cache    map[string]credEntry
	gh       *ghEntry
	ghFlight *ghFlight
}

// NewCredentials returns a resolver that runs commands with run (nil means
// ExecRunner) and reads the time from now (nil means time.Now).
func NewCredentials(run CommandRunner, now func() time.Time) *Credentials {
	if run == nil {
		run = ExecRunner
	}
	if now == nil {
		now = time.Now
	}
	return &Credentials{run: run, now: now, cache: map[string]credEntry{}}
}

// credTarget is the part of a URL the credential helper matches on. Only
// https is handled: ssh remotes authenticate through the agent and never reach
// this path.
type credTarget struct{ protocol, host, path string }

var (
	leadingSlashes = regexp.MustCompile(`^/+`)
	dotGitSuffix   = regexp.MustCompile(`\.git$`)
)

func describe(raw string) *credTarget {
	u, ok := parseWebURL(raw)
	if !ok || (u.protocol != "https:" && u.protocol != "http:") {
		return nil
	}
	path := dotGitSuffix.ReplaceAllString(leadingSlashes.ReplaceAllString(u.pathname, ""), "")
	return &credTarget{
		protocol: strings.TrimSuffix(u.protocol, ":"),
		host:     u.host, // includes a non-default port, which git wants
		path:     path,
	}
}

// cacheKey: Azure DevOps puts the organization in the first path segment and
// each org needs its own token, so the key has to include it — keying on host
// alone would hand org B the credential minted for org A.
func cacheKey(d *credTarget) string {
	org := ""
	if strings.HasSuffix(d.host, "dev.azure.com") {
		org, _, _ = strings.Cut(d.path, "/")
	}
	return d.protocol + "://" + d.host + "/" + org
}

// runCredential feeds a `key=value` block to `git credential <verb>` and
// returns its stdout, or ok=false when git failed.
func (c *Credentials) runCredential(ctx context.Context, verb, input string, opts FillOpts) (string, bool) {
	var args []string
	if opts.RepoPath != "" {
		args = append(args, "-C", opts.RepoPath)
	}
	// `credential.interactive=false` is honoured by Git Credential Manager; the
	// env vars cover git itself and GCM's older name for the same switch.
	if !opts.Interactive {
		args = append(args, "-c", "credential.interactive=false")
	}
	args = append(args, "credential", verb)
	env := map[string]string{}
	timeout := timeoutInteractive
	if !opts.Interactive {
		env["GIT_TERMINAL_PROMPT"] = "0"
		env["GCM_INTERACTIVE"] = "never"
		timeout = timeoutSilent
	}
	res := c.run(ctx, Command{Name: "git", Args: args, Stdin: input, Env: env, Timeout: timeout})
	if res.Err != nil {
		return "", false
	}
	return res.Stdout, true
}

// parseCredential reads the username and password a helper printed, or nil
// when it printed no password.
func parseCredential(stdout string) *GitCredential {
	out := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		if eq := strings.Index(line, "="); eq > 0 {
			out[line[:eq]] = strings.TrimSuffix(line[eq+1:], "\r")
		}
	}
	// A helper that knows nothing returns the request back without a password.
	if out["password"] == "" {
		return nil
	}
	return &GitCredential{Username: out["username"], Password: out["password"]}
}

// CredentialFor asks git for the credential it would use for url, or nil when
// no helper is configured, none is cached and prompting is off, or the helper
// failed.
func (c *Credentials) CredentialFor(ctx context.Context, url string, opts FillOpts) *GitCredential {
	d := describe(url)
	if d == nil {
		return nil
	}
	key := cacheKey(d)
	c.mu.Lock()
	hit, ok := c.cache[key]
	c.mu.Unlock()
	// A cached miss is honoured too, so background polling on an unconfigured host
	// does not respawn git every few seconds. An interactive ask ignores it: the
	// user is explicitly trying to connect and deserves the login window.
	if ok && c.now().Sub(hit.at) < credentialTTL && !(opts.Interactive && hit.cred == nil) {
		return hit.cred
	}
	stdout, _ := c.runCredential(ctx, "fill", "protocol="+d.protocol+"\nhost="+d.host+"\npath="+d.path+"\n\n", opts)
	var cred *GitCredential
	if stdout != "" {
		cred = parseCredential(stdout)
	}
	c.mu.Lock()
	c.cache[key] = credEntry{cred: cred, at: c.now()}
	c.mu.Unlock()
	return cred
}

// ForgetCredential tells git a credential was rejected, so the helper erases
// it and mints a fresh one next time. Called when the host answers 401/403 with
// a helper-sourced credential — otherwise an expired token stays cached until
// it is evicted.
func (c *Credentials) ForgetCredential(ctx context.Context, url string, cred GitCredential) {
	d := describe(url)
	if d == nil {
		return
	}
	c.mu.Lock()
	delete(c.cache, cacheKey(d))
	c.mu.Unlock()
	c.runCredential(ctx, "reject",
		"protocol="+d.protocol+"\nhost="+d.host+"\npath="+d.path+"\nusername="+cred.Username+"\npassword="+cred.Password+"\n\n",
		FillOpts{})
}

// ClearCache drops every cached lookup (e.g. after the user edits their tokens).
func (c *Credentials) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = map[string]credEntry{}
	c.gh = nil
}

var jwtRe = regexp.MustCompile(`^ey[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.`)

// IsJwt: an Entra ID / OAuth access token is a JWT; a PAT is opaque. The
// distinction matters because the two want different Authorization schemes on
// most hosts.
func IsJwt(token string) bool { return jwtRe.MatchString(token) }

// apiAuth is the token for one API call and, when it was borrowed from git's
// helper, the credential it came from (so a 401 can invalidate it).
type apiAuth struct {
	token string
	cred  *GitCredential
}

// apiToken is the token a host's REST API should use, preferring one the user
// typed into Settings and falling back to whatever git already has for that
// remote. Nil when there is none.
func (c *Credentials) apiToken(ctx context.Context, remoteURL, configured string, opts FillOpts) *apiAuth {
	if t := gitx.TrimJS(configured); t != "" {
		return &apiAuth{token: t}
	}
	cred := c.CredentialFor(ctx, remoteURL, opts)
	if cred == nil {
		return nil
	}
	return &apiAuth{token: cred.Password, cred: cred}
}

// HasCredentialHelper reports whether any credential helper is configured at
// all — used to explain the setup state.
func (c *Credentials) HasCredentialHelper(ctx context.Context, repoPath string) bool {
	var args []string
	if repoPath != "" {
		args = []string{"-C", repoPath}
	}
	args = append(args, "config", "--get-all", "credential.helper")
	res := c.run(ctx, Command{Name: "git", Args: args})
	return res.Err == nil && gitx.TrimJS(res.Stdout) != ""
}

// probeGithubCli reads `gh auth status` once, mapping how the process ended
// onto a verdict.
func (c *Credentials) probeGithubCli(ctx context.Context) GitHubCLIAuthStatus {
	res := c.run(ctx, Command{Name: "gh", Args: []string{"auth", "status", "--hostname", "github.com"}, Timeout: timeoutSilent})
	switch {
	case res.Err == nil:
		return GHAuthenticated
	case res.NotFound:
		return GHMissing
	case res.TimedOut:
		// Recent `gh` validates the token over the network, so a slow or offline
		// link kills the probe on our timeout. That says nothing about the login.
		return GHUnknown
	}
	return GHSignedOut
}

// GithubCliAuthStatus explains why Git could not borrow a GitHub credential
// without ever reading or returning the token itself. `gh auth status` is
// non-interactive: success means the CLI has an account, while an ordinary
// failure means it is installed but signed out (or its saved login is no
// longer valid).
//
// Cached, and de-duplicated while in flight, because one Settings page can miss
// a credential for four providers at once and each miss lands here.
func (c *Credentials) GithubCliAuthStatus(ctx context.Context) GitHubCLIAuthStatus {
	c.mu.Lock()
	if c.gh != nil && c.now().Sub(c.gh.at) < ghStatusTTL {
		s := c.gh.status
		c.mu.Unlock()
		return s
	}
	if f := c.ghFlight; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.status
		case <-ctx.Done():
			return GHUnknown
		}
	}
	f := &ghFlight{done: make(chan struct{})}
	c.ghFlight = f
	c.mu.Unlock()

	// The probe outlives a cancelled first caller: others may be waiting on it.
	f.status = c.probeGithubCli(context.WithoutCancel(ctx))

	c.mu.Lock()
	// An inconclusive probe is not worth remembering: the next attempt may well
	// reach the network and produce a real answer.
	if f.status == GHUnknown {
		c.gh = nil
	} else {
		c.gh = &ghEntry{status: f.status, at: c.now()}
	}
	c.ghFlight = nil
	c.mu.Unlock()
	close(f.done)
	return f.status
}

// ProviderOfHost is the provider a host name belongs to, for picking the right
// Authorization scheme; "" when it is none of them.
func ProviderOfHost(host string) string {
	h := strings.ToLower(host)
	switch {
	case h == "github.com" || strings.HasSuffix(h, ".github.com"):
		return ProviderGitHub
	case h == "dev.azure.com" || strings.HasSuffix(h, ".visualstudio.com"):
		return ProviderAzure
	case h == "gitlab.com" || strings.HasSuffix(h, ".gitlab.com"):
		return ProviderGitLab
	case h == "bitbucket.org" || strings.HasSuffix(h, ".bitbucket.org"):
		return ProviderBitbucket
	}
	return ""
}
