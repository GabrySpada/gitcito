// Package watcher is src/main/watcher.ts: it watches the active repository
// and tells the page when it changed on disk, so edits made outside Gitcito
// (an editor, a terminal git command) show up without a manual refresh.
//
// The TS used fs.watch({ recursive: true }). This uses fsnotify and walks the
// tree itself, adding a watch per directory (and per directory created
// later), which is what Node's recursive watcher does on Linux. Directories
// whose every event the TS dropped as noise — node_modules and .git/objects —
// are not watched at all; their own creation or removal is still seen from
// the parent.
package watcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// EventChanged is the event the page listens on.
const EventChanged = "repo:changed"

// DebounceMS is how long the watcher waits for the burst of writes one git
// command makes to settle before it tells the page.
const DebounceMS = 350

// Changed is the `repo:changed` payload.
type Changed struct {
	Path string `json:"path"`
	// Light: only the working tree changed, so a cheap status refresh is
	// enough; false when something under .git moved (branches, commits, merge
	// state) and the page needs a full refresh.
	Light bool `json:"light"`
}

// Emitter delivers events to the page.
type Emitter interface {
	Emit(name string, data any)
}

// Watcher holds the one active watch. The app is a single window with repo
// tabs, so the active repository is re-watched on every tab switch (the TS
// kept one per renderer; there is one renderer).
type Watcher struct {
	emit     Emitter
	now      func() time.Time
	debounce time.Duration

	mu     sync.Mutex
	active *repoWatch
	// seq numbers Set calls, so the last one wins even when an earlier one
	// is still walking its tree.
	seq int
}

type repoWatch struct {
	path string
	fsw  *fsnotify.Watcher
	done chan struct{}
	once sync.Once
	// Guarded by Watcher.mu.
	timer *time.Timer
	// gen invalidates a timer that fired after it was replaced or stopped.
	gen int
	// gitChanged: a `.git` metadata change occurred during the current
	// debounce window.
	gitChanged bool
	// muteUntil: change events are ignored until then. Set by `watch:mute`
	// right before the app performs its own git write, so the app's own
	// mutation does not trigger a second, redundant refresh on top of its
	// post-action one.
	muteUntil time.Time
}

// New builds a watcher that reports through emit. now is the clock, nil for
// the real one.
func New(emit Emitter, now func() time.Time) *Watcher {
	if now == nil {
		now = time.Now
	}
	return &Watcher{emit: emit, now: now, debounce: DebounceMS * time.Millisecond}
}

// normalize turns Windows separators into slashes.
func normalize(rel string) string { return strings.ReplaceAll(rel, `\`, "/") }

// isNoise: paths inside .git that churn constantly without meaningful state
// changes, and dependency folders.
func isNoise(rel string) bool {
	p := normalize(rel)
	if strings.Contains(p, "node_modules/") {
		return true
	}
	// Git object writes & lock files are intermediate; the ref/index update
	// that follows is what we actually care about.
	if strings.Contains(p, ".git/objects/") {
		return true
	}
	return strings.HasSuffix(p, ".lock")
}

// isGitMeta: a change under `.git` (HEAD, refs, logs, index, MERGE_HEAD…)
// means branches, commits or merge state may have changed → needs a full
// refresh. A change in the working tree only affects status → a cheap
// "light" refresh is enough.
func isGitMeta(rel string) bool {
	p := normalize(rel)
	return p == ".git" || strings.HasPrefix(p, ".git/")
}

// Set watches repoPath, replacing the previous watch; "" stops watching.
// Asking for the path already watched is a no-op.
func (w *Watcher) Set(repoPath string) {
	w.mu.Lock()
	if w.active != nil && w.active.path == repoPath {
		w.mu.Unlock()
		return
	}
	old := w.active
	w.active = nil
	if old != nil && old.timer != nil {
		old.timer.Stop()
	}
	w.seq++
	seq := w.seq
	w.mu.Unlock()
	if old != nil {
		old.close()
	}
	if repoPath == "" {
		return
	}

	rw, err := start(repoPath, repoPath)
	if err != nil {
		// No watch on the tree (it vanished, or the platform refused): fall
		// back to the .git directory so branch/commit changes still surface.
		if rw, err = start(repoPath, filepath.Join(repoPath, ".git")); err != nil {
			return
		}
	}
	w.mu.Lock()
	if w.seq != seq {
		// A later Set superseded this one while it was starting.
		w.mu.Unlock()
		rw.close()
		return
	}
	w.active = rw
	w.mu.Unlock()
	go w.loop(rw)
}

// Mute ignores the active watch's events for ms milliseconds, when it is
// watching repoPath.
func (w *Watcher) Mute(repoPath string, ms float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != nil && w.active.path == repoPath {
		if ms < 0 || ms != ms {
			ms = 0
		}
		w.active.muteUntil = w.now().Add(time.Duration(ms * float64(time.Millisecond)))
	}
}

// Close stops watching.
func (w *Watcher) Close() {
	w.Set("")
}

// start opens a watcher on root (the repository, or its .git in fallback)
// and every directory under it worth watching.
func start(repoPath, root string) (*repoWatch, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("not a directory")
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fsw.Add(root); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	rw := &repoWatch{path: repoPath, fsw: fsw, done: make(chan struct{})}
	rw.addTree(root, true)
	return rw, nil
}

// addTree watches every directory under dir (dir itself too unless skipRoot),
// leaving out the ones whose events would all be noise. A directory that
// cannot be watched (vanished, unreadable, out of watches) is passed over.
func (rw *repoWatch) addTree(dir string, skipRoot bool) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if rel, rerr := filepath.Rel(rw.path, p); rerr == nil && rel != "." && isNoise(rel+"/") {
			return filepath.SkipDir
		}
		if p == dir && skipRoot {
			return nil
		}
		if err := rw.fsw.Add(p); err != nil {
			return filepath.SkipDir
		}
		return nil
	})
}

func (rw *repoWatch) close() {
	rw.once.Do(func() {
		close(rw.done)
		_ = rw.fsw.Close()
	})
}

func (w *Watcher) loop(rw *repoWatch) {
	for {
		select {
		case <-rw.done:
			return
		case ev, ok := <-rw.fsw.Events:
			if !ok {
				return
			}
			if ev.Has(fsnotify.Create) {
				if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() {
					rw.addTree(ev.Name, false)
				}
			}
			rel, err := filepath.Rel(rw.path, ev.Name)
			if err != nil || rel == "." {
				rel = ""
			}
			w.onChange(rw, rel)
		case err, ok := <-rw.fsw.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				// The kernel dropped events: something changed, and we no longer
				// know what. A full refresh is the safe answer.
				w.onChange(rw, ".git")
				continue
			}
			// Any other watcher error ends the watch, as it did in the TS.
			w.mu.Lock()
			if w.active == rw {
				w.active = nil
			}
			if rw.timer != nil {
				rw.timer.Stop()
			}
			w.mu.Unlock()
			rw.close()
			return
		}
	}
}

// onChange records one event and (re)starts the debounce timer. rel is ""
// when the event names the watched root itself.
func (w *Watcher) onChange(rw *repoWatch, rel string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != rw {
		return
	}
	// Ignore events during a self-mute window (the app's own git write).
	if w.now().Before(rw.muteUntil) {
		return
	}
	if rel != "" && isNoise(rel) {
		return
	}
	if rel != "" && isGitMeta(rel) {
		rw.gitChanged = true
	}
	if rw.timer != nil {
		rw.timer.Stop()
	}
	rw.gen++
	gen := rw.gen
	rw.timer = time.AfterFunc(w.debounce, func() { w.fire(rw, gen) })
}

func (w *Watcher) fire(rw *repoWatch, gen int) {
	w.mu.Lock()
	if w.active != rw || rw.gen != gen {
		w.mu.Unlock()
		return
	}
	light := !rw.gitChanged
	rw.gitChanged = false
	rw.timer = nil
	path := rw.path
	w.mu.Unlock()
	if w.emit != nil {
		w.emit.Emit(EventChanged, Changed{Path: path, Light: light})
	}
}
