//go:build windows

package gitx

import (
	"os"
	"os/exec"
	"syscall"
)

// createNoWindow keeps a console window from flashing up for every git call a
// GUI app makes (simple-git set windowsHide for the same reason).
const createNoWindow = 0x08000000

// configureProcess: Windows has no process groups to signal; a plain kill of
// the child is what cancellation can do.
func configureProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

// signalName: Windows processes do not die of signals.
func signalName(_ *os.ProcessState) string { return "" }

// shellCommand is Node's `spawn(cmd, { shell: true })` on Windows:
// `%ComSpec% /d /s /c "<cmd>"` with the command line passed verbatim, which Go
// would otherwise re-quote.
func shellCommand(command string) (name string, args []string, prepare func(*exec.Cmd)) {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	line := shell + ` /d /s /c "` + command + `"`
	return shell, nil, func(cmd *exec.Cmd) {
		cmd.SysProcAttr.CmdLine = line
	}
}
