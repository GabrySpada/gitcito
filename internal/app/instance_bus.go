package app

import (
	"os"
	"os/exec"
	"runtime"
)

// SingleInstanceAvailable reports whether Wails can set up its single-instance
// channel. On Linux it rides the D-Bus session bus, and with no bus and no
// dbus-launch to start one Wails aborts the whole app at startup. Running
// without single-instance (a second launch opens a second window) beats not
// starting at all, so main skips the option in that case.
func SingleInstanceAvailable() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	_, err := exec.LookPath("dbus-launch")
	return err == nil
}
