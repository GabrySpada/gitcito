package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Replays testdata/vectors/localCi.json, written from src/shared/localCi.ts by
// test/port/processes.test.ts.
func TestParseWorkflowNameVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "localCi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ParseWorkflowName []struct {
			Input string  `json:"input"`
			Out   *string `json:"out"`
		} `json:"parseWorkflowName"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.ParseWorkflowName) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.ParseWorkflowName {
		name, ok := ParseWorkflowName(c.Input)
		if ok != (c.Out != nil) || (ok && name != *c.Out) {
			t.Errorf("%q: got (%q, %v), want %v", c.Input, name, ok, c.Out)
		}
	}
}

// Ported from test/pureLogic.test.ts › parseWorkflowName (local CI).
func TestParseWorkflowName(t *testing.T) {
	tests := []struct {
		name, in, want string
		ok             bool
	}{
		{"reads a plain top-level name", "name: CI\non: [push]\n", "CI", true},
		{"strips quotes", "name: \"Build & Test\"\njobs:\n", "Build & Test", true},
		{"returns null without a name", "on: [push]\njobs:\n  x: {}\n", "", false},
		{"ignores an indented name inside a job", "on: [push]\njobs:\n  build:\n    name: inner\n", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseWorkflowName(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: got (%q,%v)", tt.name, got, ok)
		}
	}
}

func TestJSCompatHelpers(t *testing.T) {
	re := MustJSRegexp(`\$\{x\}`, false)
	if got := JSReplaceAll(re, "a${x}b", "[$&|$$|$1|$`|$']"); got != "a[${x}|$|$1|a|b]b" {
		t.Errorf("JSReplaceAll = %q", got)
	}
	if got := JSReplaceFirstString("%s and %s", "%s", "v"); got != "v and %s" {
		t.Errorf("JSReplaceFirstString = %q", got)
	}
	if got := TailUTF16("ab🚀c", 3); got != "🚀c" {
		t.Errorf("TailUTF16 = %q", got)
	}
	if got := TailUTF16("ab🚀c", 2); got != "c" {
		t.Errorf("TailUTF16 split pair = %q", got)
	}
	var d UTF8Stream
	b := []byte("é🚀x")
	got := d.Decode(b[:1]) + d.Decode(b[1:3]) + d.Decode(b[3:5]) + d.Decode(b[5:]) + d.Flush()
	if got != "é🚀x" {
		t.Errorf("UTF8Stream = %q", got)
	}
	if CompareUTF16("\uffff", "\U0001F680") <= 0 || CompareUTF16("a", "a") != 0 {
		t.Error("CompareUTF16")
	}
	if LocaleCompare("a", "B") >= 0 || LocaleCompare("a", "A") >= 0 || LocaleCompare("10", "9") >= 0 {
		t.Error("LocaleCompare")
	}
}
