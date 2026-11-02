package ai

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/paths"
)

// KeySource resolves a stored API key. *settings.Store satisfies it.
type KeySource interface {
	// ActiveProfileAiKey is the trimmed key of one AI account ("" = default
	// account), or "" when none is configured.
	ActiveProfileAiKey(ctx context.Context, accountID string) (string, error)
}

// UsageRecorder receives token usage per feature (analytics' recordAIUsage).
// It is called synchronously after every model reply; implementations should
// not block.
type UsageRecorder interface {
	RecordAIUsage(feature, model string, usage TokenUsage)
}

// Options are the client's dependencies. Zero values take production defaults.
type Options struct {
	// HTTPClient performs provider requests. Default: a client with no overall
	// timeout (fetch had none), honouring the environment's proxy.
	HTTPClient *http.Client
	// Runner launches agent CLIs. Default: ExecRunner.
	Runner CommandRunner
	// Keys fills in API keys the renderer does not hold. nil: none.
	Keys KeySource
	// Usage records token usage. nil: discarded.
	Usage UsageRecorder
	// DataDir is where gitcito-models.json lives. Default: paths.DataDir.
	DataDir func() (string, error)
	// Now is the clock for the catalogue cache. Default: time.Now.
	Now func() time.Time
	// GOOS picks `which` or `where` for CLI detection. Default: runtime.GOOS.
	GOOS string
}

// Client runs the AI features. The facade (Service) binds one per channel;
// repository chat and the wiki use ChatCompleteJSON and CallModel directly.
type Client struct {
	t       *transport
	keys    KeySource
	usage   UsageRecorder
	dataDir func() (string, error)
	now     func() time.Time
	goos    string

	models modelCache

	hoverMu    sync.Mutex
	hoverCache map[string]HoverExplainResult
	hoverOrder []string
}

// NewClient builds a client.
func NewClient(opts Options) *Client {
	c := &Client{
		t:          &transport{client: opts.HTTPClient, runner: opts.Runner},
		keys:       opts.Keys,
		usage:      opts.Usage,
		dataDir:    opts.DataDir,
		now:        opts.Now,
		goos:       opts.GOOS,
		hoverCache: map[string]HoverExplainResult{},
	}
	if c.t.client == nil {
		c.t.client = &http.Client{}
	}
	if c.t.runner == nil {
		c.t.runner = ExecRunner{}
	}
	if c.dataDir == nil {
		c.dataDir = paths.DataDir
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.goos == "" {
		c.goos = defaultGOOS()
	}
	return c
}

// CallModel is one model call, whatever the provider speaks. No stored-key
// resolution and no usage recording: see ChatComplete for that.
func (c *Client) CallModel(ctx context.Context, cfg Config, messages []ChatMessage, temperature float64, opts ModelCallOptions) (ModelReply, error) {
	return c.t.CallModel(ctx, cfg, messages, temperature, opts)
}

// withStoredKey fills in the API key the renderer does not have.
//
// Stored keys are decrypted lazily — only opening Settings hydrates them into
// the renderer — so a config arriving from any other surface (commit message,
// hover explain, conflict resolve, …) is keyless on a fresh launch. Resolve it
// from the encrypted store at the point of use rather than failing.
//
// cfg.AccountID says which account the renderer resolved, so the key that
// comes back belongs to that account rather than to whichever one is default.
func (c *Client) withStoredKey(ctx context.Context, cfg Config) (Config, error) {
	if jsTrim(cfg.APIKey) != "" || c.keys == nil {
		return cfg, nil
	}
	key, err := c.keys.ActiveProfileAiKey(ctx, cfg.AccountID)
	if err != nil {
		return cfg, err
	}
	if key = jsTrim(key); key != "" {
		cfg.APIKey = key
	}
	return cfg, nil
}

// ErrNoAPIKey is returned when an account needs a key and has none.
//
//lint:ignore ST1005 shown to the user verbatim, as in TS
var ErrNoAPIKey = errors.New("No AI API key configured. Add one in Settings → AI.")

// ChatComplete is one chat completion against the account's provider, as raw
// message text. feature names the usage bucket.
func (c *Client) ChatComplete(ctx context.Context, input Config, messages []ChatMessage, feature string, temperature float64, opts ModelCallOptions) (string, error) {
	cfg, err := c.withStoredKey(ctx, input)
	if err != nil {
		return "", err
	}
	if MissingCredential(cfg) {
		return "", ErrNoAPIKey
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	reply, err := c.t.CallModel(ctx, cfg, messages, temperature, opts)
	if err != nil {
		return "", err
	}
	if c.usage != nil {
		c.usage.RecordAIUsage(feature, cfg.Model, reply.Usage)
	}
	return reply.Text, nil
}

// JSONSpec is a JSON contract the model must satisfy before its output is
// accepted.
type JSONSpec struct {
	// Schema name, for providers with native structured output.
	Name   string
	Schema *Obj
	// Returns one correction line per problem; empty means usable.
	Validate func(value any) []string
	// Off for schemas with optional or union-shaped fields. nil = on.
	Strict *bool
	// Maximum completion budget for this JSON shape. nil = unbounded.
	MaxTokens *float64
	// Skip provider-native json_schema when a self-hosted model handles prompt
	// JSON more reliably. nil = use it.
	NativeStructuredOutput *bool
	// Safely removes unusable optional entries before semantic validation.
	Normalize func(value any) any
}

// InvalidAIResponse is returned when the model's output is still unusable
// after the correction retry.
type InvalidAIResponse struct {
	Message string
	// The rejected reply, for callers that can still salvage something.
	LastReply string
}

func (e *InvalidAIResponse) Error() string { return e.Message }

// ValidateJSONReply parses and semantically validates one provider reply
// without provider I/O.
func ValidateJSONReply(text string, spec JSONSpec) (any, []string) {
	parsed, ok := ParseLooseJSON(text)
	if !ok {
		return nil, []string{"The reply was not valid JSON. Return a single JSON object and nothing else."}
	}
	value := parsed
	if spec.Normalize != nil {
		value = spec.Normalize(parsed)
	}
	return value, spec.Validate(value)
}

var (
	rejectedStatusRE  = regexp.MustCompile(`failed \(4\d\d\)`)
	rejectedFormatRE  = regexp.MustCompile(`response_format|json[_ ]schema|structured`)
	exhaustedSchemaRE = regexp.MustCompile(`structured_output_validation_error|failed to produce a schema-valid structured response`)
)

// rejectsJSONSchema is true when native json_schema was rejected or exhausted
// its own validation retries.
func rejectsJSONSchema(err error) bool {
	msg := err.Error()
	lower := asciiLower(msg)
	rejected := rejectedStatusRE.MatchString(msg) && rejectedFormatRE.MatchString(lower)
	return rejected || exhaustedSchemaRE.MatchString(lower)
}

func correctionMessage(errs []string) string {
	lines := make([]string, len(errs))
	for i, e := range errs {
		lines[i] = "- " + e
	}
	return "Your previous reply was rejected for these reasons:\n" + strings.Join(lines, "\n") +
		"\n\nReply again with the corrected JSON object only — no explanation, no markdown fences."
}

// ChatCompleteJSON asks for JSON, validates it against spec, and re-prompts
// once with the validation errors before giving up. Providers that don't
// support native structured output silently fall back to prompt-only JSON.
// The value is the decoded tree (see ParseLooseJSON).
func (c *Client) ChatCompleteJSON(ctx context.Context, cfg Config, messages []ChatMessage, feature string, spec JSONSpec, temperature float64) (any, error) {
	strict := spec.Strict == nil || *spec.Strict
	opts := ModelCallOptions{DisableThinking: true, MaxTokens: spec.MaxTokens}
	if spec.NativeStructuredOutput == nil || *spec.NativeStructuredOutput {
		opts.Extra = newObj("response_format", newObj(
			"type", "json_schema",
			"json_schema", newObj("name", spec.Name, "strict", strict, "schema", spec.Schema),
		))
	}
	complete := func(turns []ChatMessage) (string, error) {
		text, err := c.ChatComplete(ctx, cfg, turns, feature, temperature, opts)
		if err == nil {
			return text, nil
		}
		if opts.Extra == nil || !rejectsJSONSchema(err) {
			return "", err
		}
		// Once dropped, native structured output stays off for the retry too.
		opts.Extra = nil
		return c.ChatComplete(ctx, cfg, turns, feature, temperature, opts)
	}

	raw, err := complete(messages)
	if err != nil {
		return nil, err
	}
	value, errs := ValidateJSONReply(raw, spec)
	if len(errs) == 0 && value != nil {
		return value, nil
	}
	retry := append(append([]ChatMessage{}, messages...),
		ChatMessage{Role: "assistant", Content: raw},
		ChatMessage{Role: "user", Content: correctionMessage(errs)},
	)
	if raw, err = complete(retry); err != nil {
		return nil, err
	}
	value, errs = ValidateJSONReply(raw, spec)
	if len(errs) == 0 && value != nil {
		return value, nil
	}
	first := ""
	if len(errs) > 0 {
		first = errs[0]
	}
	return nil, &InvalidAIResponse{Message: "The AI returned an invalid response: " + first, LastReply: raw}
}

// chatCompleteChecked is the same contract for replies that are not JSON:
// ask, check, and re-prompt once with the problem before giving up.
func (c *Client) chatCompleteChecked(ctx context.Context, cfg Config, messages []ChatMessage, feature string, validate func(string) []string, temperature float64) (string, error) {
	raw, err := c.ChatComplete(ctx, cfg, messages, feature, temperature, ModelCallOptions{})
	if err != nil {
		return "", err
	}
	errs := validate(raw)
	if len(errs) == 0 {
		return raw, nil
	}
	retry := append(append([]ChatMessage{}, messages...),
		ChatMessage{Role: "assistant", Content: raw},
		ChatMessage{Role: "user", Content: "Your previous reply was rejected: " + strings.Join(errs, " ") + "\n\nReply again with the corrected result only."},
	)
	if raw, err = c.ChatComplete(ctx, cfg, retry, feature, temperature, ModelCallOptions{}); err != nil {
		return "", err
	}
	if errs = validate(raw); len(errs) == 0 {
		return raw, nil
	}
	return "", &InvalidAIResponse{Message: "The AI returned an invalid response: " + errs[0], LastReply: raw}
}

// clip truncates text to max UTF-16 units, marking the cut.
func clip(text string, max int) string {
	if utf16Len(text) > max {
		return sliceUTF16(text, max) + "\n…(truncated)"
	}
	return text
}
