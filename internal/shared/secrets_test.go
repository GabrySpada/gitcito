package shared

import (
	"reflect"
	"testing"
)

// Ports of test/pureLogic.test.ts › "profile secrets — keeping credentials out
// of settings.json" and › "mergeSecretStores — a save never deletes what it
// was not given". The vectors replay the same fixtures; these keep each
// assertion readable on its own.

func testProfile(id string, over Object) Object {
	p := Object{
		"id": id, "name": id, "gitName": "Dev", "gitEmail": "dev@example.com",
		"githubToken": "", "azureToken": "", "gitlabToken": "", "bitbucketToken": "",
		"ai": spread(DefaultSettings()["profiles"].([]any)[0].(Object)["ai"]),
	}
	for k, v := range over {
		p[k] = v
	}
	return p
}

func withProfiles(profiles ...any) Object {
	s := DefaultSettings()
	s["profiles"] = profiles
	return s
}

func keyedAI(key string) Object {
	ai := DefaultAIConfig()
	accounts := []any{}
	for _, a := range ai["accounts"].([]any) {
		m := spread(a)
		m["apiKey"] = key
		accounts = append(accounts, m)
	}
	ai["accounts"] = accounts
	ai["apiKey"] = key
	return ai
}

func loadedFixture() Object {
	return withProfiles(
		testProfile("p1", Object{"githubToken": "ghp_secret", "ai": keyedAI("sk-secret")}),
		testProfile("p2", Object{"gitlabToken": "glpat_secret"}),
	)
}

func profileAt(s Object, i int) Object { return s["profiles"].([]any)[i].(Object) }

func TestDetectsCredentials(t *testing.T) {
	loaded := loadedFixture()
	if !HasSettingsSecrets(loaded) {
		t.Error("loaded settings should have secrets")
	}
	if HasSettingsSecrets(StripSettingsSecrets(loaded)) {
		t.Error("stripped settings should have none")
	}
	if HasSettingsSecrets(DefaultSettings()) {
		t.Error("defaults should have none")
	}
	if HasSettingsSecrets(nil) {
		t.Error("nil settings should have none")
	}
}

func TestStripLeavesTheRestIntact(t *testing.T) {
	loaded := loadedFixture()
	stripped := StripSettingsSecrets(loaded)
	p0, p1 := profileAt(stripped, 0), profileAt(stripped, 1)
	if p0["githubToken"] != "" || p0["ai"].(Object)["apiKey"] != "" || p1["gitlabToken"] != "" {
		t.Errorf("credentials survived: %v", stripped)
	}
	if p0["gitEmail"] != "dev@example.com" {
		t.Errorf("gitEmail = %v", p0["gitEmail"])
	}
	if p0["ai"].(Object)["provider"] != profileAt(loaded, 0)["ai"].(Object)["provider"] {
		t.Error("provider changed")
	}
	// The input is untouched.
	if profileAt(loaded, 0)["githubToken"] != "ghp_secret" {
		t.Error("StripSettingsSecrets mutated its input")
	}
}

func TestExtractOnlyNonEmpty(t *testing.T) {
	loaded := loadedFixture()
	want := SecretStore{
		"p1": {GithubToken: "ghp_secret", AIKeys: map[string]string{"default": "sk-secret"}},
		"p2": {GitlabToken: "glpat_secret"},
	}
	if got := ExtractSecrets(loaded); !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractSecrets = %#v", got)
	}
	if got := ExtractSecrets(StripSettingsSecrets(loaded)); len(got) != 0 {
		t.Errorf("stripped extract = %#v", got)
	}
}

func TestRoundTripStripApply(t *testing.T) {
	loaded := loadedFixture()
	restored := ApplySecrets(StripSettingsSecrets(loaded), ExtractSecrets(loaded))
	assertJSON(t, "round trip", restored, loaded)
}

func TestRestoresPreAccountsKey(t *testing.T) {
	// What an install upgraded from an older build looks like: the encrypted
	// store still holds the single `aiApiKey`, while settings.json now has
	// accounts whose keys were stripped out.
	restored := ApplySecrets(StripSettingsSecrets(loadedFixture()), SecretStore{"p1": {AIApiKey: "sk-legacy"}})
	ai := profileAt(restored, 0)["ai"].(Object)
	if ai["accounts"].([]any)[0].(Object)["apiKey"] != "sk-legacy" || ai["apiKey"] != "sk-legacy" {
		t.Errorf("ai = %v", ai)
	}
}

func TestEachAccountItsOwnKey(t *testing.T) {
	base := DefaultAIConfig()
	first := base["accounts"].([]any)[0].(Object)
	work, personal := spread(first), spread(first)
	work["id"], work["apiKey"] = "work", "sk-work"
	personal["id"], personal["provider"], personal["apiKey"] = "personal", "anthropic", "sk-personal"
	base["accounts"] = []any{work, personal}
	two := withProfiles(testProfile("p1", Object{"ai": base}))

	secrets := ExtractSecrets(two)
	if want := map[string]string{"work": "sk-work", "personal": "sk-personal"}; !reflect.DeepEqual(secrets["p1"].AIKeys, want) {
		t.Errorf("aiKeys = %v", secrets["p1"].AIKeys)
	}
	restored := ApplySecrets(StripSettingsSecrets(two), secrets)
	accounts := profileAt(restored, 0)["ai"].(Object)["accounts"].([]any)
	if accounts[0].(Object)["apiKey"] != "sk-work" || accounts[1].(Object)["apiKey"] != "sk-personal" {
		t.Errorf("accounts = %v", accounts)
	}
}

func TestLeavesProfileAloneWithoutStoreEntry(t *testing.T) {
	restored := ApplySecrets(StripSettingsSecrets(loadedFixture()), SecretStore{"p1": {GithubToken: "ghp_secret"}})
	if profileAt(restored, 0)["githubToken"] != "ghp_secret" {
		t.Error("token not restored")
	}
	if profileAt(restored, 0)["ai"].(Object)["apiKey"] != "" || profileAt(restored, 1)["gitlabToken"] != "" {
		t.Error("restored something the store did not hold")
	}
}

func TestPruneDropsMissingProfiles(t *testing.T) {
	store := ExtractSecrets(loadedFixture())
	want := SecretStore{"p1": {GithubToken: "ghp_secret", AIKeys: map[string]string{"default": "sk-secret"}}}
	if got := PruneSecrets(store, []string{"p1"}); !reflect.DeepEqual(got, want) {
		t.Errorf("prune p1 = %#v", got)
	}
	if got := PruneSecrets(store, nil); len(got) != 0 {
		t.Errorf("prune none = %#v", got)
	}
}

func TestMigrationPlaintextWins(t *testing.T) {
	older := SecretStore{"p1": {GithubToken: "ghp_stale"}}
	merged := PruneSecrets(older.Overlay(ExtractSecrets(loadedFixture())), []string{"p1", "p2"})
	if merged["p1"].GithubToken != "ghp_secret" || merged["p2"].GitlabToken != "glpat_secret" {
		t.Errorf("merged = %#v", merged)
	}
}

func storedFixture() SecretStore {
	return SecretStore{
		"p1": {GithubToken: "ghp_stored", AIKeys: map[string]string{"default": "sk-stored"}},
		"p2": {GitlabToken: "glpat_stored"},
	}
}

func TestMergeKeepsEverythingOnEmptySave(t *testing.T) {
	// The regression: a renderer hydrated before unlock persisting a tab move
	// used to overwrite the whole store with {}, losing the AI key on the
	// next launch — the "add my key again after every update" bug.
	if got := MergeSecretStores(storedFixture(), SecretStore{}); !reflect.DeepEqual(got, storedFixture()) {
		t.Errorf("merge = %#v", got)
	}
}

func TestMergeReplacesOnlyWhatTheSaveCarries(t *testing.T) {
	merged := MergeSecretStores(storedFixture(), SecretStore{"p1": {GithubToken: "ghp_new"}})
	if merged["p1"].GithubToken != "ghp_new" ||
		!reflect.DeepEqual(merged["p1"].AIKeys, map[string]string{"default": "sk-stored"}) ||
		merged["p2"].GitlabToken != "glpat_stored" {
		t.Errorf("merge = %#v", merged)
	}
}

func TestMergeAIKeysPerAccount(t *testing.T) {
	merged := MergeSecretStores(storedFixture(), SecretStore{"p1": {AIKeys: map[string]string{"work": "sk-work"}}})
	if want := map[string]string{"default": "sk-stored", "work": "sk-work"}; !reflect.DeepEqual(merged["p1"].AIKeys, want) {
		t.Errorf("aiKeys = %v", merged["p1"].AIKeys)
	}
}

func TestMergeAddsUnseenProfile(t *testing.T) {
	stored := storedFixture()
	merged := MergeSecretStores(stored, SecretStore{"p3": {AzureToken: "az_new"}})
	if merged["p3"].AzureToken != "az_new" || !reflect.DeepEqual(merged["p1"], stored["p1"]) {
		t.Errorf("merge = %#v", merged)
	}
	// Inputs untouched.
	if len(stored) != 2 {
		t.Error("MergeSecretStores mutated stored")
	}
}

func TestProfileIDsOnlyStrings(t *testing.T) {
	s := withProfiles(Object{"id": "a"}, Object{"id": 3}, Object{}, nil, Object{"id": "b"})
	if got := ProfileIDs(s); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("ProfileIDs = %v", got)
	}
	if got := ProfileIDs(Object{}); len(got) != 0 {
		t.Errorf("no profiles = %v", got)
	}
}
