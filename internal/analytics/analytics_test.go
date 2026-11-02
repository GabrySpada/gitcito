package analytics

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
	_ "time/tzdata" // the vector's zone, on machines without a zoneinfo database

	"github.com/GabrySpada/gitcito/internal/gitsvc"
	"github.com/GabrySpada/gitcito/internal/logbook"
)

// The git service hands its activity to the ledger through this interface.
var _ gitsvc.ActivityRecorder = (*Ledger)(nil)

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

func newLedger(t *testing.T, dir string, now *int64, loc *time.Location) *Ledger {
	return New(Options{
		DataDir:  func() (string, error) { return dir, nil },
		Now:      func() time.Time { return time.UnixMilli(*now) },
		Location: loc,
	})
}

// TestScenarioVector replays internal/analytics/testdata/scenario.json,
// captured from src/main/analytics.ts (its exported recorders and ipcMain
// handlers) with a fixed clock in Europe/Rome — the first scenario crosses
// local midnight on the night DST starts.
func TestScenarioVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "scenario.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		TZ        string `json:"tz"`
		Scenarios []struct {
			Seed json.RawMessage `json:"seed"`
			Ops  []struct {
				Op     string            `json:"op"`
				Args   []json.RawMessage `json:"args"`
				Now    *int64            `json:"now"`
				Result json.RawMessage   `json:"result"`
			} `json:"ops"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(v.TZ)
	if err != nil {
		t.Fatal(err)
	}
	for si, sc := range v.Scenarios {
		t.Run(fmt.Sprint(si), func(t *testing.T) {
			dir := t.TempDir()
			if string(sc.Seed) != "null" {
				if err := os.WriteFile(filepath.Join(dir, FileName), sc.Seed, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var now int64
			l := newLedger(t, dir, &now, loc)
			defer l.Flush()
			for i, op := range sc.Ops {
				if op.Now != nil {
					now = *op.Now
				}
				var got any
				switch op.Op {
				case "get":
					got = l.Get()
				case "clear":
					got = l.Clear()
				case "setRetention":
					got = l.SetRetention(arg[float64](t, op.Args[0]))
				case "recordEvent":
					l.RecordEvent(arg[logbook.ActivityEvent](t, op.Args[0]))
				case "recordAIUsage":
					l.RecordAIUsage(arg[string](t, op.Args[0]), arg[string](t, op.Args[1]), arg[TokenUsage](t, op.Args[2]))
				default:
					t.Fatalf("unknown op %s", op.Op)
				}
				sameJSON(t, fmt.Sprintf("op %d %s", i, op.Op), got, op.Result)
			}
		})
	}
}

func TestFlushWritesPrettyJSONAndReloads(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	l := newLedger(t, dir, &now, time.UTC)
	l.RecordEvent(logbook.EventCommit)
	l.RecordAIUsage("commitMessage", "gpt-4o", TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "{\n  \"since\": ") || strings.HasSuffix(string(raw), "\n") {
		t.Errorf("file:\n%s", raw)
	}
	again := newLedger(t, dir, &now, time.UTC).Get()
	if again.Days[0].Events["commit"] != 1 || again.AIByModel["gpt-4o"].Requests != 1 || again.Since != float64(now) {
		t.Errorf("reloaded = %+v", again)
	}
}

func TestDebouncedSaveLands(t *testing.T) {
	dir := t.TempDir()
	now := int64(1_800_000_000_000)
	l := newLedger(t, dir, &now, time.UTC)
	l.RecordEvent(logbook.EventPush)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("debounced save never happened")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFlushWithoutChangesWritesNothing(t *testing.T) {
	dir := t.TempDir()
	now := int64(1)
	l := newLedger(t, dir, &now, time.UTC)
	_ = l.Get()
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Error("a read alone wrote the file")
	}
}

func TestGetReturnsACopy(t *testing.T) {
	now := int64(1_800_000_000_000)
	l := newLedger(t, t.TempDir(), &now, time.UTC)
	l.RecordEvent(logbook.EventCommit)
	a := l.Get()
	a.Days[0].Events["commit"] = 99
	if l.Get().Days[0].Events["commit"] != 1 {
		t.Error("Get leaked the cache")
	}
	_ = l.Flush()
}

func TestDayKeyOutOfDateRange(t *testing.T) {
	l := New(Options{Location: time.UTC})
	if got := l.dayKey(9e15); got != "NaN-NaN-NaN" {
		t.Errorf("dayKey = %s", got)
	}
	if got := l.dayKey(0); got != "1970-01-01" {
		t.Errorf("dayKey = %s", got)
	}
}

func TestConcurrentRecording(t *testing.T) {
	now := int64(1_800_000_000_000)
	l := newLedger(t, t.TempDir(), &now, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); l.RecordEvent(logbook.EventFetch) }()
		go func() { defer wg.Done(); _ = l.Flush() }()
	}
	wg.Wait()
	if got := l.Get().Days[0].Events["fetch"]; got != 50 {
		t.Errorf("fetch = %v", got)
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
}
