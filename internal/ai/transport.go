package ai

// The wire layer for every model call (src/main/aiTransport.ts).
//
// Gitcito used to speak one protocol — OpenAI's `POST /chat/completions` — at
// whatever endpoint the user configured. That quietly failed for Anthropic,
// whose API is `POST /v1/messages` with the system prompt hoisted out of the
// message list and `max_tokens` required. Providers now declare a transport in
// their preset and this module owns the differences, so the feature code above
// it never branches on provider.

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Config is the AIConfig the renderer resolves for one call (resolveAI's
// output). Only the fields the main process reads are declared; the rest of
// the object is ignored. Pointer fields are the ones whose absence TS tells
// apart from an empty value (`??`).
type Config struct {
	Provider            *string `json:"provider,omitempty"`
	Endpoint            string  `json:"endpoint"`
	APIKey              string  `json:"apiKey"`
	Model               string  `json:"model"`
	AccountID           string  `json:"accountId,omitempty"`
	Transport           *string `json:"transport,omitempty"`
	Cli                 *string `json:"cli,omitempty"`
	CliPath             string  `json:"cliPath,omitempty"`
	CommitStyle         string  `json:"commitStyle,omitempty"`
	ExplainStyle        string  `json:"explainStyle,omitempty"`
	ConflictStyle       string  `json:"conflictStyle,omitempty"`
	BranchNamingStyle   string  `json:"branchNamingStyle,omitempty"`
	CustomInstructions  string  `json:"customInstructions,omitempty"`
	GenerateDescription *bool   `json:"generateDescription,omitempty"`
}

// provider is `cfg.provider ?? 'custom'`.
func (c Config) provider() string {
	if c.Provider == nil {
		return "custom"
	}
	return *c.Provider
}

// cliBinary is `cfg.cli ?? 'claude'`.
func (c Config) cliBinary() string {
	if c.Cli == nil {
		return "claude"
	}
	return *c.Cli
}

// ChatMessage is one turn of a conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TokenUsage is the token counts of one reply (analytics' TokenUsage).
type TokenUsage struct {
	PromptTokens     float64 `json:"promptTokens"`
	CompletionTokens float64 `json:"completionTokens"`
	TotalTokens      float64 `json:"totalTokens"`
}

// ModelReply is a model's answer and what it cost.
type ModelReply struct {
	Text  string     `json:"text"`
	Usage TokenUsage `json:"usage"`
}

// ModelCallOptions are the per-call knobs.
type ModelCallOptions struct {
	// Provider-specific fields such as OpenAI's response_format.
	Extra *Obj
	// Bounds generation so a malformed structured reply cannot consume an
	// open-ended completion budget. nil means unset.
	MaxTokens *float64
	// Structured tasks need the answer, not a model's visible reasoning prelude.
	DisableThinking bool
}

// ParseUsage reads the token-usage block of OpenAI-compatible and native
// Anthropic replies.
func ParseUsage(raw any) TokenUsage {
	o := asObj(raw)
	pick := func(keys ...string) (float64, bool) {
		for _, k := range keys {
			if v, ok := o.Get(k); ok && v != nil {
				f, _ := v.(float64)
				return f, true
			}
		}
		return 0, false
	}
	prompt, _ := pick("prompt_tokens", "input_tokens")
	completion, _ := pick("completion_tokens", "output_tokens")
	total, ok := pick("total_tokens")
	if !ok {
		total = prompt + completion
	}
	return TokenUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
}

// TransportOf is the transport a config speaks, resolved from its provider
// when not stamped.
func TransportOf(cfg Config) string {
	if cfg.Transport != nil {
		return *cfg.Transport
	}
	return shared.ProviderPreset(cfg.provider()).Transport
}

var (
	trailingSlashes = regexp.MustCompile(`/+$`)
	chatCompletions = regexp.MustCompile(`/chat/completions$`)
	trailingV1      = regexp.MustCompile(`/v1$`)
	localURLRE      = regexp.MustCompile(`^https?://(localhost|127\.0\.0\.1|0\.0\.0\.0)([:/]|$)`)
)

// BaseURL normalizes the configured endpoint to the provider's API root.
//
// Anthropic's root has no `/v1` because the paths carry it; installs that
// predate this stored `https://api.anthropic.com/v1`, so it is trimmed rather
// than producing `/v1/v1/messages`.
func BaseURL(cfg Config) string {
	preset := shared.ProviderPreset(cfg.provider())
	raw := cfg.Endpoint
	if raw == "" {
		raw = preset.Endpoint
	}
	if raw == "" {
		raw = "https://api.openai.com/v1"
	}
	raw = chatCompletions.ReplaceAllLiteralString(trailingSlashes.ReplaceAllLiteralString(raw, ""), "")
	if TransportOf(cfg) == "anthropic" {
		return trailingV1.ReplaceAllLiteralString(raw, "")
	}
	return raw
}

// IsLocal reports whether url points at this machine.
func IsLocal(u string) bool { return localURLRE.MatchString(u) }

const anthropicVersion = "2023-06-01"

// Anthropic requires an explicit cap; large enough for a generated wiki page.
const anthropicMaxTokens = 8192

// header is one request header, in the order TS's object literal wrote it.
type header struct{ name, value string }

// AuthHeaders are the request headers for a config. Each protocol
// authenticates the way it expects: Anthropic with x-api-key, everything else
// with a bearer token.
func authHeaders(cfg Config) []header {
	h := []header{{"Content-Type", "application/json"}}
	key := jsTrim(cfg.APIKey)
	if key == "" {
		return h
	}
	if TransportOf(cfg) == "anthropic" {
		return append(h, header{"x-api-key", key}, header{"anthropic-version", anthropicVersion})
	}
	return append(h, header{"Authorization", "Bearer " + key})
}

// positiveInteger floors a finite value and clamps it to [1, maximum]; nil or
// non-finite takes the fallback.
func positiveInteger(value *float64, fallback, maximum float64) float64 {
	finite := fallback
	if value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) {
		finite = math.Floor(*value)
	}
	return math.Min(maximum, math.Max(1, finite))
}

const maxSafeInteger = 9007199254740991

var reasoningModelRE = regexp.MustCompile(`^(?:o[1-9](?:[-.]|$)|gpt-5(?:[-.]|$))`)

// UsesMaxCompletionTokens: OpenAI's reasoning families reject the legacy
// `max_tokens` field. Model ids may be provider-qualified (OpenRouter) or
// fine-tuned, so compare the base id.
func UsesMaxCompletionTokens(model string) bool {
	normalized := strings.ToLower(jsTrim(model))
	parts := strings.Split(normalized, "/")
	base := parts[len(parts)-1]
	if strings.HasPrefix(base, "ft:") {
		base = strings.Split(base[3:], ":")[0]
	}
	return reasoningModelRE.MatchString(base)
}

// FetchError is a network failure carrying a Node-style error code. The
// transport maps real Go errors onto these codes; tests inject them directly.
type FetchError struct {
	Code    string
	Message string
}

func (e *FetchError) Error() string { return e.Message }

// failureCause maps a transport error to the `cause` fetch would have
// reported: a Node error code and message.
func failureCause(err error) (code, message string) {
	var fe *FetchError
	if errors.As(err, &fe) {
		return fe.Code, fe.Message
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "ECONNREFUSED", ""
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return "ENOTFOUND", ""
	}
	// Go does not separate a self-signed root in the chain from a missing
	// issuer; an untrusted chain is almost always the former (a corporate
	// TLS-inspecting proxy), which is what this message is for.
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return "SELF_SIGNED_CERT_IN_CHAIN", ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return "", ue.Err.Error()
	}
	return "", err.Error()
}

// fetchFailureReason is the human reason for a failed fetch, or "".
func fetchFailureReason(err error) string {
	code, message := failureCause(err)
	return reasonForCode(code, message)
}

func reasonForCode(code, message string) string {
	switch code {
	case "SELF_SIGNED_CERT_IN_CHAIN":
		return "A proxy or network certificate is self-signed, so Electron rejected the TLS connection."
	case "UNABLE_TO_VERIFY_LEAF_SIGNATURE":
		return "Electron could not verify the provider certificate chain."
	case "ECONNREFUSED":
		return "The endpoint refused the connection."
	case "ENOTFOUND":
		return "The endpoint host could not be resolved."
	}
	return message
}

// MissingCredential is true when the account cannot authenticate and the
// provider demands it.
func MissingCredential(cfg Config) bool {
	if TransportOf(cfg) == "cli" {
		return false
	}
	if jsTrim(cfg.APIKey) != "" {
		return false
	}
	return !IsLocal(BaseURL(cfg))
}

// transport performs model calls. It owns the HTTP client and the CLI runner.
type transport struct {
	client *http.Client
	runner CommandRunner
}

type response struct {
	status int
	body   []byte
}

func (r response) ok() bool { return r.status >= 200 && r.status <= 299 }

func (r response) text() string { return decodeText(r.body) }

// json is `await res.json()`.
func (r response) json() (any, error) { return jsonParse(r.text()) }

func (t *transport) do(ctx context.Context, method, u string, headers []header, body []byte) (response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return response{}, err
	}
	for _, h := range headers {
		req.Header.Set(h.name, h.value)
	}
	res, err := t.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return response{}, err
	}
	return response{status: res.StatusCode, body: b}, nil
}

func couldNotReach(base string, err error) error {
	reason := fetchFailureReason(err)
	if reason != "" {
		reason = " " + reason
	}
	return errors.New("Could not reach " + base + "." + reason)
}

func readError(res response) error {
	return errors.New("AI request failed (" + strconv.Itoa(res.status) + "): " + sliceUTF16(res.text(), 200))
}

// errReadingNull is V8's TypeError for `null.<prop>`.
func errReadingNull(prop string) error {
	return errors.New("Cannot read properties of null (reading '" + prop + "')")
}

func messagesValue(messages []ChatMessage) []any {
	out := make([]any, len(messages))
	for i, m := range messages {
		out[i] = newObj("role", m.Role, "content", m.Content)
	}
	return out
}

func (t *transport) callOpenAI(ctx context.Context, cfg Config, messages []ChatMessage, temperature float64, opts ModelCallOptions) (ModelReply, error) {
	base := BaseURL(cfg)
	tokenField := "max_tokens"
	if UsesMaxCompletionTokens(cfg.Model) {
		tokenField = "max_completion_tokens"
	}
	provider := cfg.provider()
	mayDisableThinking := opts.DisableThinking && (provider == "custom" || provider == "ollama")
	payload := newObj("model", cfg.Model, "temperature", temperature, "messages", messagesValue(messages))
	if opts.MaxTokens != nil {
		payload.Set(tokenField, positiveInteger(opts.MaxTokens, 1, maxSafeInteger))
	}
	if mayDisableThinking {
		payload.Set("chat_template_kwargs", newObj("enable_thinking", false))
	}
	if opts.Extra != nil {
		for _, k := range opts.Extra.Keys() {
			payload.Set(k, opts.Extra.vals[k])
		}
	}

	post := func(body *Obj) (response, error) {
		res, err := t.do(ctx, http.MethodPost, base+"/chat/completions", authHeaders(cfg), []byte(jsonStringify(body)))
		if err != nil {
			return response{}, couldNotReach(base, err)
		}
		return res, nil
	}

	res, err := post(payload)
	if err != nil {
		return ModelReply{}, err
	}
	if !res.ok() && mayDisableThinking && (res.status == 400 || res.status == 422) {
		body := res.text()
		lower := asciiLower(body)
		if strings.Contains(lower, "chat_template_kwargs") || strings.Contains(lower, "enable_thinking") {
			// Strict OpenAI-compatible gateways may reject this self-hosted-model
			// extension. Retry once without it instead of breaking that provider.
			compatible := payload.Clone()
			compatible.Delete("chat_template_kwargs")
			if res, err = post(compatible); err != nil {
				return ModelReply{}, err
			}
		} else {
			return ModelReply{}, errors.New("AI request failed (" + strconv.Itoa(res.status) + "): " + sliceUTF16(body, 200))
		}
	}
	if !res.ok() {
		return ModelReply{}, readError(res)
	}
	j, err := res.json()
	if err != nil {
		return ModelReply{}, err
	}
	if j == nil {
		return ModelReply{}, errReadingNull("choices")
	}
	root := asObj(j)
	var content any
	if choices, ok := root.V("choices").([]any); ok && len(choices) > 0 {
		if msg := asObj(asObj(choices[0]).V("message")); msg != nil {
			content = msg.V("content")
		}
	}
	usage, _ := root.Get("usage")
	return ModelReply{Text: replyText(content), Usage: ParseUsage(usage)}, nil
}

// replyText is `content ?? ”`. A non-string content (a provider returning
// content parts) would make the TS callers throw on `.trim()`; it is read as
// its string form instead.
func replyText(content any) string {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	}
	return jsString(content)
}

// callAnthropic speaks the native Anthropic Messages API.
//
// System turns are concatenated into the top-level `system` field — Anthropic
// rejects a `system` role inside `messages` — and `response_format` is
// dropped, since structured output is negotiated differently there. Callers
// that need JSON already fall back to prompt-only JSON with a validation
// retry, which is the same path every non-OpenAI provider takes.
func (t *transport) callAnthropic(ctx context.Context, cfg Config, messages []ChatMessage, temperature float64, opts ModelCallOptions) (ModelReply, error) {
	base := BaseURL(cfg)
	var system []string
	var turns []ChatMessage
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, m.Content)
		} else {
			turns = append(turns, m)
		}
	}
	// The API rejects an empty message list, and a system-only prompt is a
	// real shape for a few callers.
	if len(turns) == 0 {
		turns = append(turns, ChatMessage{Role: "user", Content: "Proceed."})
	}
	body := newObj(
		"model", cfg.Model,
		"max_tokens", positiveInteger(opts.MaxTokens, anthropicMaxTokens, anthropicMaxTokens),
		"temperature", temperature,
	)
	if s := strings.Join(system, "\n\n"); s != "" {
		body.Set("system", s)
	}
	body.Set("messages", messagesValue(turns))

	res, err := t.do(ctx, http.MethodPost, base+"/v1/messages", authHeaders(cfg), []byte(jsonStringify(body)))
	if err != nil {
		return ModelReply{}, couldNotReach(base, err)
	}
	if !res.ok() {
		return ModelReply{}, readError(res)
	}
	j, err := res.json()
	if err != nil {
		return ModelReply{}, err
	}
	if j == nil {
		return ModelReply{}, errReadingNull("content")
	}
	root := asObj(j)
	blocks, _ := root.V("content").([]any)
	var parts []any
	for _, b := range blocks {
		block := asObj(b)
		if block == nil {
			continue
		}
		text, hasText := block.Get("text")
		_, isStr := text.(string)
		if block.V("type") == "text" || isStr {
			if !hasText {
				text = nil
			}
			parts = append(parts, text)
		}
	}
	usage, _ := root.Get("usage")
	return ModelReply{Text: jsJoin(parts, ""), Usage: ParseUsage(usage)}, nil
}

// CallModel is one model call, whatever the provider speaks.
func (t *transport) CallModel(ctx context.Context, cfg Config, messages []ChatMessage, temperature float64, opts ModelCallOptions) (ModelReply, error) {
	switch TransportOf(cfg) {
	case "anthropic":
		return t.callAnthropic(ctx, cfg, messages, temperature, opts)
	case "cli":
		return t.runCliModel(ctx, cfg, messages)
	default:
		return t.callOpenAI(ctx, cfg, messages, temperature, opts)
	}
}
