package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fakes the I/O vectors replay against: an httptest.Server that serves
// canned provider replies and records what reached it on the wire, a CLI
// runner that records argv/stdin, a key source and a usage recorder.

type fetchReply struct {
	Status  *int    `json:"status"`
	Body    *string `json:"body"`
	Network *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"network"`
}

type cliReply struct {
	Code      int    `json:"code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Enoent    bool   `json:"enoent"`
	Killed    bool   `json:"killed"`
	Signal    string `json:"signal"`
	MaxBuffer bool   `json:"maxBuffer"`
}

type recordedRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

type recordedProc struct {
	File      string   `json:"file"`
	Args      []string `json:"args"`
	Timeout   float64  `json:"timeout"`
	MaxBuffer float64  `json:"maxBuffer"`
	Input     *string  `json:"input"`
}

type usageCall struct {
	Feature string     `json:"feature"`
	Model   string     `json:"model"`
	Usage   TokenUsage `json:"usage"`
}

const (
	hdrReply = "X-Test-Reply"
	hdrURL   = "X-Test-Url"
)

type fakeWorld struct {
	t   *testing.T
	mu  sync.Mutex
	srv *httptest.Server

	fetch    []fetchReply
	requests []recordedRequest

	cli   []cliReply
	procs []recordedProc

	storedKey string
	keyError  string
	keyCalls  []*string
	usage     []usageCall
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var schemeRE = regexp.MustCompile(`^(\S+) (.*)$`)

// redactHeaders lowercases and reduces secrets to scheme + sha256, exactly as
// the TS capture does. Headers the Go transport adds on its own are not part
// of the contract.
func redactHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for name, vals := range h {
		n := strings.ToLower(name)
		switch n {
		case "user-agent", "accept-encoding", "content-length", strings.ToLower(hdrReply), strings.ToLower(hdrURL):
			continue
		}
		v := strings.Join(vals, ", ")
		switch n {
		case "authorization":
			if m := schemeRE.FindStringSubmatch(v); m != nil {
				v = m[1] + " <sha256:" + sha(m[2]) + ">"
			} else {
				v = "<sha256:" + sha(v) + ">"
			}
		case "x-api-key", "x-goog-api-key":
			v = "<sha256:" + sha(v) + ">"
		}
		out[n] = v
	}
	return out
}

func newWorld(t *testing.T) *fakeWorld {
	w := &fakeWorld{t: t}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := recordedRequest{Method: r.Method, URL: r.Header.Get(hdrURL), Headers: redactHeaders(r.Header)}
		if len(body) > 0 || r.Method != http.MethodGet {
			s := string(body)
			rec.Body = &s
		}
		idx, _ := strconv.Atoi(r.Header.Get(hdrReply))
		w.mu.Lock()
		w.requests = append(w.requests, rec)
		w.mu.Unlock()
		reply := w.replyAt(idx)
		status := 200
		if reply.Status != nil {
			status = *reply.Status
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		if reply.Body != nil {
			_, _ = io.WriteString(rw, *reply.Body)
		}
	}))
	t.Cleanup(w.srv.Close)
	return w
}

var replyLog sync.Map // index -> fetchReply, per world (indices are unique per process)

var replySeq struct {
	sync.Mutex
	n int
}

func (w *fakeWorld) replyAt(idx int) fetchReply {
	v, _ := replyLog.Load(idx)
	r, _ := v.(fetchReply)
	return r
}

// RoundTrip pops the next canned reply: a network failure is returned as the
// FetchError fetch would have carried; anything else goes over real HTTP to
// the httptest server, keeping the original URL for the record.
func (w *fakeWorld) RoundTrip(req *http.Request) (*http.Response, error) {
	w.mu.Lock()
	if len(w.fetch) == 0 {
		w.mu.Unlock()
		// The TS fake threw for a missing reply; record the request the same way.
		body := w.readBody(req)
		w.mu.Lock()
		w.requests = append(w.requests, recordedRequest{Method: req.Method, URL: req.URL.String(), Headers: redactHeaders(req.Header), Body: body})
		w.mu.Unlock()
		return nil, &FetchError{Message: "no canned fetch reply"}
	}
	reply := w.fetch[0]
	w.fetch = w.fetch[1:]
	w.mu.Unlock()
	if reply.Network != nil {
		body := w.readBody(req)
		w.mu.Lock()
		w.requests = append(w.requests, recordedRequest{Method: req.Method, URL: req.URL.String(), Headers: redactHeaders(req.Header), Body: body})
		w.mu.Unlock()
		return nil, &FetchError{Code: reply.Network.Code, Message: reply.Network.Message}
	}
	replySeq.Lock()
	replySeq.n++
	idx := replySeq.n
	replySeq.Unlock()
	replyLog.Store(idx, reply)
	out := req.Clone(req.Context())
	out.Header.Set(hdrURL, req.URL.String())
	out.Header.Set(hdrReply, strconv.Itoa(idx))
	u := *req.URL
	u.Scheme = "http"
	u.Host = strings.TrimPrefix(w.srv.URL, "http://")
	out.URL = &u
	out.Host = ""
	return http.DefaultTransport.RoundTrip(out)
}

func (w *fakeWorld) readBody(req *http.Request) *string {
	if req.Body == nil {
		if req.Method == http.MethodGet {
			return nil
		}
		s := ""
		return &s
	}
	b, _ := io.ReadAll(req.Body)
	s := string(b)
	return &s
}

// Run implements CommandRunner.
func (w *fakeWorld) Run(_ context.Context, file string, args []string, stdin string, timeout time.Duration, maxBuffer int) (CommandResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	in := stdin
	w.procs = append(w.procs, recordedProc{
		File: file, Args: append([]string{}, args...), Timeout: float64(timeout.Milliseconds()), MaxBuffer: float64(maxBuffer), Input: &in,
	})
	if len(w.cli) == 0 {
		return CommandResult{Code: 99, Stderr: "no canned reply"}, nil
	}
	r := w.cli[0]
	w.cli = w.cli[1:]
	res := CommandResult{Stdout: r.Stdout, Stderr: r.Stderr, Code: r.Code}
	switch {
	case r.Enoent:
		res = CommandResult{NotFound: true}
	case r.Killed:
		res.TimedOut = true
	case r.Signal != "":
		res.Signaled = true
	case r.MaxBuffer:
		res.Overflow = true
	}
	return res, nil
}

// ActiveProfileAiKey implements KeySource.
func (w *fakeWorld) ActiveProfileAiKey(_ context.Context, accountID string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var id *string
	if accountID != "" {
		id = &accountID
	}
	w.keyCalls = append(w.keyCalls, id)
	if w.keyError != "" {
		return "", errors.New(w.keyError)
	}
	return w.storedKey, nil
}

// RecordAIUsage implements UsageRecorder.
func (w *fakeWorld) RecordAIUsage(feature, model string, usage TokenUsage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.usage = append(w.usage, usageCall{Feature: feature, Model: model, Usage: usage})
}

func (w *fakeWorld) client(dataDir string, now func() time.Time) *Client {
	return NewClient(Options{
		HTTPClient: &http.Client{Transport: w},
		Runner:     w,
		Keys:       w,
		Usage:      w,
		DataDir:    func() (string, error) { return dataDir, nil },
		Now:        now,
		GOOS:       "linux",
	})
}

// channelMethod maps "ai:commitMessage" to the facade method CommitMessage.
func channelMethod(channel string) string {
	name := strings.TrimPrefix(channel, "ai:")
	return strings.ToUpper(name[:1]) + name[1:]
}

type callResult struct {
	Value json.RawMessage `json:"value,omitempty"`
	Error *string         `json:"error,omitempty"`
}
