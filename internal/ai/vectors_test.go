package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Helpers shared by the vector replays. Vector files are written by
// test/port/ai.test.ts (PORT_WRITE_VECTORS=1) from the TypeScript originals.

func readVectors(t *testing.T, rel string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}

func readTestdata(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// decodeJS decodes a vector value into this package's JS value tree.
func decodeJS(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	v, err := jsonParse(string(raw))
	if err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// sameJSON compares two values by their JSON meaning (key order ignored).
func sameJSON(t *testing.T, got any, want json.RawMessage) bool {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var a, b any
	if err := json.Unmarshal(g, &a); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	return reflect.DeepEqual(a, b)
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
