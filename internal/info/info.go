// Package info is the port of src/main/info.ts: per-repo, non-private
// reference metadata (App ID, bundle id, website, social handles…). Unlike the
// vault these are NOT secrets, so they are stored in plain JSON in the data dir
// (gitcito-info.json) — never synced, but never encrypted either. Keyed by repo
// path.
package info

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/paths"
)

// FileName is the info file inside the data dir.
const FileName = "gitcito-info.json"

// Entry is InfoEntry: a non-private, per-repo info field.
type Entry struct {
	ID string `json:"id"`
	// Label is the display label, e.g. "Bundle ID".
	Label string `json:"label"`
	// Value is the value itself, e.g. "com.acme.app" or a URL/handle.
	Value string `json:"value"`
	// Field is the key into the field-preset catalog (icon + link behaviour).
	Field string `json:"field"`
	// UpdatedAt is Unix ms (a JS number).
	UpdatedAt float64 `json:"updatedAt"`
}

// Export is InfoExport: the whole info store, for backup/transfer.
type Export struct {
	Repos map[string][]Entry `json:"repos"`
}

// Options configures a Store. Zero values pick the production behaviour.
type Options struct {
	// DataDir defaults to paths.DataDir.
	DataDir func() (string, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// NewID defaults to a random UUID v4.
	NewID func() string
}

// Store is the info store. Every load-modify-save runs under one mutex.
type Store struct {
	dataDir func() (string, error)
	now     func() time.Time
	newID   func() string
	mu      sync.Mutex
}

// New builds a Store.
func New(opts Options) *Store {
	s := &Store{dataDir: opts.DataDir, now: opts.Now, newID: opts.NewID}
	if s.dataDir == nil {
		s.dataDir = paths.DataDir
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newID == nil {
		s.newID = randomUUID
	}
	return s
}

type data struct {
	Repos map[string][]Entry `json:"repos"`
}

// load reads the file; missing or corrupt → start fresh. Callers hold s.mu.
func (s *Store) load() *data {
	d := &data{Repos: map[string][]Entry{}}
	dir, err := s.dataDir()
	if err != nil {
		return d
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return d
	}
	if json.Unmarshal(raw, d) != nil {
		return &data{Repos: map[string][]Entry{}}
	}
	if d.Repos == nil {
		d.Repos = map[string][]Entry{}
	}
	return d
}

// save writes the file pretty-printed like JSON.stringify(data, null, 2)
// (repo keys sorted, which nothing depends on). Callers hold s.mu.
func (s *Store) save(d *data) error {
	dir, err := s.dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, FileName), bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

func clone(in []Entry) []Entry { return append([]Entry{}, in...) }

// List is `info:list`.
func (s *Store) List(repoPath string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.load().Repos[repoPath])
}

// Upsert is `info:upsert`: replaces the entry with the same id, or adds it
// (with a fresh id when it has none). Returns the repo's entries.
func (s *Store) Upsert(repoPath string, entry Entry) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	arr := d.Repos[repoPath]
	if arr == nil {
		arr = []Entry{}
	}
	entry.UpdatedAt = float64(s.now().UnixMilli())
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
	d.Repos[repoPath] = arr
	if err := s.save(d); err != nil {
		return nil, err
	}
	return clone(arr), nil
}

func indexOf(arr []Entry, id string) int {
	for i, e := range arr {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// Remove is `info:remove`.
func (s *Store) Remove(repoPath, id string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	arr, ok := d.Repos[repoPath]
	if i := indexOf(arr, id); i >= 0 {
		arr = append(arr[:i:i], arr[i+1:]...)
	}
	// An unknown repo is not created (the TS spliced a throwaway array).
	if ok {
		d.Repos[repoPath] = arr
	}
	if err := s.save(d); err != nil {
		return nil, err
	}
	return clone(arr), nil
}

// Reorder is `info:reorder`: entries in the order of ids (an id listed twice
// yields the entry twice, as in the TS); entries missing from ids keep their
// relative order at the end.
func (s *Store) Reorder(repoPath string, ids []string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	arr := d.Repos[repoPath]
	byID := map[string]Entry{}
	for _, e := range arr {
		byID[e.ID] = e
	}
	listed := map[string]bool{}
	next := []Entry{}
	for _, id := range ids {
		listed[id] = true
		if e, ok := byID[id]; ok {
			next = append(next, e)
		}
	}
	// keep any entries not present in the id list (defensive) at the end
	for _, e := range arr {
		if !listed[e.ID] {
			next = append(next, e)
		}
	}
	d.Repos[repoPath] = next
	if err := s.save(d); err != nil {
		return nil, err
	}
	return clone(next), nil
}

// ExportAll is `info:exportAll`: the whole info store, for backup/transfer.
func (s *Store) ExportAll() Export {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	repos := make(map[string][]Entry, len(d.Repos))
	for k, v := range d.Repos {
		repos[k] = clone(v)
	}
	return Export{Repos: repos}
}

// ImportAll is `info:importAll`: merges imported info into the local store
// (incoming wins per id). Null is a no-op.
func (s *Store) ImportAll(incoming *Export) error {
	if incoming == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.load()
	for path, entries := range incoming.Repos {
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
		for _, e := range d.Repos[path] {
			set(e)
		}
		for _, e := range entries {
			if e.ID != "" {
				set(e)
			}
		}
		d.Repos[path] = out
	}
	return s.save(d)
}

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

// randomUUID is crypto.randomUUID(): a random version-4 UUID.
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails on supported platforms
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
