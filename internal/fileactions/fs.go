package fileactions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// FS is the filesystem Prepare and Apply work through — RepoFileActionFs in
// the TS, so tests can make one rename fail and watch the batch roll back.
type FS interface {
	Lstat(name string) (fs.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	// WriteFileExcl creates name (failing when it exists: the `wx` flag) and
	// writes data to it.
	WriteFileExcl(name string, data []byte) error
	MkdirAll(name string) error
	Rename(from, to string) error
	// RemoveAll is rm(path, { force: true }) on a file: a missing path is fine.
	RemoveAll(name string) error
	// Remove removes a file or an empty directory.
	Remove(name string) error
	Chmod(name string, mode fs.FileMode) error
}

type osFS struct{}

// OS is the real filesystem.
func OS() FS { return osFS{} }

func (osFS) Lstat(name string) (fs.FileInfo, error)    { return os.Lstat(name) }
func (osFS) ReadFile(name string) ([]byte, error)      { return os.ReadFile(name) }
func (osFS) MkdirAll(name string) error                { return os.MkdirAll(name, 0o777) }
func (osFS) Rename(from, to string) error              { return os.Rename(from, to) }
func (osFS) RemoveAll(name string) error               { return os.RemoveAll(name) }
func (osFS) Remove(name string) error                  { return os.Remove(name) }
func (osFS) Chmod(name string, mode fs.FileMode) error { return os.Chmod(name, mode) }

func (osFS) WriteFileExcl(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// nodeFsErr rewords a Go *fs.PathError / *os.LinkError the way Node words the
// same failure — "ENOENT: no such file or directory, rename 'a' -> 'b'" —
// since that text is what reached the renderer. Other errors pass through.
func nodeFsErr(err error) error {
	if err == nil {
		return nil
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	code, desc := errnoName(errno)
	if code == "" {
		return err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Errorf("%s: %s, %s '%s' -> '%s'", code, desc, le.Op, le.Old, le.New)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		op := pe.Op
		if op == "mkdirall" {
			op = "mkdir"
		}
		return fmt.Errorf("%s: %s, %s '%s'", code, desc, op, pe.Path)
	}
	return err
}

func errnoName(e syscall.Errno) (code, desc string) {
	switch e {
	case syscall.ENOENT:
		return "ENOENT", "no such file or directory"
	case syscall.EEXIST:
		return "EEXIST", "file already exists"
	case syscall.EACCES:
		return "EACCES", "permission denied"
	case syscall.EPERM:
		return "EPERM", "operation not permitted"
	case syscall.EISDIR:
		return "EISDIR", "illegal operation on a directory"
	case syscall.ENOTDIR:
		return "ENOTDIR", "not a directory"
	case syscall.ENOTEMPTY:
		return "ENOTEMPTY", "directory not empty"
	}
	return "", ""
}
