package app

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"github.com/GabrySpada/gitcito/internal/paths"
)

// instanceKeyFile holds the per-user key that encrypts single-instance
// hand-offs. A second `gitcito <dir>` sends its argv to the running window over
// a local channel; encrypting it means another local user cannot inject paths
// (or an editor hand-off) into this user's session.
const instanceKeyFile = "gitcito-instance.key"

// InstanceKey returns the 32-byte single-instance key, creating it (0600) on
// first use. On any failure it returns an error and the caller runs without
// single-instance encryption rather than refusing to start.
func InstanceKey() ([32]byte, error) {
	var key [32]byte
	dir, err := paths.EnsureDataDir()
	if err != nil {
		return key, err
	}
	p := filepath.Join(dir, instanceKeyFile)
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		copy(key[:], b)
		return key, nil
	}
	if _, err := rand.Read(key[:]); err != nil {
		return key, err
	}
	// O_EXCL: if two instances race on a fresh install, one wins and the other
	// re-reads the winner's key, so both agree.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		b, rerr := os.ReadFile(p)
		if rerr != nil || len(b) != 32 {
			return key, errors.New("unreadable instance key")
		}
		copy(key[:], b)
		return key, nil
	}
	if err != nil {
		return key, err
	}
	defer f.Close()
	if _, err := f.Write(key[:]); err != nil {
		return key, err
	}
	return key, nil
}

// UniqueID keeps dev and prod builds from handing paths to each other.
func UniqueID() string {
	if paths.IsDev() {
		return paths.AppID + paths.DevSuffix
	}
	return paths.AppID
}
