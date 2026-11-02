package app

import (
	"log/slog"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// MainWindowName is the name the main window is registered under.
const MainWindowName = "main"

// FilesDropped is the payload of the files:dropped event, which replaces
// Electron's synchronous webUtils.getPathForFile: a webview File has no path,
// so Go reports the native drop and the renderer routes it by targetId.
type FilesDropped struct {
	Paths    []string `json:"paths"`
	TargetID string   `json:"targetId"`
	X        int      `json:"x"`
	Y        int      `json:"y"`
}

// NewMainWindow creates the main window with the geometry and chrome the
// Electron build used.
func NewMainWindow(a *application.App) *application.WebviewWindow {
	opts := application.WebviewWindowOptions{
		Name:      MainWindowName,
		Title:     "Gitcito",
		Width:     1540,
		Height:    960,
		MinWidth:  1100,
		MinHeight: 680,
		// Revealed maximized, as showMainWindow did: the window stays
		// maximizable so the OS owns its standard maximize gestures.
		StartState:       application.WindowStateMaximised,
		BackgroundColour: application.NewRGB(0x0e, 0x0f, 0x15),
		EnableFileDrop:   true,
		URL:              "/",
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHiddenInset,
		},
	}
	// Windows and Linux run frameless: the renderer draws its own title-bar
	// buttons. macOS keeps the native traffic lights over a hidden title bar.
	if runtime.GOOS != "darwin" {
		opts.Frameless = true
	}
	w := a.Window.NewWithOptions(opts)

	w.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		// The smoke harness waits for this line.
		slog.Info("window ready")
	})
	w.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		payload := FilesDropped{Paths: e.Context().DroppedFiles()}
		if payload.Paths == nil {
			payload.Paths = []string{}
		}
		if d := e.Context().DropTargetDetails(); d != nil {
			payload.TargetID = d.ElementID
			payload.X, payload.Y = d.X, d.Y
		}
		a.Event.Emit("files:dropped", payload)
	})
	return w
}

// Focus restores and focuses the main window, for a second-instance hand-off.
func Focus(a *application.App) {
	w, ok := a.Window.GetByName(MainWindowName)
	if !ok {
		return
	}
	if w.IsMinimised() {
		w.Restore()
	}
	w.Focus()
}
