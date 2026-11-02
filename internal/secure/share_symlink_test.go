package secure

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A bundle file must never be written through a symlink that leaves the repo.
func TestWriteBundleFileRefusesSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	repo, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "config")); err != nil {
		t.Fatal(err)
	}
	err := writeBundleFile(repo, filepath.Join(repo, "config", ".env"), BundleFile{Path: "config/.env", Content: []byte("X=1")})
	if err == nil {
		t.Fatal("wrote through a directory symlink that leaves the repository")
	}
	if _, err := os.Stat(filepath.Join(outside, ".env")); err == nil {
		t.Fatal("file landed outside the repository")
	}
	target := filepath.Join(outside, "victim")
	_ = os.WriteFile(target, []byte("keep"), 0o644)
	if err := os.Symlink(target, filepath.Join(repo, "link.env")); err != nil {
		t.Fatal(err)
	}
	if err := writeBundleFile(repo, filepath.Join(repo, "link.env"), BundleFile{Path: "link.env", Content: []byte("X")}); err == nil {
		t.Fatal("wrote through a file symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("symlink target was overwritten")
	}
	if err := writeBundleFile(repo, filepath.Join(repo, "sub", "ok.env"), BundleFile{Path: "sub/ok.env", Content: []byte("X")}); err != nil {
		t.Fatalf("a plain nested write must still work: %v", err)
	}
}
