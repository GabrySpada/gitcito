package vault

// Service serves the renderer channels:
//   vault:list, vault:upsert, vault:remove, vault:exportAll, vault:importAll
// (each may emit keychain:ask through the keychain gate).
//
// A facade over Store: every exported method here is callable from the page,
// so ExportGlobalEntries / ImportGlobalEntries (secure share's access to the
// global vault) stay on Store, which is never bound.

import "context"

// Service is bound to the page.
type Service struct {
	s *Store
}

// NewService wraps s for binding.
func NewService(s *Store) *Service { return &Service{s: s} }

// List is `vault:list`: a repo's entries plus the global ones, asking for
// keychain access first if that is still undecided.
// channel: vault:list
func (sv *Service) List(ctx context.Context, repoPath string) (ListResult, error) {
	return sv.s.List(ctx, repoPath)
}

// Upsert is `vault:upsert`.
// channel: vault:upsert
func (sv *Service) Upsert(ctx context.Context, scope Scope, repoPath string, entry Entry) (ListResult, error) {
	return sv.s.Upsert(ctx, scope, repoPath, entry)
}

// Remove is `vault:remove`.
// channel: vault:remove
func (sv *Service) Remove(ctx context.Context, scope Scope, repoPath, id string) (ListResult, error) {
	return sv.s.Remove(ctx, scope, repoPath, id)
}

// ExportAll is `vault:exportAll`: the whole vault (Settings → Data → Export
// with secrets).
// channel: vault:exportAll
func (sv *Service) ExportAll(ctx context.Context) (Export, error) { return sv.s.ExportAll(ctx) }

// ImportAll is `vault:importAll`: merges an imported vault, incoming wins per id.
// channel: vault:importAll
func (sv *Service) ImportAll(ctx context.Context, data *Export) error {
	return sv.s.ImportAll(ctx, data)
}
