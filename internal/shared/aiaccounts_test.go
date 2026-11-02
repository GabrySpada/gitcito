package shared

import "testing"

// Ports of test/pureLogic.test.ts › "AI accounts — migration and resolution"
// (newAccount stays renderer-only and is not ported) and of
// test/repoChat.test.ts's migrateAIConfig defaults check.

// What a settings file written before accounts existed looks like.
func legacyAI() Object {
	return Object{
		"enabled": true, "provider": "anthropic", "endpoint": "https://api.anthropic.com/v1",
		"apiKey": "sk-ant-old", "model": "claude-3-5-haiku-latest", "repoChatModel": "claude-3-5-sonnet-latest",
		"commitStyle": "auto", "explainStyle": "normal", "conflictStyle": "clean",
		"branchNamingStyle": "prefix/description", "customInstructions": "", "generateDescription": true,
	}
}

func with(base Object, over Object) Object {
	out := spread(base)
	for k, v := range over {
		out[k] = v
	}
	return out
}

func TestMigrateFoldsSingleProvider(t *testing.T) {
	ai := MigrateAIConfig(legacyAI())
	accounts := ai["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %v", accounts)
	}
	a := accounts[0].(Object)
	if a["provider"] != "anthropic" || a["apiKey"] != "sk-ant-old" || a["model"] != "claude-3-5-haiku-latest" {
		t.Errorf("account = %v", a)
	}
	if ai["defaultAccountId"] != a["id"] {
		t.Errorf("defaultAccountId = %v", ai["defaultAccountId"])
	}
}

func TestMigrateChatModelBecomesAssignment(t *testing.T) {
	ai := MigrateAIConfig(legacyAI())
	assertJSON(t, "chat assignment", ai["assignments"].(Object)["chat"],
		Object{"accountId": ai["defaultAccountId"], "model": "claude-3-5-sonnet-latest"})
	// Left set, it would keep overriding whichever account the user picks next.
	if ai["repoChatModel"] != "" {
		t.Errorf("repoChatModel = %v", ai["repoChatModel"])
	}
	if got := ModelFor(ai, "chat"); got != "claude-3-5-sonnet-latest" {
		t.Errorf("chat model = %q", got)
	}
	if got := ModelFor(ai, "commit"); got != "claude-3-5-haiku-latest" {
		t.Errorf("commit model = %q", got)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	once := MigrateAIConfig(legacyAI())
	assertJSON(t, "twice", MigrateAIConfig(once), once)
}

func TestResolveConnectionForFeature(t *testing.T) {
	ai := MigrateAIConfig(with(legacyAI(), Object{
		"accounts": []any{
			Object{"id": "work", "label": "Work", "provider": "openai", "endpoint": "", "apiKey": "sk-work", "model": "gpt-4o-mini"},
			Object{"id": "home", "label": "Home", "provider": "anthropic", "endpoint": "", "apiKey": "sk-home", "model": "claude-sonnet-5"},
		},
		"defaultAccountId": "work",
		"assignments":      Object{"chat": Object{"accountId": "home", "model": "claude-opus-5"}},
	}))
	chat := ResolveAI(ai, "chat")
	for k, want := range map[string]any{
		"accountId": "home", "provider": "anthropic", "apiKey": "sk-home", "model": "claude-opus-5",
		"transport": "anthropic",
		// An account with no endpoint of its own falls back to the preset's.
		"endpoint": "https://api.anthropic.com",
	} {
		if chat[k] != want {
			t.Errorf("chat[%s] = %v, want %v", k, chat[k], want)
		}
	}
	commit := ResolveAI(ai, "commit")
	for k, want := range map[string]any{"accountId": "work", "apiKey": "sk-work", "model": "gpt-4o-mini", "transport": "openai"} {
		if commit[k] != want {
			t.Errorf("commit[%s] = %v, want %v", k, commit[k], want)
		}
	}
}

func TestMigrateDropsDeletedAssignment(t *testing.T) {
	ai := MigrateAIConfig(with(legacyAI(), Object{
		"accounts":         []any{Object{"id": "work", "label": "Work", "provider": "openai", "endpoint": "", "apiKey": "", "model": "gpt-4o-mini"}},
		"defaultAccountId": "work",
		"assignments":      Object{"chat": Object{"accountId": "deleted", "model": "ghost-model"}},
	}))
	if _, ok := ai["assignments"].(Object)["chat"]; ok {
		t.Error("stale chat assignment kept")
	}
	if got := ModelFor(ai, "chat"); got != "gpt-4o-mini" {
		t.Errorf("chat model = %q", got)
	}
}

func TestMigrateFallsBackToFirstAccount(t *testing.T) {
	ai := MigrateAIConfig(with(legacyAI(), Object{
		"accounts":         []any{Object{"id": "only", "label": "Only", "provider": "openai", "endpoint": "", "apiKey": "", "model": "gpt-4o"}},
		"defaultAccountId": "gone",
	}))
	if ai["defaultAccountId"] != "only" {
		t.Errorf("defaultAccountId = %v", ai["defaultAccountId"])
	}
}

func TestRepoChatReadOnlyDefault(t *testing.T) {
	if MigrateAIConfig(Object{})["repoChatReadOnly"] != true {
		t.Error("repoChatReadOnly should default to true")
	}
	if MigrateAIConfig(Object{"repoChatReadOnly": false})["repoChatReadOnly"] != false {
		t.Error("an explicit false must survive")
	}
}

func TestAccountForFallbacks(t *testing.T) {
	ai := Object{
		"accounts":         []any{Object{"id": "a"}, Object{"id": "b"}},
		"defaultAccountId": "b",
		"assignments":      Object{"chat": Object{"accountId": "a"}, "commit": Object{"accountId": "zzz"}},
	}
	cases := map[string]any{"chat": "a", "commit": "b", "": "b", "wiki": "b"}
	for feature, want := range cases {
		if got := AccountFor(ai, feature); got == nil || got["id"] != want {
			t.Errorf("AccountFor(%q) = %v, want %v", feature, got, want)
		}
	}
	ai["defaultAccountId"] = "gone"
	if got := AccountFor(ai, ""); got["id"] != "a" {
		t.Errorf("first-account fallback = %v", got)
	}
	if got := AccountFor(Object{}, "chat"); got != nil {
		t.Errorf("no accounts = %v", got)
	}
}

func TestMigrateDoesNotMutateInput(t *testing.T) {
	in := legacyAI()
	MigrateAIConfig(in)
	assertJSON(t, "input", in, legacyAI())
}
