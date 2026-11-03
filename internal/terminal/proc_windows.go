//go:build windows

package terminal

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	gopty "github.com/aymanbagabas/go-pty"
)

const createNoWindow = 0x08000000

// configureGroup hides the console window a pipe child would flash up.
func configureGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// killTree: Windows has no process groups to signal; taskkill /T takes the
// child's whole tree, the closest equivalent.
func killTree(pid int, _ bool) {
	if pid <= 0 {
		return
	}
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	configureGroup(cmd)
	if cmd.Run() != nil {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}

func forceKillTree(pid int) { killTree(pid, false) }

// stopGroup: pause/resume is not offered on Windows (TS skipped it too).
func stopGroup(int, bool) {}

// foregroundName: node-pty's Windows getter returned the spawned file.
func foregroundName(gopty.Pty) string { return "" }
