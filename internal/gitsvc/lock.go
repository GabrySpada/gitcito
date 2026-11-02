package gitsvc

import (
	"context"
	"sync"
)

// ─── Per-repo read/write lock ──────────────────────────────────────────────
//
// Writes on a repo run exclusively; reads run concurrently with other reads but
// wait for any in-progress or queued write. This closes the race where a raw
// `git log`/diff read observes the repo mid-checkout/mid-commit and returns an
// inconsistent commit set — the root cause of the graph "bugging itself" after
// an action.
//
// Grants are FIFO: a run of consecutive readers starts together, but a queued
// writer blocks the readers behind it, so a steady stream of status polls can't
// starve a checkout.

// RWLock is the per-repository lock. The zero value is ready to use.
type RWLock struct {
	mu      sync.Mutex
	readers int
	writer  bool
	waiters []*lockWaiter
}

type lockWaiter struct {
	write   bool
	ready   chan struct{}
	granted bool
}

func (l *RWLock) canRun(write bool) bool {
	if write {
		return l.readers == 0 && !l.writer
	}
	return !l.writer
}

func (l *RWLock) enter(write bool) {
	if write {
		l.writer = true
	} else {
		l.readers++
	}
}

// Acquire waits for a read (write=false) or exclusive write grant and returns
// its release function. Release is idempotent: calling it twice releases once.
//
// A caller whose ctx ends while queued leaves the queue (TS could not cancel,
// but a Go caller can) and gets ctx.Err(); the queue behind it is re-pumped so
// a cancelled writer does not keep readers waiting.
func (l *RWLock) Acquire(ctx context.Context, write bool) (release func(), err error) {
	l.mu.Lock()
	if len(l.waiters) == 0 && l.canRun(write) {
		l.enter(write)
		l.mu.Unlock()
		return l.releaser(write), nil
	}
	w := &lockWaiter{write: write, ready: make(chan struct{})}
	l.waiters = append(l.waiters, w)
	l.mu.Unlock()

	select {
	case <-w.ready:
		return l.releaser(write), nil
	case <-ctx.Done():
		l.mu.Lock()
		if w.granted {
			// Granted in the same instant: we own it, so give it back.
			l.mu.Unlock()
			l.releaser(write)()
			return nil, ctx.Err()
		}
		for i, q := range l.waiters {
			if q == w {
				l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
				break
			}
		}
		l.pump()
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (l *RWLock) releaser(write bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if write {
				l.writer = false
			} else {
				l.readers--
			}
			l.pump()
		})
	}
}

// pump grants queued waiters in FIFO order. Callers hold l.mu.
func (l *RWLock) pump() {
	for len(l.waiters) > 0 {
		head := l.waiters[0]
		if !l.canRun(head.write) {
			break
		}
		l.waiters = l.waiters[1:]
		l.enter(head.write)
		head.granted = true
		close(head.ready)
		if head.write {
			break // exclusive writer — stop granting
		}
	}
}

// lockRegistry hands out one RWLock per repository path. Locks are never
// dropped (as in TS): a handful of repo paths per session costs nothing.
type lockRegistry struct {
	mu    sync.Mutex
	locks map[string]*RWLock
}

// lockFor returns the lock for repoPath, creating it on first use.
func (r *lockRegistry) lockFor(repoPath string) *RWLock {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks == nil {
		r.locks = map[string]*RWLock{}
	}
	l, ok := r.locks[repoPath]
	if !ok {
		l = &RWLock{}
		r.locks[repoPath] = l
	}
	return l
}
