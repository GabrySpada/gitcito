//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the app in its own session, so closing the terminal (or git
// finishing) does not take the window down with it — what `cmd &` did in the
// shell shim, plus surviving the terminal's SIGHUP.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
