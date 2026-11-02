// Package settings is the port of src/main/settings.ts: the settings JSON,
// the encrypted secrets file next to it, and the rules that keep credentials
// out of the former without ever losing them.
//
// Profile tokens and the AI API key used to sit in the settings JSON in plain
// text. They now live in a separate file encrypted with the OS keychain, the
// same mechanism the vault uses. Reads hydrate them back onto the profiles, so
// nothing downstream — renderer, export, git auth — sees a difference.
// Where the OS offers no encryption (a Linux box with no keyring), settings
// keep working exactly as before rather than losing the user's credentials.
//
// # Shape
//
// AppSettings is owned by the renderer and changes often, so it is held as the
// decoded JSON tree (Settings, a map) rather than a struct: every field the
// renderer writes round-trips untouched, including the ones Go never reads.
// Typed accessors cover what Go logic needs.
//
// # Files
//
// gitcito-settings.json is the stripped settings, pretty-printed like
// JSON.stringify(x, null, 2) (key order may differ; the renderer does not care).
// gitcito-secrets.enc is the base64 text of keychain.Encrypt(JSON of the
// SecretStore) — the TS stored base64 of safeStorage output; the sealed format
// itself is new, which D3 (fresh start, no migration) allows.
package settings

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/GabrySpada/gitcito/internal/keychain"
	"github.com/GabrySpada/gitcito/internal/paths"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// File names inside the data dir.
const (
	SettingsFileName = "gitcito-settings.json"
	SecretsFileName  = "gitcito-secrets.enc"
)

// EnvForceOnboarding re-runs the first-run wizard when set to "1".
const EnvForceOnboarding = "GITCITO_FORCE_ONBOARDING"

// RepoHost mirrors RepoHost in src/shared/types.ts.
type RepoHost string

// The RepoHost values.
const (
	HostGitHub    RepoHost = "github"
	HostGitLab    RepoHost = "gitlab"
	HostBitbucket RepoHost = "bitbucket"
	HostAzure     RepoHost = "azure"
)

var tokenField = map[RepoHost]string{
	HostGitHub:    "githubToken",
	HostGitLab:    "gitlabToken",
	HostBitbucket: "bitbucketToken",
	HostAzure:     "azureToken",
}

// Options configures a Store. Zero values pick the production behaviour,
// except Keychain, which is required.
type Options struct {
	// DataDir defaults to paths.DataDir.
	DataDir func() (string, error)
	// Keychain gates and performs every encryption.
	Keychain *keychain.Keychain
	// Dialogs defaults to WailsDialogs (dialogs_wails.go).
	Dialogs Dialogs
	// Getenv defaults to os.Getenv; it is read once, in New.
	Getenv func(string) string
}

// Store holds this run's settings state. Safe for concurrent use. The mutex is
// never held across a keychain question, so a settings read is never stuck
// behind a dialog the user has not answered yet.
type Store struct {
	dataDir func() (string, error)
	kc      *keychain.Keychain
	dialogs Dialogs

	mu sync.Mutex

	// Tokens the user typed while the keychain was unavailable or refused. They
	// stay in this process only: the app keeps working for the session, and the
	// settings file on disk never gains a plaintext token behind the user's back.
	sessionSecrets shared.SecretStore

	// Decrypted secrets for this run, or nil while they are still on disk.
	//
	// Nothing hydrates them at start-up: reading the encrypted file means calling
	// the keychain, and on macOS that pops the system keychain dialog before the
	// window has even painted — exactly the unexplained prompt this whole gate
	// exists to prevent. They are decrypted on the first operation that genuinely
	// needs a token (a push, a PR listing, opening Settings) instead.
	secretCache shared.SecretStore

	// An encrypted file exists on disk but this build's master key cannot read
	// it — a different app identity wrote it (dev vs packaged, another keychain
	// entry). Distinct from "no secrets": overwriting that file with our empty
	// view of it would destroy tokens another build can still decrypt.
	secretsUnreadable bool

	// Whether the settings the renderer last loaded included the stored secrets.
	//
	// A renderer hydrated before any unlock persists a credential-less copy of the
	// settings on every mutation (a tab move, a repo opened). If a background
	// operation — a fetch resolving its token, an AI call resolving its key — has
	// unlocked the store in the meantime, treating such a save as "the user
	// cleared everything" overwrites the encrypted store with nothing. Only once
	// the renderer was actually served the secrets is an empty save a deletion.
	servedSecrets bool

	// Dev aid: launching with GITCITO_FORCE_ONBOARDING=1 re-runs the first-run
	// wizard without touching the settings file. The flag holds until the wizard
	// itself saves its completion (consuming it per read breaks under StrictMode's
	// double hydration), so a reload before finishing shows the wizard again.
	forceOnboarding bool
}

// New builds a Store.
func New(opts Options) *Store {
	if opts.Keychain == nil {
		panic("settings: Options.Keychain is required")
	}
	s := &Store{
		dataDir:        opts.DataDir,
		kc:             opts.Keychain,
		dialogs:        opts.Dialogs,
		sessionSecrets: shared.SecretStore{},
	}
	if s.dataDir == nil {
		s.dataDir = paths.DataDir
	}
	if s.dialogs == nil {
		s.dialogs = WailsDialogs{}
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	s.forceOnboarding = getenv(EnvForceOnboarding) == "1"
	return s
}

// Settings is AppSettings as decoded JSON. Numbers are json.Number.
type Settings map[string]any

// Value returns a top-level field.
func (s Settings) Value(key string) (any, bool) {
	v, ok := s[key]
	return v, ok
}

// Bool returns a top-level field when it is a boolean.
func (s Settings) Bool(key string) (value, ok bool) {
	value, ok = s[key].(bool)
	return value, ok
}

// String returns a top-level field when it is a string.
func (s Settings) String(key string) (string, bool) {
	v, ok := s[key].(string)
	return v, ok
}

// Number returns a top-level field when it is a number.
func (s Settings) Number(key string) (float64, bool) {
	switch v := s[key].(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	}
	return 0, false
}

// Object returns a top-level field when it is an object.
func (s Settings) Object(key string) (map[string]any, bool) {
	v, ok := s[key].(map[string]any)
	return v, ok && v != nil
}

// Profiles returns the profiles that are objects, in order.
func (s Settings) Profiles() []map[string]any {
	raw, _ := s["profiles"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		if m, ok := p.(map[string]any); ok && m != nil {
			out = append(out, m)
		}
	}
	return out
}

func (s *Store) file(name string) (string, error) {
	dir, err := s.dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (s *Store) ensureDir() error {
	dir, err := s.dataDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeFileAtomic replaces a file in one step: Go reads run truly in parallel
// with writes, and a reader catching a half-written settings file would fall
// back to the defaults.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func (s *Store) writeSettingsFile(data []byte) error {
	p, err := s.file(SettingsFileName)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, data)
}

func (s *Store) loadSecretsLocked() shared.SecretStore {
	p, err := s.file(SecretsFileName)
	if err != nil {
		return shared.SecretStore{}
	}
	store, err := s.decryptFile(p)
	if err != nil {
		s.secretsUnreadable = exists(p)
		return shared.SecretStore{}
	}
	s.secretsUnreadable = false
	return store
}

func (s *Store) decryptFile(p string) (shared.SecretStore, error) {
	b64, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil {
		return nil, err
	}
	plain, err := s.kc.Decrypt(sealed)
	if err != nil {
		return nil, err
	}
	var store shared.SecretStore
	if err := json.Unmarshal(plain, &store); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) saveSecretsLocked(store shared.SecretStore) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	p, err := s.file(SecretsFileName)
	if err != nil {
		return err
	}
	if s.secretsUnreadable {
		// Keep the copy we could not decrypt: the build identity that wrote it can
		// still read it, and this write would otherwise be the point of no return.
		_ = copyFile(p, p+".bak")
		s.secretsUnreadable = false
	}
	plain, err := json.Marshal(store)
	if err != nil {
		return err
	}
	enc, err := s.kc.Encrypt(plain)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, []byte(base64.StdEncoding.EncodeToString(enc)))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ReadSettings is readSettings: the settings JSON, with whatever secrets are
// already in memory applied. It never asks for keychain access and never
// fails: a missing or unparseable file reads as the defaults.
func (s *Store) ReadSettings(ctx context.Context) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readSettingsLocked()
}

func (s *Store) readSettingsLocked() Settings {
	p, err := s.file(SettingsFileName)
	if err != nil {
		return Settings(shared.DefaultSettings())
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Settings(shared.DefaultSettings())
	}
	parsed, err := shared.DecodeJSON(raw)
	if err != nil {
		return Settings(shared.DefaultSettings())
	}
	// Existing install without the key → treat as already onboarded.
	settings := shared.DefaultSettings()
	settings["onboardingCompleted"] = true
	if m, ok := parsed.(map[string]any); ok {
		for k, v := range m {
			settings[k] = v
		}
	}
	secrets := s.secretCache.Clone().Overlay(s.sessionSecrets)
	return withMigratedAI(shared.ApplySecrets(settings, secrets))
}

// withMigratedAI brings every profile's AI config forward to the account model.
//
// Runs on read rather than as a one-shot upgrade step: the settings file is
// also written by imports and by older builds sharing the same user data
// directory, so "migrate whenever we read" is the only version that stays true.
func withMigratedAI(settings map[string]any) Settings {
	out := Settings{}
	for k, v := range settings {
		out[k] = v
	}
	profiles, ok := settings["profiles"].([]any)
	if settings["profiles"] == nil {
		profiles, ok = []any{}, true
	}
	if !ok {
		// Not an array: the TS would throw here; leave it as it is instead.
		return out
	}
	next := make([]any, len(profiles))
	for i, raw := range profiles {
		p := map[string]any{}
		if m, ok := raw.(map[string]any); ok {
			for k, v := range m {
				p[k] = v
			}
		}
		p["ai"] = shared.MigrateAIConfig(p["ai"])
		next[i] = p
	}
	out["profiles"] = next
	return out
}

// readSettingsWithSecrets is the same, but decrypts the stored secrets first —
// asking for keychain access if that has not been settled yet. Only call this
// from something the user just did (opening Settings, a network git
// operation), never from start-up.
func (s *Store) readSettingsWithSecrets(ctx context.Context, reason keychain.Reason) (Settings, error) {
	s.mu.Lock()
	settings := s.readSettingsLocked()
	cached := s.secretCache != nil
	s.mu.Unlock()
	if cached {
		return settings, nil
	}

	secretsPath, err := s.file(SecretsFileName)
	if err != nil {
		return nil, err
	}
	// An install that already has an encrypted secrets file predates the consent
	// gate — its owner answered the OS prompt long ago, so they are not asked to
	// decide again, only shown the explanation once.
	if err := s.kc.AdoptExistingConsent([]string{secretsPath}); err != nil {
		return nil, err
	}
	ok, err := s.kc.EnsureKeychain(ctx, reason)
	if err != nil {
		return nil, err
	}
	if !ok {
		return settings, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.loadSecretsLocked()
	if shared.HasSettingsSecrets(settings) {
		// Upgrade from a plaintext settings file: the values on disk win, since
		// they are what the user last saved. Rewrite the JSON without them.
		ids := shared.ProfileIDs(settings)
		merged := shared.PruneSecrets(stored.Overlay(shared.ExtractSecrets(settings)), ids)
		if err := s.saveSecretsLocked(merged); err != nil {
			return nil, err
		}
		data, err := shared.MarshalIndent(shared.StripSettingsSecrets(settings))
		if err != nil {
			return nil, err
		}
		if err := s.writeSettingsFile(data); err != nil {
			return nil, err
		}
		s.secretCache = merged
		return withMigratedAI(shared.ApplySecrets(settings, merged)), nil
	}
	s.secretCache = stored
	return withMigratedAI(shared.ApplySecrets(settings, stored.Overlay(s.sessionSecrets))), nil
}

// activeProfile is the profile a repository authenticates as.
func activeProfile(settings Settings, boundID any) map[string]any {
	profiles := settings.Profiles()
	if shared.JSTruthy(boundID) {
		for _, p := range profiles {
			if jsEqual(p["id"], boundID) {
				return p
			}
		}
	}
	active := settings["activeProfileId"]
	for _, p := range profiles {
		if jsEqual(p["id"], active) {
			return p
		}
	}
	if raw, _ := settings["profiles"].([]any); len(raw) > 0 {
		p, _ := raw[0].(map[string]any)
		return p
	}
	return nil
}

// jsEqual is === for the ids compared here (strings in every real file).
func jsEqual(a, b any) bool {
	as, ok1 := a.(string)
	bs, ok2 := b.(string)
	if ok1 && ok2 {
		return as == bs
	}
	return a == nil && b == nil
}

// ActiveProfileToken is activeProfileToken: the personal access token for a
// given host, or "" when none is configured. Used by network git operations
// (push/pull/fetch) to authenticate non-interactively, mirroring how clone
// resolves its token.
//
// A repository explicitly bound to a profile uses that profile's token, even
// when a different one is active — otherwise a background fetch on one repo
// would authenticate as whichever repo the user happens to be looking at.
// repoPath "" means no repository.
func (s *Store) ActiveProfileToken(ctx context.Context, host RepoHost, repoPath string) (string, error) {
	settings, err := s.readSettingsWithSecrets(ctx, keychain.ReasonTokens)
	if err != nil {
		return "", err
	}
	var boundID any
	if repoPath != "" {
		if bindings, ok := settings.Object("repoProfiles"); ok {
			boundID = bindings[repoPath]
		}
	}
	profile := activeProfile(settings, boundID)
	field, ok := tokenField[host]
	if profile == nil || !ok {
		return "", nil
	}
	token, _ := profile[field].(string)
	return shared.JSTrim(token), nil
}

// ActiveProfileAiKey is activeProfileAiKey: the API key for one AI account of
// the active profile, or "" when none is configured.
//
// The renderer only ever sees these keys after the user opens Settings (that is
// the one call that unlocks). Every other AI action starts from a store that
// was hydrated with `settings:get`, whose keys are still encrypted on disk — so
// the main process resolves them here instead, exactly like ActiveProfileToken
// does for git. Without this, AI features fail on every fresh launch until
// Settings has been opened once.
//
// accountID comes from the resolved config the renderer sent, so a feature
// pointed at a second account authenticates with *that* account's key rather
// than whichever one happens to be the default. "" means the default account.
func (s *Store) ActiveProfileAiKey(ctx context.Context, accountID string) (string, error) {
	// Nothing stored means nothing to decrypt: never ask for keychain access to
	// discover that the user has no key.
	s.mu.Lock()
	cached := s.secretCache != nil
	s.mu.Unlock()
	if !cached {
		p, err := s.file(SecretsFileName)
		if err != nil || !exists(p) {
			return "", nil
		}
	}
	settings, err := s.readSettingsWithSecrets(ctx, keychain.ReasonTokens)
	if err != nil {
		return "", err
	}
	profile := activeProfile(settings, nil)
	if profile == nil {
		return "", nil
	}
	ai, ok := profile["ai"].(map[string]any)
	if !ok || ai == nil {
		return "", nil
	}
	var account map[string]any
	if accountID != "" {
		accounts, _ := ai["accounts"].([]any)
		for _, a := range accounts {
			if m, ok := a.(map[string]any); ok && jsEqual(m["id"], accountID) {
				account = m
				break
			}
		}
	} else {
		account = shared.AccountFor(ai, "")
	}
	// `account?.apiKey ?? profile.ai.apiKey`: an account with an empty key does
	// not fall back to the profile-level one.
	var key any
	if account != nil {
		key = account["apiKey"]
	}
	if key == nil {
		key = ai["apiKey"]
	}
	k, _ := key.(string)
	return shared.JSTrim(k), nil
}

// writeSettings persists a settings object coming from the renderer.
func (s *Store) writeSettings(ctx context.Context, settings map[string]any) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	secrets := shared.ExtractSecrets(settings)
	hasSecrets := len(secrets) > 0
	stripped, err := shared.MarshalIndent(shared.StripSettingsSecrets(settings))
	if err != nil {
		return err
	}

	// A credential-less save must not wipe the store unless the renderer was
	// actually holding the credentials (`servedSecrets`) — otherwise it simply
	// never had them to send back. Same while they are still encrypted on disk,
	// and when the file exists but this build could not decrypt it: our empty
	// view of the store is not the truth, and persisting it would erase the
	// real one.
	s.mu.Lock()
	if !hasSecrets && (!s.servedSecrets || s.secretCache == nil || s.secretsUnreadable) {
		defer s.mu.Unlock()
		return s.writeSettingsFile(stripped)
	}
	s.mu.Unlock()

	// Saving a token is the moment worth asking about — and only then. A settings
	// write with nothing sensitive in it never goes near the keychain.
	var allowed bool
	if hasSecrets {
		if allowed, err = s.kc.EnsureKeychain(ctx, keychain.ReasonTokens); err != nil {
			return err
		}
	} else {
		// Never triggers an OS prompt: it only reports a consent already given.
		allowed = s.kc.CanUseKeychain()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !allowed {
		// Refused (or no keyring): hold the secrets in memory for this session and
		// keep them out of the file, rather than writing them in the clear.
		if hasSecrets {
			s.sessionSecrets = shared.MergeSecretStores(s.sessionSecrets, secrets)
		}
		return s.writeSettingsFile(stripped)
	}
	// Secrets first: if the keychain write fails, the JSON on disk still holds the
	// previous values instead of being stripped with nowhere to read them from.
	//
	// A renderer that was served the secrets is authoritative: clearing a token
	// in the UI removes it here too, and so does deleting a profile. One that
	// never saw them can only add — its save is overlaid on the stored values,
	// so the credentials it was never given survive.
	ids := shared.ProfileIDs(settings)
	var base shared.SecretStore
	switch {
	case s.servedSecrets:
		base = shared.SecretStore{}
	case s.secretCache != nil:
		base = s.secretCache
	default:
		base = s.loadSecretsLocked()
	}
	merged := shared.PruneSecrets(shared.MergeSecretStores(base, secrets), ids)
	if err := s.saveSecretsLocked(merged); err != nil {
		return err
	}
	s.secretCache = merged
	s.sessionSecrets = shared.SecretStore{}
	return s.writeSettingsFile(stripped)
}

func (s *Store) withForcedOnboarding(settings Settings) Settings {
	s.mu.Lock()
	force := s.forceOnboarding
	s.mu.Unlock()
	if !force {
		return settings
	}
	out := Settings{}
	for k, v := range settings {
		out[k] = v
	}
	out["onboardingCompleted"] = false
	return out
}

func (s *Store) markServed(settings Settings) {
	s.mu.Lock()
	s.servedSecrets = shared.HasSettingsSecrets(settings)
	s.mu.Unlock()
}

// Get is `settings:get`: the settings without unlocking anything.
func (s *Store) Get(ctx context.Context) Settings {
	settings := s.withForcedOnboarding(s.ReadSettings(ctx))
	s.markServed(settings)
	return settings
}

// Unlock is `settings:unlock`, called when the user opens Settings: that is an
// explicit "show me my credentials", so it is a fair moment to decrypt (and to
// ask, if needed).
func (s *Store) Unlock(ctx context.Context) (Settings, error) {
	settings, err := s.readSettingsWithSecrets(ctx, keychain.ReasonSettings)
	if err != nil {
		return nil, err
	}
	settings = s.withForcedOnboarding(settings)
	s.markServed(settings)
	return settings, nil
}

// ErrNotObject is returned by Set for a payload that is not a JSON object.
var ErrNotObject = errors.New("settings: expected a JSON object")

// Set is `settings:set`: persist the renderer's settings.
func (s *Store) Set(ctx context.Context, settings Settings) error {
	if settings == nil {
		return ErrNotObject
	}
	if shared.JSTruthy(settings["onboardingCompleted"]) {
		s.mu.Lock()
		s.forceOnboarding = false
		s.mu.Unlock()
	}
	return s.writeSettings(ctx, settings)
}

// SetJSON is Set for a raw JSON payload, decoded losslessly (numbers stay
// json.Number).
func (s *Store) SetJSON(ctx context.Context, raw json.RawMessage) error {
	v, err := shared.DecodeJSON(raw)
	if err != nil {
		return err
	}
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return ErrNotObject
	}
	return s.Set(ctx, Settings(m))
}

var jsonFilter = []FileFilter{{Name: "JSON", Extensions: []string{"json"}}}

// ImportFile is `settings:importFile`: the parsed content of a JSON file the
// user picks, or null when they cancel or the file is not JSON.
func (s *Store) ImportFile(ctx context.Context) (json.RawMessage, error) {
	path, err := s.dialogs.OpenFile("Import Settings", jsonFilter)
	if err != nil || path == "" {
		// A cancelled native dialog surfaces as an error on some platforms.
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	v, err := shared.DecodeJSON(raw)
	if err != nil {
		return nil, nil
	}
	// Re-encoded rather than passed through, so invalid UTF-8 cannot reach the
	// bridge (JSON.parse saw U+FFFD there too).
	out, err := json.Marshal(v)
	if err != nil {
		return nil, nil
	}
	return out, nil
}

// ExportFile is `settings:exportFile`: writes data, pretty-printed, to a file
// the user picks. False when they cancel or the write fails.
func (s *Store) ExportFile(ctx context.Context, data json.RawMessage) (bool, error) {
	path, err := s.dialogs.SaveFile("Export Settings", SettingsFileName, jsonFilter)
	if err != nil || path == "" {
		return false, nil
	}
	if len(data) == 0 || !json.Valid(data) {
		// JSON.stringify(undefined) gave writeFile nothing to write.
		return false, nil
	}
	// The bridge hands over what JSON.stringify produced, so indenting it keeps
	// the renderer's key order and yields exactly JSON.stringify(data, null, 2).
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(data), "", "  "); err != nil {
		return false, nil
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return false, nil
	}
	return true, nil
}
