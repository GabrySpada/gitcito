package wiki

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GabrySpada/gitcito/internal/gitsvc"
	"github.com/GabrySpada/gitcito/internal/grounding/porttest"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// Replays internal/wiki/testdata/payloads.json, written by
// test/port/repochat.test.ts through registerWikiHandlers: the requests each
// wiki channel sends the model, the wiki:progress events, the return values,
// the store file and the exported Markdown.

type recorder struct {
	mu     sync.Mutex
	events []map[string]any
}

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, map[string]any{"channel": name, "payload": data})
}

type wikiPayloads struct {
	GitEnv map[string]string `json:"gitEnv"`
	Recipe []porttest.Step   `json:"recipe"`
	Now    int64             `json:"now"`
	Steps  []struct {
		Channel  string                       `json:"channel"`
		Args     []json.RawMessage            `json:"args"`
		Replies  map[string][]json.RawMessage `json:"replies"`
		Calls    json.RawMessage              `json:"calls"`
		Events   json.RawMessage              `json:"events"`
		Result   json.RawMessage              `json:"result"`
		Error    *string                      `json:"error"`
		Exported json.RawMessage              `json:"exported"`
		Stored   json.RawMessage              `json:"stored"`
	} `json:"steps"`
}

// orderEvents is orderWikiEvents in the TS: page events sorted by slug without
// their running count, the counts kept as a sorted list.
func orderEvents(events []any) map[string]any {
	isPage := func(e any) bool {
		p := e.(map[string]any)["payload"].(map[string]any)["progress"].(map[string]any)
		return p["phase"] == "page"
	}
	var pages []map[string]any
	done := []any{}
	for _, e := range events {
		if !isPage(e) {
			continue
		}
		m := e.(map[string]any)
		payload := m["payload"].(map[string]any)
		progress := map[string]any{}
		for k, v := range payload["progress"].(map[string]any) {
			if k == "done" {
				done = append(done, v)
				continue
			}
			progress[k] = v
		}
		pages = append(pages, map[string]any{"channel": m["channel"], "payload": map[string]any{"repoPath": payload["repoPath"], "progress": progress}})
	}
	sort.SliceStable(pages, func(i, j int) bool {
		return pages[i]["payload"].(map[string]any)["progress"].(map[string]any)["slug"].(string) <
			pages[j]["payload"].(map[string]any)["progress"].(map[string]any)["slug"].(string)
	})
	sort.Slice(done, func(i, j int) bool { return done[i].(float64) < done[j].(float64) })
	seq := []any{}
	next := 0
	for _, e := range events {
		if isPage(e) {
			seq = append(seq, pages[next])
			next++
		} else {
			seq = append(seq, e)
		}
	}
	return map[string]any{"sequence": seq, "done": done}
}

// orderCalls sorts the wikiPage calls between the plan and the stack by their
// page line, as the TS capture did.
func orderCalls(calls []any) []any {
	key := func(c any) string {
		msgs := c.(map[string]any)["messages"].([]any)
		if len(msgs) < 2 {
			return ""
		}
		return strings.SplitN(msgs[1].(map[string]any)["content"].(string), "\n", 2)[0]
	}
	var pages []any
	for _, c := range calls {
		if c.(map[string]any)["feature"] == "wikiPage" {
			pages = append(pages, c)
		}
	}
	sort.SliceStable(pages, func(i, j int) bool { return key(pages[i]) < key(pages[j]) })
	out := []any{}
	next := 0
	for _, c := range calls {
		if c.(map[string]any)["feature"] == "wikiPage" {
			out = append(out, pages[next])
			next++
		} else {
			out = append(out, c)
		}
	}
	return out
}

func TestWikiPayloads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "payloads.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file wikiPayloads
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	b := porttest.BuildRepo(t, file.Recipe, file.GitEnv)
	empty := filepath.Join(b.Tmp, "empty-repo")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", empty, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	store := filepath.Join(b.Tmp, "userData", FileName)
	unbind := func(v any) any {
		rawV, _ := json.Marshal(v)
		s := strings.ReplaceAll(b.Unbind(t, string(rawV)), "@TMP/empty-repo", "@EMPTY")
		var out any
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	plain := func(r json.RawMessage) any {
		var out any
		if len(r) == 0 {
			return nil
		}
		if err := json.Unmarshal(r, &out); err != nil {
			t.Fatal(err)
		}
		return unbind(out)
	}
	check := func(step int, what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			gj, _ := json.MarshalIndent(got, "", " ")
			wj, _ := json.MarshalIndent(want, "", " ")
			t.Errorf("step %d (%s) %s:\n got  %s\n want %s", step, file.Steps[step].Channel, what, gj, wj)
		}
	}

	git := gitsvc.New(nil, nil)
	for i, step := range file.Steps {
		replies := map[string][]json.RawMessage{}
		for k, list := range step.Replies {
			replies[k] = append(replies[k], list...)
		}
		model := porttest.NewModel(replies)
		rec := &recorder{}
		w := New(git, model, rec, store, func() time.Time { return time.UnixMilli(file.Now) })
		args := make([]any, len(step.Args))
		for j, a := range step.Args {
			bound := strings.ReplaceAll(string(b.Bind(t, a)), "@EMPTY", empty)
			v, err := shared.DecodeJSON([]byte(bound))
			if err != nil {
				t.Fatal(err)
			}
			args[j] = v
		}
		repoPath, _ := args[0].(string)
		ctx := context.Background()
		var result any
		var callErr error
		switch step.Channel {
		case "wiki:facts":
			result = w.Facts(ctx, repoPath, w.candidateFiles(ctx, repoPath))
		case "wiki:imports":
			depth, _ := args[1].(json.Number).Int64()
			result = w.ImportGraph(ctx, repoPath, int(depth))
		case "wiki:get":
			result = w.Get(ctx, repoPath, args[1].(string))
		case "wiki:generate":
			var wiki *RepoWiki
			wiki, callErr = w.Generate(ctx, repoPath, args[1].(map[string]any))
			if wiki != nil {
				result = wiki
			}
		case "wiki:export":
			var paths []string
			paths, callErr = w.Export(repoPath)
			if paths != nil {
				result = paths
			}
		case "wiki:clear":
			callErr = w.Clear(repoPath)
		default:
			t.Fatalf("unknown channel %s", step.Channel)
		}

		if step.Error != nil {
			if callErr == nil || unbind(callErr.Error()) != *step.Error {
				t.Errorf("step %d (%s): error = %v, want %q", i, step.Channel, callErr, *step.Error)
			}
		} else if callErr != nil {
			t.Errorf("step %d (%s): unexpected error %v", i, step.Channel, callErr)
		}
		check(i, "result", unbind(result), plain(step.Result))

		calls := []any{}
		if len(model.Calls) > 0 {
			calls = unbind(model.Calls).([]any)
		}
		want, _ := plain(step.Calls).([]any)
		if want == nil {
			want = []any{}
		}
		check(i, "calls", orderCalls(calls), want)

		evs := []any{}
		if rec.events != nil {
			evs = unbind(rec.events).([]any)
		}
		check(i, "events", unbind(orderEvents(evs)), plain(step.Events))

		exported := map[string]any{}
		if paths, ok := result.([]string); ok && step.Channel == "wiki:export" {
			for _, p := range paths {
				content, err := os.ReadFile(filepath.Join(b.Repo, p))
				if err != nil {
					t.Fatal(err)
				}
				exported[p] = string(content)
			}
		}
		check(i, "exported", unbind(exported), plain(step.Exported))

		var stored any
		if rawStore, err := os.ReadFile(store); err == nil {
			if err := json.Unmarshal(rawStore, &stored); err != nil {
				t.Fatal(err)
			}
			stored = unbind(stored)
		}
		check(i, "stored", stored, plain(step.Stored))
	}
}
