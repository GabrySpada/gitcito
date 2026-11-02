package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDataDir(t *testing.T) {
	cfg := func() (string, error) { return "/home/u/.config", nil }
	abs, _ := filepath.Abs("rel/dir")
	tests := []struct {
		name    string
		env     map[string]string
		dev     bool
		want    string
		wantErr bool
	}{
		{name: "production", want: filepath.Join("/home/u/.config", "com.gitcito.desktop")},
		{name: "development", dev: true, want: filepath.Join("/home/u/.config", "com.gitcito.desktop.dev")},
		{name: "override wins", env: map[string]string{EnvDataDir: "/tmp/gitcito-x"}, dev: true, want: "/tmp/gitcito-x"},
		{name: "relative override made absolute", env: map[string]string{EnvDataDir: "rel/dir"}, want: abs},
		{name: "blank override ignored", env: map[string]string{EnvDataDir: "  "}, want: filepath.Join("/home/u/.config", "com.gitcito.desktop")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dataDir(env(tt.env), cfg, tt.dev)
			if err != nil || got != tt.want {
				t.Fatalf("got %q,%v want %q", got, err, tt.want)
			}
			// D3: never the Electron app's directory.
			if filepath.Base(got) == "Gitcito" || strings.Contains(got, string(filepath.Separator)+"Gitcito") {
				t.Fatalf("data dir %q points at the Electron app's data", got)
			}
		})
	}
	if _, err := dataDir(env(nil), func() (string, error) { return "", errors.New("no home") }, false); err == nil {
		t.Fatal("expected the config-dir error")
	}
	if _, err := dataDir(env(nil), func() (string, error) { return "", nil }, false); err == nil {
		t.Fatal("expected an error for an empty config dir")
	}
}

func TestDataDirFromEnvironment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv(EnvDataDir, dir)
	got, err := DataDir()
	if err != nil || got != dir {
		t.Fatalf("DataDir = %q,%v", got, err)
	}
	f, err := File("gitcito-settings.json")
	if err != nil || f != filepath.Join(dir, "gitcito-settings.json") {
		t.Fatalf("File = %q,%v", f, err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("DataDir must not create the directory")
	}
	made, err := EnsureDataDir()
	if err != nil || made != dir {
		t.Fatalf("EnsureDataDir = %q,%v", made, err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatal("EnsureDataDir did not create it")
	}
}

func TestKeychainService(t *testing.T) {
	tests := []struct {
		suffix string
		dev    bool
		want   string
	}{
		{"", false, "com.gitcito.desktop"},
		{"", true, "com.gitcito.desktop.dev"},
		{"test-123", false, "com.gitcito.desktop.test-123"},
		{".smoke", true, "com.gitcito.desktop.dev.smoke"},
		{"  ", false, "com.gitcito.desktop"},
	}
	for _, tt := range tests {
		if got := keychainService(env(map[string]string{EnvKeychainSuffix: tt.suffix}), tt.dev); got != tt.want {
			t.Errorf("keychainService(%q, dev=%v) = %q, want %q", tt.suffix, tt.dev, got, tt.want)
		}
	}
	t.Setenv(EnvKeychainSuffix, "unit")
	if got := KeychainService(); !strings.HasPrefix(got, AppID) || !strings.HasSuffix(got, ".unit") {
		t.Fatalf("KeychainService = %q", got)
	}
}

func TestBuildMode(t *testing.T) {
	// `go test` builds without the dev tag.
	if devTag {
		t.Skip("built with -tags dev")
	}
	if IsDev() {
		t.Fatal("a plain build must be production")
	}
	old := buildMode
	t.Cleanup(func() { buildMode = old })
	buildMode = "dev"
	if !IsDev() {
		t.Fatal("-X buildMode=dev not honoured")
	}
}
