package settings

import "strings"

// FileFilter is one entry of a file dialog's type filter.
type FileFilter struct {
	Name       string
	Extensions []string
}

// Dialogs opens the native file dialogs import and export need. Injectable so
// tests run without a window; the real one is WailsDialogs. An empty path
// means the user cancelled.
type Dialogs interface {
	OpenFile(title string, filters []FileFilter) (string, error)
	SaveFile(title, defaultName string, filters []FileFilter) (string, error)
}

// pattern turns bare extensions into a Wails filter pattern ("*.json").
func pattern(exts []string) string {
	parts := make([]string, 0, len(exts))
	for _, e := range exts {
		parts = append(parts, "*."+strings.TrimPrefix(e, "."))
	}
	return strings.Join(parts, ";")
}
