package menu

import "testing"

func TestAccelerator(t *testing.T) {
	cases := map[string]string{
		"CmdOrCtrl+Shift+T": "CmdOrCtrl+Shift+T",
		"Control+Tab":       "Ctrl+Tab",
		"Alt+F4":            "Alt+F4",
		"control+K":         "Ctrl+K",
	}
	for in, want := range cases {
		if got := Accelerator(in); got != want {
			t.Errorf("Accelerator(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every role the renderer may send (MENU_ROLES in src/shared/menu.ts) maps to a
// Wails role; a missing one would silently drop an Edit-menu entry.
func TestRolesCoverRendererList(t *testing.T) {
	renderer := []string{"about", "services", "hide", "hideOthers", "unhide", "quit", "undo", "redo", "cut",
		"copy", "paste", "pasteAndMatchStyle", "selectAll", "reload", "forceReload", "toggleDevTools",
		"resetZoom", "zoomIn", "zoomOut", "togglefullscreen", "minimize", "zoom", "front", "windowMenu"}
	for _, r := range renderer {
		if _, ok := roles[r]; !ok {
			t.Errorf("role %q has no Wails mapping", r)
		}
	}
	if len(roles) != len(renderer) {
		t.Errorf("roles table has %d entries, renderer list %d", len(roles), len(renderer))
	}
}

func TestBuildDoesNotPanic(t *testing.T) {
	no := false
	spec := []Item{
		{Label: "File", Submenu: []Item{
			{ID: "new", Label: "New", Accelerator: "CmdOrCtrl+N"},
			{Type: "separator"},
			{ID: "x", Label: "Shown only", Accelerator: "CmdOrCtrl+K", ShowOnly: true, Enabled: &no},
			{ID: "c", Label: "Check", Type: "checkbox", Checked: true},
			{Role: "bogus"},
		}},
		{Label: "Edit", Submenu: []Item{{Role: "copy"}, {Role: "paste"}}},
	}
	if Build(spec, func(string) {}) == nil {
		t.Fatal("nil menu")
	}
}
