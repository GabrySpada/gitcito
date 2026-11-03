//go:build windows

package editor

import (
	"os/exec"
	"syscall"
)

// detach starts the child without a console tie to Gitcito and in its own
// process group, like libuv's spawn({ detached: true }).
func detach(cmd *exec.Cmd) {
	const (
		detachedProcess       = 0x00000008
		createNewProcessGroup = 0x00000200
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
