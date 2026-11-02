package app

import (
	"context"
	"io"
	"log/slog"
)

// Wails logs every bound call at debug level as "Binding call complete" with
// the JSON of its arguments and result. Hosting methods take tokens as
// arguments and settings:get returns them, so that line would write credentials
// into the terminal (and anything capturing it). The Wails system logger is
// wrapped so those attributes never reach the output, at any level.
var droppedAttrs = map[string]bool{"args": true, "result": true}

type redactingHandler struct{ inner slog.Handler }

func (h redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if droppedAttrs[a.Key] {
			clean.AddAttrs(slog.String(a.Key, "[omitted]"))
		} else {
			clean.AddAttrs(a)
		}
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	kept := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if !droppedAttrs[a.Key] {
			kept = append(kept, a)
		}
	}
	return redactingHandler{inner: h.inner.WithAttrs(kept)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{inner: h.inner.WithGroup(name)}
}

// SystemLogger is the logger handed to Wails (application.Options.Logger).
// Info level: Wails' debug chatter is not useful to users, and the redaction
// above holds even if someone lowers the level.
func SystemLogger(w io.Writer) *slog.Logger {
	return slog.New(redactingHandler{inner: slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})})
}
