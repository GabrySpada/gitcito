// Package aicompleter adapts the merged internal/ai transport to the narrow
// contract repository chat and the wiki were written against
// (grounding.Completer and repochat.ActionSchemas), so main.go can wire them
// with one call each.
//
// The two sides decode model replies differently: internal/ai hands its
// validators ordered *ai.Obj trees, while the repochat/wiki validators read
// map[string]any trees (shared.DecodeJSON, json.Number numbers). Values are
// converted at this boundary, in both directions, by re-encoding JSON.
package aicompleter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GabrySpada/gitcito/internal/ai"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Completer is grounding.Completer over *ai.Client.
type Completer struct {
	Client *ai.Client
}

// New wraps client.
func New(client *ai.Client) *Completer { return &Completer{Client: client} }

// toTree re-reads any JSON-encodable value as a shared.DecodeJSON tree.
func toTree(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return shared.DecodeJSON(raw)
}

// abort carries a validator's I/O error out through internal/ai, whose
// Validate cannot return one. In TS that error rejected chatCompleteJson
// outright; recovering it here does the same.
type abort struct{ err error }

// ChatCompleteJSON implements grounding.Completer.
func (c *Completer) ChatCompleteJSON(ctx context.Context, cfg shared.Object, messages []g.ChatMessage, feature string, spec g.JSONSpec, temperature float64) (value any, err error) {
	var config ai.Config
	if cfg != nil {
		raw, merr := json.Marshal(cfg)
		if merr != nil {
			return nil, merr
		}
		if uerr := json.Unmarshal(raw, &config); uerr != nil {
			return nil, uerr
		}
	}
	schema := &ai.Obj{}
	if err := schema.UnmarshalJSON(spec.Schema); err != nil {
		return nil, fmt.Errorf("schema %s: %w", spec.Name, err)
	}
	turns := make([]ai.ChatMessage, len(messages))
	for i, m := range messages {
		turns[i] = ai.ChatMessage{Role: m.Role, Content: m.Content}
	}
	aiSpec := ai.JSONSpec{
		Name:                   spec.Name,
		Schema:                 schema,
		Strict:                 spec.Strict,
		NativeStructuredOutput: spec.NativeStructuredOutput,
		Validate: func(v any) []string {
			tree, terr := toTree(v)
			if terr != nil {
				return []string{"The reply was not valid JSON. Return a single JSON object and nothing else."}
			}
			errs, verr := spec.Validate(ctx, tree)
			if verr != nil {
				panic(abort{verr})
			}
			return errs
		},
	}
	if spec.MaxTokens != nil {
		n := float64(*spec.MaxTokens)
		aiSpec.MaxTokens = &n
	}
	if spec.Normalize != nil {
		aiSpec.Normalize = func(v any) any {
			tree, terr := toTree(v)
			if terr != nil {
				return v
			}
			return spec.Normalize(tree)
		}
	}

	defer func() {
		if r := recover(); r != nil {
			a, ok := r.(abort)
			if !ok {
				panic(r)
			}
			value, err = nil, a.err
		}
	}()
	out, err := c.Client.ChatCompleteJSON(ctx, config, turns, feature, aiSpec, temperature)
	if err != nil {
		var inv *ai.InvalidAIResponse
		if errors.As(err, &inv) {
			return nil, &g.InvalidAIResponse{Message: inv.Message, LastReply: inv.LastReply}
		}
		return nil, err
	}
	return toTree(out)
}

// Schemas is repochat.ActionSchemas over internal/ai's schema builders.
type Schemas struct{}

// AskActionsSchema is askActionsSchema(remote).
func (Schemas) AskActionsSchema(remote bool) json.RawMessage {
	raw, _ := ai.AskActionsSchemaFor(remote).MarshalJSON()
	return raw
}

// RepoChatActionsSchema is repoChatActionsSchema(remote).
func (Schemas) RepoChatActionsSchema(remote bool) json.RawMessage {
	raw, _ := ai.RepoChatActionsSchema(remote).MarshalJSON()
	return raw
}
