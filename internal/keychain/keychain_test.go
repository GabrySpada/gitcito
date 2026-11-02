package keychain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// No test here may reach the real OS keychain: TestMain swaps go-keyring's
// provider for its in-memory mock before anything runs, and the tests
// themselves inject fakeBackend.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// fakeBackend is an in-memory keychain that can be switched off.
type fakeBackend struct {
	mu      sync.Mutex
	entries map[string]string
	down    bool
	gets    int
}

func newFake() *fakeBackend { return &fakeBackend{entries: map[string]string{}} }

func (f *fakeBackend) Get(service, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
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

func (f *fakeBackend) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// recorder is the renderer: it records every ask and optionally answers.
type recorder struct {
	mu     sync.Mutex
	asks   []AskPayload
	names  []string
	answer func(AskPayload)
}

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	r.names = append(r.names, name)
	p := data.(AskPayload)
	r.asks = append(r.asks, p)
	answer := r.answer
	r.mu.Unlock()
	if answer != nil {
		answer(p)
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.asks)
}

type env struct {
	dir     string
	backend *fakeBackend
	rec     *recorder
	window  atomic.Bool
	k       *Keychain
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{dir: t.TempDir(), backend: newFake(), rec: &recorder{}}
	e.window.Store(true)
	e.k = New(Options{
		DataDir:   func() (string, error) { return e.dir, nil },
		Backend:   e.backend,
		Service:   "com.gitcito.desktop.test",
		Emitter:   e.rec,
		HasWindow: e.window.Load,
	})
	return e
}

// autoAnswer makes the renderer reply immediately.
func (e *env) autoAnswer(granted bool) {
	e.rec.answer = func(AskPayload) { e.k.Answer(granted) }
}

func (e *env) writeConsent(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, ConsentFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *env) consentFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.dir, ConsentFileName))
	if err != nil {
		return ""
	}
	return string(raw)
}

func TestConsentFileFormatMatchesTS(t *testing.T) {
	e := newEnv(t)
	if got := e.k.GetConsent(); got != Unset {
		t.Fatalf("fresh consent = %q", got)
	}
	if err := e.k.SetConsent(Granted); err != nil {
		t.Fatal(err)
	}
	// JSON.stringify({ consent, explained }, null, 2), byte for byte.
	if got, want := e.consentFile(t), "{\n  \"consent\": \"granted\",\n  \"explained\": true\n}"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestConsentFileParsing(t *testing.T) {
	cases := []struct {
		content   string
		consent   Consent
		explained bool
	}{
		{`{"consent":"granted","explained":true}`, Granted, true},
		{`{"consent":"declined"}`, Declined, false},
		{`{"consent":"maybe","explained":"yes"}`, Unset, false},
		{`{"consent":"granted","explained":1}`, Granted, false},
		{`null`, Unset, false},
		{`[1]`, Unset, false},
		{`{ broken`, Unset, false},
	}
	for _, c := range cases {
		e := newEnv(t)
		e.writeConsent(t, c.content)
		st := e.k.readFileState()
		if st.Consent != c.consent || st.Explained != c.explained {
			t.Errorf("%s → %+v", c.content, st)
		}
	}
}

func TestConsentIsCachedAfterFirstRead(t *testing.T) {
	e := newEnv(t)
	e.writeConsent(t, `{"consent":"declined","explained":true}`)
	if e.k.GetConsent() != Declined {
		t.Fatal("not declined")
	}
	e.writeConsent(t, `{"consent":"granted","explained":true}`)
	if e.k.GetConsent() != Declined {
		t.Error("the file was re-read; the TS caches the first read for the run")
	}
}

func TestAdoptExistingConsent(t *testing.T) {
	e := newEnv(t)
	missing := filepath.Join(e.dir, "nope.enc")
	if err := e.k.AdoptExistingConsent([]string{missing}); err != nil {
		t.Fatal(err)
	}
	if e.k.GetConsent() != Unset || e.consentFile(t) != "" {
		t.Fatal("adopted without any encrypted file")
	}
	present := filepath.Join(e.dir, "secrets.enc")
	os.WriteFile(present, []byte("x"), 0o600)
	if err := e.k.AdoptExistingConsent([]string{missing, present}); err != nil {
		t.Fatal(err)
	}
	st := e.k.readFileState()
	if st.Consent != Granted || st.Explained {
		t.Errorf("adopted = %+v, want granted/unexplained", st)
	}

	// An answer already given is never overridden.
	e2 := newEnv(t)
	e2.writeConsent(t, `{"consent":"declined","explained":true}`)
	os.WriteFile(filepath.Join(e2.dir, "s.enc"), []byte("x"), 0o600)
	e2.k.AdoptExistingConsent([]string{filepath.Join(e2.dir, "s.enc")})
	if e2.k.GetConsent() != Declined {
		t.Error("adoption overrode a decline")
	}
}

func TestEnsureKeychainBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("declined never asks", func(t *testing.T) {
		e := newEnv(t)
		e.writeConsent(t, `{"consent":"declined","explained":true}`)
		ok, err := e.k.EnsureKeychain(ctx, ReasonTokens)
		if ok || err != nil || e.rec.count() != 0 || e.backend.gets != 0 {
			t.Errorf("ok=%v err=%v asks=%d gets=%d", ok, err, e.rec.count(), e.backend.gets)
		}
	})

	t.Run("granted and explained uses the keychain without asking", func(t *testing.T) {
		e := newEnv(t)
		e.writeConsent(t, `{"consent":"granted","explained":true}`)
		ok, err := e.k.EnsureKeychain(ctx, ReasonTokens)
		if !ok || err != nil || e.rec.count() != 0 {
			t.Errorf("ok=%v err=%v asks=%d", ok, err, e.rec.count())
		}
	})

	t.Run("granted but keyring down", func(t *testing.T) {
		e := newEnv(t)
		e.backend.setDown(true)
		e.writeConsent(t, `{"consent":"granted","explained":true}`)
		if ok, _ := e.k.EnsureKeychain(ctx, ReasonTokens); ok {
			t.Error("available with no backend")
		}
	})

	t.Run("no window answers no and records nothing", func(t *testing.T) {
		e := newEnv(t)
		e.window.Store(false)
		ok, err := e.k.EnsureKeychain(ctx, ReasonVault)
		if ok || err != nil || e.rec.count() != 0 || e.consentFile(t) != "" || e.backend.gets != 0 {
			t.Errorf("ok=%v err=%v asks=%d file=%q", ok, err, e.rec.count(), e.consentFile(t))
		}
	})

	t.Run("no emitter is the same as no window", func(t *testing.T) {
		k := New(Options{DataDir: func() (string, error) { return t.TempDir(), nil }, Backend: newFake(), Service: "x"})
		if ok, _ := k.EnsureKeychain(ctx, ReasonVault); ok {
			t.Error("asked nobody and got a yes")
		}
	})

	t.Run("unset asks once and records the yes", func(t *testing.T) {
		e := newEnv(t)
		e.autoAnswer(true)
		ok, err := e.k.EnsureKeychain(ctx, ReasonSettings)
		if !ok || err != nil {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if e.rec.count() != 1 || e.rec.asks[0] != (AskPayload{Reason: ReasonSettings, Adopted: false}) || e.rec.names[0] != "keychain:ask" {
			t.Errorf("asks = %v %v", e.rec.names, e.rec.asks)
		}
		if st := e.k.readFileState(); st.Consent != Granted || !st.Explained {
			t.Errorf("state = %+v", st)
		}
		// Settled: the next call does not ask again.
		e.k.EnsureKeychain(ctx, ReasonSettings)
		if e.rec.count() != 1 {
			t.Error("asked twice")
		}
	})

	t.Run("unset and the user says no", func(t *testing.T) {
		e := newEnv(t)
		e.autoAnswer(false)
		ok, err := e.k.EnsureKeychain(ctx, ReasonTokens)
		if ok || err != nil || e.k.GetConsent() != Declined || e.backend.gets != 0 {
			t.Errorf("ok=%v err=%v consent=%v gets=%d", ok, err, e.k.GetConsent(), e.backend.gets)
		}
	})

	t.Run("granted but unexplained shows the explanation, adopted", func(t *testing.T) {
		e := newEnv(t)
		e.writeConsent(t, `{"consent":"granted","explained":false}`)
		e.autoAnswer(true)
		ok, _ := e.k.EnsureKeychain(ctx, ReasonTokens)
		if !ok || e.rec.count() != 1 || !e.rec.asks[0].Adopted {
			t.Errorf("ok=%v asks=%v", ok, e.rec.asks)
		}
	})

	t.Run("yes but keyring down", func(t *testing.T) {
		e := newEnv(t)
		e.backend.setDown(true)
		e.autoAnswer(true)
		ok, _ := e.k.EnsureKeychain(ctx, ReasonTokens)
		if ok || e.k.GetConsent() != Granted {
			t.Errorf("ok=%v consent=%v", ok, e.k.GetConsent())
		}
	})
}

// Concurrent callers share one question: one event, one answer, every caller
// gets it. Run with -race.
func TestSharedAskUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	const n = 32
	results := make(chan bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := e.k.EnsureKeychain(context.Background(), ReasonTokens)
			if err != nil {
				t.Error(err)
			}
			results <- ok
		}()
	}
	waitFor(t, func() bool { return e.rec.count() == 1 })
	// Let stragglers pile onto the same question before answering.
	time.Sleep(20 * time.Millisecond)
	e.k.Answer(true)
	e.k.Answer(false) // a second answer to the same question is ignored
	wg.Wait()
	close(results)
	for ok := range results {
		if !ok {
			t.Error("a caller did not get the shared yes")
		}
	}
	if e.rec.count() != 1 {
		t.Errorf("asked %d times", e.rec.count())
	}
	if e.k.GetConsent() != Granted {
		t.Errorf("consent = %v", e.k.GetConsent())
	}
}

func TestCancelMidQuestionAnswersNo(t *testing.T) {
	e := newEnv(t)
	done := make(chan bool)
	go func() {
		ok, _ := e.k.EnsureKeychain(context.Background(), ReasonVault)
		done <- ok
	}()
	waitFor(t, func() bool { return e.rec.count() == 1 })
	e.k.Cancel()
	if <-done {
		t.Error("window closed but the answer was yes")
	}
	// Same as the TS `win.once('closed')` path: recorded as declined.
	if e.k.GetConsent() != Declined {
		t.Errorf("consent = %v", e.k.GetConsent())
	}
}

func TestAnswerWithoutQuestionIsNoop(t *testing.T) {
	e := newEnv(t)
	e.k.Answer(true)
	e.k.Cancel()
	if e.k.GetConsent() != Unset || e.consentFile(t) != "" {
		t.Error("a stray answer changed the consent")
	}
}

func TestCancelledWaiterDoesNotCancelTheQuestion(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := e.k.EnsureKeychain(ctx, ReasonTokens)
		first <- err
	}()
	waitFor(t, func() bool { return e.rec.count() == 1 })
	second := make(chan bool, 1)
	go func() {
		ok, _ := e.k.EnsureKeychain(context.Background(), ReasonTokens)
		second <- ok
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("first err = %v", err)
	}
	e.k.Answer(true)
	if !<-second {
		t.Error("the other caller lost the answer")
	}
}

func TestFailedConsentWriteDoesNotWedgeTheGate(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	var dir atomic.Value
	dir.Store(filepath.Join(blocker, "sub")) // MkdirAll fails: a file is in the way
	rec := &recorder{}
	var k *Keychain
	rec.answer = func(AskPayload) { k.Answer(true) }
	k = New(Options{
		DataDir:   func() (string, error) { return dir.Load().(string), nil },
		Backend:   newFake(),
		Service:   "x",
		Emitter:   rec,
		HasWindow: func() bool { return true },
	})
	if _, err := k.EnsureKeychain(context.Background(), ReasonTokens); err == nil {
		t.Fatal("expected the write error")
	}
	// The cache already moved (as in the TS), so the gate now reads granted;
	// what matters is that a later ask is possible again.
	k.mu.Lock()
	stuck := k.asking != nil
	k.mu.Unlock()
	if stuck {
		t.Error("the failed question stayed in flight")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	e := newEnv(t)
	if _, err := e.k.Encrypt([]byte("x")); !errors.Is(err, ErrNoConsent) {
		t.Fatalf("encrypt before consent: %v", err)
	}
	if _, err := e.k.Decrypt([]byte("x")); !errors.Is(err, ErrNoConsent) {
		t.Fatalf("decrypt before consent: %v", err)
	}
	if e.backend.gets != 0 {
		t.Fatal("touched the keychain before consent")
	}
	e.k.SetConsent(Granted)
	plain := []byte(`{"p1":{"githubToken":"ghp_x"}}`)
	a, err := e.k.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := e.k.Encrypt(plain)
	if bytes.Equal(a, b) {
		t.Error("two seals of the same plaintext are identical: nonce not random")
	}
	if len(a) != nonceSize+len(plain)+16 {
		t.Errorf("sealed length %d", len(a))
	}
	got, err := e.k.Decrypt(a)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	tampered := append([]byte(nil), a...)
	tampered[len(tampered)-1] ^= 1
	if _, err := e.k.Decrypt(tampered); err == nil {
		t.Error("tampered ciphertext opened")
	}
	if _, err := e.k.Decrypt(a[:10]); err == nil {
		t.Error("short ciphertext opened")
	}

	// The key lives in the keychain as base64 of 32 bytes, and a new instance
	// (the next launch) reads the same one.
	stored := e.backend.entries["com.gitcito.desktop.test/"+MasterKeyAccount]
	if key, err := base64.StdEncoding.DecodeString(stored); err != nil || len(key) != 32 {
		t.Fatalf("stored key %q", stored)
	}
	k2 := New(Options{DataDir: func() (string, error) { return e.dir, nil }, Backend: e.backend, Service: "com.gitcito.desktop.test"})
	if got, err := k2.Decrypt(a); err != nil || !bytes.Equal(got, plain) {
		t.Errorf("next launch decrypt = %q, %v", got, err)
	}
	// A different keychain service (dev vs prod) cannot read it.
	k3 := New(Options{DataDir: func() (string, error) { return e.dir, nil }, Backend: e.backend, Service: "other"})
	if _, err := k3.Decrypt(a); err == nil {
		t.Error("another identity's key opened the data")
	}
}

func TestSealOpenFormat(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(key, sealed); err != nil || string(got) != "hello" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := Seal(key[:16], []byte("x")); err == nil {
		t.Error("accepted a 16-byte key")
	}
	if _, err := Open(bytes.Repeat([]byte{8}, 32), sealed); err == nil {
		t.Error("wrong key opened")
	}
}

func TestAvailability(t *testing.T) {
	e := newEnv(t)
	e.k.SetConsent(Granted)
	e.backend.setDown(true)
	if e.k.Available() || e.k.CanUseKeychain() {
		t.Fatal("available with the backend down")
	}
	if _, err := e.k.Encrypt([]byte("x")); err == nil {
		t.Fatal("encrypted with the backend down")
	}
	// A keyring that comes up later is picked up: failures are not cached.
	e.backend.setDown(false)
	if !e.k.Available() {
		t.Fatal("not available once the backend is up")
	}
}

func TestCorruptMasterKeyIsNeverOverwritten(t *testing.T) {
	e := newEnv(t)
	e.k.SetConsent(Granted)
	e.backend.entries["com.gitcito.desktop.test/"+MasterKeyAccount] = "not base64!"
	if e.k.Available() {
		t.Fatal("available with a corrupt key")
	}
	if _, err := e.k.Encrypt([]byte("x")); !errors.Is(err, ErrMasterKeyCorrupt) {
		t.Errorf("err = %v", err)
	}
	if e.backend.entries["com.gitcito.desktop.test/"+MasterKeyAccount] != "not base64!" {
		t.Error("corrupt key was replaced")
	}
}

func TestStatusAndSet(t *testing.T) {
	e := newEnv(t)
	st := e.k.Status()
	raw, _ := json.Marshal(st)
	// available is null, not false and not absent, until consent is granted.
	if string(raw) != `{"consent":"unset","explained":false,"available":null}` {
		t.Errorf("status = %s", raw)
	}
	if e.backend.gets != 0 {
		t.Error("status touched the keychain without consent")
	}
	ok, err := e.k.Set(true)
	if !ok || err != nil {
		t.Fatalf("set(true) = %v, %v", ok, err)
	}
	raw, _ = json.Marshal(e.k.Status())
	if string(raw) != `{"consent":"granted","explained":true,"available":true}` {
		t.Errorf("status = %s", raw)
	}
	ok, err = e.k.Set(false)
	if ok || err != nil {
		t.Fatalf("set(false) = %v, %v", ok, err)
	}
	raw, _ = json.Marshal(e.k.Status())
	if string(raw) != `{"consent":"declined","explained":true,"available":null}` {
		t.Errorf("status = %s", raw)
	}

	e2 := newEnv(t)
	e2.backend.setDown(true)
	if ok, _ := e2.k.Set(true); ok {
		t.Error("set(true) reported usable with no backend")
	}
	raw, _ = json.Marshal(e2.k.Status())
	if string(raw) != `{"consent":"granted","explained":true,"available":false}` {
		t.Errorf("status = %s", raw)
	}
}

func TestServiceFacade(t *testing.T) {
	e := newEnv(t)
	svc := NewService(e.k)
	if st := svc.Status(); st.Consent != Unset || st.Available != nil {
		t.Errorf("status = %+v", st)
	}
	done := make(chan bool)
	go func() {
		ok, _ := e.k.EnsureKeychain(context.Background(), ReasonTokens)
		done <- ok
	}()
	waitFor(t, func() bool { return e.rec.count() == 1 })
	svc.Answer(true)
	if !<-done {
		t.Error("answer through the service was lost")
	}
	if ok, err := svc.Set(false); ok || err != nil {
		t.Errorf("set = %v %v", ok, err)
	}
}

// The default backend is go-keyring itself; under keyring.MockInit (TestMain)
// it stores in memory, so this exercises the real adapter safely.
func TestKeyringBackendWithMock(t *testing.T) {
	dir := t.TempDir()
	k := New(Options{DataDir: func() (string, error) { return dir, nil }, Service: "com.gitcito.desktop.gotest"})
	if _, ok := k.backend.(KeyringBackend); !ok {
		t.Fatalf("default backend = %T", k.backend)
	}
	k.SetConsent(Granted)
	sealed, err := k.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := keyring.Get("com.gitcito.desktop.gotest", MasterKeyAccount); err != nil || v == "" {
		t.Fatalf("mock keyring entry: %q %v", v, err)
	}
	k2 := New(Options{DataDir: func() (string, error) { return dir, nil }, Service: "com.gitcito.desktop.gotest"})
	if got, err := k2.Decrypt(sealed); err != nil || string(got) != "secret" {
		t.Errorf("decrypt = %q %v", got, err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
