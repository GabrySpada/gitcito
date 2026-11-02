package secure

// Service serves the renderer channels:
//   secure:candidates, secure:exportV2, secure:openV2, secure:previewRepoV2,
//   secure:applyV2, secure:previewNotesV2, secure:export, secure:pick,
//   secure:preview, secure:apply
// (secure:applyV2 may emit keychain:ask through the vault).
//
// A facade over Share: every exported method here is callable from the page,
// and none returns key material, file contents, secret values or note bodies.

import "context"

// Service is bound to the page.
type Service struct {
	s *Share
}

// NewService wraps s for binding.
func NewService(s *Share) *Service { return &Service{s: s} }

// Candidates lists the repo's files for the export picker, secrets first.
// channel: secure:candidates
func (sv *Service) Candidates(repoPath string) []Candidate { return sv.s.Candidates(repoPath) }

// ExportV2 packs the requested sections and saves them where the user picks.
// channel: secure:exportV2
func (sv *Service) ExportV2(ctx context.Context, specs []ExportSpec, project, password string) (ExportResult, error) {
	return sv.s.ExportV2(ctx, specs, project, password)
}

// OpenV2 decrypts a bundle to its renderer-safe shape.
// channel: secure:openV2
func (sv *Service) OpenV2(bundlePath, password string) OpenedBundle {
	return sv.s.OpenV2(bundlePath, password)
}

// PreviewRepoV2 checks one repo section against a target repo.
// channel: secure:previewRepoV2
func (sv *Service) PreviewRepoV2(bundlePath, password string, sectionIndex float64, repoPath string) PreviewResult {
	return sv.s.PreviewRepoV2(bundlePath, password, sectionIndex, repoPath)
}

// ApplyV2 applies a bundle per the import plan.
// channel: secure:applyV2
func (sv *Service) ApplyV2(ctx context.Context, bundlePath, password string, plan []ApplyPlan) (ApplyResult, error) {
	return sv.s.ApplyV2(ctx, bundlePath, password, plan)
}

// PreviewNotesV2 checks one notes section against a target repo.
// channel: secure:previewNotesV2
func (sv *Service) PreviewNotesV2(ctx context.Context, bundlePath, password string, sectionIndex float64, repoPath string) NotesPreviewResult {
	return sv.s.PreviewNotesV2(ctx, bundlePath, password, sectionIndex, repoPath)
}

// Export packs one repo's selected files (legacy single-repo export).
// channel: secure:export
func (sv *Service) Export(repoPath, project string, paths []string, password string) (ExportResult, error) {
	return sv.s.Export(repoPath, project, paths, password)
}

// Pick lets the user choose a bundle and returns its envelope; null when cancelled.
// channel: secure:pick
func (sv *Service) Pick() (*PickResult, error) { return sv.s.Pick() }

// Preview checks a bundle's files against a repo (legacy single-repo import).
// channel: secure:preview
func (sv *Service) Preview(bundlePath, password, repoPath string) PreviewResult {
	return sv.s.Preview(bundlePath, password, repoPath)
}

// Apply writes the selected files of a bundle into a repo (legacy single-repo import).
// channel: secure:apply
func (sv *Service) Apply(bundlePath, password, repoPath string, selected []string) WrittenResult {
	return sv.s.Apply(bundlePath, password, repoPath, selected)
}
