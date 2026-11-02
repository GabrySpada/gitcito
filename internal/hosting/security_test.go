package hosting

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// A sentinel token must never surface in a log line or a returned error, on
// any provider, on success or failure, typed or borrowed from git's helper.
// The fake host answers every request with an error status (and a body that
// does not echo anything), so every error path that builds a message runs.

const sentinel = "SENTINEL-TOKEN-7f3a9c"

// leakForms are the shapes the token travels in: raw, and inside the Basic
// encodings Azure DevOps and Bitbucket use.
func leakForms() []string {
	return []string{
		sentinel,
		base64.StdEncoding.EncodeToString([]byte(":" + sentinel)),
		base64.StdEncoding.EncodeToString([]byte("user:" + sentinel)),
		base64.StdEncoding.EncodeToString([]byte("helper-user:" + sentinel)),
	}
}

func TestSentinelTokenNeverLeaks(t *testing.T) {
	// Capture everything the standard and structured loggers might print.
	var logs bytes.Buffer
	var logMu sync.Mutex
	lw := writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logs.Write(p)
	})
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(lw)
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(lw, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		slog.SetDefault(prevSlog)
	})

	var carried int // requests that really sent the sentinel, so the test is not vacuous
	var carriedMu sync.Mutex
	for _, status := range []int{401, 403, 404, 422, 429, 500, 203, 200} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, form := range leakForms() {
				if strings.Contains(r.Header.Get("Authorization")+r.Header.Get("Private-Token"), form) {
					carriedMu.Lock()
					carried++
					carriedMu.Unlock()
					break
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if status == 403 || status == 429 {
				w.Header().Set("x-ratelimit-remaining", "0")
				w.Header().Set("x-ratelimit-reset", "1700000000")
			}
			w.WriteHeader(status)
			switch status {
			case 203:
				_, _ = w.Write([]byte("<html>sign in</html>"))
			case 200:
				_, _ = w.Write([]byte("not json"))
			default:
				_, _ = w.Write([]byte(`{"message":"denied","errors":[{"field":"head","code":"invalid"}],"error":{"message":"nope"}}`))
			}
		}))
		ep := Endpoints{GitHubAPI: srv.URL, GitLab: srv.URL, BitbucketAPI: srv.URL, Azure: srv.URL, AzureProfile: srv.URL}
		// The helper hands out the sentinel as a borrowed credential; gh is absent.
		helper := func(_ context.Context, cmd Command) CommandResult {
			if cmd.Name == "gh" {
				return CommandResult{Err: os.ErrNotExist, NotFound: true}
			}
			return CommandResult{Stdout: "username=helper-user\npassword=" + sentinel + "\n"}
		}
		svc := NewService(Options{
			HTTPClient: srv.Client(), Endpoints: &ep, Credentials: NewCredentials(helper, nil),
			Location: time.UTC, OpenURL: func(string) error { return nil },
		})

		for _, typed := range []string{sentinel, "user:" + sentinel, ""} {
			tokens := HostTokens{GitHub: typed, GitLab: typed, Bitbucket: typed, Azure: typed}
			ctx := context.Background()
			var errs []error
			collect := func(_ any, err error) { errs = append(errs, err) }
			for _, p := range []string{"github", "gitlab", "bitbucket", "azure"} {
				collect(svc.ListRepos(ctx, p, typed, "contoso"))
				collect(svc.ListOwners(ctx, p, typed, "contoso"))
				collect(svc.WhoAmI(ctx, p, typed, "contoso", false))
				collect(svc.WhoAmI(ctx, p, typed, "", false))
				collect(svc.CreateRepo(ctx, p, typed, CreateRepoOpts{Owner: "o", OwnerType: "org", Project: "P", Name: "n"}, "contoso"))
			}
			for _, remote := range []string{
				"https://github.com/acme/app.git", "https://gitlab.com/acme/app.git",
				"https://bitbucket.org/team/app.git", "https://dev.azure.com/contoso/P/_git/app",
			} {
				collect(svc.ListPRs(ctx, remote, tokens))
				collect(svc.CreatePR(ctx, remote, tokens, CreatePrOpts{Title: "t", Source: "s", Target: "main"}))
				collect(svc.PrDetail(ctx, remote, tokens, 1))
				collect(svc.PrChecks(ctx, remote, tokens, 1))
				collect(svc.PrFiles(ctx, remote, tokens, 1))
				collect(nil, svc.PrComment(ctx, remote, tokens, 1, "b"))
				collect(nil, svc.PrReview(ctx, remote, tokens, 1, "APPROVE", "b"))
				collect(nil, svc.PrMerge(ctx, remote, tokens, 1, "squash"))
				collect(nil, svc.PrReplyReviewComment(ctx, remote, tokens, 1, json.RawMessage(`"d1"`), "b"))
				collect(svc.ListReleases(ctx, remote, tokens))
				collect(svc.ListIssues(ctx, remote, tokens))
				collect(svc.IssueDetail(ctx, remote, tokens, 1))
				collect(svc.CiStatuses(ctx, remote, []string{"abc"}, typed))
				collect(svc.EnsureStack(ctx, remote, tokens, []int{1, 2}))
				collect(nil, svc.UpdatePR(ctx, remote, tokens, 1, PrPatch{Title: "x"}))
			}
			collect(svc.ListNotifications(ctx, typed, true))
			collect(nil, svc.MarkNotificationRead(ctx, typed, "1"))
			collect(nil, svc.MarkAllNotificationsRead(ctx, typed))
			collect(svc.UploadSshKey(ctx, typed, "t", "ssh-ed25519 AAAA"))

			for _, err := range errs {
				if err == nil {
					continue
				}
				for _, form := range leakForms() {
					if strings.Contains(err.Error(), form) {
						t.Errorf("status %d: error leaks the token: %q", status, err.Error())
					}
				}
			}
		}
		srv.Close()
	}

	if carried < 100 {
		t.Errorf("only %d requests carried the sentinel; the test is not exercising the API paths", carried)
	}
	logMu.Lock()
	defer logMu.Unlock()
	for _, form := range leakForms() {
		if strings.Contains(logs.String(), form) {
			t.Errorf("logs leak the token:\n%s", logs.String())
		}
	}
}

// A network failure must not echo the request either (Go's url.Error would
// include the URL; fetch's "fetch failed" does not).
func TestTransportErrorIsOpaque(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing listens any more
	ep := Endpoints{GitHubAPI: srv.URL + "/" + sentinel}
	svc := NewService(Options{Endpoints: &ep, Credentials: NewCredentials(func(context.Context, Command) CommandResult {
		return CommandResult{Err: os.ErrNotExist}
	}, nil)})
	_, err := svc.ListRepos(context.Background(), "github", sentinel, "")
	if err == nil || err.Error() != "fetch failed" {
		t.Errorf("got %v", err)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
