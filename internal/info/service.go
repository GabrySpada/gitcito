package info

// Service serves the renderer channels:
//   info:list, info:upsert, info:remove, info:reorder, info:exportAll, info:importAll
//
// A facade over Store: every exported method here is callable from the page.

// Service is bound to the page.
type Service struct {
	s *Store
}

// NewService wraps s for binding.
func NewService(s *Store) *Service { return &Service{s: s} }

// List is `info:list`: a repo's info entries.
// channel: info:list
func (sv *Service) List(repoPath string) []Entry { return sv.s.List(repoPath) }

// Upsert is `info:upsert`.
// channel: info:upsert
func (sv *Service) Upsert(repoPath string, entry Entry) ([]Entry, error) {
	return sv.s.Upsert(repoPath, entry)
}

// Remove is `info:remove`.
// channel: info:remove
func (sv *Service) Remove(repoPath, id string) ([]Entry, error) { return sv.s.Remove(repoPath, id) }

// Reorder is `info:reorder`.
// channel: info:reorder
func (sv *Service) Reorder(repoPath string, ids []string) ([]Entry, error) {
	return sv.s.Reorder(repoPath, ids)
}

// ExportAll is `info:exportAll`: the whole store (Settings → Data → Export).
// channel: info:exportAll
func (sv *Service) ExportAll() Export { return sv.s.ExportAll() }

// ImportAll is `info:importAll`: merges imported info, incoming wins per id.
// channel: info:importAll
func (sv *Service) ImportAll(data *Export) error { return sv.s.ImportAll(data) }
