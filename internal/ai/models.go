package ai

// The live model catalogue (src/main/aiModels.ts).
//
// The pickers used to show a list hardcoded at release time, which went stale
// the moment a provider shipped anything — and was simply wrong for a custom
// endpoint or a local Ollama, where the app cannot know what is installed. The
// list is now fetched from the provider, cached on disk for a day, and falls
// back to the bundled one only when the fetch cannot happen (no key yet, no
// network, provider down).
//
// Every picker also accepts free text, so an unlisted model is never a wall.

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/shared"
)

const modelCacheTTL = float64(24 * 60 * 60 * 1000)

// ModelsCacheFile is the catalogue cache, in the data directory.
const ModelsCacheFile = "gitcito-models.json"

// errEmptyModelList is shown to the user verbatim, as in TS.
//
//lint:ignore ST1005 user-facing text, byte-identical to the TS original
var errEmptyModelList = errors.New("The provider returned an empty model list.")

// ModelCatalog mirrors ModelCatalog in src/shared/types.ts.
type ModelCatalog struct {
	// What a picker shows: chat-capable, snapshots collapsed, newest first.
	Models []string `json:"models"`
	// Every id the provider listed, for the picker's "show all" escape.
	AllModels []string `json:"allModels"`
	Source    string   `json:"source"`
	// Epoch ms of the fetch behind this list, or null when it never happened.
	FetchedAt *float64 `json:"fetchedAt"`
	// Why the list is not live. Present with `cache` and `fallback`.
	Error *string `json:"error,omitempty"`
}

// modelCache is the in-memory copy of the cache file. TS kept it in a module
// variable; here it lives on the client, behind a mutex.
type modelCache struct {
	mu     sync.Mutex
	loaded bool
	store  *Obj
}

func (c *Client) cachePath() (string, error) {
	dir, err := c.dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ModelsCacheFile), nil
}

// readCache loads the cache once; a missing or corrupt file is a cold
// catalogue, not an error.
func (c *Client) readCache() *Obj {
	c.models.mu.Lock()
	defer c.models.mu.Unlock()
	if c.models.loaded {
		return c.models.store
	}
	c.models.loaded = true
	c.models.store = &Obj{}
	if p, err := c.cachePath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			if v, err := jsonParse(decodeText(b)); err == nil {
				if o := asObj(v); o != nil {
					c.models.store = o
				}
			}
		}
	}
	return c.models.store
}

// writeCache stores next. A catalogue that cannot be cached still works; it
// just refetches.
func (c *Client) writeCache(next *Obj) {
	c.models.mu.Lock()
	c.models.store = next
	c.models.loaded = true
	c.models.mu.Unlock()
	p, err := c.cachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(jsonStringify(next)), 0o600)
}

// cacheKey identifies one list. The endpoint is part of it because two
// accounts on the same provider can point at different gateways, and a local
// Ollama's list is whatever that machine has pulled.
func cacheKey(cfg Config) string {
	if TransportOf(cfg) == "cli" {
		return "cli:" + cfg.cliBinary()
	}
	return cfg.provider() + ":" + BaseURL(cfg)
}

var isoDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// dateParse is Date.parse for the ISO 8601 forms providers send: a date-only
// string is UTC, a date-time without a zone is local time. V8's legacy
// fallback formats are not reproduced; they read as unparseable.
func dateParse(s string) (float64, bool) {
	if isoDateRE.MatchString(s) {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return 0, false
		}
		return float64(t.UnixMilli()), true
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return float64(t.UnixMilli()), true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return float64(t.UnixMilli()), true
		}
	}
	return 0, false
}

// createdAt is epoch ms from whichever field the provider dates its models
// with (OpenAI-compatible and OpenRouter: `created` seconds; Anthropic:
// `created_at` ISO 8601).
func createdAt(m *Obj) *float64 {
	if created, ok := m.V("created").(float64); ok && created > 0 {
		// Seconds everywhere it appears; milliseconds would be off by a factor
		// of a thousand and sort every model to the top.
		if created < 1e12 {
			created *= 1000
		}
		return &created
	}
	if at := m.V("created_at"); jsTruthy(at) {
		if ms, ok := dateParse(jsString(at)); ok {
			return &ms
		}
	}
	return nil
}

func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// listRequest is where to ask, and with what.
//
// Gemini is the one case worth a detour: chat goes through Google's
// OpenAI-compatible surface, but that surface returns bare ids, while the
// native listing endpoint says which models actually support
// `generateContent`. Listing natively and chatting compatibly gets both.
func listRequest(cfg Config) (string, []header) {
	base := BaseURL(cfg)
	headers := authHeaders(cfg)
	if TransportOf(cfg) == "anthropic" {
		return base + "/v1/models?limit=1000", headers
	}
	if cfg.Provider != nil && *cfg.Provider == "google" {
		native := regexp.MustCompile(`/openai$`).ReplaceAllLiteralString(base, "")
		key := jsTrim(cfg.APIKey)
		// The native API takes the key in its own header, not as a bearer token.
		rest := []header{}
		for _, h := range headers {
			if h.name != "Authorization" {
				rest = append(rest, h)
			}
		}
		if key != "" {
			rest = append(rest, header{"x-goog-api-key", key})
		}
		return native + "/models?pageSize=200", rest
	}
	return base + "/models", headers
}

// fetchModels fetches the provider's list. It fails with a reason the user
// can act on.
func (c *Client) fetchModels(ctx context.Context, cfg Config) (shared.ModelLists, error) {
	if TransportOf(cfg) == "cli" {
		models := CliModels(cfg.cliBinary())
		return shared.ModelLists{Models: models, AllModels: models}, nil
	}
	u, headers := listRequest(cfg)
	res, err := c.t.do(ctx, http.MethodGet, u, headers, nil)
	if err != nil {
		reason := fetchFailureReason(err)
		if reason != "" {
			reason = " " + reason
		}
		hint := " Check your network, endpoint, or proxy/VPN."
		if IsLocal(u) {
			hint = " Is the local provider running?"
		}
		return shared.ModelLists{}, errors.New("Could not reach " + u + "." + reason + hint)
	}
	if !res.ok() {
		return shared.ModelLists{}, errors.New("Could not list models (" + strconv.Itoa(res.status) + "): " + sliceUTF16(res.text(), 160))
	}
	j, err := res.json()
	if err != nil {
		return shared.ModelLists{}, err
	}
	if j == nil {
		return shared.ModelLists{}, errReadingNull("data")
	}
	root := asObj(j)
	list, _ := root.V("data").([]any)
	if root.V("data") == nil {
		list, _ = root.V("models").([]any)
	}
	entries := []shared.ModelEntry{}
	for _, raw := range list {
		m := asObj(raw)
		if m == nil {
			continue
		}
		// Only Gemini's native listing says this; everything else is unfiltered.
		if methods := m.V("supportedGenerationMethods"); jsTruthy(methods) {
			arr, _ := methods.([]any)
			found := false
			for _, x := range arr {
				if x == "generateContent" {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		id, ok := m.V("id").(string)
		if m.V("id") == nil {
			id, ok = m.V("name").(string)
		}
		if !ok {
			id = ""
		}
		entries = append(entries, shared.ModelEntry{ID: id, Created: createdAt(m)})
	}
	return shared.BuildModelLists(entries), nil
}

func stringList(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// fromCache re-shapes a cached list on the way out rather than trusting it as
// written.
//
// A cache from an earlier build holds ids that the current filters exclude,
// and holds no `allModels` at all; re-running the shaping here means an
// upgrade takes effect immediately instead of a day later, when the entry
// expires.
func fromCache(entry *Obj) ModelCatalog {
	models := stringList(entry.V("models"))
	allV := entry.V("allModels")
	all := models
	if allV != nil {
		all = stringList(allV)
	}
	shaped := shared.NormalizeModelIds(models)
	if jsTruthy(allV) {
		shaped = shared.ChatModelsOnly(models)
	}
	fetched, _ := entry.V("fetchedAt").(float64)
	return ModelCatalog{Models: shaped, AllModels: all, Source: "cache", FetchedAt: &fetched}
}

// listAccountModels returns the models an account can use.
//
// It never fails: a provider that cannot be reached yields the bundled list
// with Error set, so a picker always has something in it and the UI can still
// say why the list is not live.
func (c *Client) listAccountModels(ctx context.Context, cfg Config, force bool) ModelCatalog {
	preset := shared.ProviderPreset(cfg.provider())
	key := cacheKey(cfg)
	store := c.readCache()
	c.models.mu.Lock()
	entry := asObj(store.V(key))
	c.models.mu.Unlock()
	now := float64(c.now().UnixMilli())
	entryModels := func() int { a, _ := entry.V("models").([]any); return len(a) }
	if entry != nil {
		fetched, isNum := entry.V("fetchedAt").(float64)
		fresh := isNum && now-fetched < modelCacheTTL
		if !force && fresh && entryModels() > 0 {
			return fromCache(entry)
		}
	}

	lists, err := c.fetchModels(ctx, cfg)
	if err == nil && len(lists.Models) == 0 {
		err = errEmptyModelList
	}
	if err == nil {
		fetchedAt := float64(c.now().UnixMilli())
		c.models.mu.Lock()
		next := c.models.store.Clone()
		c.models.mu.Unlock()
		next.Set(key, newObj("models", anyStrings(lists.Models), "allModels", anyStrings(lists.AllModels), "fetchedAt", fetchedAt))
		c.writeCache(next)
		return ModelCatalog{Models: lists.Models, AllModels: lists.AllModels, Source: "live", FetchedAt: &fetchedAt}
	}
	message := err.Error()
	// A stale cache beats the bundled list: it at least came from this account.
	if entry != nil && entryModels() > 0 {
		out := fromCache(entry)
		out.Error = &message
		return out
	}
	models := append([]string{}, preset.Models...)
	return ModelCatalog{Models: models, AllModels: append([]string{}, models...), Source: "fallback", Error: &message}
}
