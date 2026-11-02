package settings

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/GabrySpada/gitcito/internal/keychain"
	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/zalando/go-keyring"
)

// Nothing here may reach the real OS keychain: go-keyring's provider is the
// in-memory mock, and every Store gets an injected fake backend besides.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

const testService = "com.gitcito.desktop.test"

type fakeBackend struct {
	mu      sync.Mutex
	entries map[string]string
	down    bool
}

func (f *fakeBackend) Get(service, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return "", errors.New("no secret service")
	}
	v, ok := f.entries[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeBackend) Set(service, user, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errors.New("no secret service")
	}
	f.entries[service+"/"+user] = password
	return nil
}

// masterKey creates (or returns) the key a Keychain would use, so tests can
// write and read secrets files directly.
func (f *fakeBackend) masterKey(t *testing.T) []byte {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.entries[testService+"/"+keychain.MasterKeyAccount]; ok {
		key, _ := base64.StdEncoding.DecodeString(v)
		return key
	}
	key := bytes.Repeat([]byte{42}, 32)
	f.entries[testService+"/"+keychain.MasterKeyAccount] = base64.StdEncoding.EncodeToString(key)
	return key
}

type asker struct {
	mu        sync.Mutex
	asks      []keychain.AskPayload
	hasWindow bool
	answer    bool
	k         *keychain.Keychain
}

func (a *asker) Emit(_ string, data any) {
	a.mu.Lock()
	a.asks = append(a.asks, data.(keychain.AskPayload))
	answer := a.answer
	a.mu.Unlock()
	a.k.Answer(answer)
}

func (a *asker) window() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hasWindow
}

type fakeDialogs struct {
	openPath, savePath string
	err                error
	calls              []string
}

func (d *fakeDialogs) OpenFile(title string, filters []FileFilter) (string, error) {
	d.calls = append(d.calls, "open:"+title+":"+filters[0].Name+":"+pattern(filters[0].Extensions))
	return d.openPath, d.err
}

func (d *fakeDialogs) SaveFile(title, defaultName string, filters []FileFilter) (string, error) {
	d.calls = append(d.calls, "save:"+title+":"+defaultName+":"+pattern(filters[0].Extensions))
	return d.savePath, d.err
}

type harness struct {
	dir     string
	backend *fakeBackend
	ask     *asker
	kc      *keychain.Keychain
	store   *Store
	dialogs *fakeDialogs
}

func newHarness(t *testing.T, force bool) *harness {
	t.Helper()
	h := &harness{
		dir:     t.TempDir(),
		backend: &fakeBackend{entries: map[string]string{}},
		ask:     &asker{hasWindow: true, answer: true},
		dialogs: &fakeDialogs{},
	}
	dataDir := func() (string, error) { return h.dir, nil }
	h.kc = keychain.New(keychain.Options{
		DataDir: dataDir, Backend: h.backend, Service: testService, Emitter: h.ask, HasWindow: h.ask.window,
	})
	h.ask.k = h.kc
	h.store = New(Options{
		DataDir: dataDir, Keychain: h.kc, Dialogs: h.dialogs,
		Getenv: func(k string) string {
			if force && k == EnvForceOnboarding {
				return "1"
			}
			return ""
		},
	})
	return h
}

func (h *harness) path(name string) string { return filepath.Join(h.dir, name) }

func (h *harness) writeSecrets(t *testing.T, store any) {
	t.Helper()
	plain, _ := json.Marshal(store)
	sealed, err := keychain.Seal(h.backend.masterKey(t), plain)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(h.path(SecretsFileName), []byte(base64.StdEncoding.EncodeToString(sealed)), 0o600)
}

// readSecrets decrypts the secrets file: nil when absent, "unreadable" when
// the key cannot open it.
func (h *harness) readSecrets(t *testing.T) any {
	t.Helper()
	raw, err := os.ReadFile(h.path(SecretsFileName))
	if err != nil {
		return nil
	}
	sealed, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return "unreadable"
	}
	plain, err := keychain.Open(h.backend.masterKey(t), sealed)
	if err != nil {
		return "unreadable"
	}
	var v any
	json.Unmarshal(plain, &v)
	return v
}

func canon(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	json.Unmarshal(raw, &out)
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

// TestSettingsScenarios replays testdata/vectors/settings.json: each scenario
// was run through the real src/main/settings.ts + keychain.ts (with a fake
// electron) and recorded — every return value, every keychain question, and
// the files left on disk.
func TestSettingsScenarios(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := shared.DecodeJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := v.(map[string]any)["scenarios"].([]any)
	if len(scenarios) < 20 {
		t.Fatalf("only %d scenarios", len(scenarios))
	}
	for _, s := range scenarios {
		sc := s.(map[string]any)
		t.Run(sc["name"].(string), func(t *testing.T) { replay(t, sc) })
	}
}

func replay(t *testing.T, sc map[string]any) {
	ctx := context.Background()
	setup := sc["setup"].(map[string]any)
	h := newHarness(t, setup["forceOnboarding"] == true)
	h.ask.hasWindow = setup["hasWindow"] == true
	h.ask.answer = setup["answer"] == true
	if f, ok := setup["settingsFile"].(string); ok {
		os.WriteFile(h.path(SettingsFileName), []byte(f), 0o600)
	}
	switch secrets := setup["secrets"].(type) {
	case string: // "garbage": bytes this build cannot decrypt
		os.WriteFile(h.path(SecretsFileName), []byte("Z2FyYmFnZQ=="), 0o600)
	case map[string]any:
		h.writeSecrets(t, secrets)
	}
	if c := setup["consent"]; c != nil {
		data, _ := json.Marshal(c)
		os.WriteFile(h.path(keychain.ConsentFileName), data, 0o600)
	}
	// Unavailable = the keyring backend refuses everything (set up last, so the
	// fixtures above could still be written).
	h.backend.down = setup["available"] != true

	// "" stands for the TS `undefined`.
	orNull := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	want := sc["results"].([]any)
	for i, st := range sc["steps"].([]any) {
		step := st.(map[string]any)
		var got any
		switch step["op"] {
		case "get":
			got = h.store.Get(ctx)
		case "unlock":
			s, err := h.store.Unlock(ctx)
			if err != nil {
				t.Fatalf("step %d unlock: %v", i, err)
			}
			got = s
		case "set":
			if err := h.store.Set(ctx, Settings(step["settings"].(map[string]any))); err != nil {
				t.Fatalf("step %d set: %v", i, err)
			}
		case "token":
			repo, _ := step["repoPath"].(string)
			tok, err := h.store.ActiveProfileToken(ctx, RepoHost(step["host"].(string)), repo)
			if err != nil {
				t.Fatalf("step %d token: %v", i, err)
			}
			got = orNull(tok)
		case "aiKey":
			id, _ := step["accountId"].(string)
			key, err := h.store.ActiveProfileAiKey(ctx, id)
			if err != nil {
				t.Fatalf("step %d aiKey: %v", i, err)
			}
			got = orNull(key)
		case "status":
			got = keychain.NewService(h.kc).Status()
		case "keychainSet":
			ok, err := h.kc.Set(step["granted"] == true)
			if err != nil {
				t.Fatalf("step %d keychainSet: %v", i, err)
			}
			got = ok
		default:
			t.Fatalf("unknown op %v", step["op"])
		}
		assertJSON(t, "step "+string(rune('0'+i))+" "+step["op"].(string), got, want[i])
	}

	assertJSON(t, "asks", nonNil(h.ask.asks), sc["asks"])
	final := sc["final"].(map[string]any)

	var settingsFile any
	if raw, err := os.ReadFile(h.path(SettingsFileName)); err == nil {
		var parsed any
		if json.Unmarshal(raw, &parsed) == nil {
			settingsFile = map[string]any{"json": parsed}
		} else {
			settingsFile = map[string]any{"raw": string(raw)}
		}
	}
	assertJSON(t, "settings file", settingsFile, final["settings"])
	assertJSON(t, "secrets file", h.readSecrets(t), final["secrets"])

	var backup any
	if raw, err := os.ReadFile(h.path(SecretsFileName + ".bak")); err == nil {
		backup = "other"
		if string(raw) == "Z2FyYmFnZQ==" {
			backup = "garbage"
		}
	}
	assertJSON(t, "backup", backup, final["backup"])

	var consent any
	if raw, err := os.ReadFile(h.path(keychain.ConsentFileName)); err == nil {
		json.Unmarshal(raw, &consent)
	}
	assertJSON(t, "consent file", consent, final["consent"])
}

func nonNil(a []keychain.AskPayload) []keychain.AskPayload {
	if a == nil {
		return []keychain.AskPayload{}
	}
	return a
}

func TestRoundTripIsLossless(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, false)
	payload := `{"language":"it","future":{"nested":[1,2.5,1e300,-0,"x",null,true]},"big":12345678901234567890,
		"html":"<b>&amp;</b>","profiles":[{"id":"p","ai":{"provider":"openai","accounts":[{"id":"a","apiKey":"","x":{"y":1}}]}}]}`
	if err := h.store.SetJSON(ctx, json.RawMessage(payload)); err != nil {
		t.Fatal(err)
	}
	disk, _ := os.ReadFile(h.path(SettingsFileName))
	if !bytes.Contains(disk, []byte(`"<b>&amp;</b>"`)) || !bytes.Contains(disk, []byte("12345678901234567890")) {
		t.Errorf("file lost fidelity:\n%s", disk)
	}
	if !bytes.HasPrefix(disk, []byte("{\n  \"")) {
		t.Errorf("not pretty-printed with two spaces:\n%s", disk[:20])
	}
	got := h.store.Get(ctx)
	if got["big"] != json.Number("12345678901234567890") {
		t.Errorf("big = %#v", got["big"])
	}
	future, _ := got.Object("future")
	assertJSON(t, "unknown field", future, map[string]any{"nested": []any{1, 2.5, 1e300, 0, "x", nil, true}})
	acc := got.Profiles()[0]["ai"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	assertJSON(t, "unknown account field", acc["x"], map[string]any{"y": 1})
	if v, ok := got.String("language"); !ok || v != "it" {
		t.Errorf("language = %v", v)
	}
	if v, ok := got.Bool("onboardingCompleted"); !ok || !v {
		t.Errorf("onboardingCompleted = %v %v", v, ok)
	}
	if n, ok := got.Number("codeFontSize"); !ok || n != 12 {
		t.Errorf("codeFontSize = %v", n)
	}
}

func TestSetRejectsNonObjects(t *testing.T) {
	h := newHarness(t, false)
	for _, bad := range []string{"null", "[]", "3", `"x"`, "{"} {
		if err := h.store.SetJSON(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("SetJSON(%s) accepted", bad)
		}
	}
	if err := h.store.Set(context.Background(), nil); !errors.Is(err, ErrNotObject) {
		t.Errorf("Set(nil) = %v", err)
	}
}

func TestNoSecretsFileNeverAsks(t *testing.T) {
	h := newHarness(t, false)
	key, err := h.store.ActiveProfileAiKey(context.Background(), "")
	if key != "" || err != nil || len(h.ask.asks) != 0 {
		t.Errorf("key=%q err=%v asks=%d", key, err, len(h.ask.asks))
	}
}

func TestSecretsFileFormat(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, false)
	s := Settings(shared.DefaultSettings())
	s["profiles"].([]any)[0].(map[string]any)["githubToken"] = "ghp_x"
	if err := h.store.Set(ctx, s); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(h.path(SecretsFileName))
	sealed, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("not base64: %q", raw)
	}
	plain, err := keychain.Open(h.backend.masterKey(t), sealed)
	if err != nil || string(plain) != `{"default":{"githubToken":"ghp_x"}}` {
		t.Errorf("plain = %s, %v", plain, err)
	}
	disk, _ := os.ReadFile(h.path(SettingsFileName))
	if bytes.Contains(disk, []byte("ghp_x")) {
		t.Error("token written to the settings JSON")
	}
	for _, name := range []string{SettingsFileName, SecretsFileName} {
		if info, _ := os.Stat(h.path(name)); info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode %v", name, info.Mode())
		}
	}
	if tok, _ := h.store.ActiveProfileToken(ctx, HostGitHub, ""); tok != "ghp_x" {
		t.Errorf("token = %q", tok)
	}
	if tok, _ := h.store.ActiveProfileToken(ctx, RepoHost("nope"), ""); tok != "" {
		t.Errorf("unknown host token = %q", tok)
	}
}

func TestImportFile(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, false)

	if got, err := h.store.ImportFile(ctx); got != nil || err != nil {
		t.Errorf("cancelled = %s %v", got, err)
	}
	if h.dialogs.calls[0] != "open:Import Settings:JSON:*.json" {
		t.Errorf("dialog = %v", h.dialogs.calls)
	}
	h.dialogs.err = errors.New("cancelled")
	h.dialogs.openPath = "/whatever"
	if got, err := h.store.ImportFile(ctx); got != nil || err != nil {
		t.Errorf("dialog error = %s %v", got, err)
	}
	h.dialogs.err = nil
	h.dialogs.openPath = filepath.Join(h.dir, "missing.json")
	if got, _ := h.store.ImportFile(ctx); got != nil {
		t.Errorf("missing file = %s", got)
	}
	bad := filepath.Join(h.dir, "bad.json")
	os.WriteFile(bad, []byte(`{"a":1} trailing`), 0o600)
	h.dialogs.openPath = bad
	if got, _ := h.store.ImportFile(ctx); got != nil {
		t.Errorf("bad json = %s", got)
	}
	good := filepath.Join(h.dir, "good.json")
	os.WriteFile(good, []byte("{\n \"language\": \"de\", \"n\": 1.50, \"s\": \"\xff\"}\n"), 0o600)
	h.dialogs.openPath = good
	got, err := h.store.ImportFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, "imported", got, map[string]any{"language": "de", "n": 1.5, "s": "\ufffd"})
	// What the bridge would send: null for nothing.
	if raw, _ := json.Marshal(json.RawMessage(nil)); string(raw) != "null" {
		t.Errorf("nil RawMessage marshals as %s", raw)
	}
}

func TestExportFile(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, false)
	data := json.RawMessage(`{"z":1,"a":[1,{"b":[]}],"e":{}}`)

	if ok, err := h.store.ExportFile(ctx, data); ok || err != nil {
		t.Errorf("cancelled = %v %v", ok, err)
	}
	if h.dialogs.calls[0] != "save:Export Settings:gitcito-settings.json:*.json" {
		t.Errorf("dialog = %v", h.dialogs.calls)
	}
	out := filepath.Join(h.dir, "export.json")
	h.dialogs.savePath = out
	if ok, err := h.store.ExportFile(ctx, data); !ok || err != nil {
		t.Fatalf("export = %v %v", ok, err)
	}
	written, _ := os.ReadFile(out)
	// JSON.stringify(data, null, 2), key order included.
	want := "{\n  \"z\": 1,\n  \"a\": [\n    1,\n    {\n      \"b\": []\n    }\n  ],\n  \"e\": {}\n}"
	if string(written) != want {
		t.Errorf("written:\n%s\nwant:\n%s", written, want)
	}
	if ok, _ := h.store.ExportFile(ctx, nil); ok {
		t.Error("exported undefined")
	}
	if ok, _ := h.store.ExportFile(ctx, json.RawMessage(`{`)); ok {
		t.Error("exported invalid JSON")
	}
	h.dialogs.savePath = filepath.Join(h.dir, "no", "such", "dir.json")
	if ok, err := h.store.ExportFile(ctx, data); ok || err != nil {
		t.Errorf("unwritable = %v %v", ok, err)
	}
}

func TestServiceFacade(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, false)
	svc := NewService(h.store)
	if err := svc.Set(ctx, json.RawMessage(`{"language":"fr"}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.Get(ctx).String("language"); v != "fr" {
		t.Errorf("language = %q", v)
	}
	if s, err := svc.Unlock(ctx); err != nil || s == nil {
		t.Errorf("unlock = %v %v", s, err)
	}
	if got, _ := svc.ImportFile(ctx); got != nil {
		t.Errorf("import = %s", got)
	}
	if ok, _ := svc.ExportFile(ctx, json.RawMessage(`{}`)); ok {
		t.Error("export without a path")
	}
}

// Concurrent reads, writes and unlocks: run with -race. A reader must never
// see a half-written file (it would fall back to the defaults).
func TestConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, false)
	base := Settings(shared.DefaultSettings())
	base["language"] = "it"
	if err := h.store.Set(ctx, base); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			s := Settings(shared.DefaultSettings())
			s["language"] = "it"
			if i%2 == 0 {
				s["profiles"].([]any)[0].(map[string]any)["githubToken"] = "ghp_concurrent"
			}
			if err := h.store.Set(ctx, s); err != nil {
				t.Error(err)
			}
		}(i)
		go func() {
			defer wg.Done()
			if v, _ := h.store.Get(ctx).String("language"); v != "it" {
				t.Errorf("reader saw language %q", v)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := h.store.ActiveProfileToken(ctx, HostGitHub, "/r"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(h.ask.asks) != 1 {
		t.Errorf("asked %d times, want once", len(h.ask.asks))
	}
}

func TestDefaultsUsePathsDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GITCITO_DATA_DIR", dir)
	kc := keychain.New(keychain.Options{Backend: &fakeBackend{entries: map[string]string{}}, Service: testService})
	s := New(Options{Keychain: kc, Dialogs: &fakeDialogs{}})
	if err := s.SetJSON(context.Background(), json.RawMessage(`{"language":"es"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, SettingsFileName)); err != nil {
		t.Errorf("settings not under GITCITO_DATA_DIR: %v", err)
	}
	if _, ok := New(Options{Keychain: kc}).dialogs.(WailsDialogs); !ok {
		t.Error("default dialogs are not the Wails ones")
	}
}
