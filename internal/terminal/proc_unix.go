//go:build !windows

package terminal

import (
	"os/exec"
	"syscall"
)

// configureGroup puts a pipe child in its own process group, so stopping it
// takes down whatever it started too.
func configureGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree sends the polite signal to the group (and the leader, in case it
// left its group), plus every process still in the leader's session: an
// interactive shell gives each background job its own group, and those must
// not outlive the terminal that started them.
func killTree(pid int, terminal bool) {
	if pid <= 0 {
		return
	}
	sig := syscall.SIGTERM
	if terminal {
		sig = syscall.SIGHUP
	}
	_ = syscall.Kill(-pid, sig)
	_ = syscall.Kill(pid, sig)
	if terminal {
		signalSession(pid, sig)
	}
}

// forceKillTree is the last resort once the grace period ran out.
func forceKillTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	signalSession(pid, syscall.SIGKILL)
}

// stopGroup pauses or resumes the whole group, falling back to the leader.
func stopGroup(pid int, pause bool) {
	if pid <= 0 {
		return
	}
	sig := syscall.SIGCONT
	if pause {
		sig = syscall.SIGSTOP
	}
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}
