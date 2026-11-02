//go:build windows

package testutil

import (
	"errors"
	"os"
	"time"
)

// lockFile takes an exclusive lock by creating path with O_EXCL, waiting while
// another process holds it. A lock file older than ten minutes is treated as
// left behind by a crashed run.
func lockFile(path string) (func(), error) {
	for start := time.Now(); ; {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > 10*time.Minute {
			_ = os.Remove(path)
			continue
		}
		if time.Since(start) > 30*time.Minute {
			return nil, errors.New("timed out waiting for " + path)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
