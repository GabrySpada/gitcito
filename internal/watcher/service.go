package watcher

// Service serves the renderer's watch channels:
//   watch:repo, watch:mute
// and emits `repo:changed` ({ path, light }). It mirrors
// registerWatcherHandlers in src/main/watcher.ts. Every exported method is
// callable from the page, so nothing else is exported here except the Wails
// lifecycle hook ServiceShutdown, which Wails never binds.

import "context"

// Service is the bound facade over a Watcher.
type Service struct {
	w *Watcher
}

// NewService builds the facade. main.go passes New(<app event emitter>, nil).
func NewService(w *Watcher) *Service {
	return &Service{w: w}
}

// Repo watches repoPath (the active tab's repository); null stops watching.
// channel: watch:repo
func (s *Service) Repo(ctx context.Context, repoPath *string) {
	path := ""
	if repoPath != nil {
		path = *repoPath
	}
	s.w.Set(path)
}

// Mute ignores this repository's change events for ms milliseconds — sent
// right before the app's own git write so the resulting churn does not fire a
// redundant refresh.
// channel: watch:mute
func (s *Service) Mute(ctx context.Context, repoPath string, ms float64) {
	s.w.Mute(repoPath, ms)
}

// ServiceShutdown is the Wails lifecycle hook: stop watching when the app
// quits (the TS cleared the watch when its window was destroyed).
func (s *Service) ServiceShutdown(ctx context.Context) error {
	s.w.Close()
	return nil
}
