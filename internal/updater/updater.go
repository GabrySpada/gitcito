// Package updater keeps Gitcito current — src/main/updater.ts, on top of
// Wails' updater (github.com/wailsapp/wails/v3/pkg/updater).
//
// The feed is a Wails Update Manifest published as a GitHub release asset
// (ManifestAsset on the latest release of version.ReleaseRepo). Every
// artifact in it carries an Ed25519ph signature over its SHA-512 digest, made
// with the release key whose public half is build/updater.pub. The updater is
// configured with that key and nothing else, so it fails closed: a release
// without a signature, with one that does not verify, or with a digest that
// does not match the bytes downloaded is refused before anything is staged.
// No key, no installs — a build without a usable key falls back to pointing
// at the release page, like a development build.
//
// Wails' own update window is never shown: the renderer has its banner and
// What's-new page, driven by `update:event` exactly as before.
//
// Fork build: version.UpdatesDisabled gates every network path here. While
// it is true nothing is fetched, `supported` is false, and a check reports
// not-available — UPDATES_DISABLED's semantics.
package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/GabrySpada/gitcito/internal/version"
	wupdater "github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// EventName is the renderer event carrying the whole UpdateState.
const EventName = "update:event"

// ManifestAsset is the update manifest's file name on every release.
const ManifestAsset = "gitcito-update.json"

const (
	githubBase = "https://github.com"
	apiBase    = "https://api.github.com"
	// launchDelay: the first check waits for the window to settle.
	launchDelay = 4 * time.Second
	// recheckInterval: a session left open for days used to keep offering
	// whatever the launch check found, so the What's-new page (which refetches
	// from GitHub every time it opens) could name a newer version than the
	// updater had ever heard of.
	recheckInterval = 3 * time.Hour
)

// UpdateInfo mirrors UpdateInfo in src/shared/types.ts.
type UpdateInfo struct {
	Version     string  `json:"version"`
	Notes       *string `json:"notes"`
	ReleaseDate string  `json:"releaseDate,omitempty"`
	// URL is the GitHub release page, the manual-download fallback.
	URL string `json:"url,omitempty"`
}

// UpdateProgress mirrors UpdateProgress.
type UpdateProgress struct {
	Percent        float64 `json:"percent"`
	BytesPerSecond float64 `json:"bytesPerSecond"`
	Transferred    int64   `json:"transferred"`
	Total          int64   `json:"total"`
}

// UpdateState mirrors UpdateState. Status is 'idle' | 'checking' |
// 'available' | 'not-available' | 'downloading' | 'downloaded' | 'error'.
type UpdateState struct {
	Status   string          `json:"status"`
	Info     *UpdateInfo     `json:"info"`
	Progress *UpdateProgress `json:"progress"`
	Error    *string         `json:"error"`
	// Staged is the version already downloaded and staged on disk, if any.
	Staged *string `json:"staged"`
	// Supported is false where the app cannot install an update itself; the
	// renderer still announces a new version and opens the release page.
	Supported bool `json:"supported"`
}

// Emitter delivers events to the renderer.
type Emitter interface{ Emit(name string, data any) }

// Options are the updater's dependencies.
type Options struct {
	Emitter Emitter
	// PublicKey is build/updater.pub (PKIX PEM, or the raw 32-byte key). The
	// only trust anchor; without a valid Ed25519 key nothing is installed.
	PublicKey []byte
	// Packaged is false for development builds, which cannot swap themselves.
	Packaged bool
	// OpenURL opens a release page in the browser.
	OpenURL func(string) error
	// Quit begins the app's shutdown once the swap helper is waiting.
	Quit func()
	// HTTPClient overrides the client for the feed (tests). Nil: 30 s timeout.
	HTTPClient *http.Client
}

// Updater is the update state machine. It is not bound; Service is.
type Updater struct {
	opts     Options
	disabled bool
	key      ed25519.PublicKey
	// manifestURL and apiURL are the feed endpoints (overridden by tests).
	manifestURL string
	apiURL      string

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	state UpdateState
	// silentDepth counts checks nobody asked for — priming before a download,
	// the background re-check. They suppress the 'checking' flip so the banner
	// neither flickers nor disappears. A counter, not a flag: a background tick
	// can overlap a download prime.
	silentDepth int

	// emitMu keeps update:event in the order the state changed.
	emitMu sync.Mutex

	wireOnce sync.Once
	wails    *wupdater.Updater
	wireErr  error

	recheckOnce sync.Once
}

// New builds the updater. version.UpdatesDisabled is read here and only here:
// nothing a caller passes can turn the network paths back on.
func New(opts Options) *Updater {
	return newUpdater(opts, version.UpdatesDisabled, githubBase, apiBase)
}

func newUpdater(opts Options, disabled bool, github, api string) *Updater {
	ctx, cancel := context.WithCancel(context.Background())
	u := &Updater{
		opts:        opts,
		disabled:    disabled,
		key:         parsePublicKey(opts.PublicKey),
		manifestURL: github + "/" + version.ReleaseRepo + "/releases/latest/download/" + ManifestAsset,
		apiURL:      api + "/repos/" + version.ReleaseRepo + "/releases/latest",
		ctx:         ctx,
		cancel:      cancel,
	}
	u.state = UpdateState{Status: "idle", Supported: u.canInstall()}
	return u
}

// parsePublicKey accepts what `wails3 updater genkey` writes (PKIX PEM) or a
// raw 32-byte key. Anything else is no key at all.
func parsePublicKey(raw []byte) ed25519.PublicKey {
	if len(raw) == ed25519.PublicKeySize {
		return ed25519.PublicKey(raw)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil
	}
	k, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil
	}
	return k
}

// canInstall: packaged, enabled, and holding a trust anchor.
func (u *Updater) canInstall() bool {
	return u.opts.Packaged && !u.disabled && len(u.key) == ed25519.PublicKeySize
}

func releaseURL(v string) string {
	return githubBase + "/" + version.ReleaseRepo + "/releases/tag/v" + strings.TrimPrefix(v, "v")
}

// State is update:getState.
func (u *Updater) State() UpdateState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// setState applies patch under the lock and pushes the result, in order.
func (u *Updater) setState(patch func(s *UpdateState)) {
	u.emitMu.Lock()
	defer u.emitMu.Unlock()
	u.mu.Lock()
	patch(&u.state)
	snap := u.state
	u.mu.Unlock()
	if u.opts.Emitter != nil {
		u.opts.Emitter.Emit(EventName, snap)
	}
}

func strPtr(s string) *string { return &s }

// notesOf is notesToText: empty notes are null.
func notesOf(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func infoOf(rel *wupdater.Release) *UpdateInfo {
	info := &UpdateInfo{Version: rel.Version, Notes: notesOf(rel.Notes), URL: releaseURL(rel.Version)}
	if !rel.PublishedAt.IsZero() {
		info.ReleaseDate = rel.PublishedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return info
}

// ─── Wails host ─────────────────────────────────────────────────────────────

// host adapts the Wails updater's lifecycle events to UpdateState.
type host struct{ u *Updater }

func (h host) Emit(name string, data ...any) bool {
	var payload any
	if len(data) > 0 {
		payload = data[0]
	}
	h.u.onEvent(name, payload)
	return true
}

func (host) OnEvent(string, func(any)) func() { return func() {} }

// OpenWindow is never reached (Window: WindowNone); a no-op handle keeps a
// future framework change from dereferencing nil.
func (host) OpenWindow(wupdater.WindowOptions) wupdater.WindowHandle { return noWindow{} }

func (h host) Quit() {
	if h.u.opts.Quit != nil {
		h.u.opts.Quit()
	}
}

type noWindow struct{}

func (noWindow) EmitEvent(string, ...any) bool { return false }
func (noWindow) Show()                         {}
func (noWindow) Close()                        {}

func (u *Updater) onEvent(name string, payload any) {
	switch name {
	case wupdater.EventCheckStarted:
		u.mu.Lock()
		silent := u.silentDepth > 0
		u.mu.Unlock()
		if silent {
			return
		}
		u.setState(func(s *UpdateState) { s.Status, s.Error = "checking", nil })
	case wupdater.EventUpdateAvailable:
		rel, ok := payload.(*wupdater.Release)
		if !ok {
			return
		}
		u.setState(func(s *UpdateState) {
			// Every check re-announces whatever the feed holds, compared against
			// the *installed* version — so a re-check with something already
			// staged would otherwise knock the banner back from "restart" to
			// "download".
			if s.Staged != nil && !shared.IsNewerVersion(rel.Version, *s.Staged) {
				return
			}
			s.Status, s.Progress, s.Info = "available", nil, infoOf(rel)
		})
	case wupdater.EventNoUpdate:
		u.setState(func(s *UpdateState) {
			// A staged build is still installable even if the feed now says we
			// are current.
			if s.Status == "downloaded" {
				return
			}
			s.Status = "not-available"
		})
	case wupdater.EventDownloadStarted:
		// Wails discards a previously staged payload before downloading again,
		// so what is on disk is no longer installable.
		u.setState(func(s *UpdateState) { s.Staged = nil })
	case wupdater.EventDownloadProgress:
		p, ok := payload.(wupdater.Progress)
		if !ok {
			return
		}
		percent := 0.0
		if p.Total > 0 {
			percent = float64(p.Written) / float64(p.Total) * 100
		}
		u.setState(func(s *UpdateState) {
			s.Status = "downloading"
			s.Progress = &UpdateProgress{Percent: percent, BytesPerSecond: p.Rate, Transferred: p.Written, Total: p.Total}
		})
	case wupdater.EventUpdateReady:
		rel, ok := payload.(*wupdater.Release)
		if !ok {
			return
		}
		u.setState(func(s *UpdateState) {
			s.Status, s.Progress, s.Staged, s.Info = "downloaded", nil, strPtr(rel.Version), infoOf(rel)
		})
	case wupdater.EventError:
		info, ok := payload.(wupdater.ErrorInfo)
		if !ok {
			return
		}
		u.setState(func(s *UpdateState) {
			// A failed background re-check must not bury an update already on disk.
			if s.Status == "downloaded" {
				return
			}
			s.Status, s.Error = "error", strPtr(info.Message)
		})
	}
}

// wire configures the Wails updater once: the manifest feed, the pinned key,
// no window.
func (u *Updater) wire() (*wupdater.Updater, error) {
	u.wireOnce.Do(func() {
		if !u.canInstall() {
			u.wireErr = errors.New("updates are not supported in this build")
			return
		}
		feed, err := endpoint.New(endpoint.Config{URL: u.manifestURL, HTTPClient: u.opts.HTTPClient})
		if err != nil {
			u.wireErr = err
			return
		}
		w := wupdater.New(host{u: u})
		err = w.Init(wupdater.Config{
			CurrentVersion: version.Version,
			Providers:      []wupdater.Provider{feed},
			PublicKey:      []byte(u.key),
			Window:         wupdater.WindowNone,
		})
		if err != nil {
			u.wireErr = err
			return
		}
		u.wails = w
	})
	return u.wails, u.wireErr
}

func (u *Updater) runCheck(ctx context.Context, w *wupdater.Updater, silent bool) error {
	if silent {
		u.mu.Lock()
		u.silentDepth++
		u.mu.Unlock()
		defer func() {
			u.mu.Lock()
			u.silentDepth--
			u.mu.Unlock()
		}()
	}
	_, err := w.Check(ctx)
	return err
}

// Check is update:check. silent keeps the banner as it is instead of
// flipping to 'checking' — for checks the user did not ask for, like opening
// the What's-new page.
func (u *Updater) Check(ctx context.Context, silent bool) {
	if u.disabled {
		u.setState(func(s *UpdateState) { s.Status, s.Error = "not-available", nil })
		return
	}
	if !u.canInstall() {
		u.checkViaGitHub(ctx)
		return
	}
	w, err := u.wire()
	if err == nil {
		err = u.runCheck(ctx, w, silent)
	}
	if err != nil && !silent {
		msg := err.Error()
		u.setState(func(s *UpdateState) { s.Status, s.Error = "error", &msg })
	}
}

// Download is update:download.
func (u *Updater) Download(ctx context.Context) {
	if u.disabled {
		return
	}
	// Unsupported (dev, or no key): open the release page. The renderer also
	// handles this; guarded here in case it calls through anyway.
	if !u.canInstall() {
		st := u.State()
		if st.Info != nil && st.Info.URL != "" && u.opts.OpenURL != nil {
			_ = u.opts.OpenURL(st.Info.URL)
		}
		return
	}
	w, err := u.wire()
	if err == nil {
		// The download belongs to the app, not to the call that started it: a
		// renderer reload must not abort a transfer half way.
		dctx := u.ctx
		// Wails downloads the release its last Check found; re-prime with a
		// silent check so the button always acts on what the feed holds now.
		if u.State().Status != "downloading" {
			_ = u.runCheck(dctx, w, true)
		}
		err = w.DownloadAndInstall(dctx)
	}
	if err != nil {
		msg := err.Error()
		u.setState(func(s *UpdateState) { s.Status, s.Error = "error", &msg })
	}
}

// Install is update:install: quit and swap in the staged update. The swap
// helper waits for this process to exit, replaces the app and relaunches it.
func (u *Updater) Install() {
	if u.disabled || u.State().Status != "downloaded" {
		return
	}
	w, err := u.wire()
	if err != nil {
		return
	}
	go func() {
		if err := w.Restart(u.ctx); err != nil {
			msg := err.Error()
			u.setState(func(s *UpdateState) { s.Status, s.Error = "error", &msg })
		}
	}()
}

// CheckOnLaunch kicks a silent check shortly after launch and schedules the
// background re-checks (installable builds only). Not bound: main calls it.
func (u *Updater) CheckOnLaunch() {
	if u.disabled || !u.canInstall() {
		return
	}
	w, err := u.wire()
	if err != nil {
		return
	}
	u.recheckOnce.Do(func() {
		go func() {
			t := time.NewTicker(recheckInterval)
			defer t.Stop()
			for {
				select {
				case <-u.ctx.Done():
					return
				case <-t.C:
					// Never interrupt a transfer in flight; it re-checks when it finishes.
					if u.State().Status == "downloading" {
						continue
					}
					_ = u.runCheck(u.ctx, w, true)
				}
			}
		}()
	})
	go func() {
		select {
		case <-u.ctx.Done():
			return
		case <-time.After(launchDelay):
		}
		if _, err := w.Check(u.ctx); err != nil {
			msg := err.Error()
			u.setState(func(s *UpdateState) { s.Status, s.Error = "error", &msg })
		}
	}()
}

// Close stops background checks and any transfer in flight. Not bound.
func (u *Updater) Close() { u.cancel() }

// checkViaGitHub serves builds that cannot install (development, no key):
// poll the GitHub API so the "new version available" UI still works; the
// download falls back to the release page.
func (u *Updater) checkViaGitHub(ctx context.Context) {
	client := u.opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	fail := func() {
		u.setState(func(s *UpdateState) { s.Status, s.Error = "error", strPtr("Could not reach the update server.") })
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiURL, nil)
	if err != nil {
		fail()
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gitcito")
	res, err := client.Do(req)
	if err != nil {
		fail()
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		u.setState(func(s *UpdateState) { s.Status = "not-available" })
		return
	}
	var body struct {
		TagName     string  `json:"tag_name"`
		Body        *string `json:"body"`
		PublishedAt string  `json:"published_at"`
	}
	if json.NewDecoder(res.Body).Decode(&body) != nil {
		fail()
		return
	}
	latest := strings.TrimPrefix(body.TagName, "v")
	if latest == "" || !shared.IsNewerVersion(latest, version.Version) {
		u.setState(func(s *UpdateState) { s.Status = "not-available" })
		return
	}
	info := &UpdateInfo{Version: latest, Notes: body.Body, ReleaseDate: body.PublishedAt, URL: releaseURL(latest)}
	u.setState(func(s *UpdateState) { s.Status, s.Info = "available", info })
}
