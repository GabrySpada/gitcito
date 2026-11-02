package info

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func sameJSON(t *testing.T, what string, got any, want json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	_ = json.Unmarshal(raw, &g)
	_ = json.Unmarshal(want, &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, raw, want)
	}
}

func arg[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func newStore(t *testing.T, now *int64) (*Store, string) {
	dir := t.TempDir()
	ids := 0
	return New(Options{
		DataDir: func() (string, error) { return dir, nil },
		Now:     func() time.Time { return time.UnixMilli(*now) },
		NewID: func() string {
			ids++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", ids)
		},
	}), dir
}

// TestScenarioVector replays internal/info/testdata/scenario.json, captured
// from src/main/info.ts through its ipcMain handlers with a fixed clock and
// UUID source, including the file it left behind.
func TestScenarioVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "scenario.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Ops []struct {
			Op     string            `json:"op"`
			Args   []json.RawMessage `json:"args"`
			Now    *int64            `json:"now"`
			Result json.RawMessage   `json:"result"`
		} `json:"ops"`
		File json.RawMessage `json:"file"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var now int64
	s, dir := newStore(t, &now)
	for i, op := range v.Ops {
		if op.Now != nil {
			now = *op.Now
		}
		var got any
		switch op.Op {
		case "list":
			got = s.List(arg[string](t, op.Args[0]))
		case "upsert":
			got, err = s.Upsert(arg[string](t, op.Args[0]), arg[Entry](t, op.Args[1]))
		case "remove":
			got, err = s.Remove(arg[string](t, op.Args[0]), arg[string](t, op.Args[1]))
		case "reorder":
			got, err = s.Reorder(arg[string](t, op.Args[0]), arg[[]string](t, op.Args[1]))
		case "exportAll":
			got = s.ExportAll()
		case "importAll":
			err = s.ImportAll(arg[*Export](t, op.Args[0]))
		default:
			t.Fatalf("unknown op %s", op.Op)
		}
		if err != nil {
			t.Fatalf("op %d %s: %v", i, op.Op, err)
		}
		sameJSON(t, fmt.Sprintf("op %d %s", i, op.Op), got, op.Result)
	}
	file, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "file", json.RawMessage(file), v.File)
	// JSON.stringify(data, null, 2): pretty-printed, no trailing newline.
	if !strings.HasPrefix(string(file), "{\n  \"repos\": {") || strings.HasSuffix(string(file), "\n") {
		t.Errorf("file format:\n%s", file)
	}
}

func TestCorruptFileStartsFresh(t *testing.T) {
	var now int64 = 1
	s, dir := newStore(t, &now)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.List("/r"); len(got) != 0 || got == nil {
		t.Errorf("list = %#v", got)
	}
	if _, err := s.Upsert("/r", Entry{Label: "L"}); err != nil {
		t.Fatal(err)
	}
	if got := s.List("/r"); len(got) != 1 {
		t.Errorf("list = %+v", got)
	}
}

func TestConcurrentUpsertsAllLand(t *testing.T) {
	var now int64 = 1
	s, _ := newStore(t, &now)
	s.newID = randomUUID
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Upsert("/r", Entry{Label: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := s.List("/r"); len(got) != 20 {
		t.Errorf("got %d entries", len(got))
	}
}
