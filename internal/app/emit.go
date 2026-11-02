package app

import "github.com/wailsapp/wails/v3/pkg/application"

// Emitter adapts the running Wails app to the `Emit(name, data)` interface the
// domain packages take, so they never import Wails themselves. Before Run (or in
// tests) there is no app and the event is dropped — exactly what an Electron
// webContents.send to a window that does not exist yet did.
type Emitter struct{}

// Emit sends a named event to every window.
func (Emitter) Emit(name string, data any) {
	if a := application.Get(); a != nil {
		a.Event.Emit(name, data)
	}
}

// HasWindow reports whether any window exists to ask the user something in.
func HasWindow() bool {
	a := application.Get()
	return a != nil && len(a.Window.GetAll()) > 0
}
