package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Replays testdata/vectors/cli.json, written from the TypeScript original by
// test/port/vectors-cli.test.ts.
func TestParseCliOpenArgsVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "cli.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Argv []string        `json:"argv"`
		Out  json.RawMessage `json:"out"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range cases {
		got, _ := json.Marshal(ParseCliOpenArgs(c.Argv))
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal(c.Out, &b)
		ga, _ := json.Marshal(a)
		gb, _ := json.Marshal(b)
		if string(ga) != string(gb) {
			t.Errorf("argv %q: got %s, want %s", c.Argv, ga, gb)
		}
	}
}
