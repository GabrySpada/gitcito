package terminal

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// signalSession signals every process whose session id is sid (the pty leader
// is a session leader, so its sid is its pid). /proc is the only place Linux
// exposes it.
func signalSession(sid int, sig syscall.Signal) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == sid {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp session … — comm may hold spaces and parens.
		s := string(raw)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 4 {
			continue
		}
		if got, err := strconv.Atoi(f[3]); err == nil && got == sid {
			_ = syscall.Kill(pid, sig)
		}
	}
}

// foregroundName is node-pty's Linux pty_getproc: argv[0] of the terminal's
// foreground process group leader, read from /proc.
func foregroundName(pt gopty.Pty) string {
	up, ok := pt.(gopty.UnixPty)
	if !ok {
		return ""
	}
	pgrp := -1
	// Control, not Fd(): Fd() would switch the master to blocking mode.
	if err := up.Control(func(fd uintptr) {
		if v, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil {
			pgrp = v
		}
	}); err != nil || pgrp <= 0 {
		return ""
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pgrp) + "/cmdline")
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(string(raw), 0); i >= 0 {
		raw = raw[:i]
	}
	return string(raw)
}
