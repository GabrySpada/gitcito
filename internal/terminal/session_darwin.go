package terminal

import (
	"syscall"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// signalSession: macOS has no portable way to list a session's members; the
// group signal in killTree covers the processes that did not detach.
func signalSession(int, syscall.Signal) {}

// foregroundName is node-pty's macOS pty_getproc: the short command name
// (p_comm) of the foreground process group, with kernel_task meaning "none".
func foregroundName(pt gopty.Pty) string {
	up, ok := pt.(gopty.UnixPty)
	if !ok {
		return ""
	}
	pgrp := -1
	if err := up.Control(func(fd uintptr) {
		if v, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil {
			pgrp = v
		}
	}); err != nil || pgrp <= 0 {
		return ""
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pgrp)
	if err != nil {
		return ""
	}
	name := unix.ByteSliceToString(kp.Proc.P_comm[:])
	if name == "kernel_task" {
		return ""
	}
	return name
}
