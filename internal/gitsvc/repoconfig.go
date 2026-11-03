package gitsvc

// `.gitcito.json` as gitService methods — src/main/repoConfig.ts, exposed this
// way so the calls take the same per-repository lock as git itself and land in
// the operation log alongside them. The logic (reading, writing, validating,
// the doctor and the proposal) lives in internal/repoconfig; these are the
// locked entry points and the type names the rest of gitsvc and its goldens
// already use.

import (
	"context"

	"github.com/GabrySpada/gitcito/internal/repoconfig"
)

// The `.gitcito.json` shapes, from src/shared/types.ts.
type (
	RepoConfigLink      = repoconfig.RepoConfigLink
	RepoConfigLinks     = repoconfig.RepoConfigLinks
	RepoConfigCommit    = repoconfig.RepoConfigCommit
	RepoConfigFileReq   = repoconfig.RepoConfigFileReq
	RepoConfigRequires  = repoconfig.RepoConfigRequires
	RepoConfigChecklist = repoconfig.RepoConfigChecklist
	RepoConfig          = repoconfig.RepoConfig
	RepoConfigIssue     = repoconfig.RepoConfigIssue
	RepoConfigResult    = repoconfig.RepoConfigResult
	DoctorFix           = repoconfig.DoctorFix
	DoctorCheck         = repoconfig.DoctorCheck
)

// RepoConfig reads and validates the repository's `.gitcito.json`.
//
// gitsvc: repoConfig
func (s *GitService) RepoConfig(ctx context.Context, repoPath string) (RepoConfigResult, error) {
	return locked(s, ctx, "repoConfig", repoPath, func(ctx context.Context) (RepoConfigResult, error) {
		return repoconfig.Read(repoPath), nil
	})
}

// SetRepoConfig writes `.gitcito.json`, validated again rather than trusted
// from the renderer.
//
// gitsvc: setRepoConfig
func (s *GitService) SetRepoConfig(ctx context.Context, repoPath string, config RepoConfig) (RepoConfigResult, error) {
	return locked(s, ctx, "setRepoConfig", repoPath, func(ctx context.Context) (RepoConfigResult, error) {
		return repoconfig.Write(repoPath, config)
	})
}

// RepoDoctor runs every check the repository's config asks for, reading the
// config from disk each time.
//
// gitsvc: repoDoctor
func (s *GitService) RepoDoctor(ctx context.Context, repoPath string) ([]DoctorCheck, error) {
	return locked(s, ctx, "repoDoctor", repoPath, func(ctx context.Context) ([]DoctorCheck, error) {
		return repoconfig.Doctor(ctx, repoPath), nil
	})
}

// ApplyDoctorFix performs one of the doctor's repairs. The union is closed on
// purpose; an unknown kind does nothing.
//
// gitsvc: applyDoctorFix
func (s *GitService) ApplyDoctorFix(ctx context.Context, repoPath string, fix DoctorFix) error {
	return lockedErr(s, ctx, "applyDoctorFix", repoPath, func(ctx context.Context) error {
		return repoconfig.ApplyFix(ctx, repoPath, fix)
	})
}

// SuggestRepoConfig reads a repository and proposes the config it evidently
// wants.
//
// gitsvc: suggestRepoConfig
func (s *GitService) SuggestRepoConfig(ctx context.Context, repoPath string) (RepoConfig, error) {
	return locked(s, ctx, "suggestRepoConfig", repoPath, func(ctx context.Context) (RepoConfig, error) {
		return repoconfig.Suggest(ctx, repoPath)
	})
}

// The validator's predicates under the names d_repoconfig_test.go exercises.

func dParseJSONOrdered(raw []byte) (any, []string, error) { return repoconfig.ParseJSONOrdered(raw) }
func dIsSafePattern(source string) bool                   { return repoconfig.IsSafePattern(source) }
func dIsSafeRepoRelPath(value string) bool                { return repoconfig.IsSafeRepoRelPath(value) }
func dSatisfiesNodeRange(actual, spec string) bool {
	return repoconfig.SatisfiesNodeRange(actual, spec)
}
