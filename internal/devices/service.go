package devices

// Service serves the renderer's run-target channels:
//   devices:list, devices:boot
// It mirrors registerDeviceHandlers in src/main/devices.ts. Every exported
// method is callable from the page, so nothing else is exported here.

import (
	"context"
	"fmt"
)

// Service is the bound facade over the Lister.
type Service struct {
	l *Lister
}

// NewService builds the facade; main.go passes the --shot flag.
func NewService(shot bool) *Service {
	return &Service{l: New(Options{Shot: shot})}
}

func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("devices: internal error: %v", r)
	}
}

// List gathers every run target this machine offers for repoPath.
// channel: devices:list
func (s *Service) List(ctx context.Context, repoPath string) (snap RunDeviceSnapshot, err error) {
	defer recoverErr(&err)
	return s.l.List(ctx, repoPath), nil
}

// Boot starts a cold simulator/emulator: `{ ok: true }` or `{ error }`.
// channel: devices:boot
func (s *Service) Boot(ctx context.Context, device RunDevice) (res BootResult, err error) {
	defer recoverErr(&err)
	return s.l.Boot(ctx, device), nil
}
