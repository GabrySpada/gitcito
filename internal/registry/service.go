package registry

// Service serves the renderer's repository-index channels:
//   repos:list, repos:remember, repos:forget, repos:scan, repos:locate,
//   repos:refresh
// It mirrors registerRepoRegistryHandlers in src/main/repoRegistry.ts. Every
// exported method is callable from the page, so nothing else is exported
// here; the work happens on the Registry.

import (
	"context"
	"fmt"
)

// Service is the bound facade over a Registry.
type Service struct {
	r *Registry
}

// NewService builds the facade. main.go passes New(<data dir>/FileName, nil).
func NewService(r *Registry) *Service {
	return &Service{r: r}
}

// recoverErr turns a panic into an error: nothing may panic across the bridge.
func recoverErr(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("registry: internal error: %v", p)
	}
}

// List is every known repository, with `missing` refreshed.
// channel: repos:list
func (s *Service) List(ctx context.Context) (out []RegistryRepo, err error) {
	defer recoverErr(&err)
	return s.r.List()
}

// Remember records a repository as opened.
// channel: repos:remember
func (s *Service) Remember(ctx context.Context, repoPath string) (out []RegistryRepo, err error) {
	defer recoverErr(&err)
	return s.r.Remember(repoPath)
}

// Forget drops an entry from the index without touching the folder.
// channel: repos:forget
func (s *Service) Forget(ctx context.Context, repoPath string) (out []RegistryRepo, err error) {
	defer recoverErr(&err)
	return s.r.Forget(repoPath)
}

// Scan indexes every repository under the given roots.
// channel: repos:scan
func (s *Service) Scan(ctx context.Context, rootList []RepoScanRoot) (out RepoScanResult, err error) {
	defer recoverErr(&err)
	return s.r.Scan(ctx, rootList)
}

// Locate re-points a moved repository's entry at its new folder.
// channel: repos:locate
func (s *Service) Locate(ctx context.Context, oldPath, newPath string) (out []RegistryRepo, err error) {
	defer recoverErr(&err)
	return s.r.Locate(oldPath, newPath)
}

// Refresh re-reads the branch of the given repositories.
// channel: repos:refresh
func (s *Service) Refresh(ctx context.Context, paths []string) (out []RegistryRepo, err error) {
	defer recoverErr(&err)
	return s.r.Refresh(paths)
}
