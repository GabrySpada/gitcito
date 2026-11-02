package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/keychain"
	"github.com/zalando/go-keyring"
)

// Nothing here may reach the real OS keychain: go-keyring's provider is the
// in-memory mock, and every Keychain gets an injected fake backend besides.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

type fakeBackend struct {
	mu      sync.Mutex
	entries map[string]string
}

func (f *fakeBackend) Get(service, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.entries[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeBackend) Set(service, user, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[service+"/"+user] = password
	return nil
}

type harness struct {
	dir   string
	kc    *keychain.Keychain
	store *Store
	now   int64
	ids   int
}

func newHarness(t *testing.T, consent keychain.Consent) *harness {
	t.Helper()
	h := &harness{dir: t.TempDir()}
	dataDir := func() (string, error) { return h.dir, nil }
	h.kc = keychain.New(keychain.Options{
		DataDir: dataDir,
		Backend: &fakeBackend{entries: map[string]string{}},
		Service: "com.gitcito.desktop.test",
	})
	if consent != keychain.Unset {
		if err := h.kc.SetConsent(consent); err != nil {
			t.Fatal(err)
		}
	}
	h.store = New(Options{
		DataDir:  dataDir,
		Keychain: h.kc,
		Now:      func() time.Time { return time.UnixMilli(h.now) },
		NewID: func() string {
			h.ids++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", h.ids)
		},
	})
	return h
}

type scenarioOp struct {
	Op        string            `json:"op"`
	Args      []json.RawMessage `json:"args"`
	Now       *int64            `json:"now"`
	Available *bool             `json:"available"`
	Result    json.RawMessage   `json:"result"`
}

func sameJSON(t *testing.T, what string, got any, want json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	_ = json.Unmarshal(raw, &g)
	_ = json.Unmarshal(want, &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, raw, want)
	}
}

func arg[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestScenarioVector replays internal/vault/testdata/scenario.json, captured
// from src/main/vault.ts through its ipcMain handlers with a fixed clock and
// UUID source. "available: false" there is the keychain refused (consent
// declined here).
func TestScenarioVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "scenario.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Ops   []scenarioOp    `json:"ops"`
		Plain json.RawMessage `json:"plain"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, keychain.Granted)
	ctx := context.Background()
	var lastPlain []byte
	for i, op := range v.Ops {
		if op.Now != nil {
			h.now = *op.Now
		}
		if op.Available != nil {
			c := keychain.Declined
			if *op.Available {
				c = keychain.Granted
			}
			if err := h.kc.SetConsent(c); err != nil {
				t.Fatal(err)
			}
		}
		var got any
		switch op.Op {
		case "list":
			got, err = h.store.List(ctx, arg[string](t, op.Args[0]))
		case "upsert":
			got, err = h.store.Upsert(ctx, arg[Scope](t, op.Args[0]), arg[string](t, op.Args[1]), arg[Entry](t, op.Args[2]))
		case "remove":
			got, err = h.store.Remove(ctx, arg[Scope](t, op.Args[0]), arg[string](t, op.Args[1]), arg[string](t, op.Args[2]))
		case "exportAll":
			got, err = h.store.ExportAll(ctx)
		case "importAll":
			err = h.store.ImportAll(ctx, arg[*Export](t, op.Args[0]))
		case "exportGlobalEntries":
			got, err = h.store.ExportGlobalEntries(ctx)
		case "importGlobalEntries":
			got, err = h.store.ImportGlobalEntries(ctx, arg[[]BundleEntry](t, op.Args[0]))
		default:
			t.Fatalf("unknown op %s", op.Op)
		}
		if err != nil {
			t.Fatalf("op %d %s: %v", i, op.Op, err)
		}
		sameJSON(t, fmt.Sprintf("op %d %s", i, op.Op), got, op.Result)
		if h.kc.GetConsent() == keychain.Granted {
			lastPlain = h.decryptFile(t)
		}
	}
	sameJSON(t, "file", json.RawMessage(lastPlain), v.Plain)
}

func (h *harness) decryptFile(t *testing.T) []byte {
	t.Helper()
	b64, err := os.ReadFile(filepath.Join(h.dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return []byte("null")
	}
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := h.kc.Decrypt(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func TestFileIsEncryptedAndOwnerOnly(t *testing.T) {
	h := newHarness(t, keychain.Granted)
	if _, err := h.store.Upsert(context.Background(), ScopeGlobal, "", Entry{Key: "K", Value: "super-secret-value"}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(h.dir, FileName)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-value") {
		t.Error("vault file holds the secret in the clear")
	}
	sealed, _ := base64.StdEncoding.DecodeString(string(raw))
	if strings.Contains(string(sealed), "super-secret-value") {
		t.Error("vault file is not encrypted")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
}

func TestUnreadableVaultIsBackedUpBeforeOverwrite(t *testing.T) {
	h := newHarness(t, keychain.Granted)
	p := filepath.Join(h.dir, FileName)
	if err := os.WriteFile(p, []byte("bm90IG91cnM="), 0o600); err != nil { // "not ours"
		t.Fatal(err)
	}
	res, err := h.store.List(context.Background(), "/r")
	if err != nil || !res.Available || len(res.Global) != 0 {
		t.Fatalf("list = %+v, %v", res, err)
	}
	if _, err := h.store.Upsert(context.Background(), ScopeGlobal, "", Entry{Key: "K", Value: "v"}); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p + ".bak")
	if err != nil || string(bak) != "bm90IG91cnM=" {
		t.Errorf("backup = %q, %v", bak, err)
	}
}

func TestNoKeychainNoAccessNoAsk(t *testing.T) {
	// Undecided consent and no window to ask in: everything degrades to
	// "unavailable", and nothing is written.
	h := newHarness(t, keychain.Unset)
	ctx := context.Background()
	res, err := h.store.List(ctx, "/r")
	if err != nil || res.Available {
		t.Fatalf("list = %+v, %v", res, err)
	}
	if out, _ := json.Marshal(res); string(out) != `{"available":false,"repo":[],"global":[]}` {
		t.Errorf("json = %s", out)
	}
	if n, err := h.store.ImportGlobalEntries(ctx, []BundleEntry{{Key: "K", Value: "v"}}); n != 0 || err != nil {
		t.Errorf("import = %d, %v", n, err)
	}
	if e, _ := h.store.ExportGlobalEntries(ctx); len(e) != 0 {
		t.Errorf("export = %+v", e)
	}
	if _, err := os.Stat(filepath.Join(h.dir, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Error("vault file written without the keychain")
	}
}

func TestConcurrentUpsertsAllLand(t *testing.T) {
	h := newHarness(t, keychain.Granted)
	h.store.newID = RandomUUID
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := h.store.Upsert(context.Background(), ScopeRepo, "/r", Entry{Key: fmt.Sprint(i), Value: "v"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	res, err := h.store.List(context.Background(), "/r")
	if err != nil || len(res.Repo) != 20 {
		t.Errorf("got %d entries, %v", len(res.Repo), err)
	}
}

func TestRandomUUIDShape(t *testing.T) {
	id := RandomUUID()
	if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		t.Errorf("uuid = %s", id)
	}
}
