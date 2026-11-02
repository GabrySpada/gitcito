//go:build !windows

package app

import (
	"os/exec"
	"syscall"
)

// detach starts the child in its own session, like spawn({ detached: true }):
// closing Gitcito must not take the launched editor down with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
