package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

// Replays testdata/vectors/aiTransport.json.
func TestTransportPureVectors(t *testing.T) {
	var v struct {
		Configs []struct {
			Name              string            `json:"name"`
			Cfg               Config            `json:"cfg"`
			TransportOf       string            `json:"transportOf"`
			BaseURL           string            `json:"baseUrl"`
			AuthHeaders       map[string]string `json:"authHeaders"`
			MissingCredential bool              `json:"missingCredential"`
		} `json:"configs"`
		IsLocal []struct {
			Input string `json:"input"`
			Out   bool   `json:"out"`
		} `json:"isLocal"`
		UsesMaxCompletionTokens []struct {
			Input string `json:"input"`
			Out   bool   `json:"out"`
		} `json:"usesMaxCompletionTokens"`
		ParseUsage []struct {
			Input json.RawMessage `json:"input"`
			Out   TokenUsage      `json:"out"`
		} `json:"parseUsage"`
		FetchFailureReason []struct {
			Cause struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"cause"`
			Out *string `json:"out"`
		} `json:"fetchFailureReason"`
	}
	readVectors(t, "testdata/vectors/aiTransport.json", &v)
	if len(v.Configs) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.Configs {
		if got := TransportOf(c.Cfg); got != c.TransportOf {
			t.Errorf("%s: transportOf = %q, want %q", c.Name, got, c.TransportOf)
		}
		if got := BaseURL(c.Cfg); got != c.BaseURL {
			t.Errorf("%s: baseUrl = %q, want %q", c.Name, got, c.BaseURL)
		}
		h := http.Header{}
		for _, x := range authHeaders(c.Cfg) {
			h.Set(x.name, x.value)
		}
		if got := redactHeaders(h); !reflect.DeepEqual(got, c.AuthHeaders) {
			t.Errorf("%s: authHeaders = %v, want %v", c.Name, got, c.AuthHeaders)
		}
		if got := MissingCredential(c.Cfg); got != c.MissingCredential {
			t.Errorf("%s: missingCredential = %v, want %v", c.Name, got, c.MissingCredential)
		}
	}
	for _, c := range v.IsLocal {
		if got := IsLocal(c.Input); got != c.Out {
			t.Errorf("isLocal(%q) = %v", c.Input, got)
		}
	}
	for _, c := range v.UsesMaxCompletionTokens {
		if got := UsesMaxCompletionTokens(c.Input); got != c.Out {
			t.Errorf("usesMaxCompletionTokens(%q) = %v", c.Input, got)
		}
	}
	for _, c := range v.ParseUsage {
		if got := ParseUsage(decodeJS(t, c.Input)); got != c.Out {
			t.Errorf("parseUsage(%s) = %+v, want %+v", c.Input, got, c.Out)
		}
	}
	for _, c := range v.FetchFailureReason {
		got := fetchFailureReason(&FetchError{Code: c.Cause.Code, Message: c.Cause.Message})
		if got != deref0(c.Out) {
			t.Errorf("fetchFailureReason(%+v) = %q, want %q", c.Cause, got, deref0(c.Out))
		}
	}
}

// Replays testdata/vectors/aiCli.json.
func TestCliPureVectors(t *testing.T) {
	var v struct {
		Binaries []string `json:"binaries"`
		Specs    []struct {
			Binary string              `json:"binary"`
			Bin    string              `json:"bin"`
			Label  string              `json:"label"`
			Models []string            `json:"models"`
			Args   map[string][]string `json:"args"`
		} `json:"specs"`
		FlattenMessages []struct {
			Messages []ChatMessage `json:"messages"`
			Out      string        `json:"out"`
		} `json:"flattenMessages"`
	}
	readVectors(t, "testdata/vectors/aiCli.json", &v)
	if !reflect.DeepEqual(v.Binaries, CliBinaries) {
		t.Errorf("binaries = %v, want %v", CliBinaries, v.Binaries)
	}
	for _, s := range v.Specs {
		spec := specFor(s.Binary)
		if spec.bin != s.Bin || spec.label != s.Label || !reflect.DeepEqual(CliModels(s.Binary), s.Models) {
			t.Errorf("%s: spec %s/%s/%v", s.Binary, spec.bin, spec.label, CliModels(s.Binary))
		}
		for model, want := range s.Args {
			if got := spec.args(model); !reflect.DeepEqual(got, want) {
				t.Errorf("%s args(%q) = %v, want %v", s.Binary, model, got, want)
			}
		}
	}
	for _, c := range v.FlattenMessages {
		if got := FlattenMessages(c.Messages); got != c.Out {
			t.Errorf("flattenMessages = %q, want %q", got, c.Out)
		}
	}
}

// Ported from test/pureLogic.test.ts, "AI transport — one wire layer per protocol".
func TestAITransport(t *testing.T) {
	account := func(provider, endpoint, key string) Config {
		cfg := Config{Provider: strPtr(provider), Endpoint: endpoint, APIKey: key}
		tr := TransportOf(cfg)
		cfg.Transport = &tr
		return cfg
	}
	t.Run("trims the legacy /v1 off an Anthropic endpoint instead of doubling it", func(t *testing.T) {
		cfg := account("anthropic", "https://api.anthropic.com/v1", "")
		if got := BaseURL(cfg); got != "https://api.anthropic.com" {
			t.Errorf("baseUrl = %q", got)
		}
		if got := TransportOf(cfg); got != "anthropic" {
			t.Errorf("transport = %q", got)
		}
	})
	t.Run("strips a full chat-completions URL back to the API root", func(t *testing.T) {
		if got := BaseURL(account("custom", "https://gateway.internal/v1/chat/completions/", "")); got != "https://gateway.internal/v1" {
			t.Errorf("baseUrl = %q", got)
		}
	})
	t.Run("routes Gemini through the OpenAI-compatible client", func(t *testing.T) {
		if got := TransportOf(account("google", "", "")); got != "openai" {
			t.Errorf("transport = %q", got)
		}
	})
	t.Run("authenticates each protocol the way that protocol expects", func(t *testing.T) {
		get := func(h []header, name string) (string, bool) {
			for _, x := range h {
				if x.name == name {
					return x.value, true
				}
			}
			return "", false
		}
		anthropic := authHeaders(account("anthropic", "", "sk-ant"))
		if v, _ := get(anthropic, "x-api-key"); v != "sk-ant" {
			t.Errorf("x-api-key = %q", v)
		}
		if v, _ := get(anthropic, "anthropic-version"); v != "2023-06-01" {
			t.Errorf("anthropic-version = %q", v)
		}
		// Sending a Bearer token as well is what made the old client look like
		// it worked right up until Anthropic rejected the request body.
		if _, ok := get(anthropic, "Authorization"); ok {
			t.Error("anthropic sends Authorization")
		}
		openai := authHeaders(account("openai", "", "sk-oai"))
		if v, _ := get(openai, "Authorization"); v != "Bearer sk-oai" {
			t.Errorf("Authorization = %q", v)
		}
		if _, ok := get(openai, "x-api-key"); ok {
			t.Error("openai sends x-api-key")
		}
	})
	t.Run("only demands a credential when the provider actually needs one", func(t *testing.T) {
		if !MissingCredential(account("openai", "", "")) || MissingCredential(account("openai", "", "sk-x")) {
			t.Error("openai")
		}
		// A local model and a signed-in CLI both authenticate elsewhere.
		if MissingCredential(account("ollama", "", "")) || MissingCredential(account("cli", "", "")) {
			t.Error("ollama/cli")
		}
	})
	t.Run("flattens a chat into one prompt for CLIs that take a single string", func(t *testing.T) {
		got := FlattenMessages([]ChatMessage{
			{Role: "system", Content: "Be terse."},
			{Role: "user", Content: "Why?"},
			{Role: "assistant", Content: "Because."},
			{Role: "user", Content: "Expand."},
		})
		if got != "Instructions:\nBe terse.\n\nWhy?\n\nYour previous reply:\nBecause.\n\nExpand." {
			t.Errorf("got %q", got)
		}
	})
}

// ─── test/aiJson.test.ts ────────────────────────────────────────────────────

func bodyOf(t *testing.T, r recordedRequest) *Obj {
	t.Helper()
	v, err := jsonParse(deref(r.Body))
	if err != nil {
		t.Fatal(err)
	}
	return asObj(v)
}

func okOpenAI(content string) fetchReply {
	body := jsonStringify(newObj("choices", []any{newObj("message", newObj("content", content))}))
	return fetchReply{Body: &body}
}

func statusReply(status int, body string) fetchReply { return fetchReply{Status: &status, Body: &body} }

func f64(f float64) *float64 { return &f }

func customLocal() Config {
	return Config{Provider: strPtr("custom"), Endpoint: "http://127.0.0.1:8000/v1", Model: "default", Transport: strPtr("openai")}
}

func TestAIJSONValidation(t *testing.T) {
	user := []ChatMessage{{Role: "user", Content: "Return JSON."}}
	always := func(any) []string { return []string{} }
	run := func(t *testing.T, cfg Config, spec JSONSpec, replies ...fetchReply) []recordedRequest {
		t.Helper()
		w := newWorld(t)
		w.fetch = replies
		if _, err := w.client(t.TempDir(), time.Now).ChatCompleteJSON(context.Background(), cfg, user, "test", spec, 0.2); err != nil {
			t.Fatal(err)
		}
		return w.requests
	}

	t.Run("awaits an asynchronous semantic validator", func(t *testing.T) {
		v, errs := ValidateJSONReply(`{"value":"x"}`, JSONSpec{Name: "async_test", Schema: newObj("type", "object"), Validate: func(v any) []string {
			if asObj(v).V("value") == "x" {
				return []string{}
			}
			return []string{"value must be x"}
		}})
		if len(errs) != 0 || jsonStringify(v) != `{"value":"x"}` {
			t.Errorf("got %s %q", jsonStringify(v), errs)
		}
	})
	t.Run("returns parse errors without invoking semantic validation", func(t *testing.T) {
		called := false
		_, errs := ValidateJSONReply("not json", JSONSpec{Name: "parse_test", Schema: newObj(), Validate: func(any) []string {
			called = true
			return nil
		}})
		if called || len(errs) == 0 || !strings.Contains(errs[0], "not valid JSON") {
			t.Errorf("called=%v errs=%q", called, errs)
		}
	})
	t.Run("accepts valid JSON after a self-hosted model think block", func(t *testing.T) {
		v, errs := ValidateJSONReply("<think>We need produce one object.</think>\n```json\n{\"value\":\"x\"}\n```",
			JSONSpec{Name: "thinking_test", Schema: newObj("type", "object"), Validate: always})
		if len(errs) != 0 || jsonStringify(v) != `{"value":"x"}` {
			t.Errorf("got %s %q", jsonStringify(v), errs)
		}
	})
	t.Run("bounds structured output and disables thinking for a custom endpoint", func(t *testing.T) {
		reqs := run(t, customLocal(), JSONSpec{Name: "bounded_test", Schema: newObj("type", "object"), Validate: always, MaxTokens: f64(2048)},
			okOpenAI(`{"value":"x"}`))
		if len(reqs) != 1 {
			t.Fatalf("%d requests", len(reqs))
		}
		b := bodyOf(t, reqs[0])
		if b.V("max_tokens") != 2048.0 || jsonStringify(b.V("chat_template_kwargs")) != `{"enable_thinking":false}` {
			t.Errorf("body %s", jsonStringify(b))
		}
	})
	t.Run("does not send the self-hosted thinking toggle to OpenAI", func(t *testing.T) {
		cfg := Config{Provider: strPtr("openai"), Endpoint: "http://127.0.0.1:8000/v1", APIKey: "test-key", Model: "gpt-test"}
		reqs := run(t, cfg, JSONSpec{Name: "hosted_test", Schema: newObj("type", "object"), Validate: always, MaxTokens: f64(256)},
			okOpenAI(`{"value":"x"}`))
		b := bodyOf(t, reqs[0])
		if len(reqs) != 1 || b.Has("chat_template_kwargs") || b.V("max_tokens") != 256.0 {
			t.Errorf("body %s", jsonStringify(b))
		}
	})
	for _, model := range []string{"o3-mini", "gpt-5-mini"} {
		t.Run("uses max_completion_tokens for reasoning model "+model, func(t *testing.T) {
			cfg := Config{Provider: strPtr("openai"), Endpoint: "http://127.0.0.1:8000/v1", APIKey: "test-key", Model: model}
			reqs := run(t, cfg, JSONSpec{Name: "reasoning_model_test", Schema: newObj("type", "object"), Validate: always, MaxTokens: f64(256)},
				okOpenAI(`{"value":"x"}`))
			b := bodyOf(t, reqs[0])
			if len(reqs) != 1 || b.V("max_completion_tokens") != 256.0 || b.Has("max_tokens") {
				t.Errorf("body %s", jsonStringify(b))
			}
		})
	}
	for _, c := range []struct {
		requested, expected float64
		name                string
	}{{99999.9, 8192, "99999.9"}, {-4.2, 1, "-4.2"}, {nan(), 8192, "NaN"}} {
		t.Run("sanitizes Anthropic max_tokens "+c.name+" to "+jsNumberString(c.expected), func(t *testing.T) {
			cfg := Config{Provider: strPtr("anthropic"), Endpoint: "http://127.0.0.1:8000", APIKey: "test-key", Model: "claude-test", Transport: strPtr("anthropic")}
			body := `{"content":[{"type":"text","text":"{\"value\":\"x\"}"}]}`
			reqs := run(t, cfg, JSONSpec{Name: "anthropic_token_limit_test", Schema: newObj("type", "object"), Validate: always, MaxTokens: f64(c.requested)},
				fetchReply{Body: &body})
			if b := bodyOf(t, reqs[0]); len(reqs) != 1 || b.V("max_tokens") != c.expected {
				t.Errorf("body %s", jsonStringify(b))
			}
		})
	}
	t.Run("can start in prompt-only JSON mode for an unreliable self-hosted schema adapter", func(t *testing.T) {
		native := false
		reqs := run(t, customLocal(), JSONSpec{Name: "prompt_only_test", Schema: newObj("type", "object"), Validate: always,
			MaxTokens: f64(1024), NativeStructuredOutput: &native}, okOpenAI(`{"value":"x"}`))
		b := bodyOf(t, reqs[0])
		if len(reqs) != 1 || b.Has("response_format") || !b.Has("chat_template_kwargs") || b.V("max_tokens") != 1024.0 {
			t.Errorf("body %s", jsonStringify(b))
		}
	})
	t.Run("retries without the thinking toggle when a strict custom gateway rejects it", func(t *testing.T) {
		reqs := run(t, customLocal(), JSONSpec{Name: "fallback_test", Schema: newObj("type", "object"), Validate: always, MaxTokens: f64(256)},
			statusReply(400, "unknown field chat_template_kwargs"), okOpenAI(`{"value":"x"}`))
		if len(reqs) != 2 || !bodyOf(t, reqs[0]).Has("chat_template_kwargs") || bodyOf(t, reqs[1]).Has("chat_template_kwargs") ||
			bodyOf(t, reqs[1]).V("max_tokens") != 256.0 {
			t.Errorf("requests %s", asJSON(reqs))
		}
	})
	t.Run("falls back to prompt-only JSON when native structured validation is exhausted", func(t *testing.T) {
		reqs := run(t, customLocal(), JSONSpec{Name: "validation_fallback_test", Schema: newObj("type", "object"), Validate: always, MaxTokens: f64(4096)},
			statusReply(502, `{"error":{"message":"DiffusionGemma failed to produce a schema-valid structured response after 3 attempt(s)","type":"structured_output_validation_error","code":502}}`),
			okOpenAI(`{"value":"x"}`))
		if len(reqs) != 2 || !bodyOf(t, reqs[0]).Has("response_format") || bodyOf(t, reqs[1]).Has("response_format") ||
			!bodyOf(t, reqs[1]).Has("chat_template_kwargs") || bodyOf(t, reqs[1]).V("max_tokens") != 4096.0 {
			t.Errorf("requests %s", asJSON(reqs))
		}
	})
	t.Run("also drops native structured output when a correction attempt exhausts it", func(t *testing.T) {
		reqs := run(t, customLocal(), JSONSpec{Name: "correction_fallback_test", Schema: newObj("type", "object"), MaxTokens: f64(512),
			Validate: func(v any) []string {
				if asObj(v).V("value") == "x" {
					return []string{}
				}
				return []string{"value must equal x"}
			}},
			okOpenAI("{}"),
			statusReply(502, `{"error":{"message":"DiffusionGemma failed to produce a schema-valid structured response after 3 attempt(s)","type":"structured_output_validation_error","code":502}}`),
			okOpenAI(`{"value":"x"}`))
		if len(reqs) != 3 || !bodyOf(t, reqs[0]).Has("response_format") || !bodyOf(t, reqs[1]).Has("response_format") ||
			bodyOf(t, reqs[2]).Has("response_format") || bodyOf(t, reqs[2]).V("max_tokens") != 512.0 {
			t.Errorf("requests %s", asJSON(reqs))
		}
	})
}

func nan() float64 { var z float64; return z / z }

// ─── Security ───────────────────────────────────────────────────────────────

// A sentinel key must never surface in an error the renderer shows, whatever
// fails: transport, status, parsing, validation, CLI.
func TestSentinelKeyNeverInErrors(t *testing.T) {
	const sentinel = "sk-SENTINEL-do-not-leak-7f3a"
	openai := Config{Provider: strPtr("openai"), APIKey: sentinel, Model: "gpt-4o-mini"}
	anthropic := Config{Provider: strPtr("anthropic"), APIKey: sentinel, Model: "claude", Transport: strPtr("anthropic")}
	refused := fetchReply{}
	refused.Network = &struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "ECONNREFUSED", Message: "connect ECONNREFUSED"}
	cases := []struct {
		name  string
		cfg   Config
		fetch []fetchReply
	}{
		{"network", openai, []fetchReply{refused}},
		{"anthropic network", anthropic, []fetchReply{refused}},
		{"status", openai, []fetchReply{statusReply(401, `{"error":"bad key"}`)}},
		{"anthropic status", anthropic, []fetchReply{statusReply(500, "boom")}},
		{"not json", openai, []fetchReply{statusReply(200, "<html>")}},
		{"invalid twice", openai, []fetchReply{okOpenAI("no"), okOpenAI("still no")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.fetch = c.fetch
			svc := NewService(w.client(t.TempDir(), time.Now))
			_, err := svc.GenerateBranchName(context.Background(), "x", c.cfg, BranchNameContext{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Errorf("error leaks the key: %v", err)
			}
			cat := svc.c.listAccountModels(context.Background(), c.cfg, true)
			if cat.Error != nil && strings.Contains(*cat.Error, sentinel) {
				t.Errorf("catalogue error leaks the key: %s", *cat.Error)
			}
		})
	}
	// The package logs nothing at all: no logger, no fmt.Print, no stderr.
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, banned := range []string{`"log"`, `"log/slog"`, "fmt.Print", "os.Stderr", "os.Stdout"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s uses %s", f, banned)
			}
		}
	}
}

// Real transport failures map onto the messages fetch's causes produced.
func TestRealNetworkFailures(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		c := NewClient(Options{DataDir: func() (string, error) { return t.TempDir(), nil }})
		cfg := Config{Provider: strPtr("custom"), Endpoint: "http://" + addr + "/v1", Model: "m"}
		_, err = c.ChatComplete(context.Background(), cfg, []ChatMessage{{Role: "user", Content: "x"}}, "t", 0.2, ModelCallOptions{})
		want := "Could not reach http://" + addr + "/v1. The endpoint refused the connection."
		if err == nil || err.Error() != want {
			t.Errorf("got %v, want %q", err, want)
		}
	})
	t.Run("untrusted certificate", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		srv.Config.ErrorLog = nil
		c := NewClient(Options{})
		cfg := Config{Provider: strPtr("custom"), Endpoint: srv.URL + "/v1", Model: "m", APIKey: "k"}
		_, err := c.ChatComplete(context.Background(), cfg, []ChatMessage{{Role: "user", Content: "x"}}, "t", 0.2, ModelCallOptions{})
		want := "Could not reach " + srv.URL + "/v1. A proxy or network certificate is self-signed, so Electron rejected the TLS connection."
		if err == nil || err.Error() != want {
			t.Errorf("got %v, want %q", err, want)
		}
	})
}

// ─── Real subprocesses ──────────────────────────────────────────────────────

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell scripts")
	}
	dir := t.TempDir()
	ctx := context.Background()
	r := ExecRunner{}
	t.Run("stdin in, exit code out", func(t *testing.T) {
		p := writeScript(t, dir, "echo.sh", "cat; echo err >&2; exit 3\n")
		res, err := r.Run(ctx, p, []string{"-x"}, "hello", time.Minute, cliMaxBuffer)
		if err != nil || res.Code != 3 || res.Stdout != "hello" || res.Stderr != "err\n" {
			t.Errorf("got %+v %v", res, err)
		}
	})
	t.Run("missing binary", func(t *testing.T) {
		res, err := r.Run(ctx, filepath.Join(dir, "nope"), nil, "", time.Minute, cliMaxBuffer)
		if err != nil || !res.NotFound {
			t.Errorf("got %+v %v", res, err)
		}
		res, err = r.Run(ctx, "gitcito-no-such-binary-on-path", nil, "", time.Minute, cliMaxBuffer)
		if err != nil || !res.NotFound {
			t.Errorf("PATH lookup: got %+v %v", res, err)
		}
	})
	t.Run("timeout kills it", func(t *testing.T) {
		p := writeScript(t, dir, "slow.sh", "sleep 30\n")
		start := time.Now()
		res, err := r.Run(ctx, p, nil, "", 200*time.Millisecond, cliMaxBuffer)
		if err != nil || !res.TimedOut || time.Since(start) > 10*time.Second {
			t.Errorf("got %+v %v after %v", res, err, time.Since(start))
		}
	})
	t.Run("output over the cap", func(t *testing.T) {
		p := writeScript(t, dir, "loud.sh", "i=0; while [ $i -lt 200 ]; do echo 0123456789; i=$((i+1)); done\n")
		res, err := r.Run(ctx, p, nil, "", time.Minute, 100)
		if err != nil || !res.Overflow {
			t.Errorf("got %+v %v", res, err)
		}
	})
	t.Run("an agent CLI end to end", func(t *testing.T) {
		// A stand-in `claude` that answers with the JSON envelope and echoes its
		// argv, proving the prompt goes in on stdin and never through a shell.
		p := writeScript(t, dir, "claude", `input=$(cat); printf '{"result":"%s|%s","usage":{"input_tokens":3,"output_tokens":4}}' "$*" "$(printf %s "$input" | wc -c | tr -d ' ')"`+"\n")
		c := NewClient(Options{})
		cfg := Config{Provider: strPtr("cli"), Cli: strPtr("claude"), CliPath: p, Model: "sonnet"}
		msg := "$(touch " + filepath.Join(dir, "pwned") + ") `id` ; rm -rf /"
		reply, err := c.CallModel(ctx, cfg, []ChatMessage{{Role: "user", Content: msg}}, 0.2, ModelCallOptions{})
		if err != nil {
			t.Fatal(err)
		}
		want := "-p --output-format json --model sonnet|" + itoa(len(msg))
		if reply.Text != want || reply.Usage.TotalTokens != 7 {
			t.Errorf("got %+v, want %q", reply, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "pwned")); !errors.Is(err, os.ErrNotExist) {
			t.Error("prompt reached a shell")
		}
	})
}

func itoa(n int) string { return jsNumberString(float64(n)) }

// The hover cache and the model catalogue are shared state; concurrent calls
// must be race-free (run with -race).
func TestConcurrentCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = rw.Write([]byte(`{"data":[{"id":"m1","created":5}]}`))
			return
		}
		_, _ = rw.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"s\",\"bullets\":[],\"lines\":[]}"}}]}`))
	}))
	defer srv.Close()
	c := NewClient(Options{DataDir: func() (string, error) { return t.TempDir(), nil }})
	svc := NewService(c)
	cfg := Config{Provider: strPtr("custom"), Endpoint: srv.URL + "/v1", Model: "m"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			req := HoverExplainRequest{Path: "a", Token: "t", Line: 1, Lines: []NumberedLine{{No: 1, Text: itoa(i % 4)}}}
			if _, err := svc.HoverExplain(context.Background(), req, cfg); err != nil {
				t.Error(err)
			}
		}(i)
		go func() {
			defer wg.Done()
			if cat, _ := svc.ListModels(context.Background(), cfg, true); cat.Source != "live" {
				t.Errorf("catalogue %+v", cat)
			}
		}()
	}
	wg.Wait()
}

// The facade exposes exactly the channels this package serves.
func TestFacadeIsExactlyTheChannels(t *testing.T) {
	want := []string{
		"CommitMessage", "DetectCli", "ExplainCode", "GenerateAppTheme", "GenerateBranchName", "GenerateCodeTheme",
		"GenerateConfig", "GenerateGraphPalette", "HoverExplain", "ListModels", "PlanActions", "PrDescription",
		"ResolveConflict", "ReviewPR", "SmartStage", "SuggestArtifacts",
	}
	typ := reflect.TypeOf(&Service{})
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("facade methods %v, want %v", got, want)
	}
}
