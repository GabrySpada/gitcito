package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

const sentinel = "ghp_SENTINELTOKEN0123456789"

func TestSystemLoggerNeverPrintsCallArguments(t *testing.T) {
	var buf bytes.Buffer
	// Debug enabled on purpose: the redaction must not depend on the level.
	l := slog.New(redactingHandler{inner: slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})})
	// The exact shape of Wails' messageprocessor_call.go debug line.
	l.Debug("Binding call complete:", "id", "1", "method", "hosting.Service.ListRepos",
		"args", `["github","`+sentinel+`"]`, "result", `{"token":"`+sentinel+`"}`)
	l.With("args", sentinel).Info("bound")
	out := buf.String()
	if strings.Contains(out, sentinel) {
		t.Fatalf("token leaked into the log:\n%s", out)
	}
	if !strings.Contains(out, "Binding call complete") || !strings.Contains(out, "hosting.Service.ListRepos") {
		t.Fatalf("the rest of the record must survive:\n%s", out)
	}
}

func TestSystemLoggerIsInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	l := SystemLogger(&buf)
	l.Debug("noise", "args", sentinel)
	if buf.Len() != 0 {
		t.Fatalf("debug output at the default level: %s", buf.String())
	}
}
