package repochat

// Service serves the renderer's repository-chat channels:
//   ai:repoChat, ai:repoChatFinalize
// It mirrors registerRepoChatHandlers in src/main/repoChat.ts. Every exported
// method is callable from the page, so nothing else is exported here; the work
// happens on Chat. Arguments the TS typed `unknown` arrive as decoded JSON and
// are validated exactly as the TS validated them.

import (
	"context"
	"fmt"

	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Service is the bound facade over Chat.
type Service struct {
	chat *Chat
}

// NewService builds the facade. main.go passes the git service, the AI
// transport's chatCompleteJson (internal/ai) and its action schemas.
func NewService(git Git, model g.Completer, schemas ActionSchemas) *Service {
	return &Service{chat: &Chat{Git: git, Model: model, Schemas: schemas}}
}

func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("repochat: internal error: %v", r)
	}
}

// RepoChat answers a repository question from grounded evidence, possibly
// proposing (never running) actions.
// channel: ai:repoChat
func (s *Service) RepoChat(ctx context.Context, repoPath, messages any, cfg shared.Object, attachments any) (out Reply, err error) {
	defer recoverErr(&err)
	return s.chat.Answer(ctx, repoPath, messages, cfg, attachments)
}

// RepoChatFinalize writes the factual report after the renderer ran a plan.
// channel: ai:repoChatFinalize
func (s *Service) RepoChatFinalize(ctx context.Context, repoPath, messages, execution any, cfg shared.Object) (out Reply, err error) {
	defer recoverErr(&err)
	return s.chat.Finalize(ctx, repoPath, messages, execution, cfg)
}
