package shared

// Go port of src/shared/aiModelNames.ts: turning a provider's raw model list
// into one worth showing. Vector-tested against the TS through
// testdata/vectors/aiModelNames.json.
//
// No provider publishes a curated or ranked list — `/v1/models` is a flat
// inventory. OpenAI's is around ninety entries, most of them dated snapshots of
// a handful of actual models, and sorting it by name buries `gpt-4o` under
// `babbage-002`. So the shaping happens here:
//
//  1. Models that cannot answer a chat request are dropped.
//  2. Dated snapshots collapse into the model they are a snapshot of.
//  3. What remains is ordered newest first, which is far closer to "the ones
//     you want" than alphabetical.
//
// Every step is a heuristic over names, and each is reversible from the UI:
// the full list is one click away, and any model can be typed by hand.

import (
	"regexp"
	"sort"
	"strings"
)

// ModelEntry is one model as the provider described it.
type ModelEntry struct {
	ID string `json:"id"`
	// Epoch ms, when the provider dates its models.
	Created *float64 `json:"created,omitempty"`
}

var nonChatRE = regexp.MustCompile(`(embed|whisper|tts|audio|speech|dall-?e|image|moderation|rerank|guard|transcribe|realtime|sora|codestral-embed)`)

// Completion-era models a chat request cannot use at all. Matched separately
// because these are whole names rather than a substring anyone would search
// for — `babbage-002` and `gpt-3.5-turbo-instruct` still show up in OpenAI's
// `/models`, and they 400 the moment they are handed a message list.
var completionOnlyRE = regexp.MustCompile(`(^|[/-])(babbage|davinci|ada|curie)([-.]|$)|-instruct($|[-.])`)

// asciiFold lowers A–Z only: a non-unicode JS regexp with the i flag matches
// an ASCII pattern exactly that way (RE2's (?i) would also fold ſ and K).
func asciiFold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func isChatModel(id string) bool {
	f := asciiFold(id)
	return !nonChatRE.MatchString(f) && !completionOnlyRE.MatchString(f)
}

// ChatModelsOnly drops ids that cannot answer a chat request — unless that
// would drop everything, since the filter is a heuristic.
func ChatModelsOnly(ids []string) []string {
	filtered := []string{}
	for _, id := range ids {
		if isChatModel(id) {
			filtered = append(filtered, id)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return append([]string{}, ids...)
}

// A trailing release stamp: `-2024-05-13`, `-20251001`, `-0125`. Deliberately
// anchored and exact-width, so `mixtral-8x7b-32768` (a context size) and
// `gpt-3.5-turbo-16k` are left alone.
var snapshotRE = regexp.MustCompile(`-(\d{4}-\d{2}-\d{2}|\d{8}|\d{4})$`)

// BaseModelName is the model a dated id is a snapshot of, or the id itself.
func BaseModelName(id string) string { return snapshotRE.ReplaceAllLiteralString(id, "") }

// IsSnapshot reports whether id ends in a release stamp.
func IsSnapshot(id string) bool { return snapshotRE.MatchString(id) }

// CollapseSnapshots drops dated snapshots that add nothing.
//
// A snapshot whose base model is also listed is redundant — `gpt-4o` covers
// `gpt-4o-2024-08-06`. Where only snapshots exist (Anthropic dates every id),
// the newest of each family is kept, so the model does not vanish.
func CollapseSnapshots(entries []ModelEntry) []ModelEntry {
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.ID] = true
	}
	best := map[string]ModelEntry{}
	for _, e := range entries {
		if !IsSnapshot(e.ID) {
			continue
		}
		base := BaseModelName(e.ID)
		if listed[base] {
			continue // the undated id already covers it
		}
		if cur, ok := best[base]; !ok || newerThan(e, cur) {
			best[base] = e
		}
	}
	keep := map[string]bool{}
	for _, e := range best {
		keep[e.ID] = true
	}
	out := []ModelEntry{}
	for _, e := range entries {
		if !IsSnapshot(e.ID) || keep[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// newerThan: later `created` wins; with no dates, the higher-sorting id does.
func newerThan(a, b ModelEntry) bool {
	switch {
	case a.Created != nil && b.Created != nil:
		return *a.Created > *b.Created
	case a.Created != nil:
		return true
	case b.Created != nil:
		return false
	}
	return LocaleCompare(a.ID, b.ID) > 0
}

// OrderModels is newest first, with undated models after the dated ones in
// name order.
//
// Providers that date their models (OpenAI, OpenRouter, Anthropic) get a list
// whose top is the current generation. Ones that do not (Ollama tags, most
// custom gateways) fall back to alphabetical, which is what they had before.
func OrderModels(entries []ModelEntry) []ModelEntry {
	out := append([]ModelEntry{}, entries...)
	// Array.prototype.sort is stable, so is this.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Created != nil && b.Created != nil && *a.Created != *b.Created {
			return *b.Created < *a.Created
		}
		if a.Created != nil && b.Created == nil {
			return true
		}
		if a.Created == nil && b.Created != nil {
			return false
		}
		return LocaleCompare(a.ID, b.ID) < 0
	})
	return out
}

// NormalizeEntries trims, unqualifies and de-duplicates ids, keeping the
// first date seen.
func NormalizeEntries(entries []ModelEntry) []ModelEntry {
	seen := map[string]bool{}
	out := []ModelEntry{}
	for _, e := range entries {
		// Gemini returns fully-qualified names like `models/gemini-2.5-flash`.
		id := strings.TrimPrefix(JSTrim(e.ID), "models/")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ModelEntry{ID: id, Created: e.Created})
	}
	return out
}

func ids(entries []ModelEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func chatOnly(entries []ModelEntry) []ModelEntry {
	chat := map[string]bool{}
	for _, id := range ChatModelsOnly(ids(entries)) {
		chat[id] = true
	}
	out := []ModelEntry{}
	for _, e := range entries {
		if chat[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// ModelLists are the two lists a picker needs: the shaped one it shows, and
// everything the provider returned for when the shaping hid something wanted.
type ModelLists struct {
	Models    []string `json:"models"`
	AllModels []string `json:"allModels"`
}

// BuildModelLists shapes a provider's raw list.
func BuildModelLists(raw []ModelEntry) ModelLists {
	entries := NormalizeEntries(raw)
	return ModelLists{
		Models: ids(OrderModels(CollapseSnapshots(chatOnly(entries)))),
		// "All" still means every id the provider listed, snapshots included.
		AllModels: ids(OrderModels(entries)),
	}
}

// NormalizeModelIds is the legacy shape: ids only, no dates. Used for cached
// lists written earlier.
func NormalizeModelIds(list []string) []string {
	entries := make([]ModelEntry, len(list))
	for i, id := range list {
		entries[i] = ModelEntry{ID: id}
	}
	return ids(OrderModels(CollapseSnapshots(chatOnly(NormalizeEntries(entries)))))
}
