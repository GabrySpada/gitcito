//go:build !linux && !darwin && !windows

package terminal

import (
	"syscall"

	gopty "github.com/aymanbagabas/go-pty"
)

func signalSession(int, syscall.Signal) {}

// foregroundName has no implementation here; callers fall back to the shell.
func foregroundName(gopty.Pty) string { return "" }
