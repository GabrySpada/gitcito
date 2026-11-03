package cli

// Service serves the renderer channels for the `gitcito` command:
//   cli:isInstalled, cli:install, cli:uninstall
// (cli:finishEdit is internal/app.AppService.FinishEdit.)

import "context"

// Service is the page-facing facade over an Installer.
type Service struct{ inst *Installer }

// NewService wraps an installer for binding.
func NewService(inst *Installer) *Service { return &Service{inst: inst} }

// IsInstalled reports whether the PATH link exists and points at our binary.
// channel: cli:isInstalled
func (s *Service) IsInstalled() bool { return s.inst.IsInstalled() }

// Install links gitcito-cli into PATH as `gitcito`.
// channel: cli:install
func (s *Service) Install(ctx context.Context) Result { return s.inst.Install(ctx) }

// Uninstall removes our PATH link (never someone else's file).
// channel: cli:uninstall
func (s *Service) Uninstall(ctx context.Context) Result { return s.inst.Uninstall(ctx) }
