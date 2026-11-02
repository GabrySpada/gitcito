package shared

// Go port of the parts of src/shared/aiAccounts.ts the main process needs:
// accountFor (settings resolves an AI key with it) and migrateAIConfig (run on
// every settings read), plus what those two call. Vector-tested against the
// TS functions through testdata/vectors/aiAccounts.json.
//
// Accounts, migration and resolution — pure, so both processes and the test
// suite can use them.
//
// Gitcito used to hold exactly one provider, one endpoint and one key per
// profile. That is now the first entry of `ai.accounts`, and the old fields are
// kept on `AIConfig` as the *resolved* connection for a single call. Every
// main-process feature reads those same four fields, so accounts stayed out of
// the IPC contract entirely.
//
// Configs are the decoded JSON objects (see jsvalue.go); none of these
// functions mutates its input.

// AccountEndpoint is accountEndpoint: the endpoint an account actually talks
// to, falling back to its preset.
func AccountEndpoint(account Object) string {
	own := JSTrim(strOr(get(account, "endpoint"), ""))
	if own != "" {
		return own
	}
	return ProviderPreset(get(account, "provider")).Endpoint
}

// FindAccount is findAccount: the account whose id is accountID (compared with
// ===), or nil. A falsy id finds nothing.
func FindAccount(ai Object, accountID any) Object {
	if !JSTruthy(accountID) {
		return nil
	}
	accounts, _ := asArray(get(ai, "accounts"))
	for _, a := range accounts {
		if m, ok := asObject(a); ok && jsStrictEqual(m["id"], accountID) {
			return m
		}
	}
	return nil
}

// AccountFor is accountFor: the account a feature uses, falling back to the
// default and then to the first. feature "" means no feature.
func AccountFor(ai Object, feature string) Object {
	var assigned any
	if feature != "" {
		if assignments, ok := asObject(get(ai, "assignments")); ok {
			if a, ok := asObject(assignments[feature]); ok {
				assigned = a["accountId"]
			}
		}
	}
	if a := FindAccount(ai, assigned); a != nil {
		return a
	}
	if a := FindAccount(ai, get(ai, "defaultAccountId")); a != nil {
		return a
	}
	accounts, _ := asArray(get(ai, "accounts"))
	if len(accounts) > 0 {
		// `(ai.accounts ?? [])[0]` returns whatever is first; a non-object entry
		// cannot be used as an account, so the port reports none.
		if m, ok := asObject(accounts[0]); ok {
			return m
		}
	}
	return nil
}

// ModelFor is modelFor: the model a feature uses — its own override, else the
// account's default.
func ModelFor(ai Object, feature string) string {
	account := AccountFor(ai, feature)
	var assignment Object
	if feature != "" {
		if assignments, ok := asObject(get(ai, "assignments")); ok {
			assignment, _ = asObject(assignments[feature])
		}
	}
	// An assignment only supplies the model when it names the account we resolved
	// to — otherwise a leftover model from a deleted account would win.
	if assignment != nil && account != nil && jsStrictEqual(assignment["accountId"], account["id"]) {
		if own := JSTrim(strOr(assignment["model"], "")); own != "" {
			return own
		}
	}
	if m := JSTrim(strOr(get(account, "model"), "")); m != "" {
		return m
	}
	provider := get(account, "provider")
	if provider == nil {
		provider = "custom"
	}
	return ProviderPreset(provider).DefaultModel
}

// setOrDrop is `{ ...out, [key]: src[key] }`: a missing source property writes
// `undefined`, which JSON drops — so the key disappears from the result.
func setOrDrop(out Object, key string, src Object, srcKey string) {
	if v, ok := src[srcKey]; ok {
		out[key] = v
	} else {
		delete(out, key)
	}
}

// ResolveAI is resolveAI: flattens an account into the connection fields the
// main process reads.
//
// Call this at every renderer site that used to pass `activeProfile().ai`
// straight through — the shape is unchanged, the connection is just the one the
// user picked for that feature rather than the only one that existed.
func ResolveAI(ai Object, feature string) Object {
	account := AccountFor(ai, feature)
	if account == nil {
		return ai
	}
	preset := ProviderPreset(account["provider"])
	out := spread(ai)
	setOrDrop(out, "provider", account, "provider")
	out["endpoint"] = AccountEndpoint(account)
	if k := account["apiKey"]; k != nil {
		out["apiKey"] = k
	} else {
		out["apiKey"] = ""
	}
	out["model"] = ModelFor(ai, feature)
	setOrDrop(out, "accountId", account, "id")
	out["transport"] = preset.Transport
	setOrDrop(out, "cli", account, "cli")
	setOrDrop(out, "cliPath", account, "cliPath")
	return out
}

// MigrateAIConfig is migrateAIConfig: brings a stored config forward to the
// account model. Idempotent: a config that already has accounts is only
// repaired, never rebuilt, so this is safe to run on every settings read.
//
// The migration is deliberately lossless — the single provider/endpoint/key
// becomes one account, and `repoChatModel` becomes the chat assignment — so a
// user who never opens Settings keeps exactly the behaviour they had.
//
// input is `Partial<AIConfig> | undefined`: nil (or any non-object) is treated
// as an empty partial.
func MigrateAIConfig(input any) Object {
	in, _ := asObject(input)
	ai := spread(DefaultAIConfig())
	for k, v := range in {
		ai[k] = v
	}

	// Read from `input`, never from the merged value: the defaults carry a
	// starter account, so merging first would make every pre-accounts config look
	// migrated and silently drop the key it already had.
	accounts := []any{}
	if stored, ok := asArray(get(in, "accounts")); ok {
		for _, a := range stored {
			if m, ok := asObject(a); ok {
				if _, isStr := m["id"].(string); isStr {
					accounts = append(accounts, m)
				}
			}
		}
	}
	// True only for a config written before accounts existed.
	preAccounts := len(accounts) == 0

	if preAccounts {
		// Pre-accounts install: fold the one connection it had into an account. A
		// blank provider means it never got that far, so the default stands.
		//
		// Endpoint and model come from `input`, not the merged value: taking them
		// from the defaults would hand, say, an Anthropic config OpenAI's endpoint.
		var provider any = "openai"
		if p := get(in, "provider"); p != nil {
			provider = p
		}
		preset := ProviderPreset(provider)
		var apiKey any = ""
		if k := get(in, "apiKey"); k != nil {
			apiKey = k
		}
		model := JSTrim(strOr(get(in, "model"), ""))
		if model == "" {
			model = preset.DefaultModel
		}
		accounts = append(accounts, Object{
			"id":       "default",
			"label":    preset.Label,
			"provider": provider,
			"endpoint": JSTrim(strOr(get(in, "endpoint"), "")),
			"apiKey":   apiKey,
			"model":    model,
		})
	}

	ids := map[string]bool{}
	for _, a := range accounts {
		ids[a.(Object)["id"].(string)] = true
	}
	var defaultAccountID any = accounts[0].(Object)["id"]
	if s, ok := ai["defaultAccountId"].(string); ok && ids[s] {
		defaultAccountID = s
	}

	// Drop assignments pointing at accounts that no longer exist rather than
	// letting a feature silently fall back with a stale model attached.
	assignments := Object{}
	existingAll, _ := asObject(ai["assignments"])
	for _, feature := range defaults.AIFeatures {
		if existing, ok := asObject(existingAll[feature]); ok {
			if id, ok := existing["accountId"].(string); ok && ids[id] {
				assignments[feature] = existing
			}
		}
	}

	// The old chat-only model override becomes a chat assignment on whichever
	// account the migration just created. Only on a pre-accounts config: once
	// accounts exist, a dropped assignment means the user deleted that account,
	// and reviving it from a leftover field would undo their change.
	legacyChatModel := ""
	if preAccounts {
		legacyChatModel = JSTrim(strOr(ai["repoChatModel"], ""))
	}
	if legacyChatModel != "" && !JSTruthy(assignments["chat"]) {
		assignments["chat"] = Object{"accountId": defaultAccountID, "model": legacyChatModel}
	}

	migrated := spread(ai)
	migrated["accounts"] = accounts
	migrated["defaultAccountId"] = defaultAccountID
	migrated["assignments"] = assignments
	// Cleared once folded into the chat assignment: leaving it set would let
	// the old value win over the account the user has since chosen.
	migrated["repoChatModel"] = ""
	return ResolveAI(migrated, "")
}
