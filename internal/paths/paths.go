// Package paths decides where Gitcito keeps its data and which keychain
// service holds its secrets (port brief §5.3, decision D3).
//
// Fresh start: the data dir is `<os.UserConfigDir()>/com.gitcito.desktop`
// (production) or `com.gitcito.desktop.dev` (development), never the Electron
// app's `<UserConfigDir>/Gitcito` — nothing here may read or write that. File
// names inside stay `gitcito-*.json`.
//
// Development vs production is a build property, not a runtime guess:
//
//   - building with `-tags dev` (what the Wails dev loop should pass) makes a
//     dev build, or
//   - `-ldflags "-X github.com/GabrySpada/gitcito/internal/paths.buildMode=dev"`
//     does the same without a tag.
//
// Anything else — including plain `go test` — is production naming, which is
// why tests must set GITCITO_DATA_DIR (or use the injectable helpers) and never
// rely on the real directory.
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// AppID is the reverse-DNS identifier the data dir and keychain service are named after.
const AppID = "com.gitcito.desktop"

// DevSuffix distinguishes a development build's data and secrets from a
// released app's on the same machine.
const DevSuffix = ".dev"

// Environment overrides.
const (
	// EnvDataDir points the whole data dir elsewhere: every headless or smoke
	// launch and every test uses a throwaway one (brief D4).
	EnvDataDir = "GITCITO_DATA_DIR"
	// EnvKeychainSuffix appends ".<suffix>" to the keychain service, so tests
	// and smoke runs never touch the real entry.
	EnvKeychainSuffix = "GITCITO_KEYCHAIN_SUFFIX"
)

// buildMode can be set to "dev" with -ldflags -X (see package doc).
var buildMode string

// IsDev reports whether this binary is a development build.
func IsDev() bool { return devTag || buildMode == "dev" }

// DataDir is the directory holding settings, logs and every other app file.
// GITCITO_DATA_DIR wins when set (made absolute); otherwise it is
// `<UserConfigDir>/com.gitcito.desktop[.dev]`. It does not create the directory.
func DataDir() (string, error) {
	return dataDir(os.Getenv, os.UserConfigDir, IsDev())
}

func dataDir(getenv func(string) string, configDir func() (string, error), dev bool) (string, error) {
	if override := strings.TrimSpace(getenv(EnvDataDir)); override != "" {
		return filepath.Abs(override)
	}
	base, err := configDir()
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", errors.New("no user config directory")
	}
	name := AppID
	if dev {
		name += DevSuffix
	}
	return filepath.Join(base, name), nil
}

// File is the path of a file named name inside the data dir.
func File(name string) (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// EnsureDataDir returns DataDir after creating it (0700: it holds tokens'
// encrypted files and logs naming private repositories).
func EnsureDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// KeychainService is the OS keychain service name: `com.gitcito.desktop` or
// `com.gitcito.desktop.dev`, plus `.<GITCITO_KEYCHAIN_SUFFIX>` when that is set.
func KeychainService() string {
	return keychainService(os.Getenv, IsDev())
}

func keychainService(getenv func(string) string, dev bool) string {
	name := AppID
	if dev {
		name += DevSuffix
	}
	if suffix := strings.Trim(strings.TrimSpace(getenv(EnvKeychainSuffix)), "."); suffix != "" {
		name += "." + suffix
	}
	return name
}
