// Package menu builds the native application menu from the renderer's spec.
//
// Serves the renderer channels:
//
//	menu:set      (MenuService.Set)
//	menu:command  (event, emitted when an item with an id is clicked)
//
// Everything about *what* the menu says is decided in the renderer (see
// renderer/src/lib/appMenu.ts) and arrives here already translated; this
// package only turns that description into Wails menu items and routes clicks
// back. Keeping the decisions on the renderer side is what lets the menu track
// the language, the active repository and the tab list without Go knowing
// anything about them.
//
// The menu is installed on macOS only. Windows and Linux run frameless, where an
// application menu has nowhere to draw.
package menu

import (
	"log/slog"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Item mirrors MenuSpecItem in src/shared/menu.ts.
type Item struct {
	ID          string `json:"id,omitempty"`
	Label       string `json:"label,omitempty"`
	Role        string `json:"role,omitempty"`
	Accelerator string `json:"accelerator,omitempty"`
	ShowOnly    bool   `json:"showOnly,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Type        string `json:"type,omitempty"`
	Checked     bool   `json:"checked,omitempty"`
	Submenu     []Item `json:"submenu,omitempty"`
}

// roles maps the renderer's MENU_ROLES (Electron names) onto Wails roles. A role
// outside this table is dropped rather than passed through, so a bad spec can
// never take the whole menu down.
var roles = map[string]application.Role{
	"about":              application.About,
	"services":           application.ServicesMenu,
	"hide":               application.Hide,
	"hideOthers":         application.HideOthers,
	"unhide":             application.UnHide,
	"quit":               application.Quit,
	"undo":               application.Undo,
	"redo":               application.Redo,
	"cut":                application.Cut,
	"copy":               application.Copy,
	"paste":              application.Paste,
	"pasteAndMatchStyle": application.PasteAndMatchStyle,
	"selectAll":          application.SelectAll,
	"reload":             application.Reload,
	"forceReload":        application.ForceReload,
	"toggleDevTools":     application.OpenDevTools,
	"resetZoom":          application.ResetZoom,
	"zoomIn":             application.ZoomIn,
	"zoomOut":            application.ZoomOut,
	"togglefullscreen":   application.ToggleFullscreen,
	"minimize":           application.Minimise,
	"zoom":               application.Zoom,
	"front":              application.BringAllToFront,
	"windowMenu":         application.WindowMenu,
}

// Accelerator converts an Electron accelerator into Wails syntax. The two agree
// except for Electron's "Control" spelling, which Wails only knows as "Ctrl".
func Accelerator(electron string) string {
	parts := strings.Split(electron, "+")
	for i, p := range parts {
		if strings.EqualFold(p, "control") {
			parts[i] = "Ctrl"
		}
	}
	return strings.Join(parts, "+")
}

// Build turns a spec into a Wails menu. Clicking an item with an id emits
// menu:command with that id.
func Build(spec []Item, emit func(id string)) *application.Menu {
	m := application.NewMenu()
	addItems(m, spec, emit)
	return m
}

func addItems(m *application.Menu, items []Item, emit func(id string)) {
	for _, it := range items {
		addItem(m, it, emit)
	}
}

func addItem(m *application.Menu, it Item, emit func(id string)) {
	if it.Type == "separator" {
		m.AddSeparator()
		return
	}
	if it.Role != "" {
		r, ok := roles[it.Role]
		if !ok {
			return
		}
		m.AddRole(r)
		return
	}
	if len(it.Submenu) > 0 || it.Type == "submenu" {
		sub := m.AddSubmenu(it.Label)
		addItems(sub, it.Submenu, emit)
		return
	}
	var mi *application.MenuItem
	if it.Type == "checkbox" {
		mi = m.AddCheckbox(it.Label, it.Checked)
	} else {
		mi = m.Add(it.Label)
	}
	// showOnly items document a shortcut the renderer's own keydown listener
	// owns. Wails cannot display an accelerator without registering it, and a
	// registered one would consume the key before the renderer sees it — so the
	// shortcut is left off the item (listed under limitations).
	if it.Accelerator != "" && !it.ShowOnly {
		mi.SetAccelerator(Accelerator(it.Accelerator))
	}
	if it.Enabled != nil && !*it.Enabled {
		mi.SetEnabled(false)
	}
	if id := it.ID; id != "" {
		mi.OnClick(func(*application.Context) { emit(id) })
	}
}

// Service is bound to the page.
type Service struct{}

// Set answers menu:set. A malformed spec must never leave the app menu-less:
// a panic while building keeps the previous menu and logs the reason.
func (s *Service) Set(spec []Item) {
	if runtime.GOOS != "darwin" {
		return
	}
	a := application.Get()
	if a == nil || spec == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("menu: failed to build application menu", "err", r)
		}
	}()
	m := Build(spec, func(id string) { a.Event.Emit("menu:command", id) })
	a.Menu.Set(m)
}
