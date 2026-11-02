// Package keychain is the consent gate in front of the OS keychain, and the
// encryption that replaces Electron's safeStorage — a port of
// src/main/keychain.ts (port brief §5.3).
//
// Electron's safeStorage asks the OS for an encryption key, and on macOS that
// pops the alarming "Gitcito wants to use your confidential information stored
// in your keychain" dialog — with no explanation of what for. Every keychain
// touch therefore goes through here: nothing reaches the keychain until the user
// has seen our own window saying what is stored, what is not, and that none of
// it leaves the machine.
//
// The answer lives in its own tiny file rather than in the settings JSON, so
// reading it can never depend on the very code path it is meant to gate.
//
// # Encryption
//
// safeStorage is replaced by one random 32-byte master key kept in the OS
// keychain (github.com/zalando/go-keyring) under service paths.KeychainService(),
// account "master-key", stored as standard base64. It is created the first time
// it is needed after consent. Encrypt seals with AES-256-GCM; the format is
//
//	nonce (12 random bytes) || ciphertext || GCM tag (16 bytes)
//
// with no associated data. Callers that store it as text base64 it themselves,
// exactly as the TS stored base64 of safeStorage output.
//
// # Wiring
//
// The core never imports Wails: the ask goes out through an injected Emitter
// (`keychain:ask`, payload {reason, adopted}) and a HasWindow predicate, and the
// app layer calls Cancel when the window closes mid-question. Service (in
// service.go) is the thin facade bound to the page.
package keychain

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/GabrySpada/gitcito/internal/paths"
	"github.com/zalando/go-keyring"
)

// Consent mirrors KeychainConsent in src/shared/types.ts: whether the user has
// let Gitcito use the OS keychain.
type Consent string

// The KeychainConsent values.
const (
	Granted  Consent = "granted"
	Declined Consent = "declined"
	Unset    Consent = "unset"
)

// Reason mirrors KeychainReason: what Gitcito is about to encrypt, so the
// explainer can say why it asks.
type Reason string

// The KeychainReason values.
const (
	ReasonTokens   Reason = "tokens"
	ReasonVault    Reason = "vault"
	ReasonSettings Reason = "settings"
)

// ConsentFileName is the consent file inside the data dir.
const ConsentFileName = "gitcito-keychain.json"

// MasterKeyAccount is the keychain account holding the master key.
const MasterKeyAccount = "master-key"

// AskEvent is the event that asks the renderer to show the explainer.
const AskEvent = "keychain:ask"

const (
	keySize   = 32
	nonceSize = 12
)

// ErrNoConsent is returned by Encrypt/Decrypt while consent is not granted:
// those calls are where the OS prompt would appear, so they refuse to run
// before the gate has been passed.
var ErrNoConsent = errors.New("keychain: access not granted")

// ErrMasterKeyCorrupt means the keychain entry exists but is not a 32-byte
// base64 key. It is never overwritten: data sealed with the real key may
// still be recoverable by whoever wrote it.
var ErrMasterKeyCorrupt = errors.New("keychain: master key entry is not a valid key")

// Backend is the OS keychain. KeyringBackend is the real one; tests inject a
// fake (or call keyring.MockInit) so they never touch the user's keychain.
type Backend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
}

// KeyringBackend stores in the OS keychain through go-keyring.
type KeyringBackend struct{}

// Get reads one entry; a missing one is keyring.ErrNotFound.
func (KeyringBackend) Get(service, user string) (string, error) { return keyring.Get(service, user) }

// Set writes one entry.
func (KeyringBackend) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

// Emitter sends an event to the renderer.
type Emitter interface {
	Emit(name string, data any)
}

// AskPayload is the `keychain:ask` event's data.
type AskPayload struct {
	Reason  Reason `json:"reason"`
	Adopted bool   `json:"adopted"`
}

// Status is what `keychain:status` returns. Available is null unless consent
// is granted, exactly as the TS returned it.
type Status struct {
	Consent   Consent `json:"consent"`
	Explained bool    `json:"explained"`
	Available *bool   `json:"available"`
}

// consentFile is the consent file's JSON. Field order matches the TS object so
// the file is byte-identical to what keychain.ts wrote.
type consentFile struct {
	Consent Consent `json:"consent"`
	// Whether the user has actually seen our explanation (see AdoptExistingConsent).
	Explained bool `json:"explained"`
}

// Options configures a Keychain. Zero values pick the production behaviour.
type Options struct {
	// DataDir locates the consent file; defaults to paths.DataDir.
	DataDir func() (string, error)
	// Backend defaults to KeyringBackend.
	Backend Backend
	// Service defaults to paths.KeychainService().
	Service string
	// Emitter receives `keychain:ask`. Nil means there is nobody to ask.
	Emitter Emitter
	// HasWindow reports whether a window exists to ask in. Nil means none.
	HasWindow func() bool
}

// Keychain is the consent gate plus the master-key encryption. Safe for
// concurrent use.
type Keychain struct {
	dataDir   func() (string, error)
	backend   Backend
	service   string
	emitter   Emitter
	hasWindow func() bool

	mu     sync.Mutex
	cached *consentFile
	asking *ask

	keyMu sync.Mutex
	key   []byte
}

// ask is the single in-flight question every concurrent caller shares.
type ask struct {
	answer  chan bool // buffered(1); the first answer wins
	once    sync.Once
	done    chan struct{}
	granted bool
	err     error
}

func (a *ask) resolve(granted bool) {
	a.once.Do(func() { a.answer <- granted })
}

// New builds a Keychain.
func New(opts Options) *Keychain {
	k := &Keychain{
		dataDir:   opts.DataDir,
		backend:   opts.Backend,
		service:   opts.Service,
		emitter:   opts.Emitter,
		hasWindow: opts.HasWindow,
	}
	if k.dataDir == nil {
		k.dataDir = paths.DataDir
	}
	if k.backend == nil {
		k.backend = KeyringBackend{}
	}
	if k.service == "" {
		k.service = paths.KeychainService()
	}
	return k
}

func (k *Keychain) consentPath() (string, error) {
	dir, err := k.dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ConsentFileName), nil
}

// readFileState returns the consent file, cached after the first read — a
// missing or unreadable file reads (and caches) as unset.
func (k *Keychain) readFileState() consentFile {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.readFileStateLocked()
}

func (k *Keychain) readFileStateLocked() consentFile {
	if k.cached != nil {
		return *k.cached
	}
	state := consentFile{Consent: Unset}
	if p, err := k.consentPath(); err == nil {
		if raw, err := os.ReadFile(p); err == nil {
			var parsed map[string]any
			if json.Unmarshal(raw, &parsed) == nil && parsed != nil {
				if c, _ := parsed["consent"].(string); c == string(Granted) || c == string(Declined) {
					state.Consent = Consent(c)
				}
				state.Explained = parsed["explained"] == true
			}
		}
	}
	k.cached = &state
	return state
}

func (k *Keychain) writeFileState(next consentFile) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.writeFileStateLocked(next)
}

func (k *Keychain) writeFileStateLocked(next consentFile) error {
	// The cache moves first, as in the TS: a failed write still changes this
	// run's answer.
	k.cached = &next
	dir, err := k.dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ConsentFileName), data, 0o600)
}

// GetConsent is getConsent.
func (k *Keychain) GetConsent() Consent { return k.readFileState().Consent }

// SetConsent is setConsent: records an answer the user gave knowingly, so it
// also marks the explanation as seen.
func (k *Keychain) SetConsent(value Consent) error {
	return k.writeFileState(consentFile{Consent: value, Explained: true})
}

// AdoptExistingConsent is adoptExistingConsent. Someone who already has
// encrypted data on disk answered the OS prompt long before this gate existed
// — re-asking them would look like a regression and would hide their tokens
// until they clicked again. Adopt that as consent.
func (k *Keychain) AdoptExistingConsent(files []string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.readFileStateLocked().Consent != Unset {
		return nil
	}
	for _, p := range files {
		if _, err := os.Stat(p); err == nil {
			// `explained: false` — they keep their access, but still get the
			// explanation once, before the system dialog can appear out of nowhere.
			return k.writeFileStateLocked(consentFile{Consent: Granted, Explained: false})
		}
	}
	return nil
}

// CanUseKeychain is canUseKeychain: true when the keychain may be used right
// now, without asking anything.
func (k *Keychain) CanUseKeychain() bool {
	if k.GetConsent() != Granted {
		return false
	}
	return k.Available()
}

// Available is safeStorage.isEncryptionAvailable(): whether the keychain
// backend works and holds (or can now hold) the master key. False when there
// is no usable backend — no Secret Service on Linux, a locked or refused
// keychain — or when the stored entry is corrupt.
//
// It touches the OS keychain (creating the key on first use), so — like the
// TS, which only ever called isEncryptionAvailable behind the consent check —
// call it only once consent is granted. CanUseKeychain and EnsureKeychain do
// exactly that.
func (k *Keychain) Available() bool {
	_, err := k.masterKey()
	return err == nil
}

// masterKey loads the key from the keychain, creating it on first use. A
// loaded key is kept for the rest of the run; a failure is not, so a keyring
// that comes up later is picked up on the next call.
func (k *Keychain) masterKey() ([]byte, error) {
	k.keyMu.Lock()
	defer k.keyMu.Unlock()
	if k.key != nil {
		return k.key, nil
	}
	stored, err := k.backend.Get(k.service, MasterKeyAccount)
	switch {
	case err == nil:
		key, decErr := base64.StdEncoding.DecodeString(stored)
		if decErr != nil || len(key) != keySize {
			return nil, ErrMasterKeyCorrupt
		}
		k.key = key
		return key, nil
	case errors.Is(err, keyring.ErrNotFound):
		key := make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("keychain: generate master key: %w", err)
		}
		if err := k.backend.Set(k.service, MasterKeyAccount, base64.StdEncoding.EncodeToString(key)); err != nil {
			return nil, fmt.Errorf("keychain: store master key: %w", err)
		}
		k.key = key
		return key, nil
	default:
		return nil, fmt.Errorf("keychain: read master key: %w", err)
	}
}

// Encrypt seals plaintext with the master key (format in the package doc).
// Refuses with ErrNoConsent unless consent is granted.
func (k *Keychain) Encrypt(plaintext []byte) ([]byte, error) {
	if k.GetConsent() != Granted {
		return nil, ErrNoConsent
	}
	key, err := k.masterKey()
	if err != nil {
		return nil, err
	}
	return Seal(key, plaintext)
}

// Decrypt opens what Encrypt sealed. Refuses with ErrNoConsent unless consent
// is granted; fails on anything sealed with another key or tampered with.
func (k *Keychain) Decrypt(sealed []byte) ([]byte, error) {
	if k.GetConsent() != Granted {
		return nil, ErrNoConsent
	}
	key, err := k.masterKey()
	if err != nil {
		return nil, err
	}
	return Open(key, sealed)
}

// Seal is the raw format: AES-256-GCM under a 32-byte key, random 12-byte
// nonce prefixed. Exported so tests (and future format vectors) can produce
// files without a keychain.
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize, nonceSize+len(plaintext)+gcm.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keychain: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal.
func Open(key, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < nonceSize+gcm.Overhead() {
		return nil, errors.New("keychain: ciphertext too short")
	}
	plain, err := gcm.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("keychain: decrypt: %w", err)
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("keychain: key must be %d bytes", keySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}

// askUser asks the renderer to show the explainer, once, and waits for the
// answer. Concurrent callers share the same dialog. With no window to ask in
// (headless runs, tests) the answer is "no" — never a silent keychain prompt —
// and nothing is recorded.
//
// The question itself outlives a cancelled ctx: ctx only stops this caller
// waiting (it then reads as "no"), so other callers still get the user's answer.
func (k *Keychain) askUser(ctx context.Context, reason Reason, adopted bool) (bool, error) {
	k.mu.Lock()
	a := k.asking
	if a == nil {
		if k.emitter == nil || k.hasWindow == nil || !k.hasWindow() {
			k.mu.Unlock()
			return false, nil
		}
		a = &ask{answer: make(chan bool, 1), done: make(chan struct{})}
		k.asking = a
		go k.settle(a)
		k.mu.Unlock()
		k.emitter.Emit(AskEvent, AskPayload{Reason: reason, Adopted: adopted})
	} else {
		k.mu.Unlock()
	}
	select {
	case <-a.done:
		return a.granted, a.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// settle waits for the answer, records it, and only then releases the
// waiters — callers arriving while the answer is being written still share it.
func (k *Keychain) settle(a *ask) {
	granted := <-a.answer
	consent := Declined
	if granted {
		consent = Granted
	}
	err := k.SetConsent(consent)
	k.mu.Lock()
	// Cleared even when the write failed (the TS left a rejected promise behind
	// for good, which would have broken every later ask until restart).
	k.asking = nil
	k.mu.Unlock()
	a.granted, a.err = granted, err
	close(a.done)
}

// Answer is the renderer's reply to `keychain:ask`. Without a question in
// flight it does nothing.
func (k *Keychain) Answer(granted bool) {
	k.mu.Lock()
	a := k.asking
	k.mu.Unlock()
	if a != nil {
		a.resolve(granted)
	}
}

// Cancel answers "no" to a question in flight. The app layer calls it when the
// window closes mid-question, so callers are not left hanging; like the TS
// `win.once('closed')` path, that records the consent as declined.
func (k *Keychain) Cancel() { k.Answer(false) }

// EnsureKeychain is ensureKeychain: the gate for any operation that needs the
// keychain. Returns false when the user says no — callers must degrade (keep
// secrets in memory, leave the vault locked) rather than write anything
// sensitive to disk in the clear. An error means the answer could not be
// recorded (or ctx ended while waiting).
func (k *Keychain) EnsureKeychain(ctx context.Context, reason Reason) (bool, error) {
	state := k.readFileState()
	if state.Consent == Declined {
		return false, nil
	}
	// Granted but never explained: an install that predates this gate. Show the
	// explanation before the OS dialog, so it can still be turned down.
	if state.Consent == Granted && state.Explained {
		return k.CanUseKeychain(), nil
	}
	ok, err := k.askUser(ctx, reason, state.Consent == Granted)
	if err != nil || !ok {
		return false, err
	}
	// First real keychain call — the OS prompt now arrives right after our own
	// explanation, which is the whole point.
	return k.Available(), nil
}

// Status is `keychain:status`.
func (k *Keychain) Status() Status {
	state := k.readFileState()
	st := Status{Consent: state.Consent, Explained: state.Explained}
	if state.Consent == Granted {
		v := k.CanUseKeychain()
		st.Available = &v
	}
	return st
}

// Set is `keychain:set` — Settings → Security: turn it on or off after the fact.
func (k *Keychain) Set(granted bool) (bool, error) {
	if !granted {
		if err := k.SetConsent(Declined); err != nil {
			return false, err
		}
		return false, nil
	}
	// Turning it on from Settings → Security is itself an informed choice, so
	// it counts as explained: no dialog on the next keychain use.
	if err := k.SetConsent(Granted); err != nil {
		return false, err
	}
	return k.CanUseKeychain(), nil
}
