package shared

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// tsdefaults.json is generated from src/shared/types.ts by
// test/port/vectors-settings.test.ts (PORT_WRITE_VECTORS=1), and that same
// test fails whenever the TS values drift from this file. Embedding the TS
// output instead of hand-copying it keeps defaultSettings() — which is large
// and edited often — identical on both sides by construction.
//
//go:embed tsdefaults.json
var tsDefaultsJSON []byte

// AIProviderPreset mirrors AIProviderPreset in src/shared/types.ts.
type AIProviderPreset struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Endpoint     string   `json:"endpoint"`
	DefaultModel string   `json:"defaultModel"`
	NeedsKey     bool     `json:"needsKey"`
	Transport    string   `json:"transport"`
	Models       []string `json:"models"`
	KeyURL       string   `json:"keyUrl,omitempty"`
}

type tsDefaults struct {
	DefaultSettings json.RawMessage    `json:"defaultSettings"`
	DefaultAIConfig json.RawMessage    `json:"defaultAIConfig"`
	AIProviders     []AIProviderPreset `json:"aiProviders"`
	AIFeatures      []string           `json:"aiFeatures"`
	TokenFields     []string           `json:"tokenFields"`
}

// Parsed once; read-only afterwards, so safe to share between goroutines.
var defaults = mustLoadDefaults()

func mustLoadDefaults() tsDefaults {
	var d tsDefaults
	if err := json.Unmarshal(tsDefaultsJSON, &d); err != nil {
		panic(fmt.Sprintf("shared: tsdefaults.json is corrupt: %v", err))
	}
	if len(d.AIProviders) == 0 || len(d.AIFeatures) == 0 || len(d.TokenFields) == 0 {
		panic("shared: tsdefaults.json is incomplete")
	}
	return d
}

func decodeObject(raw json.RawMessage) Object {
	v, err := DecodeJSON(raw)
	if err != nil {
		panic(fmt.Sprintf("shared: tsdefaults.json is corrupt: %v", err))
	}
	m, _ := v.(map[string]any)
	return m
}

// DefaultSettings is defaultSettings() from src/shared/types.ts: a fresh deep
// copy on every call, so callers may mutate it.
func DefaultSettings() Object { return decodeObject(defaults.DefaultSettings) }

// DefaultAIConfig is defaultAIConfig(): a fresh deep copy on every call.
func DefaultAIConfig() Object { return decodeObject(defaults.DefaultAIConfig) }

// AIProviders is AI_PROVIDERS, in order. The returned slice is a copy.
func AIProviders() []AIProviderPreset {
	out := make([]AIProviderPreset, len(defaults.AIProviders))
	copy(out, defaults.AIProviders)
	return out
}

// AIFeatures is AI_FEATURES, in order.
func AIFeatures() []string { return append([]string(nil), defaults.AIFeatures...) }

// TokenFields is TOKEN_FIELDS from src/shared/secrets.ts: the profile fields
// holding a host access token.
func TokenFields() []string { return append([]string(nil), defaults.TokenFields...) }

// ProviderPreset is aiProviderPreset(id): an unknown id — or one that is not a
// string at all — falls back to the last preset ("custom").
func ProviderPreset(id any) AIProviderPreset {
	if s, ok := id.(string); ok {
		for _, p := range defaults.AIProviders {
			if p.ID == s {
				return p
			}
		}
	}
	return defaults.AIProviders[len(defaults.AIProviders)-1]
}
