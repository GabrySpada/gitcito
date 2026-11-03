package editor

// Service serves the renderer's external-editor channels:
//   editor:detect, editor:open
// It mirrors registerEditorHandlers in src/main/editor.ts. Every exported
// method is callable from the page, so nothing else is exported here; the work
// happens on Editors.

import (
	"context"
	"fmt"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Service is the bound facade over Editors.
type Service struct {
	e *Editors
}

// NewService builds the facade. main.go passes New().
func NewService(e *Editors) *Service {
	return &Service{e: e}
}

// Detect lists the editors installed on this machine, CLI installs first.
// channel: editor:detect
func (s *Service) Detect(ctx context.Context) (out []shared.DetectedEditor, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, fmt.Errorf("editor: internal error: %v", p)
		}
	}()
	return s.e.Detect(), nil
}

// Open launches the configured editor on a file or folder. It returns ” on
// success, else the message to toast.
// channel: editor:open
func (s *Service) Open(ctx context.Context, setting *shared.EditorSetting, target *shared.EditorTarget) (msg string) {
	defer func() {
		if p := recover(); p != nil {
			msg = fmt.Sprintf("editor: internal error: %v", p)
		}
	}()
	// TS: `!setting?.command || typeof target?.path !== 'string'`. A JSON
	// target without a path decodes to "" here, so an empty path is treated as
	// no target at all rather than handed to the editor.
	if setting == nil || setting.Command == "" || target == nil || target.Path == "" {
		return NoEditorMessage
	}
	return s.e.Open(ctx, *setting, *target)
}
