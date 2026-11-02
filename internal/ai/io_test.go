package ai

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// invokeChannel calls the facade method for channel with JSON arguments, the
// way the bridge does: each argument decoded into the parameter's type, a
// missing trailing argument left at its zero value.
func invokeChannel(t *testing.T, svc *Service, channel string, args []json.RawMessage) callResult {
	t.Helper()
	m := reflect.ValueOf(svc).MethodByName(channelMethod(channel))
	if !m.IsValid() {
		t.Fatalf("no facade method for %s", channel)
	}
	mt := m.Type()
	in := []reflect.Value{reflect.ValueOf(context.Background())}
	for i := 1; i < mt.NumIn(); i++ {
		p := reflect.New(mt.In(i))
		if i-1 < len(args) {
			if err := json.Unmarshal(args[i-1], p.Interface()); err != nil {
				t.Fatalf("%s arg %d: %v", channel, i-1, err)
			}
		}
		in = append(in, p.Elem())
	}
	out := m.Call(in)
	if errV := out[len(out)-1]; !errV.IsNil() {
		msg := errV.Interface().(error).Error()
		return callResult{Error: &msg}
	}
	b, err := json.Marshal(out[0].Interface())
	if err != nil {
		t.Fatal(err)
	}
	return callResult{Value: b}
}

// compareResult checks a Go result against the TS one. Theme channels return
// typed themes, so a key the model added beyond name/light/dark (which TS
// passed through) is not expected.
func compareResult(t *testing.T, label, channel string, got callResult, want json.RawMessage) {
	t.Helper()
	var w callResult
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if w.Error != nil || got.Error != nil {
		if w.Error == nil || got.Error == nil || *w.Error != *got.Error {
			t.Errorf("%s: error %v, want %v (value %s)", label, deref(got.Error), deref(w.Error), got.Value)
		}
		return
	}
	expected := w.Value
	if channel == "ai:generateAppTheme" || channel == "ai:generateCodeTheme" {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(expected, &m)
		for k := range m {
			if k != "name" && k != "light" && k != "dark" {
				delete(m, k)
			}
		}
		expected, _ = json.Marshal(m)
	}
	var a, b any
	_ = json.Unmarshal(got.Value, &a)
	_ = json.Unmarshal(expected, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s:\n got %s\nwant %s", label, got.Value, expected)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func compareRecords(t *testing.T, label string, got, want any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	var a, b any
	_ = json.Unmarshal(g, &a)
	_ = json.Unmarshal(w, &b)
	if a == nil {
		a = []any{}
	}
	if b == nil {
		b = []any{}
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s:\n got %s\nwant %s", label, g, w)
	}
}

// compareRequests checks every request byte-for-byte: URL, method, headers
// (secrets as scheme + hash) and the exact body.
func compareRequests(t *testing.T, label string, got, want []recordedRequest) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d requests, want %d\n got %s\nwant %s", label, len(got), len(want), asJSON(got), asJSON(want))
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Method != w.Method || g.URL != w.URL {
			t.Errorf("%s request %d: %s %s, want %s %s", label, i, g.Method, g.URL, w.Method, w.URL)
		}
		if !reflect.DeepEqual(g.Headers, w.Headers) {
			t.Errorf("%s request %d headers:\n got %v\nwant %v", label, i, g.Headers, w.Headers)
		}
		if deref(g.Body) != deref(w.Body) {
			t.Errorf("%s request %d body:\n got %s\nwant %s", label, i, deref(g.Body), deref(w.Body))
		}
	}
}

type featureVector struct {
	Name      string       `json:"name"`
	StoredKey *string      `json:"storedKey"`
	KeyError  *string      `json:"keyError"`
	Fetch     []fetchReply `json:"fetch"`
	Cli       []cliReply   `json:"cli"`
	Calls     []struct {
		Channel string            `json:"channel"`
		Args    []json.RawMessage `json:"args"`
	} `json:"calls"`
	Requests    []recordedRequest `json:"requests"`
	Procs       []recordedProc    `json:"procs"`
	KeyCalls    []*string         `json:"keyCalls"`
	Usage       []usageCall       `json:"usage"`
	Results     []json.RawMessage `json:"results"`
	UnusedFetch int               `json:"unusedFetch"`
	UnusedCli   int               `json:"unusedCli"`
}

// Replays internal/ai/testdata/features.json: every ai:* channel this package
// serves, through every wire protocol, with the TS request bodies.
func TestFeatureVectors(t *testing.T) {
	var cases []featureVector
	readTestdata(t, "features.json", &cases)
	if len(cases) < 100 {
		t.Fatalf("only %d feature vectors", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			w := newWorld(t)
			w.fetch = append(w.fetch, c.Fetch...)
			w.cli = append(w.cli, c.Cli...)
			w.storedKey = deref0(c.StoredKey)
			w.keyError = deref0(c.KeyError)
			svc := NewService(w.client(t.TempDir(), time.Now))
			for i, call := range c.Calls {
				got := invokeChannel(t, svc, call.Channel, call.Args)
				compareResult(t, "result", call.Channel, got, c.Results[i])
			}
			compareRequests(t, "requests", w.requests, c.Requests)
			compareRecords(t, "procs", w.procs, c.Procs)
			compareRecords(t, "keyCalls", w.keyCalls, c.KeyCalls)
			compareRecords(t, "usage", w.usage, c.Usage)
			if len(w.fetch) != c.UnusedFetch || len(w.cli) != c.UnusedCli {
				t.Errorf("unused replies: fetch %d cli %d, want %d %d", len(w.fetch), len(w.cli), c.UnusedFetch, c.UnusedCli)
			}
		})
	}
}

func deref0(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Replays internal/ai/testdata/transport.json: direct CallModel shapes no
// feature produces (system-only Anthropic prompts, token-limit sanitizing,
// extra fields overriding payload fields in place, CLI argv).
func TestTransportVectors(t *testing.T) {
	var cases []struct {
		Name        string        `json:"name"`
		Cfg         Config        `json:"cfg"`
		Messages    []ChatMessage `json:"messages"`
		Temperature float64       `json:"temperature"`
		Options     *struct {
			Extra           json.RawMessage `json:"extra"`
			MaxTokens       json.RawMessage `json:"maxTokens"`
			DisableThinking bool            `json:"disableThinking"`
		} `json:"options"`
		Fetch    []fetchReply      `json:"fetch"`
		Cli      []cliReply        `json:"cli"`
		Requests []recordedRequest `json:"requests"`
		Procs    []recordedProc    `json:"procs"`
		Result   json.RawMessage   `json:"result"`
	}
	readTestdata(t, "transport.json", &cases)
	if len(cases) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			w := newWorld(t)
			w.fetch = append(w.fetch, c.Fetch...)
			w.cli = append(w.cli, c.Cli...)
			client := w.client(t.TempDir(), time.Now)
			var opts ModelCallOptions
			if c.Options != nil {
				opts.DisableThinking = c.Options.DisableThinking
				if len(c.Options.Extra) > 0 {
					opts.Extra = asObj(decodeJS(t, c.Options.Extra))
				}
				if len(c.Options.MaxTokens) > 0 {
					var f float64
					switch s := strings.Trim(string(c.Options.MaxTokens), `"`); s {
					case "NaN":
						f = math.NaN()
					case "Infinity":
						f = math.Inf(1)
					default:
						_ = json.Unmarshal(c.Options.MaxTokens, &f)
					}
					opts.MaxTokens = &f
				}
			}
			reply, err := client.CallModel(context.Background(), c.Cfg, c.Messages, c.Temperature, opts)
			got := callResult{}
			if err != nil {
				msg := err.Error()
				got.Error = &msg
			} else {
				got.Value, _ = json.Marshal(reply)
			}
			compareResult(t, "result", "", got, c.Result)
			compareRequests(t, "requests", w.requests, c.Requests)
			compareRecords(t, "procs", w.procs, c.Procs)
		})
	}
}

// Replays internal/ai/testdata/models.json: the catalogue, its day-long disk
// cache, and its fallbacks.
func TestModelCatalogVectors(t *testing.T) {
	var scenarios []struct {
		Name      string  `json:"name"`
		CacheFile *string `json:"cacheFile"`
		Steps     []struct {
			At        float64           `json:"at"`
			Cfg       json.RawMessage   `json:"cfg"`
			Force     *bool             `json:"force"`
			StoredKey *string           `json:"storedKey"`
			Fetch     []fetchReply      `json:"fetch"`
			Requests  []recordedRequest `json:"requests"`
			KeyCalls  []*string         `json:"keyCalls"`
			Result    json.RawMessage   `json:"result"`
			CacheFile *string           `json:"cacheFile"`
		} `json:"steps"`
	}
	readTestdata(t, "models.json", &scenarios)
	if len(scenarios) == 0 {
		t.Fatal("no vectors")
	}
	for _, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			dir := t.TempDir()
			cachePath := filepath.Join(dir, ModelsCacheFile)
			if s.CacheFile != nil {
				if err := os.WriteFile(cachePath, []byte(*s.CacheFile), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			w := newWorld(t)
			var now time.Time
			svc := NewService(w.client(dir, func() time.Time { return now }))
			for i, step := range s.Steps {
				now = time.UnixMilli(int64(step.At))
				w.fetch = append([]fetchReply{}, step.Fetch...)
				w.requests = nil
				w.keyCalls = nil
				w.storedKey = deref0(step.StoredKey)
				args := []json.RawMessage{step.Cfg}
				if step.Force != nil {
					b, _ := json.Marshal(*step.Force)
					args = append(args, b)
				}
				got := invokeChannel(t, svc, "ai:listModels", args)
				label := "step " + string(rune('0'+i))
				compareResult(t, label+" result", "ai:listModels", got, step.Result)
				compareRequests(t, label+" requests", w.requests, step.Requests)
				compareRecords(t, label+" keyCalls", w.keyCalls, step.KeyCalls)
				b, err := os.ReadFile(cachePath)
				switch {
				case step.CacheFile == nil && err == nil:
					t.Errorf("%s: cache file written: %s", label, b)
				case step.CacheFile != nil && (err != nil || string(b) != *step.CacheFile):
					t.Errorf("%s: cache file\n got %s (%v)\nwant %s", label, b, err, *step.CacheFile)
				}
			}
		})
	}
}

// Replays internal/ai/testdata/detect.json: agent CLI detection.
func TestDetectCliVectors(t *testing.T) {
	var cases []struct {
		Name   string          `json:"name"`
		Cli    []cliReply      `json:"cli"`
		Procs  []recordedProc  `json:"procs"`
		Result json.RawMessage `json:"result"`
	}
	readTestdata(t, "detect.json", &cases)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			w := newWorld(t)
			w.cli = append(w.cli, c.Cli...)
			svc := NewService(w.client(t.TempDir(), time.Now))
			got := invokeChannel(t, svc, "ai:detectCli", nil)
			compareResult(t, "result", "ai:detectCli", got, c.Result)
			compareRecords(t, "procs", w.procs, c.Procs)
		})
	}
}
