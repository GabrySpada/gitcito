package app

import (
	"github.com/GabrySpada/gitcito/internal/ai"
	"github.com/GabrySpada/gitcito/internal/analytics"
)

// AIUsage feeds the AI client's token counts into the analytics ledger. The two
// packages each declare their own TokenUsage so neither imports the other.
type AIUsage struct{ Ledger *analytics.Ledger }

// RecordAIUsage implements ai.UsageRecorder.
func (u AIUsage) RecordAIUsage(feature, model string, t ai.TokenUsage) {
	u.Ledger.RecordAIUsage(feature, model, analytics.TokenUsage{
		PromptTokens:     t.PromptTokens,
		CompletionTokens: t.CompletionTokens,
		TotalTokens:      t.TotalTokens,
	})
}
