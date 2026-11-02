//go:build !windows

package localci

import (
	"os"
	"os/exec"
	"syscall"
)

// configureGroup puts act in its own process group, so cancel stops whatever
// it started too.
func configureGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// termGroup is child.kill('SIGTERM'), widened to the group.
func termGroup(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

func killGroup(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// signaled reports a death by signal (Node's `close` code null).
func signaled(ps *os.ProcessState) bool {
	if ps == nil {
		return true
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}
