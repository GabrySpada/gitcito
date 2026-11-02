package analytics

// Service serves the renderer channels:
//   analytics:get, analytics:clear, analytics:setRetention
//
// A facade over Ledger: every exported method here is callable from the page,
// so RecordEvent (the git service's hook) and RecordAIUsage (the AI features')
// stay on Ledger, which is never bound.

// Service is bound to the page.
type Service struct {
	l *Ledger
}

// NewService wraps l for binding.
func NewService(l *Ledger) *Service { return &Service{l: l} }

// Get is `analytics:get`: the whole ledger.
// channel: analytics:get
func (s *Service) Get() Analytics { return s.l.Get() }

// Clear is `analytics:clear`: empties the ledger, keeping the retention.
// channel: analytics:clear
func (s *Service) Clear() Analytics { return s.l.Clear() }

// SetRetention is `analytics:setRetention`: days of history to keep (0 =
// forever), pruning right away.
// channel: analytics:setRetention
func (s *Service) SetRetention(days float64) Analytics { return s.l.SetRetention(days) }
