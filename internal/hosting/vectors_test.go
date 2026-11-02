package hosting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The request vectors are written by test/port/hosting.test.ts from the TS
// implementation (fake fetch + fake `git credential` / `gh`). Each case is
// replayed here against an httptest.Server: Go must send the same requests,
// spawn the same credential processes, open the same URLs and produce the same
// result or error message.

type vecAuth struct {
	Header string  `json:"header"`
	Scheme *string `json:"scheme"`
	Sha256 string  `json:"sha256"`
}

type vecRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Auth      *vecAuth          `json:"auth"`
	Body      *string           `json:"body"`
	Unmatched bool              `json:"unmatched,omitempty"`
}

type vecCanned struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	JSON    json.RawMessage   `json:"json"`
	Raw     *string           `json:"raw"`
	Network bool              `json:"network"`
}

type vecProc struct {
	File    string            `json:"file"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Timeout int64             `json:"timeout"`
	Input   *string           `json:"input"`
}

type vecCase struct {
	Name    string            `json:"name"`
	Channel string            `json:"channel"`
	Args    []json.RawMessage `json:"args"`
	Helper  struct {
		Fill map[string]json.RawMessage `json:"fill"`
		GH   string                     `json:"gh"`
	} `json:"helper"`
	Responses []vecCanned     `json:"responses"`
	Requests  []vecRequest    `json:"requests"`
	Processes []vecProc       `json:"processes"`
	Opened    []string        `json:"opened"`
	Result    json.RawMessage `json:"result"`
	Error     *string         `json:"error"`
}

func loadCases(t *testing.T) []vecCase {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "requests", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no request vectors: %v", err)
	}
	var out []vecCase
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cases []vecCase
		if err := json.Unmarshal(raw, &cases); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, cases...)
	}
	return out
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var passwordLine = regexp.MustCompile(`(?m)^password=(.*)$`)

// redactInput hides a password the same way the TS capture did.
func redactInput(s string) string {
	return passwordLine.ReplaceAllStringFunc(s, func(line string) string {
		return "password=<sha256:" + sha(strings.TrimPrefix(line, "password=")) + ">"
	})
}

// fakeHelper replays the TS fake execFile: `git credential fill` answers from
// the case's fill table (a string, a sequence, or failure), `reject` succeeds,
// and `gh auth status` ends the way the case says.
type fakeHelper struct {
	mu    sync.Mutex
	fill  map[string][]*string // nil entry = failure
	once  map[string]bool      // single (non-sequence) answers are reused
	gh    string
	calls []vecProc
}

func newFakeHelper(fill map[string]json.RawMessage, gh string) *fakeHelper {
	f := &fakeHelper{fill: map[string][]*string{}, once: map[string]bool{}, gh: gh, calls: []vecProc{}}
	for k, raw := range fill {
		var seq []*string
		if json.Unmarshal(raw, &seq) == nil && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
			f.fill[k] = seq
			continue
		}
		var s *string
		_ = json.Unmarshal(raw, &s)
		f.fill[k] = []*string{s}
		f.once[k] = true
	}
	return f
}

func (f *fakeHelper) run(_ context.Context, cmd Command) CommandResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := vecProc{File: cmd.Name, Args: append([]string{}, cmd.Args...), Env: map[string]string{}, Timeout: cmd.Timeout.Milliseconds()}
	for k, v := range cmd.Env {
		call.Env[k] = v
	}
	if cmd.Stdin != "" {
		in := redactInput(cmd.Stdin)
		call.Input = &in
	}
	f.calls = append(f.calls, call)
	fail := CommandResult{Err: fmt.Errorf("Command failed")}
	if cmd.Name == "gh" {
		switch f.gh {
		case "authenticated":
			return CommandResult{}
		case "signed-out":
			return fail
		case "unknown":
			fail.TimedOut = true
			return fail
		}
		fail.NotFound = true
		return fail
	}
	if cmd.Name != "git" || !contains(cmd.Args, "credential") {
		return fail
	}
	if cmd.Args[len(cmd.Args)-1] == "reject" {
		return CommandResult{}
	}
	fields := map[string]string{}
	for _, line := range strings.Split(cmd.Stdin, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k != "" {
			fields[k] = v
		}
	}
	key := fields["host"] + "/" + fields["path"]
	if _, ok := f.fill[key]; !ok {
		key = fields["host"]
	}
	seq, ok := f.fill[key]
	if !ok || len(seq) == 0 {
		return fail
	}
	reply := seq[0]
	if !f.once[key] {
		f.fill[key] = seq[1:]
	}
	if reply == nil {
		return fail
	}
	return CommandResult{Stdout: *reply}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// origins maps the path prefix each endpoint gets on the fake server back to
// the real origin the TS addressed.
var origins = []string{"api.github.com", "gitlab.com", "api.bitbucket.org", "dev.azure.com", "app.vssps.visualstudio.com"}

// fakeHost serves canned responses and records requests as the TS fake did.
type fakeHost struct {
	mu       sync.Mutex
	queues   map[string][]vecCanned
	requests []vecRequest
}

var transportHeaders = map[string]bool{"user-agent": true, "accept-encoding": true, "content-length": true}

var nullBody = map[int]bool{101: true, 204: true, 205: true, 304: true}

func (h *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	uri := r.RequestURI
	url := ""
	for _, o := range origins {
		if p := "/@" + o; strings.HasPrefix(uri, p+"/") || strings.HasPrefix(uri, p+"?") {
			url = "https://" + o + strings.TrimPrefix(uri, p)
			break
		}
	}
	body, _ := io.ReadAll(r.Body)
	rec := vecRequest{Method: r.Method, URL: url, Headers: map[string]string{}}
	for name, vals := range r.Header {
		k := strings.ToLower(name)
		v := strings.Join(vals, ", ")
		switch {
		case k == "authorization":
			scheme, _, _ := strings.Cut(v, " ")
			rec.Auth = &vecAuth{Header: k, Scheme: &scheme, Sha256: sha(v)}
		case k == "private-token":
			rec.Auth = &vecAuth{Header: k, Sha256: sha(v)}
		case !transportHeaders[k]:
			rec.Headers[k] = v
		}
	}
	if len(body) > 0 {
		s := string(body)
		rec.Body = &s
	}

	h.mu.Lock()
	key := r.Method + " " + url
	q := h.queues[key]
	var canned *vecCanned
	switch {
	case len(q) > 1:
		canned = &q[0]
		h.queues[key] = q[1:]
	case len(q) == 1:
		canned = &q[0]
	}
	if canned == nil {
		rec.Unmatched = true
	}
	h.requests = append(h.requests, rec)
	h.mu.Unlock()

	if canned == nil || canned.Network {
		// Simulate a transport failure: answer garbage and drop the connection.
		// (A silent close would make Go's transport retry the idempotent request
		// on a fresh connection, recording it twice.)
		hj, ok := w.(http.Hijacker)
		if ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_, _ = conn.Write([]byte("NOT-HTTP\r\n\r\n"))
				conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	status := canned.Status
	if status == 0 {
		status = 200
	}
	w.Header().Set("Content-Type", "application/json")
	for k, v := range canned.Headers {
		w.Header().Set(k, v)
	}
	var out []byte
	switch {
	case nullBody[status]:
	case canned.Raw != nil:
		out = []byte(*canned.Raw)
	case len(canned.JSON) > 0:
		var buf bytes.Buffer
		_ = json.Compact(&buf, canned.JSON)
		out = buf.Bytes()
	}
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

// replay runs one channel through the Service, by name, with the case's args.
var replay = map[string]func(context.Context, *Service, []json.RawMessage) (any, error){
	"hosting:listRepos": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.ListRepos(ctx, str(a, 0), str(a, 1), str(a, 2))
	},
	"hosting:listOwners": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.ListOwners(ctx, str(a, 0), str(a, 1), str(a, 2))
	},
	"hosting:whoAmI": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.WhoAmI(ctx, str(a, 0), str(a, 1), str(a, 2), arg[bool](a, 3))
	},
	"hosting:createRepo": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.CreateRepo(ctx, str(a, 0), str(a, 1), arg[CreateRepoOpts](a, 2), str(a, 3))
	},
	"hosting:listPRs": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.ListPRs(ctx, str(a, 0), arg[HostTokens](a, 1))
	},
	"hosting:listReleases": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.ListReleases(ctx, str(a, 0), arg[HostTokens](a, 1))
	},
	"hosting:ciStatuses": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.CiStatuses(ctx, str(a, 0), arg[[]string](a, 1), str(a, 2))
	},
	"hosting:openCreatePR": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.OpenCreatePR(ctx, str(a, 0), str(a, 1), str(a, 2))
	},
	"hosting:createPR": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.CreatePR(ctx, str(a, 0), arg[HostTokens](a, 1), arg[CreatePrOpts](a, 2))
	},
	"hosting:prDetail": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.PrDetail(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2))
	},
	"hosting:prComment": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.PrComment(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2), str(a, 3))
	},
	"hosting:prReplyReviewComment": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.PrReplyReviewComment(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2), a[3], str(a, 4))
	},
	"hosting:prFiles": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.PrFiles(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2))
	},
	"hosting:prChecks": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.PrChecks(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2))
	},
	"hosting:prReview": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.PrReview(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2), str(a, 3), str(a, 4))
	},
	"hosting:prMerge": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.PrMerge(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2), str(a, 3))
	},
	"hosting:mergedPrHeads": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.MergedPrHeads(ctx, str(a, 0), arg[HostTokens](a, 1), arg[[]string](a, 2))
	},
	"hosting:ensureStack": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.EnsureStack(ctx, str(a, 0), arg[HostTokens](a, 1), arg[[]int](a, 2))
	},
	"hosting:updatePR": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.UpdatePR(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2), arg[PrPatch](a, 3))
	},
	"hosting:listNotifications": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.ListNotifications(ctx, str(a, 0), arg[bool](a, 1))
	},
	"hosting:markNotificationRead": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.MarkNotificationRead(ctx, str(a, 0), str(a, 1))
	},
	"hosting:markAllNotificationsRead": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.MarkAllNotificationsRead(ctx, str(a, 0))
	},
	"hosting:listIssues": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.ListIssues(ctx, str(a, 0), arg[HostTokens](a, 1))
	},
	"hosting:listMilestones": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.ListMilestones(ctx, str(a, 0), arg[HostTokens](a, 1))
	},
	"hosting:milestoneIssues": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.MilestoneIssues(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2))
	},
	"hosting:uploadSshKey": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.UploadSshKey(ctx, str(a, 0), str(a, 1), str(a, 2))
	},
	"hosting:issueDetail": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.IssueDetail(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2))
	},
	"hosting:setIssueState": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.SetIssueState(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2), str(a, 3))
	},
	"hosting:createIssue": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return s.CreateIssue(ctx, str(a, 0), arg[HostTokens](a, 1), arg[CreateIssueOpts](a, 2))
	},
	"hosting:applyPrMeta": func(ctx context.Context, s *Service, a []json.RawMessage) (any, error) {
		return nil, s.ApplyPrMeta(ctx, str(a, 0), arg[HostTokens](a, 1), arg[int](a, 2), arg[PrMeta](a, 3))
	},
}

// arg decodes argument i the way the bridge would; null or missing is the zero value.
func arg[T any](a []json.RawMessage, i int) T {
	var v T
	if i < len(a) {
		_ = json.Unmarshal(a[i], &v)
	}
	return v
}

func str(a []json.RawMessage, i int) string { return arg[string](a, i) }

// canonical is v as plain decoded JSON, for comparison by meaning.
func canonical(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func sortRequests(rs []vecRequest) []vecRequest {
	out := append([]vecRequest{}, rs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Method+" "+a.URL != b.Method+" "+b.URL {
			return a.Method+" "+a.URL < b.Method+" "+b.URL
		}
		return a.Auth != nil && b.Auth != nil && a.Auth.Sha256 < b.Auth.Sha256
	})
	return out
}

// newReplayService wires a Service to a fake host and a fake credential helper.
func newReplayService(t *testing.T, host http.Handler, helper *fakeHelper, opened *[]string) *Service {
	t.Helper()
	srv := httptest.NewServer(host)
	t.Cleanup(srv.Close)
	ep := Endpoints{
		GitHubAPI:    srv.URL + "/@api.github.com",
		GitLab:       srv.URL + "/@gitlab.com",
		BitbucketAPI: srv.URL + "/@api.bitbucket.org",
		Azure:        srv.URL + "/@dev.azure.com",
		AzureProfile: srv.URL + "/@app.vssps.visualstudio.com",
	}
	var mu sync.Mutex
	return NewService(Options{
		HTTPClient:  srv.Client(),
		Endpoints:   &ep,
		Credentials: NewCredentials(helper.run, nil),
		Location:    time.UTC,
		OpenURL: func(u string) error {
			mu.Lock()
			defer mu.Unlock()
			*opened = append(*opened, u)
			return nil
		},
	})
}

func TestRequestVectors(t *testing.T) {
	cases := loadCases(t)
	seen := map[string]bool{}
	for _, tc := range cases {
		seen[tc.Channel] = true
		t.Run(strings.TrimPrefix(tc.Channel, "hosting:")+"/"+tc.Name, func(t *testing.T) {
			host := &fakeHost{queues: map[string][]vecCanned{}}
			for _, r := range tc.Responses {
				k := r.Method + " " + r.URL
				host.queues[k] = append(host.queues[k], r)
			}
			helper := newFakeHelper(tc.Helper.Fill, tc.Helper.GH)
			opened := []string{}
			svc := newReplayService(t, host, helper, &opened)

			fn := replay[tc.Channel]
			if fn == nil {
				t.Fatalf("no replay for %s", tc.Channel)
			}
			got, err := fn(context.Background(), svc, tc.Args)

			if tc.Error != nil {
				if err == nil {
					t.Fatalf("want error %q, got result %s", *tc.Error, mustJSON(got))
				}
				if err.Error() != *tc.Error {
					t.Errorf("error:\n got  %q\n want %q", err.Error(), *tc.Error)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error %q", err)
				}
				want := canonical(t, json.RawMessage(tc.Result))
				have := canonical(t, got)
				if tc.Channel == "hosting:uploadSshKey" {
					// The TS returned GitHub's whole key object although its type said
					// `{ id }`; the Go port returns the typed `{ id }`.
					want = map[string]any{"id": want.(map[string]any)["id"]}
				}
				if !reflect.DeepEqual(have, want) {
					t.Errorf("result:\n got  %s\n want %s", mustJSON(have), mustJSON(want))
				}
			}

			gotReq, wantReq := sortRequests(host.requests), sortRequests(tc.Requests)
			if !reflect.DeepEqual(canonical(t, gotReq), canonical(t, wantReq)) {
				t.Errorf("requests:\n got  %s\n want %s", mustJSON(gotReq), mustJSON(wantReq))
			}
			if !reflect.DeepEqual(canonical(t, helper.calls), canonical(t, tc.Processes)) {
				t.Errorf("processes:\n got  %s\n want %s", mustJSON(helper.calls), mustJSON(tc.Processes))
			}
			if !reflect.DeepEqual(canonical(t, opened), canonical(t, tc.Opened)) {
				t.Errorf("opened:\n got  %v\n want %v", opened, tc.Opened)
			}
		})
	}
	for ch := range replay {
		if !seen[ch] {
			t.Errorf("no vectors for %s", ch)
		}
	}
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}
