package grounding

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// The slice of the AI transport (src/main/ai.ts) that repository chat and the
// wiki call: chatCompleteJson and its InvalidAIResponse. internal/ai is ported
// separately; it satisfies Completer (or is adapted to it) where main.go wires
// the services. Kept here because internal/ai imports this package for
// ParseLooseJSON and the validators, so the contract cannot live in ai without
// an import cycle for the packages that only need the call.

// ChatMessage is aiTransport's ChatMessage: one turn sent to the provider.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// JSONSpec is ai.ts's JsonSpec: the JSON contract a reply must satisfy before
// it is accepted.
type JSONSpec struct {
	// Name is the schema name, for providers with native structured output.
	Name string
	// Schema is the JSON schema exactly as the TS JSON.stringify'd it — the
	// bytes go into the request body.
	Schema json.RawMessage
	// Validate returns one correction line per problem; empty means usable.
	// value is the decoded reply (after Normalize). An error aborts the call.
	Validate func(ctx context.Context, value any) ([]string, error)
	// Strict is `strict` (nil = the TS default, true). Off for schemas with
	// optional or union-shaped fields.
	Strict *bool
	// MaxTokens is the completion budget for this shape (nil = provider default).
	MaxTokens *int
	// NativeStructuredOutput false skips provider-native json_schema (nil = default).
	NativeStructuredOutput *bool
	// Normalize safely removes unusable optional entries before validation.
	Normalize func(value any) any
}

// Completer is chatCompleteJson(cfg, messages, feature, spec, temperature):
// ask for JSON, validate it against spec, re-prompt once with the validation
// errors, and return the accepted decoded value — or an *InvalidAIResponse
// when the reply is still unusable. cfg is the renderer's AIConfig as decoded
// JSON. Implementations must be safe for concurrent use: the wiki writes three
// pages at once.
type Completer interface {
	ChatCompleteJSON(ctx context.Context, cfg shared.Object, messages []ChatMessage, feature string, spec JSONSpec, temperature float64) (any, error)
}

// InvalidAIResponse is ai.ts's InvalidAIResponse: the model's JSON was still
// unusable after the correction retry.
type InvalidAIResponse struct {
	Message string
	// LastReply is the rejected reply, for callers that can still salvage it.
	LastReply string
}

func (e *InvalidAIResponse) Error() string { return e.Message }

// IsInvalidAIResponse is `err instanceof InvalidAIResponse`.
func IsInvalidAIResponse(err error) bool {
	var inv *InvalidAIResponse
	return errors.As(err, &inv)
}

// Bool returns a pointer to b, for JSONSpec's optional flags.
func Bool(b bool) *bool { return &b }
