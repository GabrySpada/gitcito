package updater

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GabrySpada/gitcito/internal/version"
)

type recorder struct {
	mu     sync.Mutex
	states []UpdateState
}

func (r *recorder) Emit(name string, data any) {
	if name != EventName {
		panic("unexpected event " + name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, data.(UpdateState))
}

func (r *recorder) statuses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, s := range r.states {
		out = append(out, s.Status)
	}
	return out
}

// noNetwork fails the test on any request: the disabled build must not reach
// the network at all.
type noNetwork struct {
	t     *testing.T
	calls atomic.Int32
}

func (n *noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.calls.Add(1)
	n.t.Errorf("network request while updates are disabled: %s", r.URL)
	return nil, errors.New("no network")
}

func pemKey(t *testing.T, pub ed25519.PublicKey) []byte {
	t.Helper()
	// Same PKIX PEM shape `wails3 updater genkey` writes.
	raw, err := os.ReadFile(filepath.Join("..", "..", "build", "updater.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if pub == nil {
		return raw
	}
	return []byte("-----BEGIN PUBLIC KEY-----\n" +
		base64.StdEncoding.EncodeToString(append([]byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}, pub...)) +
		"\n-----END PUBLIC KEY-----\n")
}

// The key shipped in the repository is a usable Ed25519 trust anchor.
func TestShippedPublicKeyParses(t *testing.T) {
	if k := parsePublicKey(pemKey(t, nil)); len(k) != ed25519.PublicKeySize {
		t.Fatalf("build/updater.pub is not an Ed25519 PKIX key (%d bytes)", len(k))
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if k := parsePublicKey(pemKey(t, pub)); !k.Equal(pub) {
		t.Fatal("PEM round trip")
	}
	if parsePublicKey([]byte("garbage")) != nil || parsePublicKey(nil) != nil {
		t.Fatal("garbage parsed as a key")
	}
	if k := parsePublicKey(pub); !k.Equal(pub) {
		t.Fatal("raw 32-byte key")
	}
}

// UPDATES_DISABLED semantics: no network path runs, supported is false, a
// check reports not-available, download and install do nothing.
func TestDisabledBuildNeverTouchesTheNetwork(t *testing.T) {
	if !version.UpdatesDisabled {
		t.Skip("updates are enabled in this build")
	}
	nn := &noNetwork{t: t}
	rec := &recorder{}
	opened := false
	u := New(Options{
		Emitter: rec, PublicKey: pemKey(t, nil), Packaged: true,
		HTTPClient: &http.Client{Transport: nn},
		OpenURL:    func(string) error { opened = true; return nil },
		Quit:       func() { t.Error("quit while disabled") },
	})
	defer u.Close()
	if st := u.State(); st.Supported || st.Status != "idle" {
		t.Fatalf("initial state %+v", st)
	}
	u.Check(context.Background(), false)
	u.Check(context.Background(), true)
	u.Download(context.Background())
	u.Install()
	u.CheckOnLaunch()
	if nn.calls.Load() != 0 || opened {
		t.Fatal("a network path ran")
	}
	st := u.State()
	if st.Status != "not-available" || st.Error != nil || st.Supported {
		t.Fatalf("state %+v", st)
	}
	if u.wails != nil {
		t.Fatal("the Wails updater was configured")
	}
}

// ─── A fake release feed ────────────────────────────────────────────────────

type feed struct {
	srv      *httptest.Server
	manifest atomic.Value // []byte; nil → 404
	artifact atomic.Value // []byte
	hits     atomic.Int32
}

func newFeed(t *testing.T) *feed {
	f := &feed{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		switch {
		case r.URL.Path == "/"+version.ReleaseRepo+"/releases/latest/download/"+ManifestAsset:
			m, _ := f.manifest.Load().([]byte)
			if m == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(m)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			a, _ := f.artifact.Load().([]byte)
			_, _ = w.Write(a)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type artifactSig struct {
	digestAlgo, digest, sigAlgo, sig string
}

// sign is what `wails3 updater sign` / `manifest -key` produce: Ed25519ph over
// the artifact's SHA-512 digest.
func sign(priv ed25519.PrivateKey, data []byte) artifactSig {
	d := sha512.Sum512(data)
	s, err := priv.Sign(nil, d[:], &ed25519.Options{Hash: crypto.SHA512})
	if err != nil {
		panic(err)
	}
	return artifactSig{"sha512", base64.StdEncoding.EncodeToString(d[:]), "ed25519ph", base64.StdEncoding.EncodeToString(s)}
}

func (f *feed) publish(t *testing.T, ver string, served []byte, sig artifactSig) {
	t.Helper()
	name := "Gitcito-" + runtime.GOOS + "-" + runtime.GOARCH
	art := map[string]any{
		"url": f.srv.URL + "/dl/" + name, "filename": name, "platform": runtime.GOOS, "arch": runtime.GOARCH,
		"size": len(served),
	}
	if sig.digest != "" {
		art["digestAlgo"], art["digest"] = sig.digestAlgo, sig.digest
	}
	if sig.sig != "" {
		art["signature"] = sig.sig
		if sig.sigAlgo != "" {
			art["signatureAlgo"] = sig.sigAlgo
		}
	}
	m, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "version": ver, "name": "Gitcito " + ver, "notes": "## What's new\n\nThings.",
		"publishedAt": "2026-10-01T12:00:00Z", "artifacts": []any{art},
	})
	f.manifest.Store(m)
	f.artifact.Store(served)
}

func enabled(t *testing.T, f *feed, pub ed25519.PublicKey, packaged bool) (*Updater, *recorder, *atomic.Int32) {
	t.Helper()
	rec := &recorder{}
	quits := &atomic.Int32{}
	u := newUpdater(Options{
		Emitter: rec, PublicKey: pemKey(t, pub), Packaged: packaged, HTTPClient: f.srv.Client(),
		Quit: func() { quits.Add(1) },
	}, false, f.srv.URL, f.srv.URL)
	t.Cleanup(func() {
		u.Close()
		if u.wails != nil {
			if p := u.wails.DownloadedPath(); p != "" {
				_ = os.RemoveAll(filepath.Dir(p))
			}
		}
	})
	return u, rec, quits
}

func newer() string { return "99.0.0" }

func TestCheckAnnouncesASignedRelease(t *testing.T) {
	f := newFeed(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("the new gitcito binary")
	f.publish(t, newer(), payload, sign(priv, payload))
	u, rec, _ := enabled(t, f, pub, true)

	if !u.State().Supported {
		t.Fatal("not supported with a key in a packaged build")
	}
	u.Check(context.Background(), false)
	st := u.State()
	if st.Status != "available" || st.Info == nil || st.Info.Version != newer() {
		t.Fatalf("state %+v", st)
	}
	if st.Info.URL != "https://github.com/"+version.ReleaseRepo+"/releases/tag/v99.0.0" ||
		st.Info.Notes == nil || *st.Info.Notes != "## What's new\n\nThings." ||
		st.Info.ReleaseDate != "2026-10-01T12:00:00.000Z" {
		t.Fatalf("info %+v", st.Info)
	}
	if got := strings.Join(rec.statuses(), ","); got != "checking,available" {
		t.Fatalf("events %s", got)
	}
	// A silent check does not flip the banner to 'checking'.
	rec.states = nil
	u.Check(context.Background(), true)
	if got := strings.Join(rec.statuses(), ","); got != "available" {
		t.Fatalf("silent events %s", got)
	}
}

func TestUpToDateAndMissingFeed(t *testing.T) {
	f := newFeed(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f.publish(t, version.Version, []byte("x"), sign(priv, []byte("x")))
	u, _, _ := enabled(t, f, pub, true)
	u.Check(context.Background(), false)
	if st := u.State(); st.Status != "not-available" {
		t.Fatalf("same version: %+v", st)
	}
	// No manifest published yet (404) reads as up to date, not as an error.
	f.manifest.Store([]byte(nil))
	u.Check(context.Background(), false)
	if st := u.State(); st.Status != "not-available" || st.Error != nil {
		t.Fatalf("404: %+v", st)
	}
}

func TestDownloadStagesAVerifiedReleaseAndInstallRestarts(t *testing.T) {
	f := newFeed(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte(strings.Repeat("signed bytes ", 1000))
	f.publish(t, newer(), payload, sign(priv, payload))
	u, rec, _ := enabled(t, f, pub, true)

	u.Download(context.Background())
	st := u.State()
	if st.Status != "downloaded" || st.Staged == nil || *st.Staged != newer() || st.Progress != nil || st.Error != nil {
		t.Fatalf("state %+v", st)
	}
	staged := u.wails.DownloadedPath()
	got, err := os.ReadFile(staged)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("staged %q: %v", staged, err)
	}
	statuses := strings.Join(rec.statuses(), ",")
	if !strings.Contains(statuses, "downloading") || !strings.HasSuffix(statuses, "downloaded") {
		t.Fatalf("events %s", statuses)
	}
	var last *UpdateProgress
	for _, s := range rec.states {
		if s.Progress != nil {
			last = s.Progress
		}
	}
	if last == nil || last.Transferred != int64(len(payload)) || last.Total != int64(len(payload)) || last.Percent != 100 {
		t.Fatalf("progress %+v", last)
	}

	// A re-check finding the same version keeps the banner on "restart", and
	// an up-to-date answer does not bury the staged build.
	u.Check(context.Background(), true)
	if st := u.State(); st.Status != "downloaded" {
		t.Fatalf("after re-check: %+v", st)
	}
	u.onEvent("wails:updater:no-update", nil)
	if st := u.State(); st.Status != "downloaded" {
		t.Fatalf("after no-update: %+v", st)
	}
}

// Fail closed: nothing is staged, and install is refused, unless the bytes
// carry a valid Ed25519 signature from the pinned key.
func TestTamperedOrUnsignedReleasesAreRefused(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	good := []byte("the real release")
	evil := []byte("the real release, with a backdoor")
	goodSig := sign(priv, good)

	cases := []struct {
		name    string
		served  []byte
		sig     artifactSig
		wantErr string
	}{
		// The feed still advertises the genuine digest and signature, but the
		// bytes on the wire were swapped.
		{"tampered artifact", evil, goodSig, "digest mismatch"},
		// Digest and signature describe the evil bytes, signed by someone else.
		{"signed by another key", evil, sign(other, evil), "signature did not verify"},
		// A correct digest without a signature is exactly the downgrade a
		// compromised feed would try.
		{"digest only", evil, artifactSig{digestAlgo: "sha512", digest: sign(other, evil).digest}, "must carry a signature"},
		{"no verification", evil, artifactSig{}, "must carry a signature"},
		{"signature without algorithm", good, artifactSig{digest: goodSig.digest, digestAlgo: "sha512", sig: goodSig.sig}, ""},
		{"corrupt signature", good, artifactSig{"sha512", goodSig.digest, "ed25519ph", base64.StdEncoding.EncodeToString(make([]byte, 64))}, "signature did not verify"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFeed(t)
			f.publish(t, newer(), c.served, c.sig)
			u, _, quits := enabled(t, f, pub, true)
			u.Download(context.Background())
			st := u.State()
			if st.Status != "error" || st.Error == nil || st.Staged != nil {
				t.Fatalf("state %+v", st)
			}
			t.Logf("refused: %s", *st.Error)
			if c.wantErr != "" && !strings.Contains(*st.Error, c.wantErr) {
				t.Fatalf("error %q, want %q", *st.Error, c.wantErr)
			}
			if u.wails != nil && u.wails.DownloadedPath() != "" {
				t.Fatal("something was staged")
			}
			u.Install()
			if quits.Load() != 0 {
				t.Fatal("install quit the app without a verified update")
			}
		})
	}
}

// A packaged build without a usable key never configures the installer: it
// behaves like a development build and points at the release page.
func TestNoKeyMeansNoInstalls(t *testing.T) {
	f := newFeed(t)
	rec := &recorder{}
	u := newUpdater(Options{Emitter: rec, PublicKey: []byte("not a key"), Packaged: true, HTTPClient: f.srv.Client()},
		false, f.srv.URL, f.srv.URL)
	defer u.Close()
	if u.State().Supported {
		t.Fatal("supported without a key")
	}
	u.Download(context.Background())
	u.Install()
	if u.wails != nil {
		t.Fatal("installer configured without a key")
	}
}

func TestDevelopmentBuildUsesTheGitHubAPI(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	var tag atomic.Value
	tag.Store("v99.1.0")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+version.ReleaseRepo+"/releases/latest" || r.Header.Get("User-Agent") != "gitcito" {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		w.WriteHeader(int(status.Load()))
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag.Load(), "body": "notes", "published_at": "2026-10-01T00:00:00Z"})
	}))
	defer api.Close()
	rec := &recorder{}
	var opened []string
	u := newUpdater(Options{Emitter: rec, PublicKey: pemKey(t, nil), Packaged: false, HTTPClient: api.Client(),
		OpenURL: func(s string) error { opened = append(opened, s); return nil }}, false, api.URL, api.URL)
	defer u.Close()

	u.Check(context.Background(), false)
	st := u.State()
	if st.Supported || st.Status != "available" || st.Info.Version != "99.1.0" || *st.Info.Notes != "notes" ||
		st.Info.ReleaseDate != "2026-10-01T00:00:00Z" || st.Info.URL != "https://github.com/"+version.ReleaseRepo+"/releases/tag/v99.1.0" {
		t.Fatalf("state %+v / %+v", st, st.Info)
	}
	u.Download(context.Background())
	if len(opened) != 1 || opened[0] != st.Info.URL {
		t.Fatalf("opened %v", opened)
	}

	tag.Store("v0.0.1")
	u.Check(context.Background(), false)
	if u.State().Status != "not-available" {
		t.Fatal("older release offered")
	}
	status.Store(500)
	u.Check(context.Background(), false)
	if u.State().Status != "not-available" {
		t.Fatal("HTTP error not read as not-available")
	}
	api.Close()
	u.Check(context.Background(), false)
	if st := u.State(); st.Status != "error" || *st.Error != "Could not reach the update server." {
		t.Fatalf("offline: %+v", st)
	}
}

func TestServiceAndWireShape(t *testing.T) {
	f := newFeed(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f.publish(t, newer(), []byte("x"), sign(priv, []byte("x")))
	u, _, _ := enabled(t, f, pub, true)
	s := NewService(u)
	b, _ := json.Marshal(s.GetState())
	want := `{"status":"idle","info":null,"progress":null,"error":null,"staged":null,"supported":true}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
	yes := true
	s.Check(context.Background(), &yes)
	s.Check(context.Background(), nil)
	if st := s.GetState(); st.Status != "available" {
		t.Fatalf("state %+v", st)
	}
	s.Download(context.Background())
	if st := s.GetState(); st.Status != "downloaded" {
		t.Fatalf("state %+v", st)
	}
}

// The release workflow signs with `wails3 updater manifest -key …`. testdata/
// cli-signed is that command's real output (made with a throwaway key, whose
// public half is throwaway.pub): the updater must accept it as published, and
// refuse it once a single byte of the artifact changes.
func TestAcceptsWhatTheWails3CLIPublishes(t *testing.T) {
	dir := filepath.Join("testdata", "cli-signed")
	manifest, err := os.ReadFile(filepath.Join(dir, ManifestAsset))
	if err != nil {
		t.Fatal(err)
	}
	name := "Gitcito-" + runtime.GOOS + "-" + runtime.GOARCH
	artifact, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Skipf("no fixture artifact for %s", name)
	}
	pubPEM, err := os.ReadFile(filepath.Join(dir, "throwaway.pub"))
	if err != nil {
		t.Fatal(err)
	}
	pub := parsePublicKey(pubPEM)

	for _, tamper := range []bool{false, true} {
		f := newFeed(t)
		served := append([]byte{}, artifact...)
		if tamper {
			served[0] ^= 1
		}
		f.manifest.Store([]byte(strings.ReplaceAll(string(manifest),
			"https://github.com/"+version.ReleaseRepo+"/releases/download/v99.0.0/", f.srv.URL+"/dl/")))
		f.artifact.Store(served)
		u, _, _ := enabled(t, f, pub, true)
		u.Check(context.Background(), false)
		if st := u.State(); st.Status != "available" || st.Info == nil || *st.Info.Notes != "Signed by the wails3 CLI.\n" {
			t.Fatalf("check: %+v", st)
		}
		u.Download(context.Background())
		st := u.State()
		if tamper {
			if st.Status != "error" || st.Staged != nil || !strings.Contains(*st.Error, "digest mismatch") {
				t.Fatalf("tampered: %+v", st)
			}
			continue
		}
		if st.Status != "downloaded" || st.Staged == nil || *st.Staged != "99.0.0" {
			t.Fatalf("genuine: %+v", st)
		}
	}
}
