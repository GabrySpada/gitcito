package app

// AppService serves the renderer channels about the app itself:
//   bootstrap (Wails only: the synchronous facts preload used to expose),
//   app:version, app:releases, and the ready handshake that releases queued
//   cli:open-path / cli:edit deliveries.

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/version"
)

// Bootstrap is what the renderer needs before its first render. Mirrors
// BootstrapInfo in src/shared/bridgeApi.ts.
type Bootstrap struct {
	Platform string  `json:"platform"`
	ShotMode bool    `json:"shotMode"`
	Version  string  `json:"version"`
	Dev      bool    `json:"dev"`
	Zoom     float64 `json:"zoom"`
}

// AppRelease mirrors AppRelease in src/shared/types.ts.
type AppRelease struct {
	Tag         string  `json:"tag"`
	Name        *string `json:"name"`
	Body        *string `json:"body"`
	PublishedAt string  `json:"publishedAt"`
	URL         string  `json:"url"`
	Prerelease  bool    `json:"prerelease"`
}

// Core holds the app-level state main.go drives: launch flags, the ready gate
// and the pending editor hand-offs. It is NOT registered with Wails — its
// exported methods (HandleSecondInstance, ReleaseAllWaits, …) must never be
// callable from the page: a script that could forge a second-instance argv
// could make the app read, then overwrite, any file via the editor flow.
type Core struct {
	shot bool
	dev  bool

	mu      sync.Mutex
	ready   bool
	pending []func()

	edits waits
}

// NewCore builds the core from the launch flags.
func NewCore(shot, dev bool) *Core {
	return &Core{shot: shot, dev: dev}
}

// AppService is the page-facing facade over Core. Only the methods below are
// bound; keep it that way.
type AppService struct{ core *Core }

// NewAppService wraps a Core for binding.
func NewAppService(c *Core) *AppService { return &AppService{core: c} }

// Bootstrap returns the facts the renderer reads synchronously.
// channel: bootstrap (Wails only)
func (s *AppService) Bootstrap() Bootstrap {
	return Bootstrap{Platform: nodePlatform(), ShotMode: s.core.shot, Version: version.Version, Dev: s.core.dev, Zoom: 1}
}

// Version answers app:version.
// channel: app:version
func (s *AppService) Version() string { return version.Version }

// Ready is called by the renderer once its settings store has loaded. Anything
// queued for it before then (a CLI-requested path, an editor hand-off) is
// delivered now: the settings load resolves asynchronously and would otherwise
// clobber a tab added too early. Replaces Electron's did-finish-load gating.
// channel: app:ready (Wails only)
func (s *AppService) Ready() { s.core.markReady() }

func (s *Core) markReady() {
	s.mu.Lock()
	s.ready = true
	queued := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, f := range queued {
		f()
	}
}

// whenReady runs f now if the renderer is ready, or queues it until it is.
// Not exported: it must not be callable from the page.
func (s *Core) whenReady(f func()) {
	s.mu.Lock()
	if !s.ready {
		s.pending = append(s.pending, f)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	f()
}

// Releases answers app:releases: public release notes from GitHub. Done in Go
// (not the renderer) because the renderer CSP forbids cross-origin requests;
// no token needed for public repos. Same upstream feed as the updater, so the
// same kill switch applies: it still announces this install to a repo the fork
// does not control.
// channel: app:releases
func (s *AppService) Releases(ctx context.Context) []AppRelease {
	out := []AppRelease{}
	if version.UpdatesDisabled {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+version.ReleaseRepo+"/releases?per_page=20", nil)
	if err != nil {
		return out
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gitcito")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return out
	}
	var raw []struct {
		TagName     string  `json:"tag_name"`
		Name        *string `json:"name"`
		Body        *string `json:"body"`
		PublishedAt string  `json:"published_at"`
		HTMLURL     string  `json:"html_url"`
		Draft       bool    `json:"draft"`
		Prerelease  bool    `json:"prerelease"`
	}
	if json.NewDecoder(res.Body).Decode(&raw) != nil {
		return out
	}
	for _, r := range raw {
		if r.Draft {
			continue
		}
		out = append(out, AppRelease{Tag: r.TagName, Name: r.Name, Body: r.Body, PublishedAt: r.PublishedAt, URL: r.HTMLURL, Prerelease: r.Prerelease})
	}
	return out
}

// nodePlatform reports the OS the way Node's process.platform did, because the
// renderer branches on 'darwin' / 'win32' / 'linux'.
func nodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}
