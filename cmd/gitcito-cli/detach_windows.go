//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the app in its own process group without a console of its
// own, so closing the terminal does not take the window down with it.
func detach(cmd *exec.Cmd) {
	const createNewProcessGroup, detachedProcess = 0x00000200, 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
