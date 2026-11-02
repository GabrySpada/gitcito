package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Replays testdata/vectors/aiModelNames.json, written from the TypeScript
// original by test/port/ai.test.ts.
func TestAIModelNamesVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "aiModelNames.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		NormalizeModelIds []struct {
			Input []string `json:"input"`
			Out   []string `json:"out"`
		} `json:"normalizeModelIds"`
		ChatModelsOnly []struct {
			Input []string `json:"input"`
			Out   []string `json:"out"`
		} `json:"chatModelsOnly"`
		BuildModelLists []struct {
			Input []ModelEntry `json:"input"`
			Out   ModelLists   `json:"out"`
		} `json:"buildModelLists"`
		BaseModelName []struct {
			Input    string `json:"input"`
			Out      string `json:"out"`
			Snapshot bool   `json:"snapshot"`
		} `json:"baseModelName"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.NormalizeModelIds) == 0 || len(v.BuildModelLists) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.NormalizeModelIds {
		if got := NormalizeModelIds(c.Input); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("normalizeModelIds(%q) = %q, want %q", c.Input, got, c.Out)
		}
	}
	for _, c := range v.ChatModelsOnly {
		if got := ChatModelsOnly(c.Input); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("chatModelsOnly(%q) = %q, want %q", c.Input, got, c.Out)
		}
	}
	for _, c := range v.BuildModelLists {
		if got := BuildModelLists(c.Input); !reflect.DeepEqual(got, c.Out) {
			t.Errorf("buildModelLists(%v) = %q, want %q", c.Input, got, c.Out)
		}
	}
	for _, c := range v.BaseModelName {
		if got := BaseModelName(c.Input); got != c.Out || IsSnapshot(c.Input) != c.Snapshot {
			t.Errorf("baseModelName(%q) = %q/%v, want %q/%v", c.Input, got, IsSnapshot(c.Input), c.Out, c.Snapshot)
		}
	}
}

func created(ms float64) *float64 { return &ms }

func entries(ids ...string) []ModelEntry {
	out := make([]ModelEntry, len(ids))
	for i, id := range ids {
		out[i] = ModelEntry{ID: id}
	}
	return out
}

// Ported from test/pureLogic.test.ts, "AI model names — what a picker should show".
func TestAIModelNames(t *testing.T) {
	eq := func(t *testing.T, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	t.Run("drops the models that cannot answer a chat request", func(t *testing.T) {
		eq(t, NormalizeModelIds([]string{"gpt-4o-mini", "text-embedding-3-small", "whisper-1", "dall-e-3", "omni-moderation-latest", "gpt-4o"}),
			[]string{"gpt-4o", "gpt-4o-mini"})
	})
	t.Run("unqualifies Gemini names and de-duplicates", func(t *testing.T) {
		eq(t, NormalizeModelIds([]string{"models/gemini-2.5-flash", "gemini-2.5-flash", " ", ""}), []string{"gemini-2.5-flash"})
	})
	t.Run("drops completion-era models a chat request cannot use", func(t *testing.T) {
		eq(t, NormalizeModelIds([]string{"gpt-4o", "babbage-002", "davinci-002", "gpt-3.5-turbo-instruct", "gpt-3.5-turbo"}),
			[]string{"gpt-3.5-turbo", "gpt-4o"})
	})
	t.Run("keeps a chat model whose name merely contains a filtered word", func(t *testing.T) {
		eq(t, NormalizeModelIds([]string{"gpt-4o", "nova-adaptive"}), []string{"gpt-4o", "nova-adaptive"})
	})
	t.Run("collapses dated snapshots into the model they are a snapshot of", func(t *testing.T) {
		l := BuildModelLists(entries("gpt-4o", "gpt-4o-2024-05-13", "gpt-4o-2024-08-06", "gpt-4o-2024-11-20",
			"gpt-4o-mini", "gpt-4o-mini-2024-07-18", "gpt-3.5-turbo", "gpt-3.5-turbo-0125"))
		eq(t, l.Models, []string{"gpt-3.5-turbo", "gpt-4o", "gpt-4o-mini"})
		if len(l.AllModels) != 8 {
			t.Errorf("allModels has %d entries, want 8", len(l.AllModels))
		}
	})
	t.Run("keeps the newest snapshot when no undated id exists", func(t *testing.T) {
		l := BuildModelLists([]ModelEntry{
			{ID: "claude-haiku-4-5-20251001", Created: created(3)},
			{ID: "claude-haiku-4-5-20250101", Created: created(1)},
			{ID: "claude-opus-5-20260401", Created: created(4)},
		})
		eq(t, l.Models, []string{"claude-opus-5-20260401", "claude-haiku-4-5-20251001"})
	})
	t.Run("leaves a number that is not a release stamp alone", func(t *testing.T) {
		l := BuildModelLists(entries("mixtral-8x7b-32768", "gpt-3.5-turbo-16k", "llama-3.1-8b-instant"))
		eq(t, l.Models, []string{"gpt-3.5-turbo-16k", "llama-3.1-8b-instant", "mixtral-8x7b-32768"})
	})
	t.Run("orders by release date, newest first, before falling back to name order", func(t *testing.T) {
		l := BuildModelLists([]ModelEntry{
			{ID: "old-model", Created: created(1_600_000_000_000)},
			{ID: "new-model", Created: created(1_700_000_000_000)},
			{ID: "undated-b"},
			{ID: "undated-a"},
		})
		eq(t, l.Models, []string{"new-model", "old-model", "undated-a", "undated-b"})
	})
	t.Run("keeps everything rather than showing an empty picker", func(t *testing.T) {
		eq(t, NormalizeModelIds([]string{"my-embed-model"}), []string{"my-embed-model"})
	})
}
