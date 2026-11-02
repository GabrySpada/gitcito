package shared

// Go port of src/shared/secrets.ts, vector-tested through
// testdata/vectors/secrets.json.
//
// Settings are the decoded JSON tree (see jsvalue.go); none of these functions
// mutates its input. Where the TS would throw on a malformed shape (a profile
// that is null, `profiles` that is not an array), the port is lenient instead
// and leaves that value alone — see the port report.

// ProfileSecrets is the credentials a profile can hold. They are kept out of
// the settings JSON on disk (see internal/settings, which stores them with the
// OS keychain instead) and out of exported settings files unless the user opts
// in.
//
// An empty string means "absent": the TS store never holds an empty credential
// (extractSecrets only keeps truthy values), so omitempty loses nothing.
type ProfileSecrets struct {
	GithubToken    string `json:"githubToken,omitempty"`
	AzureToken     string `json:"azureToken,omitempty"`
	GitlabToken    string `json:"gitlabToken,omitempty"`
	BitbucketToken string `json:"bitbucketToken,omitempty"`
	// Legacy single AI key, written before accounts existed. Still read.
	AIApiKey string `json:"aiApiKey,omitempty"`
	// One API key per AI account id.
	AIKeys map[string]string `json:"aiKeys,omitempty"`
}

// SecretStore is the secrets for every profile that has any, keyed by profile id.
type SecretStore map[string]ProfileSecrets

// Token returns the value of one of TokenFields() ("" when unknown or unset).
func (p ProfileSecrets) Token(field string) string {
	switch field {
	case "githubToken":
		return p.GithubToken
	case "azureToken":
		return p.AzureToken
	case "gitlabToken":
		return p.GitlabToken
	case "bitbucketToken":
		return p.BitbucketToken
	}
	return ""
}

func (p *ProfileSecrets) setToken(field, value string) {
	switch field {
	case "githubToken":
		p.GithubToken = value
	case "azureToken":
		p.AzureToken = value
	case "gitlabToken":
		p.GitlabToken = value
	case "bitbucketToken":
		p.BitbucketToken = value
	}
}

// IsEmpty is `Object.keys(secrets).length === 0`.
func (p ProfileSecrets) IsEmpty() bool {
	return p.GithubToken == "" && p.AzureToken == "" && p.GitlabToken == "" && p.BitbucketToken == "" &&
		p.AIApiKey == "" && len(p.AIKeys) == 0
}

// Clone is a deep copy.
func (s SecretStore) Clone() SecretStore {
	out := make(SecretStore, len(s))
	for id, p := range s {
		if p.AIKeys != nil {
			keys := make(map[string]string, len(p.AIKeys))
			for k, v := range p.AIKeys {
				keys[k] = v
			}
			p.AIKeys = keys
		}
		out[id] = p
	}
	return out
}

// Overlay is `{ ...s, ...other }`: whole-profile replacement, not a merge.
func (s SecretStore) Overlay(other SecretStore) SecretStore {
	out := s.Clone()
	for id, p := range other.Clone() {
		out[id] = p
	}
	return out
}

// profilesOf returns `settings.profiles ?? []` and whether it may be rebuilt:
// false when it holds something other than an array or null, which the port
// leaves untouched rather than throwing like the TS would.
func profilesOf(settings Object) ([]any, bool) {
	v := get(settings, "profiles")
	if v == nil {
		return []any{}, true
	}
	a, ok := asArray(v)
	return a, ok
}

// HasSettingsSecrets is hasSettingsSecrets: whether any profile carries a
// credential. A nil settings object has none.
func HasSettingsSecrets(settings Object) bool {
	profiles, _ := profilesOf(settings)
	for _, raw := range profiles {
		p, ok := asObject(raw)
		if !ok {
			continue
		}
		for _, f := range defaults.TokenFields {
			if JSTruthy(p[f]) {
				return true
			}
		}
		ai, _ := asObject(p["ai"])
		if JSTruthy(get(ai, "apiKey")) {
			return true
		}
		accounts, _ := asArray(get(ai, "accounts"))
		for _, a := range accounts {
			if m, ok := asObject(a); ok && JSTruthy(m["apiKey"]) {
				return true
			}
		}
	}
	return false
}

// StripSettingsSecrets is stripSettingsSecrets: the same settings with every
// credential blanked out.
func StripSettingsSecrets(settings Object) Object {
	out := spread(settings)
	profiles, ok := profilesOf(settings)
	if !ok {
		return out
	}
	next := make([]any, len(profiles))
	for i, raw := range profiles {
		p := spread(raw)
		for _, f := range defaults.TokenFields {
			p[f] = ""
		}
		if ai, present := p["ai"]; present && JSTruthy(ai) {
			a := spread(ai)
			a["apiKey"] = ""
			accounts, isArr := asArray(get(asObjectOrNil(ai), "accounts"))
			if get(asObjectOrNil(ai), "accounts") == nil {
				accounts, isArr = []any{}, true
			}
			if isArr {
				blanked := make([]any, len(accounts))
				for j, acc := range accounts {
					m := spread(acc)
					m["apiKey"] = ""
					blanked[j] = m
				}
				a["accounts"] = blanked
			}
			p["ai"] = a
		}
		next[i] = p
	}
	out["profiles"] = next
	return out
}

func asObjectOrNil(v any) Object {
	m, _ := asObject(v)
	return m
}

// ExtractSecrets is extractSecrets: pulls every non-empty credential out, keyed
// by profile id. Only string credentials are taken (the renderer never writes
// anything else there).
func ExtractSecrets(settings Object) SecretStore {
	store := SecretStore{}
	profiles, _ := profilesOf(settings)
	for _, raw := range profiles {
		p, ok := asObject(raw)
		if !ok {
			continue
		}
		var secrets ProfileSecrets
		for _, f := range defaults.TokenFields {
			if v, ok := p[f].(string); ok && v != "" {
				secrets.setToken(f, v)
			}
		}
		ai, _ := asObject(p["ai"])
		aiKeys := map[string]string{}
		accounts, _ := asArray(get(ai, "accounts"))
		for _, a := range accounts {
			m, ok := asObject(a)
			if !ok {
				continue
			}
			if k, ok := m["apiKey"].(string); ok && k != "" {
				aiKeys[keyOf(m, "id")] = k
			}
		}
		if len(aiKeys) > 0 {
			secrets.AIKeys = aiKeys
		} else if k, ok := get(ai, "apiKey").(string); ok && k != "" {
			// Only written when the config predates accounts; migration folds it into
			// `aiKeys` the first time the settings are saved afterwards.
			secrets.AIApiKey = k
		}
		if !secrets.IsEmpty() {
			store[keyOf(p, "id")] = secrets
		}
	}
	return store
}

// lookupStore is `store[p.id]`, falsy when absent.
func lookupStore(store SecretStore, p Object) (ProfileSecrets, bool) {
	s, ok := store[keyOf(p, "id")]
	return s, ok
}

// ApplySecrets is applySecrets: puts stored credentials back on their
// profiles, leaving the rest untouched.
func ApplySecrets(settings Object, store SecretStore) Object {
	out := spread(settings)
	profiles, ok := profilesOf(settings)
	if !ok {
		return out
	}
	next := make([]any, len(profiles))
	for i, raw := range profiles {
		next[i] = applyProfileSecrets(raw, store)
	}
	out["profiles"] = next
	return out
}

func applyProfileSecrets(raw any, store SecretStore) any {
	p, ok := asObject(raw)
	if !ok {
		return raw
	}
	secrets, ok := lookupStore(store, p)
	if !ok {
		return p
	}
	restored := spread(p)
	for _, f := range defaults.TokenFields {
		if v := secrets.Token(f); v != "" {
			restored[f] = v
		}
	}
	ai, ok := asObject(restored["ai"])
	if !ok {
		// `if (!restored.ai) return restored` — and a truthy non-object has no
		// accounts to restore onto.
		return restored
	}

	accounts, _ := asArray(ai["accounts"])
	if len(accounts) > 0 {
		// A pre-accounts key belongs to the account the migration built from it.
		var legacyOwner any = ai["defaultAccountId"]
		if !JSTruthy(legacyOwner) {
			if first, ok := asObject(accounts[0]); ok {
				legacyOwner = first["id"]
			} else {
				legacyOwner = nil
			}
		}
		rebuilt := make([]any, len(accounts))
		for j, accRaw := range accounts {
			a, ok := asObject(accRaw)
			if !ok {
				rebuilt[j] = accRaw
				continue
			}
			var key string
			if k, found := secrets.AIKeys[keyOf(a, "id")]; found {
				key = k
			} else if jsStrictEqual(a["id"], legacyOwner) {
				key = secrets.AIApiKey
			}
			if key != "" {
				withKey := spread(a)
				withKey["apiKey"] = key
				rebuilt[j] = withKey
			} else {
				rebuilt[j] = a
			}
		}
		ai = spread(ai)
		ai["accounts"] = rebuilt
		restored["ai"] = ai
	}

	// Keep the resolved field in step so a config used before migration still
	// authenticates.
	resolvedKey, found := "", false
	pick := ai["accountId"]
	if pick == nil {
		pick = ai["defaultAccountId"]
	}
	pickKey := ""
	if pick != nil {
		pickKey = jsKey(pick)
	}
	if k, ok := secrets.AIKeys[pickKey]; ok {
		resolvedKey, found = k, true
	}
	if !found {
		firstKey := ""
		if len(accounts) > 0 {
			if first, ok := asObject(accounts[0]); ok {
				if id := first["id"]; id != nil {
					firstKey = jsKey(id)
				}
			}
		}
		if k, ok := secrets.AIKeys[firstKey]; ok {
			resolvedKey, found = k, true
		}
	}
	if !found {
		resolvedKey = secrets.AIApiKey
	}
	if resolvedKey != "" {
		ai = spread(ai)
		ai["apiKey"] = resolvedKey
		restored["ai"] = ai
	}
	return restored
}

// MergeSecretStores is mergeSecretStores: overlays next onto stored without
// dropping credentials next never had.
// Used for saves coming from a renderer that was hydrated before the store was
// unlocked: such a save can add or replace a credential, but an absence in it
// means "I was never given this", not "the user cleared it".
func MergeSecretStores(stored, next SecretStore) SecretStore {
	out := stored.Clone()
	for id, secrets := range next {
		base := out[id]
		aiKeys := map[string]string{}
		for k, v := range base.AIKeys {
			aiKeys[k] = v
		}
		for k, v := range secrets.AIKeys {
			aiKeys[k] = v
		}
		merged := base
		for _, f := range defaults.TokenFields {
			if v := secrets.Token(f); v != "" {
				merged.setToken(f, v)
			}
		}
		if secrets.AIApiKey != "" {
			merged.AIApiKey = secrets.AIApiKey
		}
		if len(aiKeys) > 0 {
			merged.AIKeys = aiKeys
		} else {
			merged.AIKeys = nil
		}
		out[id] = merged
	}
	return out
}

// PruneSecrets is pruneSecrets: drops entries for profiles that no longer exist.
func PruneSecrets(store SecretStore, profileIDs []string) SecretStore {
	keep := make(map[string]bool, len(profileIDs))
	for _, id := range profileIDs {
		keep[id] = true
	}
	out := SecretStore{}
	for id, secrets := range store {
		if keep[id] {
			out[id] = secrets
		}
	}
	return out
}

// ProfileIDs is `(settings.profiles ?? []).map((p) => p.id)` restricted to the
// ids that can match a store key: Set.has compares with SameValueZero, so only
// string ids ever keep an entry in PruneSecrets.
func ProfileIDs(settings Object) []string {
	profiles, _ := profilesOf(settings)
	ids := []string{}
	for _, raw := range profiles {
		if p, ok := asObject(raw); ok {
			if id, ok := p["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}
