// Package hosting is the Go port of src/main/hosting.ts and
// src/main/credentials.ts: the GitHub, GitLab, Bitbucket and Azure DevOps REST
// calls behind every `hosting:*` channel (repositories, owners, pull/merge
// requests, reviews, checks, merges, issues, milestones, notifications,
// releases, stacks, SSH key upload, CI statuses), and the fallback to git's own
// credential helper when no token was typed into Settings.
//
// The core is an unexported client with injected dependencies (HTTP client,
// API endpoints, credential resolver, URL opener, time zone); Service in
// service.go is the thin page-facing facade. Requests, mapped results and
// error strings are replayed against vectors captured from the TypeScript
// (test/port/hosting.test.ts → testdata/requests).
//
// Tokens arrive from the renderer per call, exactly as before; this package
// never stores, logs or returns them.
package hosting

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitx"
)

// Endpoints are the API origins. Production uses DefaultEndpoints; tests point
// them at an httptest.Server. Web links shown to the user are not affected.
type Endpoints struct {
	GitHubAPI    string // https://api.github.com
	GitLab       string // https://gitlab.com (REST under /api/v4)
	BitbucketAPI string // https://api.bitbucket.org
	Azure        string // https://dev.azure.com
	AzureProfile string // https://app.vssps.visualstudio.com
}

// DefaultEndpoints are the real hosts.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		GitHubAPI:    "https://api.github.com",
		GitLab:       "https://gitlab.com",
		BitbucketAPI: "https://api.bitbucket.org",
		Azure:        "https://dev.azure.com",
		AzureProfile: "https://app.vssps.visualstudio.com",
	}
}

// Options configures a Service. Zero values pick production behaviour.
type Options struct {
	// HTTPClient sends every API request. Nil: a client with undici's
	// five-minute header timeout and redirect limit (see newHTTPClient).
	HTTPClient *http.Client
	// Endpoints overrides the API origins (nil: DefaultEndpoints).
	Endpoints *Endpoints
	// Credentials resolves tokens from git's credential helper when none was
	// typed (nil: NewCredentials(ExecRunner, time.Now)).
	Credentials *Credentials
	// OpenURL opens a web page in the default browser (hosting:openCreatePR).
	// Nil: the URL is not opened.
	OpenURL func(url string) error
	// Location is the zone a rate-limit reset time is shown in (nil: time.Local).
	Location *time.Location
}

type client struct {
	http    *http.Client
	ep      Endpoints
	creds   *Credentials
	openURL func(string) error
	loc     *time.Location
}

func newClient(o Options) *client {
	c := &client{http: o.HTTPClient, creds: o.Credentials, openURL: o.OpenURL, loc: o.Location}
	if c.http == nil {
		c.http = newHTTPClient()
	}
	if o.Endpoints != nil {
		c.ep = *o.Endpoints
	} else {
		c.ep = DefaultEndpoints()
	}
	if c.creds == nil {
		c.creds = NewCredentials(nil, nil)
	}
	if c.loc == nil {
		c.loc = time.Local
	}
	return c
}

const (
	// headerTimeout is undici's default headersTimeout: Node's fetch gave up on a
	// server that sent no response headers for five minutes.
	headerTimeout = 300 * time.Second
	// maxRedirects is the fetch standard's redirect limit.
	maxRedirects = 20
	// maxResponseBytes bounds what one API response may hold in memory. Node had
	// no cap; no hosting API answer comes near this.
	maxResponseBytes = 64 << 20
)

func newHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: t, CheckRedirect: checkRedirect}
}

// checkRedirect follows up to 20 redirects like fetch. Go already drops
// Authorization when a redirect leaves the original host; the GitLab
// PRIVATE-TOKEN header is custom, so it is dropped here on the same condition.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("redirect count exceeded")
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		req.Header.Del("Private-Token")
	}
	return nil
}

// errFetchFailed is what Node's fetch rejects with on any transport failure
// (DNS, refused, reset, TLS). The cause is not surfaced: it would only repeat
// the URL.
var errFetchFailed = errors.New("fetch failed")

type response struct {
	status int
	header http.Header
	body   []byte // decoded as Response.text() would: BOM stripped
}

func (r *response) ok() bool { return r.status >= 200 && r.status <= 299 }

// header is one request header; a slice keeps the order the TS object literal had.
type header struct{ name, value string }

// fetch sends one request. body nil means no body.
func (c *client) fetch(ctx context.Context, method, url string, headers []header, body *string) (*response, error) {
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(*body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, errors.New("Failed to parse URL from " + url)
	}
	for _, h := range headers {
		req.Header.Set(h.name, h.value)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, errFetchFailed
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, errFetchFailed
	}
	return &response{status: res.StatusCode, header: res.Header, body: stripBOM(data)}, nil
}

// json is `res.json()`: parse the body into out (nil: parse and discard).
func (r *response) json(out any) error {
	if out == nil {
		var discard any
		out = &discard
	}
	return parseJSON(r.body, out)
}

func strPtr(s string) *string { return &s }

// ── auth schemes ────────────────────────────────────────────────────────────

func basic(s string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(s)) }

// bitbucketAuth: an "app password" is stored as user:password → Basic; a raw
// access token → Bearer.
func bitbucketAuth(token string) string {
	if strings.Contains(token, ":") {
		return basic(token)
	}
	return "Bearer " + token
}

// gitlabAuth: a token typed into Settings keeps using the `PRIVATE-TOKEN`
// header it has always used; a credential borrowed from git is an OAuth access
// token, which GitLab only accepts as a bearer token.
func gitlabAuth(a *apiAuth) header {
	if a.cred != nil {
		return header{"Authorization", "Bearer " + a.token}
	}
	return header{"PRIVATE-TOKEN", a.token}
}

// bitbucketAuthFor: a borrowed credential arrives already split into username
// and password, which is exactly the Basic pair Bitbucket wants.
func bitbucketAuthFor(a *apiAuth) string {
	if a.cred != nil && a.cred.Username != "" {
		return basic(a.cred.Username + ":" + a.cred.Password)
	}
	return bitbucketAuth(a.token)
}

// AzureAuth is the Azure DevOps Authorization value. A PAT goes in as Basic
// with an empty username — the scheme the docs describe. An Entra ID access
// token (what Git Credential Manager hands out, and what PATs are being
// replaced by) is a JWT and must be sent as Bearer.
func AzureAuth(token string) string {
	if IsJwt(token) {
		return "Bearer " + token
	}
	return basic(":" + token)
}

// ── GitHub ──────────────────────────────────────────────────────────────────

// ghJSON is the GitHub call every authenticated operation goes through: one
// place for the headers and for turning an error body into words.
func (c *client) ghJSON(ctx context.Context, method, url, token string, body *string, out any) error {
	hs := []header{{"Accept", "application/vnd.github+json"}, {"Authorization", "Bearer " + token}}
	if body != nil {
		hs = append(hs, header{"Content-Type", "application/json"})
	}
	res, err := c.fetch(ctx, method, url, hs, body)
	if err != nil {
		return err
	}
	if !res.ok() {
		msg := decodeAny(res.body)
		// Surface when the rate limit resets so the user knows how long to wait.
		if res.status == 403 || res.status == 429 {
			remaining := res.header.Get("x-ratelimit-remaining")
			reset := res.header.Get("x-ratelimit-reset")
			if remaining == "0" && reset != "" {
				return errors.New("GitHub rate limit exceeded — resets at " + c.localeTime(jsToNumber(reset)*1000) + ".")
			}
		}
		if m := prop(msg, "message"); jsTruthy(m) {
			return errors.New("GitHub: " + jsString(m))
		}
		return errors.New("GitHub API error (" + strconv.Itoa(res.status) + ")")
	}
	return res.json(out)
}

// localeTime is `new Date(ms).toLocaleTimeString()` in en-US: "3:04:05 PM".
func (c *client) localeTime(ms float64) string {
	// ECMAScript's time range is ±8.64e15 ms; anything else is an Invalid Date.
	if math.IsNaN(ms) || math.Abs(ms) > 8.64e15 {
		return "Invalid Date"
	}
	return time.UnixMilli(int64(ms)).In(c.loc).Format("3:04:05 PM")
}

func (c *client) ghRepoAPI(owner, repo string) string {
	return c.ep.GitHubAPI + "/repos/" + owner + "/" + repo
}

// ── GitLab ──────────────────────────────────────────────────────────────────

// glJSON is the GitLab counterpart to ghJSON — one place for auth headers and
// error extraction. An empty 2xx body leaves out untouched (undefined in TS).
func (c *client) glJSON(ctx context.Context, method, url string, auth *apiAuth, body *string, out any) error {
	hs := []header{gitlabAuth(auth)}
	if body != nil {
		hs = append(hs, header{"Content-Type", "application/json"})
	}
	res, err := c.fetch(ctx, method, url, hs, body)
	if err != nil {
		return err
	}
	if !res.ok() {
		// GitLab errors carry {message} (string or array) or {error}.
		d := decodeAny(res.body)
		raw := prop(d, "message")
		if raw == nil {
			raw = prop(d, "error")
		}
		msg := ""
		switch x := raw.(type) {
		case string:
			msg = x
		case []any:
			msg = jsJoin(x, "; ")
		}
		if msg != "" {
			return errors.New("GitLab: " + msg)
		}
		return errors.New("GitLab API error (" + strconv.Itoa(res.status) + ")")
	}
	if len(res.body) == 0 {
		return nil
	}
	return res.json(out)
}

func (c *client) glAPI() string { return c.ep.GitLab + "/api/v4" }

// ── Azure DevOps ────────────────────────────────────────────────────────────

// azureError is an Azure DevOps failure carrying the status, so callers can
// retry or re-auth.
type azureError struct {
	status int
	msg    string
}

func (e *azureError) Error() string { return e.msg }

func newAzureError(status int, message any) *azureError {
	if jsTruthy(message) {
		return &azureError{status: status, msg: "Azure DevOps: " + jsString(message)}
	}
	return &azureError{status: status, msg: azureAuthError(status, "")}
}

// isAuthFailure: whether a failure means "this credential is no good" rather
// than "this request was".
func isAuthFailure(err error) bool {
	var ae *azureError
	return errors.As(err, &ae) && (ae.status == 401 || ae.status == 403)
}

// azureAuthError builds a human-readable message for Azure DevOps auth
// failures, which are otherwise opaque.
func azureAuthError(status int, org string) string {
	s := strconv.Itoa(status)
	if status == 401 || status == 403 {
		if org != "" {
			return "Azure DevOps rejected the credential (" + s + "). Check that it is valid and not expired, that it has access to the \"" +
				org + "\" organization, and that the organization name is spelled correctly."
		}
		return "Azure DevOps rejected the credential (" + s + "). Check that it is valid, not expired, and has at least User Profile (read) or Code (read) scope — or set an organization so it can be checked against that org directly."
	}
	if status == 404 && org != "" {
		return "Azure DevOps organization \"" + org + "\" was not found (404). Double-check the organization name."
	}
	return "Azure DevOps API error (" + s + ")"
}

// adoJSON calls the Azure DevOps REST API.
//
// Azure DevOps does not answer an unauthenticated API request with 401. It
// replies **203 Non-Authoritative Information** carrying an HTML sign-in page,
// which `res.ok` accepts, so a bad or expired PAT used to surface as
// `Unexpected token '<'` from JSON.parse rather than as an auth error. Anything
// that is not JSON is therefore treated as a rejected credential here. An
// empty body (204) leaves out untouched.
func (c *client) adoJSON(ctx context.Context, method, url, token string, body *string, out any) error {
	hs := []header{{"Authorization", AzureAuth(token)}, {"Accept", "application/json"}}
	if body != nil {
		hs = append(hs, header{"Content-Type", "application/json"})
	}
	res, err := c.fetch(ctx, method, url, hs, body)
	if err != nil {
		return err
	}
	if !res.ok() {
		// The API returns {message} on real errors; fall back to the status.
		return newAzureError(res.status, prop(decodeAny(res.body), "message"))
	}
	// A sign-in page (203 + HTML), or an empty body from a 204.
	trimmed := gitx.TrimJS(string(res.body))
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return newAzureError(401, nil)
	}
	if out == nil {
		var discard any
		out = &discard
	}
	if parseJSON([]byte(trimmed), out) != nil {
		return newAzureError(res.status, nil)
	}
	return nil
}

// withAzureRetry runs an Azure DevOps request, and if a *borrowed* credential
// is rejected, drops it from the helper and tries once more with a freshly
// minted one. A credential the user typed in themselves is never retried — it
// is simply wrong, and silently re-asking would hide that.
func (c *client) withAzureRetry(ctx context.Context, remoteURL string, auth *apiAuth, run func(token string) error) error {
	err := run(auth.token)
	if err == nil || auth.cred == nil || !isAuthFailure(err) {
		return err
	}
	c.creds.ForgetCredential(ctx, remoteURL, *auth.cred)
	fresh := c.creds.apiToken(ctx, remoteURL, "", FillOpts{})
	if fresh == nil {
		return err
	}
	return run(fresh.token)
}

// ── credentials ─────────────────────────────────────────────────────────────

// HostTokens are the four provider token slots the renderer passes down.
type HostTokens struct {
	GitHub    string `json:"github,omitempty"`
	Azure     string `json:"azure,omitempty"`
	GitLab    string `json:"gitlab,omitempty"`
	Bitbucket string `json:"bitbucket,omitempty"`
}

func (t HostTokens) of(provider string) string {
	switch provider {
	case ProviderGitHub:
		return t.GitHub
	case ProviderAzure:
		return t.Azure
	case ProviderGitLab:
		return t.GitLab
	case ProviderBitbucket:
		return t.Bitbucket
	}
	return ""
}

// tokenForProvider: the credential for a provider outside any repository. Same
// precedence as for a remote: what the user configured, else what git already
// holds.
func (c *client) tokenForProvider(ctx context.Context, provider, token, org string) *apiAuth {
	return c.creds.apiToken(ctx, ProviderBaseURL(provider, org), token, FillOpts{})
}

// GithubCredentialMessage is actionable GitHub setup text for the exact local
// CLI state observed.
func GithubCredentialMessage(status GitHubCLIAuthStatus, host string) string {
	if host == "" {
		host = "github.com"
	}
	switch status {
	case GHMissing:
		return "No GitHub credential for " + host + ". GitHub CLI (gh) is not installed. Install gh and run `gh auth login`, or add a personal access token in Settings → Integrations."
	case GHSignedOut:
		return "No GitHub credential for " + host + ". GitHub CLI (gh) is installed but not authenticated. Run `gh auth login`, or add a personal access token in Settings → Integrations."
	case GHUnknown:
		return "No GitHub credential for " + host + ", and checking the GitHub CLI (gh) timed out — it may be offline. Sign in with git (any credential helper), run `gh auth login` then `gh auth setup-git`, or add a personal access token in Settings → Integrations."
	}
	return "No GitHub credential for " + host + ". GitHub CLI (gh) is authenticated, but Git is not configured to provide that credential to Gitcito. Run `gh auth setup-git`, then retry, or add a personal access token in Settings → Integrations."
}

// noCredential is a clear "no credential at all" error naming the setup step
// that failed.
func (c *client) noCredential(ctx context.Context, provider, remoteURL string) error {
	host := ""
	if remoteURL != "" {
		host = urlHost(remoteURL)
	}
	if strings.ToLower(provider) == "github" {
		return errors.New(GithubCredentialMessage(c.creds.GithubCliAuthStatus(ctx), host))
	}
	where := ""
	if host != "" {
		where = " for " + host
	}
	return errors.New("No " + provider + " credential" + where +
		". Either sign in with git (any credential helper — the same one `git clone` uses) or add a token in Settings → Integrations.")
}

// ── concurrency ─────────────────────────────────────────────────────────────

// all runs fns concurrently — the TS Promise.all — and returns the error of the
// first one in argument order that failed (with equal chains, the one
// Promise.all would have rejected with first).
func all(fns ...func() error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
