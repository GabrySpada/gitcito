//go:build server

package main

import (
	"log"
	"os"
)

// The server build exists for automation (e2e tests, screenshots) and must only
// ever be reachable from this machine. Wails lets WAILS_SERVER_HOST override the
// bind address; refuse anything that is not loopback rather than serve a
// credential-holding app to the network.
func init() {
	switch os.Getenv("WAILS_SERVER_HOST") {
	case "", "127.0.0.1", "localhost", "::1":
	default:
		log.Fatalf("server build: refusing non-loopback WAILS_SERVER_HOST=%q", os.Getenv("WAILS_SERVER_HOST"))
	}
}
