// Package vault is the port of src/main/vault.ts: a small local secrets store,
// encrypted at rest with the OS keychain. Two scopes: per-repo entries (keyed
// by repo path) and a global set referenceable from any repo. Never synced,
// never leaves this box.
//
// # File
//
// gitcito-vault.enc in the data dir is the base64 text of
// keychain.Encrypt(JSON of {repos, global}). The TS stored base64 of
// safeStorage output; the sealed format itself is new, which D3 (fresh start,
// no migration) allows — the same choice internal/settings made for its
// secrets file.
package vault

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/keychain"
	"github.com/GabrySpada/gitcito/internal/paths"
)

// FileName is the vault file inside the data dir.
const FileName = "gitcito-vault.enc"

// Scope is 'repo' | 'global'. Anything but "global" addresses the repo bucket,
// as in the TS.
type Scope string

// The Scope values.
const (
	ScopeRepo   Scope = "repo"
	ScopeGlobal Scope = "global"
)

// Entry is VaultEntry: one secret stored in the vault.
type Entry struct {
	ID    string  `json:"id"`
	Key   string  `json:"key"`
	Value string  `json:"value"`
	Note  *string `json:"note,omitempty"`
	// UpdatedAt is Unix ms (a JS number).
	UpdatedAt float64 `json:"updatedAt"`
}

// ListResult is VaultListResult: a repo's own entries plus the global ones.
// Available is false when the keychain cannot (or may not) be used.
type ListResult struct {
	Available bool    `json:"available"`
	Repo      []Entry `json:"repo"`
	Global    []Entry `json:"global"`
}

// Export is VaultExport: the whole vault, for backup/transfer.
type Export struct {
	Repos  map[string][]Entry `json:"repos"`
	Global []Entry            `json:"global"`
}

// BundleEntry is BundleVaultEntry in secureBundle.ts: a portable secret, no
// id/updatedAt. Note is nil when absent.
type BundleEntry struct {
	Key   string  `json:"key"`
	Value string  `json:"value"`
	Note  *string `json:"note,omitempty"`
}

// data is the decrypted file. Repo key order is not kept (Go maps); nothing
// reads it in order except exportAll, whose consumer does not care.
type data struct {
	Repos  map[string][]Entry `json:"repos"`
	Global []Entry            `json:"global"`
}

func empty() *data { return &data{Repos: map[string][]Entry{}, Global: []Entry{}} }

// Options configures a Store. Keychain is required.
type Options struct {
	// DataDir defaults to paths.DataDir.
	DataDir func() (string, error)
	// Keychain gates and performs every encryption.
	Keychain *keychain.Keychain
	// Now defaults to time.Now.
	Now func() time.Time
	// NewID defaults to a random UUID v4 (crypto.randomUUID).
	NewID func() string
}

// Store is the vault. Safe for concurrent use: every load-modify-save runs
// under one mutex, which is never held across a keychain question.
type Store struct {
	dataDir func() (string, error)
	kc      *keychain.Keychain
	now     func() time.Time
	newID   func() string

	mu sync.Mutex
	// A vault file exists but this build's master key cannot read it — written
	// by a different app identity (dev vs packaged). See the same flag in
	// internal/settings.
	unreadable bool
}

// New builds a Store.
func New(opts Options) *Store {
	if opts.Keychain == nil {
		panic("vault: Options.Keychain is required")
	}
	s := &Store{dataDir: opts.DataDir, kc: opts.Keychain, now: opts.Now, newID: opts.NewID}
	if s.dataDir == nil {
		s.dataDir = paths.DataDir
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newID == nil {
		s.newID = RandomUUID
	}
	return s
}

// RandomUUID is crypto.randomUUID(): a random version-4 UUID.
func RandomUUID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (s *Store) filePath() (string, error) {
	dir, err := s.dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

func (s *Store) nowMs() float64 { return float64(s.now().UnixMilli()) }

// load reads the vault; missing, corrupt, or key changed → start fresh.
// Callers hold s.mu.
func (s *Store) load() *data {
	p, err := s.filePath()
	if err != nil {
		return empty()
	}
	d, err := s.decrypt(p)
	if err != nil {
		_, statErr := os.Stat(p)
		s.unreadable = statErr == nil
		return empty()
	}
	s.unreadable = false
	return d
}

func (s *Store) decrypt(p string) (*data, error) {
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
	d := empty()
	if err := json.Unmarshal(plain, d); err != nil {
		return nil, err
	}
	// `{ ...empty(), ...parsed }`: a null field stays usable.
	if d.Repos == nil {
		d.Repos = map[string][]Entry{}
	}
	if d.Global == nil {
		d.Global = []Entry{}
	}
	return d, nil
}

// save writes the vault. Callers hold s.mu.
func (s *Store) save(d *data) error {
	dir, err := s.dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, FileName)
	if s.unreadable {
		// Preserve the copy the other build identity can still decrypt before we
		// overwrite it with a vault that started fresh.
		_ = copyFile(p, p+".bak")
		s.unreadable = false
	}
	plain, err := json.Marshal(d)
	if err != nil {
		return err
	}
	enc, err := s.kc.Encrypt(plain)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, []byte(base64.StdEncoding.EncodeToString(enc)))
}

func bucket(d *data, scope Scope, repoPath string) *[]Entry {
	if scope == ScopeGlobal {
		return &d.Global
	}
	if _, ok := d.Repos[repoPath]; !ok {
		d.Repos[repoPath] = []Entry{}
	}
	arr := d.Repos[repoPath]
	return &arr
}

func setBucket(d *data, scope Scope, repoPath string, arr []Entry) {
	if scope == ScopeGlobal {
		d.Global = arr
		return
	}
	d.Repos[repoPath] = arr
}

func unavailable() ListResult {
	return ListResult{Available: false, Repo: []Entry{}, Global: []Entry{}}
}

func listOf(d *data, repoPath string) ListResult {
	repo := d.Repos[repoPath]
	if repo == nil {
		repo = []Entry{}
	}
	return ListResult{Available: true, Repo: clone(repo), Global: clone(d.Global)}
}

func clone(in []Entry) []Entry { return append([]Entry{}, in...) }

func (s *Store) ensure(ctx context.Context) (bool, error) {
	return s.kc.EnsureKeychain(ctx, keychain.ReasonVault)
}

// List is `vault:list`.
func (s *Store) List(ctx context.Context, repoPath string) (ListResult, error) {
	p, err := s.filePath()
	if err != nil {
		return ListResult{}, err
	}
	// A vault file from before the consent gate means the OS prompt was already
	// answered once; don't make its owner do it again.
	if err := s.kc.AdoptExistingConsent([]string{p}); err != nil {
		return ListResult{}, err
	}
	// Opening the vault is an explicit ask for the thing the keychain protects,
	// so this is the right moment to explain and request access.
	ok, err := s.ensure(ctx)
	if err != nil {
		return ListResult{}, err
	}
	if !ok {
		return unavailable(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return listOf(s.load(), repoPath), nil
}

// Upsert is `vault:upsert`: replaces the entry with the same id, or adds it
// (with a fresh id when it has none).
func (s *Store) Upsert(ctx context.Context, scope Scope, repoPath string, entry Entry) (ListResult, error) {
	ok, err := s.ensure(ctx)
	if err != nil {
		return ListResult{}, err
	}
	if !ok {
		return unavailable(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	arr := *bucket(d, scope, repoPath)
	entry.UpdatedAt = s.nowMs()
	if entry.ID != "" {
		i := indexOf(arr, entry.ID)
		if i >= 0 {
			arr[i] = entry
		} else {
			arr = append(arr, entry)
		}
	} else {
		entry.ID = s.newID()
		arr = append(arr, entry)
	}
	setBucket(d, scope, repoPath, arr)
	if err := s.save(d); err != nil {
		return ListResult{}, err
	}
	return listOf(d, repoPath), nil
}

func indexOf(arr []Entry, id string) int {
	for i, e := range arr {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// Remove is `vault:remove`.
func (s *Store) Remove(ctx context.Context, scope Scope, repoPath, id string) (ListResult, error) {
	ok, err := s.ensure(ctx)
	if err != nil {
		return ListResult{}, err
	}
	if !ok {
		return unavailable(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	arr := *bucket(d, scope, repoPath)
	if i := indexOf(arr, id); i >= 0 {
		arr = append(arr[:i:i], arr[i+1:]...)
	}
	setBucket(d, scope, repoPath, arr)
	if err := s.save(d); err != nil {
		return ListResult{}, err
	}
	return listOf(d, repoPath), nil
}

// mergeEntries merges one entry list into another, incoming entries overriding
// by id (a Map: an existing id keeps its position, a new one goes last).
func mergeEntries(base, incoming []Entry) []Entry {
	out := []Entry{}
	pos := map[string]int{}
	set := func(e Entry) {
		if i, ok := pos[e.ID]; ok {
			out[i] = e
			return
		}
		pos[e.ID] = len(out)
		out = append(out, e)
	}
	for _, e := range base {
		set(e)
	}
	for _, e := range incoming {
		if e.ID != "" {
			set(e)
		}
	}
	return out
}

// ExportAll is `vault:exportAll`: the whole vault, for backup/transfer. Empty
// when the keychain is unavailable.
func (s *Store) ExportAll(ctx context.Context) (Export, error) {
	ok, err := s.ensure(ctx)
	if err != nil {
		return Export{}, err
	}
	if !ok {
		return Export{Repos: map[string][]Entry{}, Global: []Entry{}}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	repos := make(map[string][]Entry, len(d.Repos))
	for k, v := range d.Repos {
		repos[k] = clone(v)
	}
	return Export{Repos: repos, Global: clone(d.Global)}, nil
}

// ImportAll is `vault:importAll`: merges an imported vault into the local one
// (incoming wins per id). No-op when the keychain is unavailable (secrets
// cannot be persisted safely) or incoming is null.
func (s *Store) ImportAll(ctx context.Context, incoming *Export) error {
	if incoming == nil {
		return nil
	}
	ok, err := s.ensure(ctx)
	if err != nil || !ok {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	d.Global = mergeEntries(d.Global, incoming.Global)
	for path, entries := range incoming.Repos {
		d.Repos[path] = mergeEntries(d.Repos[path], entries)
	}
	return s.save(d)
}

// ExportGlobalEntries is exportGlobalEntries: the global secrets as portable
// bundle entries (no id/updatedAt). Empty when the keychain may not be used
// right now — it never asks. Used by secure share to pack a vault section.
func (s *Store) ExportGlobalEntries(ctx context.Context) ([]BundleEntry, error) {
	if !s.kc.CanUseKeychain() {
		return []BundleEntry{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	out := make([]BundleEntry, 0, len(d.Global))
	for _, e := range d.Global {
		be := BundleEntry{Key: e.Key, Value: e.Value}
		if e.Note != nil && *e.Note != "" {
			note := *e.Note
			be.Note = &note
		}
		out = append(out, be)
	}
	return out, nil
}

// ImportGlobalEntries is importGlobalEntries: merges bundle entries into the
// global vault, matching by key (incoming value/note win). Returns how many
// were written; 0 when the keychain is unavailable.
func (s *Store) ImportGlobalEntries(ctx context.Context, entries []BundleEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	ok, err := s.ensure(ctx)
	if err != nil || !ok {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	now := s.nowMs()
	for _, inc := range entries {
		found := false
		for i := range d.Global {
			if d.Global[i].Key == inc.Key {
				e := &d.Global[i]
				e.Value = inc.Value
				if inc.Note != nil {
					e.Note = inc.Note
				}
				e.UpdatedAt = now
				found = true
				break
			}
		}
		if !found {
			d.Global = append(d.Global, Entry{ID: s.newID(), Key: inc.Key, Value: inc.Value, Note: inc.Note, UpdatedAt: now})
		}
	}
	if err := s.save(d); err != nil {
		return 0, err
	}
	return len(entries), nil
}

// writeFileAtomic replaces a file in one step, owner-only: a reader racing a
// write never sees half a vault.
func writeFileAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
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
