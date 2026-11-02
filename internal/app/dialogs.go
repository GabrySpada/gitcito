package app

// DialogService serves the renderer channels that open native file dialogs:
//   dialog:selectDirectory, dialog:savePatch, dialog:openPatch,
//   dialog:choosePath, dialog:openFilePath, dialog:saveBinary
// It mirrors the handlers that lived in src/main/index.ts. Titles stay the
// English defaults the Electron build used when the renderer passes none.
//
// Dialogs only work after app.Run(): Wails returns an empty selection before
// that, which these methods report as "cancelled" (nil).

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// DialogService is bound to the page.
type DialogService struct{}

// DialogFilter matches Electron's FileFilter: a name and bare extensions.
type DialogFilter struct {
	Name       string   `json:"name"`
	Extensions []string `json:"extensions"`
}

// PatchFile is what OpenPatch returns.
type PatchFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func pattern(exts []string) string {
	parts := make([]string, 0, len(exts))
	for _, e := range exts {
		if e == "*" {
			parts = append(parts, "*")
			continue
		}
		parts = append(parts, "*."+strings.TrimPrefix(e, "."))
	}
	return strings.Join(parts, ";")
}

func withDefaultFilter(filters []DialogFilter) []DialogFilter {
	if len(filters) > 0 {
		return filters
	}
	return []DialogFilter{{Name: "All files", Extensions: []string{"*"}}}
}

// save opens a save dialog. defaultPath may be a bare name or a full path, as
// Electron's defaultPath allowed.
func save(title, defaultPath string, filters []DialogFilter) (string, error) {
	a := application.Get()
	if a == nil {
		return "", nil
	}
	d := a.Dialog.SaveFile().CanCreateDirectories(true)
	d.SetOptions(&application.SaveFileDialogOptions{Title: title, CanCreateDirectories: true})
	if dir := filepath.Dir(defaultPath); dir != "." && filepath.IsAbs(defaultPath) {
		d = d.SetDirectory(dir)
	}
	if defaultPath != "" {
		d = d.SetFilename(filepath.Base(defaultPath))
	}
	for _, f := range filters {
		d = d.AddFilter(f.Name, pattern(f.Extensions))
	}
	return d.PromptForSingleSelection()
}

func open(title string, filters []DialogFilter, dirs bool) (string, error) {
	a := application.Get()
	if a == nil {
		return "", nil
	}
	d := a.Dialog.OpenFile().SetTitle(title)
	if dirs {
		d = d.CanChooseDirectories(true).CanChooseFiles(false).CanCreateDirectories(true)
	} else {
		d = d.CanChooseFiles(true)
	}
	for _, f := range filters {
		d = d.AddFilter(f.Name, pattern(f.Extensions))
	}
	return d.PromptForSingleSelection()
}

func orNil(p string, err error) (*string, error) {
	if err != nil || p == "" {
		// A cancelled native dialog surfaces as an error on some platforms;
		// the renderer has always treated every non-selection as "cancelled".
		return nil, nil
	}
	return &p, nil
}

// SelectDirectory picks a folder (dialog:selectDirectory).
// channel: dialog:selectDirectory
func (s *DialogService) SelectDirectory(title string) (*string, error) {
	if title == "" {
		title = "Open repository"
	}
	return orNil(open(title, nil, true))
}

// SavePatch saves a generated patch to a user-chosen file. Returns the path, or
// nil if cancelled.
// channel: dialog:savePatch
func (s *DialogService) SavePatch(ctx context.Context, defaultName, content string) (*string, error) {
	p, err := orNil(save("Export patch", defaultName, []DialogFilter{{Name: "Patch", Extensions: []string{"patch", "diff"}}}))
	if p == nil || err != nil {
		return nil, err
	}
	if err := os.WriteFile(*p, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return p, nil
}

// SaveBinary saves binary content (the timelapse video) to a user-chosen file.
// The renderer records in-page and hands the bytes over; nothing is written
// anywhere until the user picks a destination. []byte arrives base64-encoded
// over the bridge and is decoded by encoding/json.
// channel: dialog:saveBinary
func (s *DialogService) SaveBinary(ctx context.Context, defaultName string, data []byte, filters []DialogFilter) (*string, error) {
	p, err := orNil(save("Save file", defaultName, withDefaultFilter(filters)))
	if p == nil || err != nil {
		return nil, err
	}
	if err := os.WriteFile(*p, data, 0o644); err != nil {
		return nil, err
	}
	return p, nil
}

// ChoosePath picks a destination path without writing anything: git writes the
// file itself (bundle, archive), so the renderer only needs somewhere to point it.
// channel: dialog:choosePath
func (s *DialogService) ChoosePath(title, defaultName string, filters []DialogFilter) (*string, error) {
	return orNil(save(title, defaultName, withDefaultFilter(filters)))
}

// OpenFilePath picks an existing file, by path only — for binary formats the
// renderer never reads itself (a bundle is handed straight to git).
// channel: dialog:openFilePath
func (s *DialogService) OpenFilePath(title string, filters []DialogFilter) (*string, error) {
	return orNil(open(title, withDefaultFilter(filters), false))
}

// OpenPatch opens a patch/diff file. Returns { path, content } or nil if cancelled.
// channel: dialog:openPatch
func (s *DialogService) OpenPatch(ctx context.Context) (*PatchFile, error) {
	p, err := orNil(open("Apply patch", []DialogFilter{
		{Name: "Patch", Extensions: []string{"patch", "diff"}},
		{Name: "All files", Extensions: []string{"*"}},
	}, false))
	if p == nil || err != nil {
		return nil, err
	}
	b, err := os.ReadFile(*p)
	if err != nil {
		return nil, err
	}
	return &PatchFile{Path: *p, Content: string(b)}, nil
}
