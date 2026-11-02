package gitsvc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// acquireAsync starts an Acquire in a goroutine and reports, on the returned
// channel, the release func once granted.
func acquireAsync(ctx context.Context, l *RWLock, write bool) <-chan func() {
	ch := make(chan func(), 1)
	go func() {
		rel, err := l.Acquire(ctx, write)
		if err != nil {
			close(ch)
			return
		}
		ch <- rel
	}()
	return ch
}

func granted(ch <-chan func(), within time.Duration) (func(), bool) {
	select {
	case rel, ok := <-ch:
		return rel, ok
	case <-time.After(within):
		return nil, false
	}
}

// queued waits until n waiters sit in the lock's queue.
func queued(t *testing.T, l *RWLock, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		got := len(l.waiters)
		l.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiters = %d, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

const short = 50 * time.Millisecond

func TestRWLockReadersShare(t *testing.T) {
	var l RWLock
	ctx := context.Background()
	r1, err := l.Acquire(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := l.Acquire(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	r1()
	r2()

	// Many concurrent readers all run at once.
	const n = 20
	var inside, peak atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rel, _ := l.Acquire(ctx, false)
			cur := inside.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			inside.Add(-1)
			rel()
		}()
	}
	close(start)
	wg.Wait()
	if peak.Load() < 2 {
		t.Fatalf("readers never overlapped (peak %d)", peak.Load())
	}
}

func TestRWLockWriterExclusive(t *testing.T) {
	var l RWLock
	ctx := context.Background()
	var inside atomic.Int32
	var violations atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		write := i%3 == 0
		go func() {
			defer wg.Done()
			rel, _ := l.Acquire(ctx, write)
			defer rel()
			if write {
				if inside.Add(100) != 100 {
					violations.Add(1)
				}
				time.Sleep(2 * time.Millisecond)
				inside.Add(-100)
			} else {
				if inside.Add(1) >= 100 {
					violations.Add(1)
				}
				time.Sleep(2 * time.Millisecond)
				inside.Add(-1)
			}
		}()
	}
	wg.Wait()
	if violations.Load() != 0 {
		t.Fatalf("%d exclusivity violations", violations.Load())
	}
}

func TestRWLockWriterWaitsForReaders(t *testing.T) {
	var l RWLock
	ctx := context.Background()
	r, _ := l.Acquire(ctx, false)
	w := acquireAsync(ctx, &l, true)
	if _, ok := granted(w, short); ok {
		t.Fatal("writer granted while a reader holds the lock")
	}
	r()
	wrel, ok := granted(w, time.Second)
	if !ok {
		t.Fatal("writer not granted after the reader left")
	}
	// And a reader waits for the writer.
	r2 := acquireAsync(ctx, &l, false)
	if _, ok := granted(r2, short); ok {
		t.Fatal("reader granted during a write")
	}
	wrel()
	if rel, ok := granted(r2, time.Second); !ok {
		t.Fatal("reader not granted after the write")
	} else {
		rel()
	}
}

// A queued writer blocks the readers that arrive after it, so a stream of
// status polls cannot starve a checkout.
func TestRWLockQueuedWriterBlocksLaterReaders(t *testing.T) {
	var l RWLock
	ctx := context.Background()
	r1, _ := l.Acquire(ctx, false)
	w := acquireAsync(ctx, &l, true)
	queued(t, &l, 1)
	r2 := acquireAsync(ctx, &l, false)
	queued(t, &l, 2)
	if _, ok := granted(r2, short); ok {
		t.Fatal("a reader jumped a queued writer")
	}
	r1()
	wrel, ok := granted(w, time.Second)
	if !ok {
		t.Fatal("writer not granted")
	}
	if _, ok := granted(r2, short); ok {
		t.Fatal("reader ran alongside the writer")
	}
	wrel()
	if rel, ok := granted(r2, time.Second); !ok {
		t.Fatal("reader not granted after the writer")
	} else {
		rel()
	}
}

// Grants are FIFO; consecutive readers at the head start together, and a
// writer stops the batch.
func TestRWLockFIFO(t *testing.T) {
	var l RWLock
	ctx := context.Background()
	w0, _ := l.Acquire(ctx, true)

	var mu sync.Mutex
	var order []string
	type step struct {
		name  string
		write bool
	}
	steps := []step{{"r1", false}, {"r2", false}, {"w1", true}, {"r3", false}, {"w2", true}}
	rels := make([]chan func(), len(steps))
	for i, s := range steps {
		rels[i] = make(chan func(), 1)
		go func(i int, s step) {
			rel, _ := l.Acquire(ctx, s.write)
			mu.Lock()
			order = append(order, s.name)
			mu.Unlock()
			rels[i] <- rel
		}(i, s)
		queued(t, &l, i+1) // enqueue strictly in this order
	}

	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), order...)
	}
	w0()
	r1, _ := granted(rels[0], time.Second)
	r2, _ := granted(rels[1], time.Second)
	if r1 == nil || r2 == nil {
		t.Fatalf("readers at the head not granted together: %v", snapshot())
	}
	if _, ok := granted(rels[2], short); ok {
		t.Fatal("w1 granted while readers hold the lock")
	}
	r1()
	r2()
	w1, ok := granted(rels[2], time.Second)
	if !ok {
		t.Fatal("w1 not granted")
	}
	if _, ok := granted(rels[3], short); ok {
		t.Fatal("r3 granted during w1")
	}
	w1()
	r3, _ := granted(rels[3], time.Second)
	if _, ok := granted(rels[4], short); ok {
		t.Fatal("w2 granted during r3")
	}
	r3()
	w2, _ := granted(rels[4], time.Second)
	w2()
	got := snapshot()
	want := []string{"r1", "r2", "w1", "r3", "w2"}
	if len(got) != 5 || got[2] != "w1" || got[3] != "r3" || got[4] != "w2" {
		t.Fatalf("order = %v, want %v (r1/r2 in either order)", got, want)
	}
}

func TestRWLockReleaseIdempotent(t *testing.T) {
	var l RWLock
	ctx := context.Background()
	r1, _ := l.Acquire(ctx, false)
	r2, _ := l.Acquire(ctx, false)
	r1()
	r1() // must not release r2's share
	w := acquireAsync(ctx, &l, true)
	if _, ok := granted(w, short); ok {
		t.Fatal("double release freed another reader's share")
	}
	r2()
	wrel, ok := granted(w, time.Second)
	if !ok {
		t.Fatal("writer not granted")
	}
	wrel()
	wrel()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.readers != 0 || l.writer {
		t.Fatalf("state readers=%d writer=%v", l.readers, l.writer)
	}
}

func TestRWLockCancelWhileQueued(t *testing.T) {
	var l RWLock
	r1, _ := l.Acquire(context.Background(), false)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := l.Acquire(ctx, true)
		errCh <- err
	}()
	queued(t, &l, 1)
	// A reader queued behind the cancelled writer must be let through once the
	// writer leaves the queue.
	r2 := acquireAsync(context.Background(), &l, false)
	queued(t, &l, 2)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	rel, ok := granted(r2, time.Second)
	if !ok {
		t.Fatal("reader stuck behind a cancelled writer")
	}
	rel()
	r1()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.readers != 0 || l.writer || len(l.waiters) != 0 {
		t.Fatalf("state readers=%d writer=%v waiters=%d", l.readers, l.writer, len(l.waiters))
	}
}

func TestLockRegistry(t *testing.T) {
	var r lockRegistry
	var wg sync.WaitGroup
	locks := make([]*RWLock, 50)
	for i := range locks {
		wg.Add(1)
		go func(i int) { defer wg.Done(); locks[i] = r.lockFor("/repo/a") }(i)
	}
	wg.Wait()
	for _, l := range locks {
		if l != locks[0] {
			t.Fatal("lockFor returned different locks for one repo")
		}
	}
	if r.lockFor("/repo/b") == locks[0] {
		t.Fatal("two repos share a lock")
	}
}
