package ai

// Service serves the renderer's AI channels:
//   ai:planActions, ai:commitMessage, ai:listModels, ai:detectCli,
//   ai:explainCode, ai:hoverExplain, ai:resolveConflict, ai:generateConfig,
//   ai:suggestArtifacts, ai:smartStage, ai:generateAppTheme,
//   ai:generateCodeTheme, ai:generateGraphPalette, ai:generateBranchName,
//   ai:reviewPR, ai:prDescription
// It mirrors registerAiHandlers in src/main/ai.ts. ai:repoChat and
// ai:repoChatFinalize belong to internal/repochat, which uses the same *Client.
// Every exported method is callable from the page, so nothing else is
// exported here; the work happens on the Client.

import (
	"context"
	"fmt"
)

// Service is the bound facade over a Client.
type Service struct {
	c *Client
}

// NewService builds the facade over client (shared with repository chat and
// the wiki, so the hover cache and model catalogue are one per app).
func NewService(client *Client) *Service {
	return &Service{c: client}
}

// recoverErr turns a panic into an error: nothing may panic across the bridge.
func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("ai: internal error: %v", r)
	}
}

// PlanActions turns a plain-language instruction into an executable plan
// against the working tree.
// channel: ai:planActions
func (s *Service) PlanActions(ctx context.Context, prompt string, status RepoStatus, cfg Config) (out AskPlan, err error) {
	defer recoverErr(&err)
	return s.c.planRepoActions(ctx, prompt, status, cfg)
}

// CommitMessage drafts a commit message for a staged diff.
// channel: ai:commitMessage
func (s *Service) CommitMessage(ctx context.Context, diff string, cfg Config, commit CommitContext) (out CommitMessage, err error) {
	defer recoverErr(&err)
	return s.c.generateCommitMessage(ctx, diff, cfg, commit)
}

// ListModels is the model catalogue of an account (live, cached for a day, or
// the bundled fallback). It never fails for a provider problem; the catalogue
// says why it is not live.
// channel: ai:listModels
func (s *Service) ListModels(ctx context.Context, cfg Config, force bool) (out ModelCatalog, err error) {
	defer recoverErr(&err)
	resolved, err := s.c.withStoredKey(ctx, cfg)
	if err != nil {
		return ModelCatalog{}, err
	}
	return s.c.listAccountModels(ctx, resolved, force), nil
}

// DetectCli lists the agent CLIs on PATH, without running any of them.
// channel: ai:detectCli
func (s *Service) DetectCli(ctx context.Context) (out []DetectedCli, err error) {
	defer recoverErr(&err)
	return detectCliBinaries(ctx, s.c.t.runner, s.c.goos), nil
}

// ExplainCode explains a code file or snippet in plain language.
// channel: ai:explainCode
func (s *Service) ExplainCode(ctx context.Context, code, lang string, cfg Config) (out string, err error) {
	defer recoverErr(&err)
	return s.c.explainCode(ctx, code, lang, cfg)
}

// HoverExplain explains the token under the pointer from a numbered window.
// channel: ai:hoverExplain
func (s *Service) HoverExplain(ctx context.Context, req HoverExplainRequest, cfg Config) (out HoverExplainResult, err error) {
	defer recoverErr(&err)
	return s.c.hoverExplain(ctx, req, cfg)
}

// ResolveConflict proposes a merged file for content with conflict markers.
// channel: ai:resolveConflict
func (s *Service) ResolveConflict(ctx context.Context, file, content string, cfg Config) (out string, err error) {
	defer recoverErr(&err)
	return s.c.resolveConflict(ctx, file, content, cfg)
}

// GenerateConfig writes the requested AI-tool configuration files.
// channel: ai:generateConfig
func (s *Service) GenerateConfig(ctx context.Context, repoName string, artifacts []ArtifactRequest, projectContext string, cfg Config) (out GeneratedFiles, err error) {
	defer recoverErr(&err)
	return s.c.generateProjectConfig(ctx, repoName, artifacts, projectContext, cfg)
}

// SuggestArtifacts suggests further configuration files.
// channel: ai:suggestArtifacts
func (s *Service) SuggestArtifacts(ctx context.Context, repoName string, selectedTools []string, projectContext string, alreadySelected []ArtifactRequest, cfg Config) (out ArtifactSuggestions, err error) {
	defer recoverErr(&err)
	return s.c.suggestArtifacts(ctx, repoName, selectedTools, projectContext, alreadySelected, cfg)
}

// SmartStage picks which changed files belong in the commit.
// channel: ai:smartStage
func (s *Service) SmartStage(ctx context.Context, files []SmartStageFile, cfg Config) (out SmartStageResult, err error) {
	defer recoverErr(&err)
	return s.c.smartStageFiles(ctx, files, cfg)
}

// GenerateAppTheme generates an app colour theme from a description.
// channel: ai:generateAppTheme
func (s *Service) GenerateAppTheme(ctx context.Context, prompt string, cfg Config) (out Theme, err error) {
	defer recoverErr(&err)
	return s.c.generateTheme(ctx, prompt, cfg, appThemeSystem, "generateAppTheme", "app_theme", AppThemeKeys)
}

// GenerateCodeTheme generates a syntax-highlighting theme from a description.
// channel: ai:generateCodeTheme
func (s *Service) GenerateCodeTheme(ctx context.Context, prompt string, cfg Config) (out Theme, err error) {
	defer recoverErr(&err)
	return s.c.generateTheme(ctx, prompt, cfg, codeThemeSystem, "generateCodeTheme", "code_theme", CodeThemeKeys)
}

// GenerateGraphPalette generates the commit graph's 8 lane colours.
// channel: ai:generateGraphPalette
func (s *Service) GenerateGraphPalette(ctx context.Context, prompt string, cfg Config) (out GraphPalette, err error) {
	defer recoverErr(&err)
	return s.c.generateGraphPalette(ctx, prompt, cfg)
}

// GenerateBranchName names a branch from a description of the work.
// channel: ai:generateBranchName
func (s *Service) GenerateBranchName(ctx context.Context, description string, cfg Config, branch BranchNameContext) (out string, err error) {
	defer recoverErr(&err)
	return s.c.generateBranchName(ctx, description, cfg, branch)
}

// ReviewPR reviews a diff, grounding every finding in a hunk.
// channel: ai:reviewPR
func (s *Service) ReviewPR(ctx context.Context, diff string, cfg Config) (out PRReviewResult, err error) {
	defer recoverErr(&err)
	return s.c.reviewPR(ctx, diff, cfg)
}

// PrDescription drafts a pull request title and body.
// channel: ai:prDescription
func (s *Service) PrDescription(ctx context.Context, commits, diff string, cfg Config) (out PRDescriptionResult, err error) {
	defer recoverErr(&err)
	return s.c.prDescription(ctx, commits, diff, cfg)
}
