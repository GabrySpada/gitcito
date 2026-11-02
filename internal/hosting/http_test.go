package hosting

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// stubServer answers every request with one status/body and records the last
// request's headers — the vitest stubFetch.
func stubServer(t *testing.T, status int, body string, contentType string) (*client, *http.Header) {
	t.Helper()
	var last http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.Header.Clone()
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		if !nullBody[status] {
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(Options{HTTPClient: srv.Client(), Credentials: NewCredentials(func(context.Context, Command) CommandResult {
		t.Fatal("no credential lookup expected")
		return CommandResult{}
	}, nil)})
	c.ep.GitLab = srv.URL
	c.ep.Azure = srv.URL
	return c, &last
}

// ── test/gitlabMr.test.ts: glJson ───────────────────────────────────────────

const glToken = "glpat-abcdefghijklmnop"

func TestGlJSON(t *testing.T) {
	ctx := context.Background()
	t.Run("sends a Settings token as PRIVATE-TOKEN", func(t *testing.T) {
		c, last := stubServer(t, 200, `{"ok":true}`, "application/json")
		if err := c.glJSON(ctx, "GET", c.glAPI()+"/x", &apiAuth{token: glToken}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if got := last.Get("Private-Token"); got != glToken {
			t.Errorf("PRIVATE-TOKEN = %q", got)
		}
	})
	t.Run("sends a borrowed git credential as Bearer", func(t *testing.T) {
		c, last := stubServer(t, 200, `{"ok":true}`, "application/json")
		auth := &apiAuth{token: glToken, cred: &GitCredential{Username: "oauth2", Password: glToken}}
		if err := c.glJSON(ctx, "GET", c.glAPI()+"/x", auth, nil, nil); err != nil {
			t.Fatal(err)
		}
		if got := last.Get("Authorization"); got != "Bearer "+glToken {
			t.Errorf("Authorization = %q", got)
		}
	})
	errCases := []struct{ name, body, want string }{
		{"surfaces a string error message", `{"message":"403 Forbidden"}`, "GitLab: 403 Forbidden"},
		// GitLab validation errors arrive as {message: ["a", "b"]}.
		{"joins an array error message", `{"message":["source is invalid","target is protected"]}`, "GitLab: source is invalid; target is protected"},
		{"falls back to the status code for unparseable errors", "not json", "GitLab API error (500)"},
	}
	for _, ec := range errCases {
		t.Run(ec.name, func(t *testing.T) {
			status := 403
			if ec.body == "not json" {
				status = 500
			} else if ec.name == "joins an array error message" {
				status = 422
			}
			c, _ := stubServer(t, status, ec.body, "application/json")
			err := c.glJSON(ctx, "GET", c.glAPI()+"/x", &apiAuth{token: glToken}, nil, nil)
			if err == nil || err.Error() != ec.want {
				t.Errorf("got %v want %q", err, ec.want)
			}
		})
	}
	t.Run("tolerates an empty 2xx body", func(t *testing.T) {
		c, _ := stubServer(t, 204, "", "application/json")
		out := map[string]any{"untouched": true}
		if err := c.glJSON(ctx, "GET", c.glAPI()+"/x", &apiAuth{token: glToken}, nil, &out); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(out, map[string]any{"untouched": true}) {
			t.Errorf("out = %v", out)
		}
	})
}

// ── test/hostingAuth.test.ts: adoJson ───────────────────────────────────────

func TestAdoJSON(t *testing.T) {
	ctx := context.Background()
	t.Run("parses a normal JSON response", func(t *testing.T) {
		c, _ := stubServer(t, 200, `{"value":[{"name":"repo"}]}`, "application/json")
		var out map[string]any
		if err := c.adoJSON(ctx, "GET", c.ep.Azure+"/org/_apis/x", testPAT, nil, &out); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(out, map[string]any{"value": []any{map[string]any{"name": "repo"}}}) {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("treats a 203 sign-in page as an auth failure, not a parse error", func(t *testing.T) {
		// Azure DevOps answers unauthenticated API calls with 203 + an HTML sign-in
		// page. res.ok is true for 203, so this used to surface as
		// "Unexpected token '<'" from JSON.parse instead of an auth error.
		c, _ := stubServer(t, 203, "<!DOCTYPE html><html><body>Sign In</body></html>", "text/html")
		err := c.adoJSON(ctx, "GET", c.ep.Azure+"/org/_apis/x", testPAT, nil, nil)
		if err == nil || !regexp.MustCompile(`(?i)rejected the credential`).MatchString(err.Error()) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("surfaces the API message on a real error", func(t *testing.T) {
		c, _ := stubServer(t, 404, `{"message":"TF200016: project does not exist"}`, "application/json")
		err := c.adoJSON(ctx, "GET", c.ep.Azure+"/org/_apis/x", testPAT, nil, nil)
		if err == nil || !regexp.MustCompile(`TF200016`).MatchString(err.Error()) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("falls back to a status message when the error body is not JSON", func(t *testing.T) {
		c, _ := stubServer(t, 500, "upstream exploded", "text/plain")
		err := c.adoJSON(ctx, "GET", c.ep.Azure+"/org/_apis/x", testPAT, nil, nil)
		if err == nil || !regexp.MustCompile(`500`).MatchString(err.Error()) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("accepts an empty body (204 No Content)", func(t *testing.T) {
		c, _ := stubServer(t, 204, "", "application/json")
		var out any
		if err := c.adoJSON(ctx, "GET", c.ep.Azure+"/org/_apis/x", testPAT, nil, &out); err != nil || out != nil {
			t.Errorf("got %v, %v", out, err)
		}
	})
}

// A redirect to another host must not carry the GitLab PRIVATE-TOKEN header
// (Go already strips Authorization the same way).
func TestRedirectDropsPrivateToken(t *testing.T) {
	var gotToken, gotAuth string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken, gotAuth = r.Header.Get("Private-Token"), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
	}))
	defer first.Close()
	c := newClient(Options{})
	// localhost → 127.0.0.1: distinct hosts on the same machine.
	c.ep.GitLab = "http://localhost:" + strconv.Itoa(first.Listener.Addr().(*net.TCPAddr).Port)
	if err := c.glJSON(context.Background(), "GET", c.glAPI()+"/x", &apiAuth{token: "secret"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if gotToken != "" || gotAuth != "" {
		t.Errorf("credentials leaked across hosts: %q %q", gotToken, gotAuth)
	}
}

func TestLocaleTime(t *testing.T) {
	c := newClient(Options{Location: time.UTC})
	if got := c.localeTime(1700000000 * 1000); got != "10:13:20 PM" {
		t.Errorf("got %q", got)
	}
	if got := c.localeTime(jsToNumber("soon") * 1000); got != "Invalid Date" {
		t.Errorf("got %q", got)
	}
}
