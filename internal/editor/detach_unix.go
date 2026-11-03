//go:build !windows

package editor

import (
	"os/exec"
	"syscall"
)

// detach starts the child in its own session (and so its own process group),
// like spawn({ detached: true }): closing Gitcito must not take the launched
// editor down with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
