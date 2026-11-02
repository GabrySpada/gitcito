package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFinishEditWritesRecordedFileAndReleasesOnce(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "COMMIT_EDITMSG")
	sentinel := filepath.Join(dir, "sentinel")
	for _, p := range []string{file, sentinel} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var w waits
	w.track(sentinel, file)
	msg := "feat: hi"
	if r := w.finish(sentinel, &msg); !r.OK {
		t.Fatalf("finish: %+v", r)
	}
	if b, _ := os.ReadFile(file); string(b) != msg {
		t.Errorf("file = %q", b)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Error("sentinel not released")
	}
	if r := w.finish(sentinel, &msg); r.OK || r.Error != "No edit is pending" {
		t.Errorf("second finish = %+v", r)
	}
}

func TestFinishEditCancelWritesEmpty(t *testing.T) {
	dir := t.TempDir()
	file, sentinel := filepath.Join(dir, "f"), filepath.Join(dir, "s")
	_ = os.WriteFile(file, []byte("old"), 0o644)
	_ = os.WriteFile(sentinel, nil, 0o644)
	var w waits
	w.track(sentinel, file)
	if r := w.finish(sentinel, nil); !r.OK {
		t.Fatal(r)
	}
	if b, _ := os.ReadFile(file); len(b) != 0 {
		t.Errorf("cancel should empty the file, got %q", b)
	}
}

func TestReleaseAll(t *testing.T) {
	dir := t.TempDir()
	var w waits
	for _, n := range []string{"a", "b"} {
		s := filepath.Join(dir, n)
		_ = os.WriteFile(s, nil, 0o644)
		w.track(s, filepath.Join(dir, n+".txt"))
	}
	w.releaseAll()
	for _, n := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s not released", n)
		}
	}
}

func TestWriteRepoFilesRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	err := writeRepoFiles(dir, []FileToWrite{{Path: "ok.txt", Content: "1"}, {Path: "../evil", Content: "x"}})
	if err == nil || err.Error() != "Refusing to write outside the repository: ../evil" {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.txt")); !os.IsNotExist(err) {
		t.Error("a bad batch must write nothing")
	}
	if err := writeRepoFiles(dir, []FileToWrite{{Path: ".git/hooks/pre-commit", Content: "#!/bin/sh"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dir, ".git/hooks/pre-commit")); st.Mode().Perm()&0o100 == 0 {
		t.Error("hook not executable")
	}
}
