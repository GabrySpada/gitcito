//go:build windows

package app

import (
	"os/exec"
	"syscall"
)

// detach starts the child without a console tie to Gitcito, like
// spawn({ detached: true }).
func detach(cmd *exec.Cmd) {
	const detachedProcess = 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
}
