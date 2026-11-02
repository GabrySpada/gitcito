//go:build !windows

package gitx

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Cancelling must kill the whole process group: a background grandchild (a
// hook's helper, a `bisect run` script) dies with git.
func TestCancelKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var out strings.Builder
	pidSeen := make(chan struct{})
	var once sync.Once

	done := make(chan error, 1)
	go func() {
		_, err := Exec(ctx, "", "sh", []string{"-c", "sleep 60 & echo $!; wait"}, RunOpts{OnStdout: func(c []byte) {
			mu.Lock()
			out.Write(c)
			mu.Unlock()
			once.Do(func() { close(pidSeen) })
		}})
		done <- err
	}()
	select {
	case <-pidSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("child never started")
	}
	mu.Lock()
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after cancel")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return // the grandchild is gone
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the cancel", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSignalName(t *testing.T) {
	_, err := Exec(context.Background(), "", "sh", []string{"-c", "kill -TERM $$"}, RunOpts{})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Signal != "SIGTERM" || ee.Code != -1 || ee.Killed {
		t.Fatalf("ee = %+v", ee)
	}
	if _, ok := ExitCode(err); ok {
		t.Fatal("a signal death has no exit code")
	}
}
