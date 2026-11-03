//go:build windows

package localci

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

func configureGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// termGroup: Windows has no SIGTERM; taskkill /T ends act's whole tree.
func termGroup(pid int) {
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	configureGroup(cmd)
	if cmd.Run() != nil {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}

func killGroup(pid int) { termGroup(pid) }

// signaled: a Windows process killed by taskkill exits with code 1, which TS
// also saw as a number, not null.
func signaled(ps *os.ProcessState) bool { return ps == nil }
