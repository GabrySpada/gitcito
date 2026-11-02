package settings

// Service serves the renderer channels:
//   settings:get, settings:unlock, settings:set,
//   settings:importFile, settings:exportFile
//
// A facade over Store: every exported method here is callable from the page,
// so the token lookups other packages use (ActiveProfileToken,
// ActiveProfileAiKey) stay on Store, which is never bound.

import (
	"context"
	"encoding/json"
)

// Service is bound to the page.
type Service struct {
	s *Store
}

// NewService wraps s for binding.
func NewService(s *Store) *Service { return &Service{s: s} }

// Get is `settings:get`: AppSettings without unlocking the stored secrets.
// channel: settings:get
func (sv *Service) Get(ctx context.Context) Settings { return sv.s.Get(ctx) }

// Unlock is `settings:unlock`: AppSettings with the stored secrets, asking for
// keychain access first if that is still undecided.
// channel: settings:unlock
func (sv *Service) Unlock(ctx context.Context) (Settings, error) { return sv.s.Unlock(ctx) }

// Set is `settings:set`: persists AppSettings (credentials go to the keychain).
// channel: settings:set
func (sv *Service) Set(ctx context.Context, settings json.RawMessage) error {
	return sv.s.SetJSON(ctx, settings)
}

// ImportFile is `settings:importFile`: the parsed JSON of a user-picked file,
// or null.
// channel: settings:importFile
func (sv *Service) ImportFile(ctx context.Context) (json.RawMessage, error) {
	return sv.s.ImportFile(ctx)
}

// ExportFile is `settings:exportFile`: writes data to a user-picked file.
// channel: settings:exportFile
func (sv *Service) ExportFile(ctx context.Context, data json.RawMessage) (bool, error) {
	return sv.s.ExportFile(ctx, data)
}
