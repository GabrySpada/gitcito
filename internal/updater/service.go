package updater

// Service serves the renderer channels of the updater:
//   update:getState, update:check, update:download, update:install
// and pushes the event update:event (the whole UpdateState) on every change.

import "context"

// Service is the page-facing facade over an Updater.
type Service struct{ u *Updater }

// NewService wraps an updater for binding.
func NewService(u *Updater) *Service { return &Service{u: u} }

// GetState returns the current update state.
// channel: update:getState
func (s *Service) GetState() UpdateState { return s.u.State() }

// Check looks for a newer release. silent keeps the banner as it is.
// channel: update:check
func (s *Service) Check(ctx context.Context, silent *bool) {
	s.u.Check(ctx, silent != nil && *silent)
}

// Download fetches, verifies and stages the release the feed offers.
// channel: update:download
func (s *Service) Download(ctx context.Context) { s.u.Download(ctx) }

// Install quits and swaps in the staged update. Fire-and-forget.
// channel: update:install
func (s *Service) Install() { s.u.Install() }
