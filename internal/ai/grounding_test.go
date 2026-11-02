package ai

import (
	"encoding/json"
	"testing"
)

// Replays testdata/vectors/aiJson.json: parseLooseJson and jsonrepair.
func TestLooseJSONVectors(t *testing.T) {
	var v struct {
		ParseLooseJSON []struct {
			Input string          `json:"input"`
			Out   json.RawMessage `json:"out"`
			JSON  *string         `json:"json"`
		} `json:"parseLooseJson"`
		JSONRepair []struct {
			Input string  `json:"input"`
			Out   *string `json:"out"`
			Error *string `json:"error"`
		} `json:"jsonrepair"`
	}
	readVectors(t, "testdata/vectors/aiJson.json", &v)
	if len(v.ParseLooseJSON) == 0 || len(v.JSONRepair) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.ParseLooseJSON {
		got, ok := ParseLooseJSON(c.Input)
		if c.JSON == nil {
			if ok {
				t.Errorf("parseLooseJson(%q) = %s, want null", c.Input, jsonStringify(got))
			}
			continue
		}
		if !ok {
			t.Errorf("parseLooseJson(%q) = null, want %s", c.Input, *c.JSON)
			continue
		}
		// Byte-identical, key order included: a pass-through result goes on to
		// the renderer exactly as TS serialized it.
		if s := jsonStringify(got); s != *c.JSON {
			t.Errorf("parseLooseJson(%q)\n got %s\nwant %s", c.Input, s, *c.JSON)
		}
	}
	for _, c := range v.JSONRepair {
		got, err := jsonRepair(c.Input)
		switch {
		case c.Error != nil:
			if err == nil || err.Error() != *c.Error {
				t.Errorf("jsonrepair(%q) = %q, %v; want error %q", c.Input, got, err, *c.Error)
			}
		case err != nil:
			t.Errorf("jsonrepair(%q) error %v, want %q", c.Input, err, *c.Out)
		case got != *c.Out:
			t.Errorf("jsonrepair(%q)\n got %q\nwant %q", c.Input, got, *c.Out)
		}
	}
}

// Replays testdata/vectors/aiGrounding.json: the grounding helpers ai.ts calls.
func TestGroundingVectors(t *testing.T) {
	var v struct {
		Evidence []struct {
			Diff string `json:"diff"`
			Opts struct {
				MaxBytes     int `json:"maxBytes"`
				MaxHunks     int `json:"maxHunks"`
				MaxHunkBytes int `json:"maxHunkBytes"`
			} `json:"opts"`
			Set        json.RawMessage `json:"set"`
			Serialized string          `json:"serialized"`
		} `json:"evidence"`
		ValidateReview []struct {
			Input   json.RawMessage `json:"input"`
			Allowed []string        `json:"allowed"`
			Out     []string        `json:"out"`
		} `json:"validateReview"`
		GroundFindings struct {
			Diff string          `json:"diff"`
			Raw  json.RawMessage `json:"raw"`
			Out  json.RawMessage `json:"out"`
		} `json:"groundFindings"`
		RenderFindings struct {
			Risk       string `json:"risk"`
			Suggestion string `json:"suggestion"`
		} `json:"renderFindings"`
		Windows []struct {
			Lines []NumberedLine `json:"lines"`
			Line  float64        `json:"line"`
			Opts  struct {
				Radius   int `json:"radius"`
				MaxBytes int `json:"maxBytes"`
			} `json:"opts"`
			Out json.RawMessage `json:"out"`
		} `json:"windows"`
		ValidateHoverExplain []struct {
			Input json.RawMessage `json:"input"`
			Out   []string        `json:"out"`
		} `json:"validateHoverExplain"`
		HoverWindow LineWindow `json:"hoverWindow"`
		ValidateAsk []struct {
			Input json.RawMessage `json:"input"`
			Known []string        `json:"known"`
			Out   []string        `json:"out"`
		} `json:"validateAskPlan"`
		ValidateActions []struct {
			Input json.RawMessage `json:"input"`
			Known []string        `json:"known"`
			Allow struct {
				History bool `json:"history"`
				Remote  bool `json:"remote"`
			} `json:"allow"`
			Out []string `json:"out"`
		} `json:"validateAskActions"`
	}
	readVectors(t, "testdata/vectors/aiGrounding.json", &v)
	set := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range list {
			m[s] = true
		}
		return m
	}
	for i, c := range v.Evidence {
		got := BuildDiffEvidence(c.Diff, EvidenceOptions(c.Opts))
		if !sameJSON(t, got, c.Set) {
			t.Errorf("evidence[%d]: got %s want %s", i, asJSON(got), c.Set)
		}
		if s := SerializeEvidence(got); s != c.Serialized {
			t.Errorf("serialize[%d]:\n got %q\nwant %q", i, s, c.Serialized)
		}
	}
	for _, c := range v.ValidateReview {
		if got := ValidateReview(decodeJS(t, c.Input), c.Allowed); !equalStrings(got, c.Out) {
			t.Errorf("validateReview(%s) = %q, want %q", c.Input, got, c.Out)
		}
	}
	ev := BuildDiffEvidence(v.GroundFindings.Diff, EvidenceOptions{})
	raw, _ := decodeJS(t, v.GroundFindings.Raw).([]any)
	grounded := GroundFindings(raw, EvidenceIndex(ev))
	if !sameJSON(t, grounded, v.GroundFindings.Out) {
		t.Errorf("groundFindings = %s, want %s", asJSON(grounded), v.GroundFindings.Out)
	}
	if got := RenderFindings(grounded, "risk"); got != v.RenderFindings.Risk {
		t.Errorf("renderFindings(risk) = %q, want %q", got, v.RenderFindings.Risk)
	}
	if got := RenderFindings(grounded, "suggestion"); got != v.RenderFindings.Suggestion {
		t.Errorf("renderFindings(suggestion) = %q, want %q", got, v.RenderFindings.Suggestion)
	}
	for i, c := range v.Windows {
		got := BuildWindowFromLines(c.Lines, c.Line, WindowOptions(c.Opts))
		if !sameJSON(t, got, c.Out) {
			t.Errorf("window[%d] = %s, want %s", i, asJSON(got), c.Out)
		}
	}
	for _, c := range v.ValidateHoverExplain {
		if got := ValidateHoverExplain(decodeJS(t, c.Input), v.HoverWindow); !equalStrings(got, c.Out) {
			t.Errorf("validateHoverExplain(%s) = %q, want %q", c.Input, got, c.Out)
		}
	}
	for _, c := range v.ValidateAsk {
		if got := ValidateAskPlan(decodeJS(t, c.Input), set(c.Known)); !equalStrings(got, c.Out) {
			t.Errorf("validateAskPlan(%s) = %q, want %q", c.Input, got, c.Out)
		}
	}
	for _, c := range v.ValidateActions {
		got := ValidateAskActions(decodeJS(t, c.Input), set(c.Known), ActionAllow(c.Allow))
		if !equalStrings(got, c.Out) {
			t.Errorf("validateAskActions(%s) = %q, want %q", c.Input, got, c.Out)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
