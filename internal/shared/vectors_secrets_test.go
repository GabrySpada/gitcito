package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The vector files are written by test/port/vectors-settings.test.ts from the
// TS implementation; vitest fails when TS drifts from them, these tests fail
// when Go does.

func vectorPath(name string) string {
	return filepath.Join("..", "..", "testdata", "vectors", name)
}

func loadVector(t *testing.T, name string) Object {
	t.Helper()
	raw, err := os.ReadFile(vectorPath(name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	v, err := DecodeJSON(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return v.(Object)
}

// canon turns any value into plain decoded JSON (float64 numbers), so outputs
// compare by JSON meaning only — exactly what crosses the bridge.
func canon(t *testing.T, v any) any {
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

func assertJSON(t *testing.T, label string, got, want any) {
	t.Helper()
	g, w := canon(t, got), canon(t, want)
	if !reflect.DeepEqual(g, w) {
		gj, _ := json.Marshal(g)
		wj, _ := json.Marshal(w)
		t.Errorf("%s:\n got  %s\n want %s", label, gj, wj)
	}
}

func toStore(t *testing.T, v any) SecretStore {
	t.Helper()
	raw, _ := json.Marshal(v)
	var s SecretStore
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("store: %v", err)
	}
	return s
}

func TestSecretsVectors(t *testing.T) {
	vec := loadVector(t, "secrets.json")
	settings := vec["settings"].(Object)
	stores := vec["stores"].(Object)
	cases := vec["cases"].([]any)
	if len(cases) < 100 {
		t.Fatalf("only %d cases", len(cases))
	}
	settingsOf := func(c Object) Object {
		name, ok := c["settings"].(string)
		if !ok {
			return nil
		}
		m, _ := settings[name].(Object)
		return m
	}
	for i, raw := range cases {
		c := raw.(Object)
		fn := c["fn"].(string)
		label := fn + " " + jsKey(c["settings"]) + " " + jsKey(c["store"]) + " " + jsKey(c["stored"]) + "<-" + jsKey(c["next"])
		switch fn {
		case "hasSettingsSecrets":
			if got := HasSettingsSecrets(settingsOf(c)); got != c["out"].(bool) {
				t.Errorf("#%d %s = %v", i, label, got)
			}
		case "stripSettingsSecrets":
			assertJSON(t, label, StripSettingsSecrets(settingsOf(c)), c["out"])
		case "extractSecrets":
			assertJSON(t, label, ExtractSecrets(settingsOf(c)), c["out"])
		case "applySecrets":
			store := toStore(t, stores[c["store"].(string)])
			assertJSON(t, label, ApplySecrets(settingsOf(c), store), c["out"])
		case "mergeSecretStores":
			stored := toStore(t, stores[c["stored"].(string)])
			next := toStore(t, stores[c["next"].(string)])
			assertJSON(t, label, MergeSecretStores(stored, next), c["out"])
		case "pruneSecrets":
			var ids []string
			for _, id := range c["ids"].([]any) {
				ids = append(ids, id.(string))
			}
			store := toStore(t, stores[c["store"].(string)])
			assertJSON(t, label, PruneSecrets(store, ids), c["out"])
		default:
			t.Fatalf("unknown fn %q", fn)
		}
	}
}

func TestAIAccountsVectors(t *testing.T) {
	vec := loadVector(t, "aiAccounts.json")
	inputs := vec["inputs"].(Object)
	cases := vec["cases"].([]any)
	if len(cases) < 100 {
		t.Fatalf("only %d cases", len(cases))
	}
	// Migration: every input, compared to the TS result.
	for name, raw := range inputs {
		in := raw.(Object)
		if name == "raw" || name == "none" {
			continue
		}
		assertJSON(t, "migrateAIConfig "+name, MigrateAIConfig(in["input"]), in["migrated"])
	}
	migrated := func(name string) Object {
		m, _ := inputs[name].(Object)["migrated"].(Object)
		return m
	}
	feature := func(v any) string {
		s, _ := v.(string)
		return s
	}
	for _, raw := range cases {
		c := raw.(Object)
		fn := c["fn"].(string)
		name := c["ai"].(string)
		ai := migrated(name)
		label := fn + " " + name + " " + jsKey(c["feature"]) + " " + jsKey(c["accountId"])
		switch fn {
		case "accountFor":
			got := AccountFor(ai, feature(c["feature"]))
			if got == nil {
				assertJSON(t, label, nil, c["out"])
			} else {
				assertJSON(t, label, got, c["out"])
			}
		case "modelFor":
			if got := ModelFor(ai, feature(c["feature"])); got != c["out"].(string) {
				t.Errorf("%s = %q, want %q", label, got, c["out"])
			}
		case "resolveAI":
			assertJSON(t, label, ResolveAI(ai, feature(c["feature"])), c["out"])
		case "accountEndpoint":
			account := FindAccount(ai, c["accountId"])
			if got := AccountEndpoint(account); got != c["out"].(string) {
				t.Errorf("%s = %q, want %q", label, got, c["out"])
			}
		case "findAccount":
			got := FindAccount(ai, c["accountId"])
			if got == nil {
				assertJSON(t, label, nil, c["out"])
			} else {
				assertJSON(t, label, got, c["out"])
			}
		default:
			t.Fatalf("unknown fn %q", fn)
		}
	}
}

// The embedded defaults are what vitest checks against types.ts; here we only
// make sure they parse into the shapes Go relies on.
func TestEmbeddedDefaults(t *testing.T) {
	s := DefaultSettings()
	profiles, ok := s["profiles"].([]any)
	if !ok || len(profiles) != 1 {
		t.Fatalf("profiles = %#v", s["profiles"])
	}
	if s["onboardingCompleted"] != false || s["activeProfileId"] != "default" {
		t.Fatalf("unexpected defaults: onboarding=%v active=%v", s["onboardingCompleted"], s["activeProfileId"])
	}
	// Fresh copies: mutating one must not leak into the next call.
	s["language"] = "xx"
	profiles[0].(Object)["id"] = "mutated"
	again := DefaultSettings()
	if again["language"] == "xx" || again["profiles"].([]any)[0].(Object)["id"] != "default" {
		t.Fatal("DefaultSettings returned shared state")
	}
	ai := DefaultAIConfig()
	assertJSON(t, "defaultAIConfig == defaultProfile().ai", ai, again["profiles"].([]any)[0].(Object)["ai"])
	if got := ProviderPreset("anthropic"); got.Transport != "anthropic" || got.Endpoint != "https://api.anthropic.com" {
		t.Errorf("anthropic preset = %+v", got)
	}
	for _, id := range []any{"nope", nil, 3, ""} {
		if got := ProviderPreset(id); got.ID != "custom" {
			t.Errorf("ProviderPreset(%v) = %q, want custom", id, got.ID)
		}
	}
	if got := AIFeatures(); len(got) != 7 || got[0] != "commit" || got[1] != "chat" {
		t.Errorf("AIFeatures = %v", got)
	}
	if got := TokenFields(); !reflect.DeepEqual(got, []string{"githubToken", "azureToken", "gitlabToken", "bitbucketToken"}) {
		t.Errorf("TokenFields = %v", got)
	}
	if len(AIProviders()) != 9 {
		t.Errorf("AIProviders = %d", len(AIProviders()))
	}
}
