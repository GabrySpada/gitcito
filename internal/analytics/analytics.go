// Package analytics is the port of src/main/analytics.ts: the persisted,
// machine-local activity ledger — a per-day timeline of user-driven git
// activity plus lifetime AI token totals (by feature and by model). Stored as
// plain JSON in the data dir (gitcito-analytics.json); never exported, never
// sent anywhere.
//
// The git service feeds it through gitsvc.WithActivityRecorder (Ledger
// implements gitsvc.ActivityRecorder); the AI features call RecordAIUsage.
package analytics

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GabrySpada/gitcito/internal/logbook"
	"github.com/GabrySpada/gitcito/internal/paths"
)

// FileName is the ledger file inside the data dir.
const FileName = "gitcito-analytics.json"

// saveDelay debounces writes, as the TS did.
const saveDelay = 400 * time.Millisecond

const dayMs = 86_400_000

// AIUsageStat is AIUsageStat: accumulated token counts and estimated cost.
// Every field is a JS number.
type AIUsageStat struct {
	Requests         float64 `json:"requests"`
	PromptTokens     float64 `json:"promptTokens"`
	CompletionTokens float64 `json:"completionTokens"`
	TotalTokens      float64 `json:"totalTokens"`
	// Cost is estimated USD from the built-in price table; 0 if the model is unknown.
	Cost float64 `json:"cost"`
}

// DayBucket is DayBucket: one local calendar day of recorded activity.
type DayBucket struct {
	// Date is 'YYYY-MM-DD' in local time.
	Date   string             `json:"date"`
	Events map[string]float64 `json:"events"`
	AI     AIUsageStat        `json:"ai"`
}

// Analytics is Analytics: the whole ledger.
type Analytics struct {
	// Since is Unix ms of the first recorded entry, 0 when nothing yet.
	Since float64 `json:"since"`
	// RetentionDays is the history kept; 0 = forever. Older buckets are pruned.
	RetentionDays float64                `json:"retentionDays"`
	Days          []DayBucket            `json:"days"`
	AITotal       AIUsageStat            `json:"aiTotal"`
	AIByFeature   map[string]AIUsageStat `json:"aiByFeature"`
	AIByModel     map[string]AIUsageStat `json:"aiByModel"`
}

func emptyAnalytics() *Analytics {
	return &Analytics{Days: []DayBucket{}, AIByFeature: map[string]AIUsageStat{}, AIByModel: map[string]AIUsageStat{}}
}

// clone deep-copies the ledger so callers never share the cache.
func (a *Analytics) clone() Analytics {
	out := *a
	out.Days = make([]DayBucket, len(a.Days))
	for i, d := range a.Days {
		ev := make(map[string]float64, len(d.Events))
		for k, v := range d.Events {
			ev[k] = v
		}
		d.Events = ev
		out.Days[i] = d
	}
	out.AIByFeature = make(map[string]AIUsageStat, len(a.AIByFeature))
	for k, v := range a.AIByFeature {
		out.AIByFeature[k] = v
	}
	out.AIByModel = make(map[string]AIUsageStat, len(a.AIByModel))
	for k, v := range a.AIByModel {
		out.AIByModel[k] = v
	}
	return out
}

// TokenUsage is the token counts pulled from a single OpenAI-compatible API
// response.
type TokenUsage struct {
	PromptTokens     float64 `json:"promptTokens"`
	CompletionTokens float64 `json:"completionTokens"`
	TotalTokens      float64 `json:"totalTokens"`
}

// Options configures a Ledger. Zero values pick the production behaviour.
type Options struct {
	// DataDir defaults to paths.DataDir.
	DataDir func() (string, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// Location is the zone days are bucketed in; defaults to time.Local.
	Location *time.Location
}

// Ledger holds the analytics in memory and persists them on a debounce.
// Safe for concurrent use.
type Ledger struct {
	dataDir func() (string, error)
	now     func() time.Time
	loc     *time.Location

	// writeMu serialises Flush, so an older snapshot never lands after a newer one.
	writeMu sync.Mutex

	mu    sync.Mutex
	cache *Analytics
	timer *time.Timer
	gen   uint64 // bumps on every change; a save writes only the newest snapshot
	saved uint64
}

// New builds a Ledger. Nothing is read until first use.
func New(opts Options) *Ledger {
	l := &Ledger{dataDir: opts.DataDir, now: opts.Now, loc: opts.Location}
	if l.dataDir == nil {
		l.dataDir = paths.DataDir
	}
	if l.now == nil {
		l.now = time.Now
	}
	if l.loc == nil {
		l.loc = time.Local
	}
	return l
}

func (l *Ledger) nowMs() float64 { return float64(l.now().UnixMilli()) }

// load returns the cache, reading the file the first time. Callers hold l.mu.
func (l *Ledger) load() *Analytics {
	if l.cache != nil {
		return l.cache
	}
	a := emptyAnalytics()
	if dir, err := l.dataDir(); err == nil {
		if raw, err := os.ReadFile(filepath.Join(dir, FileName)); err == nil {
			// `{ ...emptyAnalytics(), ...JSON.parse(raw) }`: absent fields keep
			// their empty value.
			parsed := emptyAnalytics()
			if json.Unmarshal(raw, parsed) == nil {
				a = parsed
			}
		}
	}
	if a.Days == nil {
		a.Days = []DayBucket{}
	}
	for i := range a.Days {
		if a.Days[i].Events == nil {
			a.Days[i].Events = map[string]float64{}
		}
	}
	if a.AIByFeature == nil {
		a.AIByFeature = map[string]AIUsageStat{}
	}
	if a.AIByModel == nil {
		a.AIByModel = map[string]AIUsageStat{}
	}
	l.cache = a
	l.prune(a)
	return a
}

// dayKey is the local-time 'YYYY-MM-DD' for an epoch-ms. Outside the range a
// JS Date can hold it is "NaN-NaN-NaN", as `new Date(ms)` makes it.
func (l *Ledger) dayKey(ms float64) string {
	if math.IsNaN(ms) || math.Abs(ms) > 8.64e15 {
		return "NaN-NaN-NaN"
	}
	t := time.UnixMilli(int64(ms)).In(l.loc)
	return strconv.Itoa(t.Year()) + "-" + pad2(int(t.Month())) + "-" + pad2(t.Day())
}

func pad2(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// prune drops buckets older than the retention window (0 = keep forever).
func (l *Ledger) prune(a *Analytics) {
	if a.RetentionDays <= 0 || math.IsNaN(a.RetentionDays) {
		return
	}
	cutoff := l.dayKey(l.nowMs() - a.RetentionDays*dayMs)
	kept := []DayBucket{}
	for _, b := range a.Days {
		if b.Date >= cutoff {
			kept = append(kept, b)
		}
	}
	a.Days = kept
}

func (l *Ledger) todayBucket(a *Analytics) *DayBucket {
	key := l.dayKey(l.nowMs())
	for i := range a.Days {
		if a.Days[i].Date == key {
			return &a.Days[i]
		}
	}
	a.Days = append(a.Days, DayBucket{Date: key, Events: map[string]float64{}})
	// localeCompare on 'YYYY-MM-DD' keys orders them like plain comparison.
	sort.SliceStable(a.Days, func(i, j int) bool { return a.Days[i].Date < a.Days[j].Date })
	for i := range a.Days {
		if a.Days[i].Date == key {
			return &a.Days[i]
		}
	}
	return nil // unreachable
}

// pricing is rough public list prices, USD per 1M tokens [input, output].
// Matched by substring on the model id (first hit wins, so order specific →
// generic). Unknown models cost 0 — tokens are still tracked, only the $
// estimate is omitted.
var pricing = []struct {
	key           string
	input, output float64
}{
	{"gpt-4o-mini", 0.15, 0.6},
	{"gpt-4o", 2.5, 10},
	{"gpt-4.1-mini", 0.4, 1.6},
	{"gpt-4.1", 2, 8},
	{"claude-3-5-haiku", 0.8, 4},
	{"claude-3-5-sonnet", 3, 15},
	{"claude-3-7-sonnet", 3, 15},
	{"llama-3.3-70b", 0.59, 0.79},
	{"llama-3.1-8b", 0.05, 0.08},
	{"mixtral-8x7b", 0.24, 0.24},
	{"mistral-small", 0.2, 0.6},
	{"mistral-medium", 0.4, 2},
	{"mistral-large", 2, 6},
	{"codestral", 0.3, 0.9},
}

func estimateCost(model string, prompt, completion float64) float64 {
	m := strings.ToLower(model)
	for _, p := range pricing {
		if strings.Contains(m, p.key) {
			return (prompt*p.input + completion*p.output) / 1_000_000
		}
	}
	return 0
}

func addUsage(stat *AIUsageStat, u TokenUsage, cost float64) {
	stat.Requests++
	stat.PromptTokens += u.PromptTokens
	stat.CompletionTokens += u.CompletionTokens
	stat.TotalTokens += u.TotalTokens
	stat.Cost += cost
}

// RecordEvent records one user-driven activity event into today's bucket. It
// is gitsvc.ActivityRecorder.
func (l *Ledger) RecordEvent(event logbook.ActivityEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.load()
	if a.Since == 0 {
		a.Since = l.nowMs()
	}
	b := l.todayBucket(a)
	b.Events[string(event)]++
	l.scheduleSave()
}

// RecordAIUsage folds one API response's token usage into the ledger
// (lifetime totals, per feature, per model, and today's bucket). A response
// with no positive count is ignored.
func (l *Ledger) RecordAIUsage(feature, model string, u TokenUsage) {
	if u.PromptTokens <= 0 && u.CompletionTokens <= 0 && u.TotalTokens <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.load()
	if a.Since == 0 {
		a.Since = l.nowMs()
	}
	cost := estimateCost(model, u.PromptTokens, u.CompletionTokens)
	addUsage(&a.AITotal, u, cost)
	f := a.AIByFeature[feature]
	addUsage(&f, u, cost)
	a.AIByFeature[feature] = f
	m := a.AIByModel[model]
	addUsage(&m, u, cost)
	a.AIByModel[model] = m
	addUsage(&l.todayBucket(a).AI, u, cost)
	l.scheduleSave()
}

// Get is `analytics:get`.
func (l *Ledger) Get() Analytics {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.load().clone()
}

// Clear is `analytics:clear`: an empty ledger that keeps the retention setting.
// Like the TS, it reads the retention from memory only — cleared before
// anything loaded the file, the retention on disk is reset too.
func (l *Ledger) Clear() Analytics {
	l.mu.Lock()
	defer l.mu.Unlock()
	retention := 0.0
	if l.cache != nil {
		retention = l.cache.RetentionDays
	}
	l.cache = emptyAnalytics()
	l.cache.RetentionDays = retention
	l.scheduleSave()
	return l.cache.clone()
}

// SetRetention is `analytics:setRetention`: `Math.max(0, Math.floor(days) || 0)`
// days, pruning right away.
func (l *Ledger) SetRetention(days float64) Analytics {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.load()
	d := math.Floor(days)
	if math.IsNaN(d) || d < 0 {
		d = 0
	}
	a.RetentionDays = d
	l.prune(a)
	l.scheduleSave()
	return a.clone()
}

// scheduleSave restarts the debounce timer. Callers hold l.mu.
func (l *Ledger) scheduleSave() {
	l.gen++
	if l.timer != nil {
		l.timer.Stop()
	}
	l.timer = time.AfterFunc(saveDelay, func() { _ = l.Flush() })
}

// Flush writes the ledger now, cancelling any pending debounced save. Call it
// on shutdown so the last 400ms of activity are not lost.
func (l *Ledger) Flush() error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.mu.Lock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	if l.cache == nil || l.gen == l.saved {
		l.mu.Unlock()
		return nil
	}
	snapshot := l.cache.clone()
	gen := l.gen
	l.mu.Unlock()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snapshot); err != nil {
		return err
	}
	dir, err := l.dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, FileName), bytes.TrimSuffix(buf.Bytes(), []byte("\n"))); err != nil {
		return err
	}
	l.mu.Lock()
	l.saved = gen
	l.mu.Unlock()
	return nil
}

func writeFileAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
