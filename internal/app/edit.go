package app

// The `gitcito --wait <file>` editor flow (core.editor = gitcito --wait).
//
// git is blocked on a shell process polling a sentinel file. The file's
// contents go to the renderer and the sentinel is recorded here; deleting the
// sentinel is what unblocks git. Every path out — finished, cancelled, unreadable
// file, app quitting — must release it exactly once.

import (
	"errors"
	"os"
	"sync"

	"github.com/GabrySpada/gitcito/internal/shared"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// EditRequest is the payload of the cli:edit event.
type EditRequest struct {
	File     string `json:"file"`
	Sentinel string `json:"sentinel"`
	Content  string `json:"content"`
}

// FinishResult mirrors cli:finishEdit's { ok, error? }.
type FinishResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// waits maps a sentinel we handed out to the file it guards.
type waits struct {
	mu sync.Mutex
	m  map[string]string
}

func (w *waits) track(sentinel, file string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil {
		w.m = map[string]string{}
	}
	w.m[sentinel] = file
}

func (w *waits) take(sentinel string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, ok := w.m[sentinel]
	return f, ok
}

// release unblocks a waiting git. Safe to call twice.
func (w *waits) release(sentinel string) {
	w.mu.Lock()
	delete(w.m, sentinel)
	w.mu.Unlock()
	_ = os.Remove(sentinel)
}

// releaseAll releases every outstanding wait — called when the app is going
// away, so a quit mid-edit aborts the commit instead of freezing the terminal.
func (w *waits) releaseAll() {
	w.mu.Lock()
	keys := make([]string, 0, len(w.m))
	for k := range w.m {
		keys = append(keys, k)
	}
	w.mu.Unlock()
	for _, k := range keys {
		w.release(k)
	}
}

// finish writes the renderer's result. Only a sentinel we handed out is
// honoured, and the file written is the one *we* recorded for it — the renderer
// never names a path to write to. A nil content means cancel: git treats an
// empty message as "abort the commit", the safe reading of a closed dialog.
func (w *waits) finish(sentinel string, content *string) FinishResult {
	file, ok := w.take(sentinel)
	if !ok {
		return FinishResult{Error: "No edit is pending"}
	}
	defer w.release(sentinel)
	text := ""
	if content != nil {
		text = *content
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		return FinishResult{Error: errMessage(err)}
	}
	return FinishResult{OK: true}
}

func errMessage(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Error()
	}
	return err.Error()
}

// deliver sends a CLI request to the renderer once it is ready.
func (s *AppService) deliver(p *shared.CliOpenPayload) {
	s.whenReady(func() {
		a := application.Get()
		if a == nil {
			return
		}
		a.Event.Emit("cli:open-path", p)
		if p.Edit != "" && p.Wait != "" {
			s.deliverEdit(p.Edit, p.Wait)
		}
	})
}

// deliverEdit hands a file git is waiting on to the renderer. Reading fails?
// Release immediately: a hung `git commit` is a far worse outcome than an editor
// that refused to open.
func (s *AppService) deliverEdit(file, sentinel string) {
	b, err := os.ReadFile(file)
	if err != nil {
		_ = os.Remove(sentinel)
		return
	}
	s.edits.track(sentinel, file)
	if a := application.Get(); a != nil {
		a.Event.Emit("cli:edit", EditRequest{File: file, Sentinel: sentinel, Content: string(b)})
	}
}

// HandleColdLaunch delivers a `gitcito <dir>` request from this process's own
// argv. Not bound: main calls it.
func (s *AppService) HandleColdLaunch(argv []string) {
	if p := shared.ParseCliOpenArgs(argv); p != nil {
		s.deliver(p)
	}
}

// HandleSecondInstance delivers a request handed over by a second launch.
func (s *AppService) HandleSecondInstance(argv []string) {
	if p := shared.ParseCliOpenArgs(argv); p != nil {
		s.deliver(p)
	}
}

// FinishEdit answers cli:finishEdit.
func (s *AppService) FinishEdit(sentinel string, content *string) FinishResult {
	return s.edits.finish(sentinel, content)
}

// ReleaseAllWaits runs on shutdown.
func (s *AppService) ReleaseAllWaits() { s.edits.releaseAll() }
