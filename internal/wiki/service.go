package wiki

// Service serves the renderer's wiki channels:
//   wiki:imports, wiki:facts, wiki:get, wiki:generate, wiki:export, wiki:clear
// and emits wiki:progress while generating. It mirrors registerWikiHandlers in
// src/main/wiki.ts. Every exported method is callable from the page, so
// nothing else is exported here; the work happens on Wiki.

import (
	"context"
	"fmt"
	"time"

	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Service is the bound facade over Wiki.
type Service struct {
	w *Wiki
}

// NewService builds the facade. main.go passes the git service, the AI
// transport's chatCompleteJson (internal/ai), the app emitter and the store
// path (paths.File(FileName)).
func NewService(git Git, model g.Completer, emitter Emitter, storePath string) *Service {
	return &Service{w: New(git, model, emitter, storePath, time.Now)}
}

func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("wiki: internal error: %v", r)
	}
}

// Imports reads the folder-level import graph from the source.
// channel: wiki:imports
func (s *Service) Imports(ctx context.Context, repoPath string, depth int) (out shared.ImportGraph, err error) {
	defer recoverErr(&err)
	return s.w.ImportGraph(ctx, repoPath, depth), nil
}

// Facts counts languages and declared dependencies.
// channel: wiki:facts
func (s *Service) Facts(ctx context.Context, repoPath string) (out RepoFacts, err error) {
	defer recoverErr(&err)
	return s.w.Facts(ctx, repoPath, s.w.candidateFiles(ctx, repoPath)), nil
}

// Get returns the stored wiki and its freshness for model.
// channel: wiki:get
func (s *Service) Get(ctx context.Context, repoPath, model string) (out StoredWiki, err error) {
	defer recoverErr(&err)
	return s.w.Get(ctx, repoPath, model), nil
}

// Generate plans and writes the wiki, emitting wiki:progress.
// channel: wiki:generate
func (s *Service) Generate(ctx context.Context, repoPath string, cfg shared.Object) (out *RepoWiki, err error) {
	defer recoverErr(&err)
	return s.w.Generate(ctx, repoPath, cfg)
}

// Export writes the stored wiki into the repo under docs/wiki.
// channel: wiki:export
func (s *Service) Export(repoPath string) (out []string, err error) {
	defer recoverErr(&err)
	return s.w.Export(repoPath)
}

// Clear forgets the stored wiki of a repository.
// channel: wiki:clear
func (s *Service) Clear(repoPath string) (err error) {
	defer recoverErr(&err)
	return s.w.Clear(repoPath)
}
