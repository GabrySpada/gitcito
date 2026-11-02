package aicompleter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GabrySpada/gitcito/internal/ai"
	g "github.com/GabrySpada/gitcito/internal/grounding"
	"github.com/GabrySpada/gitcito/internal/shared"
)

// The action schemas internal/ai builds must be the bytes the TS composed into
// the repository-chat answer schema (captured in testdata/vectors/repoChat.json).
func TestSchemasMatchTypeScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "vectors", "repoChat.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Fn  string          `json:"fn"`
			Out json.RawMessage `json:"out"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		if c.Fn != "actionSchemas" {
			continue
		}
		var want struct{ Ask, AskRemote, RepoChat, RepoChatRemote string }
		if err := json.Unmarshal(c.Out, &want); err != nil {
			t.Fatal(err)
		}
		s := Schemas{}
		for name, pair := range map[string][2]string{
			"ask":            {string(s.AskActionsSchema(false)), want.Ask},
			"askRemote":      {string(s.AskActionsSchema(true)), want.AskRemote},
			"repoChat":       {string(s.RepoChatActionsSchema(false)), want.RepoChat},
			"repoChatRemote": {string(s.RepoChatActionsSchema(true)), want.RepoChatRemote},
		} {
			if pair[0] != pair[1] {
				t.Errorf("%s:\n got  %s\n want %s", name, pair[0], pair[1])
			}
		}
		return
	}
	t.Fatal("no actionSchemas vector")
}

// fakeProvider is an OpenAI-compatible endpoint answering from a queue.
type fakeProvider struct {
	mu      sync.Mutex
	replies []string
	bodies  []string
}

func (f *fakeProvider) serve(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(body))
	reply := f.replies[0]
	f.replies = f.replies[1:]
	f.mu.Unlock()
	out, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}},
	})
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(out)
}

func newCompleter(t *testing.T, replies ...string) (*Completer, *fakeProvider, shared.Object) {
	t.Helper()
	f := &fakeProvider{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	client := ai.NewClient(ai.Options{DataDir: func() (string, error) { return t.TempDir(), nil }})
	cfg := shared.Object{"enabled": true, "provider": "openai", "endpoint": srv.URL, "apiKey": "test-key", "model": "gpt-test"}
	return New(client), f, cfg
}

var schema = g.Raw(g.Obj{
	{K: "type", V: "object"},
	{K: "additionalProperties", V: false},
	{K: "required", V: []string{"paths"}},
	{K: "properties", V: g.Obj{{K: "paths", V: g.Obj{{K: "type", V: "array"}, {K: "maxItems", V: 8}, {K: "items", V: g.Obj{{K: "type", V: "string"}}}}}}},
})

func TestCompleterSendsSchemaBytesAndConvertsValues(t *testing.T) {
	c, f, cfg := newCompleter(t, `{"paths":["a.ts"],"n":1.50}`)
	var seen any
	value, err := c.ChatCompleteJSON(context.Background(), cfg, []g.ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}},
		"repoChatPlan", g.JSONSpec{
			Name:   "repo_chat_context",
			Schema: schema,
			Validate: func(_ context.Context, v any) ([]string, error) {
				seen = v
				return nil, nil
			},
		}, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seen.(map[string]any); !ok {
		t.Fatalf("validator got %T, want map[string]any", seen)
	}
	if m, ok := value.(map[string]any); !ok || m["n"] != json.Number("1.5") {
		t.Fatalf("value = %#v", value)
	}
	if len(f.bodies) != 1 || !strings.Contains(f.bodies[0], `"schema":`+string(schema)) {
		t.Fatalf("request body does not carry the schema bytes:\n%s", f.bodies)
	}
}

func TestCompleterMapsInvalidResponseAndAborts(t *testing.T) {
	c, _, cfg := newCompleter(t, `{"paths":[]}`, `{"paths":[]}`)
	_, err := c.ChatCompleteJSON(context.Background(), cfg, []g.ChatMessage{{Role: "user", Content: "u"}}, "wikiStack",
		g.JSONSpec{Name: "x", Schema: schema, Validate: func(context.Context, any) ([]string, error) {
			return []string{"bad"}, nil
		}}, 0.2)
	if !g.IsInvalidAIResponse(err) || err.Error() != "The AI returned an invalid response: bad" {
		t.Fatalf("err = %v", err)
	}

	c, _, cfg = newCompleter(t, `{"paths":[]}`)
	boom := errors.New("git failed")
	_, err = c.ChatCompleteJSON(context.Background(), cfg, []g.ChatMessage{{Role: "user", Content: "u"}}, "repoChatAnswer",
		g.JSONSpec{Name: "x", Schema: schema, Validate: func(context.Context, any) ([]string, error) {
			return nil, boom
		}}, 0.2)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the validator's error", err)
	}
}
