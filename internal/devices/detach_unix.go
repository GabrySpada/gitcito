//go:build !windows

package devices

import (
	"os/exec"
	"syscall"
)

// detach starts the child in a new session, so no signal aimed at Gitcito's
// group (or its terminal) reaches it.
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
