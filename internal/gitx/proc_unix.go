//go:build !windows

package gitx

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcess puts the child in its own process group so cancelling
// kills everything git started — hooks, credential helpers, `bisect run`
// scripts — not just git. Killing only the leader would orphan a hook that
// keeps the pipes open, and Wait would sit out the whole WaitDelay.
func configureProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid = the whole group. Fall back to the leader alone if the
		// group is already gone (ESRCH) or was never formed.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

// signalName is Node's `signal` for a process ended by one, "" otherwise.
func signalName(ps *os.ProcessState) string {
	if ps == nil {
		return ""
	}
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	return unixSignalNames[ws.Signal()]
}

var unixSignalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGTERM: "SIGTERM",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGBUS:  "SIGBUS",
}

// shellCommand is Node's `spawn(cmd, { shell: true })` on Unix: /bin/sh -c.
func shellCommand(command string) (name string, args []string, prepare func(*exec.Cmd)) {
	return "/bin/sh", []string{"-c", command}, nil
}
