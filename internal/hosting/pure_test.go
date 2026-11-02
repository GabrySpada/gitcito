package hosting

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Pure-logic vectors: testdata/vectors/hosting.json, written by
// test/port/hosting.test.ts from the TS functions.

type pureVectors struct {
	ParseRemoteURL []struct {
		In    string          `json:"in"`
		Out   json.RawMessage `json:"out"`
		Error *string         `json:"error"`
	} `json:"parseRemoteUrl"`
	AzureAuth []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"azureAuth"`
	IsJwt []struct {
		In  string `json:"in"`
		Out bool   `json:"out"`
	} `json:"isJwt"`
	ProviderOfHost []struct {
		In  string  `json:"in"`
		Out *string `json:"out"`
	} `json:"providerOfHost"`
	ParseCredential []struct {
		In  string         `json:"in"`
		Out *GitCredential `json:"out"`
	} `json:"parseCredential"`
	GithubCredentialMessage []struct {
		Status string  `json:"status"`
		Host   *string `json:"host"`
		Out    string  `json:"out"`
	} `json:"githubCredentialMessage"`
	ProviderBaseURL []struct {
		Provider string  `json:"provider"`
		Org      *string `json:"org"`
		Out      string  `json:"out"`
	} `json:"providerBaseUrl"`
	GlJobToCheck []struct {
		In  GlJob           `json:"in"`
		Out json.RawMessage `json:"out"`
	} `json:"glJobToCheck"`
	DiffLineCounts []struct {
		In  string `json:"in"`
		Out struct {
			Additions int `json:"additions"`
			Deletions int `json:"deletions"`
		} `json:"out"`
	} `json:"diffLineCounts"`
	JSONParseErrors []struct {
		In    string  `json:"in"`
		Error *string `json:"error"`
	} `json:"jsonParseErrors"`
}

func loadPure(t *testing.T) pureVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "hosting.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v pureVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func jsonEqual(t *testing.T, got any, want json.RawMessage) bool {
	t.Helper()
	return reflect.DeepEqual(canonical(t, got), canonical(t, want))
}

func TestVectorParseRemoteURL(t *testing.T) {
	for _, c := range loadPure(t).ParseRemoteURL {
		got, err := ParseRemoteURL(c.In)
		if c.Error != nil {
			if err == nil || err.Error() != *c.Error {
				t.Errorf("%q: error %v, want %q", c.In, err, *c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.In, err)
			continue
		}
		if !jsonEqual(t, got, c.Out) {
			t.Errorf("%q: got %s want %s", c.In, mustJSON(got), c.Out)
		}
	}
}

func TestVectorAuthAndCredentials(t *testing.T) {
	v := loadPure(t)
	for _, c := range v.AzureAuth {
		if got := AzureAuth(c.In); got != c.Out {
			t.Errorf("AzureAuth(%q) = %q want %q", c.In, got, c.Out)
		}
	}
	for _, c := range v.IsJwt {
		if got := IsJwt(c.In); got != c.Out {
			t.Errorf("IsJwt(%q) = %v", c.In, got)
		}
	}
	for _, c := range v.ProviderOfHost {
		if got := ProviderOfHost(c.In); got != derefOr(c.Out, "") {
			t.Errorf("ProviderOfHost(%q) = %q", c.In, got)
		}
	}
	for _, c := range v.ParseCredential {
		if got := parseCredential(c.In); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("parseCredential(%q) = %+v want %+v", c.In, got, c.Out)
		}
	}
	for _, c := range v.GithubCredentialMessage {
		if got := GithubCredentialMessage(GitHubCLIAuthStatus(c.Status), derefOr(c.Host, "")); got != c.Out {
			t.Errorf("GithubCredentialMessage(%s) = %q", c.Status, got)
		}
	}
	for _, c := range v.ProviderBaseURL {
		if got := ProviderBaseURL(c.Provider, derefOr(c.Org, "")); got != c.Out {
			t.Errorf("ProviderBaseURL(%s, %v) = %q want %q", c.Provider, c.Org, got, c.Out)
		}
	}
}

func TestVectorGitLabMapping(t *testing.T) {
	v := loadPure(t)
	for _, c := range v.GlJobToCheck {
		if got := GlJobToCheck(c.In); !jsonEqual(t, got, c.Out) {
			t.Errorf("GlJobToCheck(%+v) = %s want %s", c.In, mustJSON(got), c.Out)
		}
	}
	for _, c := range v.DiffLineCounts {
		a, d := DiffLineCounts(c.In)
		if a != c.Out.Additions || d != c.Out.Deletions {
			t.Errorf("DiffLineCounts(%q) = %d,%d want %+v", c.In, a, d, c.Out)
		}
	}
}

func TestVectorJSONParseErrors(t *testing.T) {
	for _, c := range loadPure(t).JSONParseErrors {
		var v any
		err := parseJSON([]byte(c.In), &v)
		switch {
		case c.Error == nil && err != nil:
			t.Errorf("%q: unexpected error %q", c.In, err)
		case c.Error != nil && (err == nil || err.Error() != *c.Error):
			t.Errorf("%q:\n got  %v\n want %q", c.In, err, *c.Error)
		}
	}
}

// ── test/hostingAuth.test.ts ────────────────────────────────────────────────

const (
	testJWT = "eyJhbGciOiJSUzI1NiJ9.eyJhdWQiOiJhZG8ifQ.c2ln"
	testPAT = "abcdefghijklmnopqrstuvwxyz234567"
)

func TestAzureAuth(t *testing.T) {
	t.Run("sends a PAT as Basic with an empty username", func(t *testing.T) {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+testPAT))
		if got := AzureAuth(testPAT); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("sends an Entra ID access token as Bearer", func(t *testing.T) {
		// Git Credential Manager hands out JWTs, not PATs. Basic-encoding one is
		// rejected by Azure DevOps.
		if got := AzureAuth(testJWT); got != "Bearer "+testJWT {
			t.Errorf("got %q", got)
		}
	})
}

func TestProviderBaseURL(t *testing.T) {
	// Each org needs its own token, so they must not share a credential key.
	cases := []struct{ provider, org, want string }{
		{"azure", "contoso", "https://dev.azure.com/contoso"},
		{"azure", "fabrikam", "https://dev.azure.com/fabrikam"},
		{"github", "", "https://github.com"},
		{"gitlab", "", "https://gitlab.com"},
		{"bitbucket", "", "https://bitbucket.org"},
	}
	for _, c := range cases {
		if got := ProviderBaseURL(c.provider, c.org); got != c.want {
			t.Errorf("ProviderBaseURL(%s,%s) = %q", c.provider, c.org, got)
		}
	}
}

func TestParseCredential(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *GitCredential
	}{
		{"reads the username and password a helper printed", "protocol=https\nhost=dev.azure.com\nusername=me\npassword=secret\n", &GitCredential{"me", "secret"}},
		{`tolerates a password containing "="`, "password=a=b=c\n", &GitCredential{"", "a=b=c"}},
		// A helper with no match echoes the request back, without a password.
		{"returns null when the helper knows nothing", "protocol=https\nhost=dev.azure.com\n", nil},
		{"strips carriage returns", "username=me\r\npassword=secret\r\n", &GitCredential{"me", "secret"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseCredential(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func TestIsJwt(t *testing.T) {
	if !IsJwt(testJWT) {
		t.Error("recognises an access token")
	}
	for _, s := range []string{testPAT, "ghp_abcdef", ""} {
		if IsJwt(s) {
			t.Errorf("does not mistake %q for one", s)
		}
	}
}

func TestProviderOfHost(t *testing.T) {
	cases := map[string]string{
		"github.com": "github", "dev.azure.com": "azure", "contoso.visualstudio.com": "azure",
		"gitlab.com": "gitlab", "bitbucket.org": "bitbucket",
		"git.example.com": "", // does not claim an unrelated host
	}
	for host, want := range cases {
		if got := ProviderOfHost(host); got != want {
			t.Errorf("ProviderOfHost(%q) = %q want %q", host, got, want)
		}
	}
}

func TestGitHubCredentialGuidance(t *testing.T) {
	t.Run("distinguishes a missing GitHub CLI", func(t *testing.T) {
		m := GithubCredentialMessage(GHMissing, "")
		if !strings.Contains(m, "GitHub CLI (gh) is not installed") || !strings.Contains(m, "gh auth login") {
			t.Error(m)
		}
	})
	t.Run("distinguishes an installed but signed-out GitHub CLI", func(t *testing.T) {
		m := GithubCredentialMessage(GHSignedOut, "")
		if !strings.Contains(m, "installed but not authenticated") || !strings.Contains(m, "gh auth login") {
			t.Error(m)
		}
	})
	t.Run("explains when gh is authenticated but not wired into Git", func(t *testing.T) {
		if m := GithubCredentialMessage(GHAuthenticated, ""); !strings.Contains(m, "gh auth setup-git") {
			t.Error(m)
		}
	})
	t.Run("does not claim a signed-out CLI when the probe merely timed out", func(t *testing.T) {
		m := GithubCredentialMessage(GHUnknown, "")
		if !strings.Contains(m, "timed out") || strings.Contains(m, "not authenticated") || strings.Contains(m, "not installed") {
			t.Error(m)
		}
	})
}

// ── test/gitlabMr.test.ts (glJobToCheck, diffLineCounts) ────────────────────

func TestGlJobToCheck(t *testing.T) {
	t.Run("maps running jobs to in_progress with no conclusion", func(t *testing.T) {
		got := GlJobToCheck(GlJob{Name: "build", Status: "running", WebURL: strPtr("https://x")})
		want := PrCheck{Name: "build", Status: "in_progress", Conclusion: nil, URL: "https://x"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("maps terminal states to the check-run vocabulary", func(t *testing.T) {
		for status, want := range map[string]string{
			"success": "success", "failed": "failure", "canceled": "cancelled", "skipped": "skipped", "manual": "action_required",
		} {
			if got := GlJobToCheck(GlJob{Name: "a", Status: status}).Conclusion; got == nil || *got != want {
				t.Errorf("%s → %v want %s", status, got, want)
			}
		}
	})
	t.Run("treats queued-ish states as queued", func(t *testing.T) {
		for _, s := range []string{"created", "pending", "waiting_for_resource", "preparing", "scheduled"} {
			if got := GlJobToCheck(GlJob{Name: "a", Status: s}).Status; got != "queued" {
				t.Errorf("%s → %s", s, got)
			}
		}
	})
	t.Run("does not crash on an unknown status", func(t *testing.T) {
		got := GlJobToCheck(GlJob{Name: "a", Status: "somethingnew"})
		if got.Status != "completed" || got.Conclusion == nil || *got.Conclusion != "neutral" || got.URL != "" {
			t.Errorf("got %+v", got)
		}
	})
}

func TestDiffLineCounts(t *testing.T) {
	t.Run("counts added and removed lines, skipping file headers", func(t *testing.T) {
		diff := strings.Join([]string{"--- a/f.txt", "+++ b/f.txt", "@@ -1,3 +1,3 @@", " ctx", "-old", "+new", "+more"}, "\n")
		if a, d := DiffLineCounts(diff); a != 2 || d != 1 {
			t.Errorf("got %d,%d", a, d)
		}
	})
	t.Run("returns zeros for an empty diff", func(t *testing.T) {
		if a, d := DiffLineCounts(""); a != 0 || d != 0 {
			t.Errorf("got %d,%d", a, d)
		}
	})
}

// ── test/pureLogic.test.ts: parseRemoteUrl ──────────────────────────────────

func TestParseRemoteURLCases(t *testing.T) {
	type want struct{ provider, owner, project, repo string }
	cases := []struct {
		name string
		url  string
		want *want
	}{
		{"parses GitHub https + ssh", "https://github.com/o/r.git", &want{"github", "o", "", "r"}},
		{"parses GitHub https + ssh", "git@github.com:o/r.git", &want{"github", "o", "", "r"}},
		{"parses GitLab, including multi-level subgroups (https + ssh)", "https://gitlab.com/group/repo.git", &want{"gitlab", "group", "", "repo"}},
		{"parses GitLab, including multi-level subgroups (https + ssh)", "git@gitlab.com:group/sub/repo.git", &want{"gitlab", "group/sub", "", "repo"}},
		{"parses GitLab, including multi-level subgroups (https + ssh)", "https://gitlab.com/group/sub/repo", &want{"gitlab", "group/sub", "", "repo"}},
		{"parses Bitbucket https + ssh", "https://bitbucket.org/team/repo.git", &want{"bitbucket", "team", "", "repo"}},
		{"parses Bitbucket https + ssh", "git@bitbucket.org:team/repo.git", &want{"bitbucket", "team", "", "repo"}},
		{"parses Azure DevOps", "https://dev.azure.com/org/proj/_git/repo", &want{"azure", "org", "proj", "repo"}},
		{"returns null for unknown hosts", "https://example.com/x/y.git", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseRemoteURL(c.url)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == nil {
				if got != nil {
					t.Errorf("%s: got %+v", c.url, got)
				}
				return
			}
			if got == nil || got.Provider != c.want.provider || got.Owner != c.want.owner || got.Project != c.want.project || got.Repo != c.want.repo {
				t.Errorf("%s: got %+v want %+v", c.url, got, c.want)
			}
		})
	}
}

// ── JS helpers ──────────────────────────────────────────────────────────────

func TestJSHelpers(t *testing.T) {
	for in, want := range map[float64]string{
		0: "0", 3: "3", 3.5: "3.5", -2: "-2", 1e21: "1e+21", 1e-7: "1e-7", 123456789012: "123456789012", 0.000001: "0.000001",
	} {
		if got := jsNumberString(in); got != want {
			t.Errorf("jsNumberString(%v) = %q want %q", in, got, want)
		}
	}
	if !math.IsNaN(jsToNumber("abc")) || jsToNumber(" 42 ") != 42 || jsToNumber("0x10") != 16 || jsToNumber("") != 0 || jsToNumber("1e3") != 1000 {
		t.Error("jsToNumber")
	}
	// JSON.stringify leaves <, >, & and U+2028 alone; escaped backslashes stay escaped.
	if got := jsonStringify("a<b>&\u2028\\u2028"); got != "\"a<b>&\u2028\\\\u2028\"" {
		t.Errorf("jsonStringify = %s", got)
	}
	if got := jsonStringify(jsObject{{"a", 1}, {"skip", undefined}, {"b", jsNumber(math.NaN())}}); got != `{"a":1,"b":null}` {
		t.Errorf("jsObject = %s", got)
	}
	// Integer-like keys first, then insertion order; duplicates keep their first slot.
	if got := stringifyRaw(json.RawMessage(`{"b":1,"2":2,"a":{"x":"\u00e9"},"1":3,"b":4}`)); got != `{"1":3,"2":2,"b":4,"a":{"x":"é"}}` {
		t.Errorf("stringifyRaw = %s", got)
	}
	if got := encodeURIComponent("a b/ä!*'()~"); got != "a%20b%2F%C3%A4!*'()~" {
		t.Errorf("encodeURIComponent = %s", got)
	}
	for in, want := range map[string]string{"a%20b": "a b", "%E2%9C%93": "✓", "a+b": "a+b"} {
		if got, err := decodeURIComponent(in); err != nil || got != want {
			t.Errorf("decodeURIComponent(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"%", "%Z1", "%C3", "%C3%28", "%ED%A0%80"} {
		if _, err := decodeURIComponent(bad); err == nil || err.Error() != "URI malformed" {
			t.Errorf("decodeURIComponent(%q) should be URI malformed, got %v", bad, err)
		}
	}
}

func TestParseWebURL(t *testing.T) {
	cases := []struct {
		in, protocol, host, path string
		ok                       bool
	}{
		{"https://GitHub.com/a/b.git", "https:", "github.com", "/a/b.git", true},
		{"https://user:pw@dev.azure.com:443/org/p", "https:", "dev.azure.com", "/org/p", true},
		{"http://ghe.local:8080/x", "http:", "ghe.local:8080", "/x", true},
		{"https://example.com", "https:", "example.com", "/", true},
		{"https://h/a b/../c", "https:", "h", "/c", true},
		{"ssh://git@Host:22/x", "ssh:", "Host:22", "/x", true},
		{"git@github.com:a/b.git", "", "", "", false},
		{"", "", "", "", false},
		{"https://h:99999/", "", "", "", false},
	}
	for _, c := range cases {
		u, ok := parseWebURL(c.in)
		if ok != c.ok || (ok && (u.protocol != c.protocol || u.host != c.host || u.pathname != c.path)) {
			t.Errorf("parseWebURL(%q) = %+v, %v", c.in, u, ok)
		}
	}
}
