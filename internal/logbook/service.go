package logbook

// Service serves the renderer channels log:get and log:clear. It is the
// page-facing facade: Logbook.Record stays unbound so the page cannot forge
// history entries.
type Service struct{ l *Logbook }

// NewService wraps a Logbook for binding.
func NewService(l *Logbook) *Service { return &Service{l: l} }

// Get answers log:get.
// channel: log:get
func (s *Service) Get() []LogEntry { return s.l.Entries() }

// Clear answers log:clear.
// channel: log:clear
func (s *Service) Clear() []LogEntry { return s.l.Clear() }
