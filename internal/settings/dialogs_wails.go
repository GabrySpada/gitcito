package settings

import "github.com/wailsapp/wails/v3/pkg/application"

// WailsDialogs opens the real native dialogs. Before app.Run() there is no
// application and nothing to show, which reads as "cancelled".
type WailsDialogs struct{}

// OpenFile picks one existing file.
func (WailsDialogs) OpenFile(title string, filters []FileFilter) (string, error) {
	a := application.Get()
	if a == nil {
		return "", nil
	}
	d := a.Dialog.OpenFile().SetTitle(title).CanChooseFiles(true).CanChooseDirectories(false)
	for _, f := range filters {
		d = d.AddFilter(f.Name, pattern(f.Extensions))
	}
	return d.PromptForSingleSelection()
}

// SaveFile picks a destination, pre-filled with defaultName.
func (WailsDialogs) SaveFile(title, defaultName string, filters []FileFilter) (string, error) {
	a := application.Get()
	if a == nil {
		return "", nil
	}
	d := a.Dialog.SaveFile()
	d.SetOptions(&application.SaveFileDialogOptions{Title: title, CanCreateDirectories: true, Filename: defaultName})
	for _, f := range filters {
		d = d.AddFilter(f.Name, pattern(f.Extensions))
	}
	return d.PromptForSingleSelection()
}
