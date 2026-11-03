package repoconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Replays testdata/vectors/repoConfig.json, written from src/shared/repoConfig.ts
// by test/port/registry.test.ts.
type vectorFile struct {
	Parse []struct {
		Raw        string          `json:"raw"`
		Config     json.RawMessage `json:"config"`
		Issues     json.RawMessage `json:"issues"`
		Serialized *string         `json:"serialized"`
		Empty      bool            `json:"empty"`
	} `json:"parse"`
	RelPaths []struct {
		In  string `json:"in"`
		Out bool   `json:"out"`
	} `json:"relPaths"`
	NodeRanges []struct {
		Actual string `json:"actual"`
		Spec   string `json:"spec"`
		Out    bool   `json:"out"`
	} `json:"nodeRanges"`
	Patterns []struct {
		In  string `json:"in"`
		Out bool   `json:"out"`
	} `json:"patterns"`
	LinkURLs []struct {
		In  string `json:"in"`
		Out bool   `json:"out"`
	} `json:"linkUrls"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "repoConfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Parse) == 0 {
		t.Fatal("no vectors")
	}
	return v
}

// canonical re-encodes a JSON document so two spellings of one value compare equal.
func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func TestParseVectors(t *testing.T) {
	for _, c := range loadVectors(t).Parse {
		config, issues := Parse(c.Raw)
		if issues == nil {
			issues = []RepoConfigIssue{}
		}
		gotConfig, _ := json.Marshal(config)
		gotIssues, _ := json.Marshal(issues)
		if canonical(t, gotConfig) != canonical(t, c.Config) {
			t.Errorf("Parse(%q) config = %s, want %s", c.Raw, gotConfig, c.Config)
		}
		if canonical(t, gotIssues) != canonical(t, c.Issues) {
			t.Errorf("Parse(%q) issues = %s, want %s", c.Raw, gotIssues, c.Issues)
		}
		if IsEmpty(config) != c.Empty {
			t.Errorf("IsEmpty(Parse(%q)) = %v, want %v", c.Raw, IsEmpty(config), c.Empty)
		}
		if c.Serialized != nil {
			text, err := Serialize(config)
			if err != nil || text != *c.Serialized {
				t.Errorf("Serialize(Parse(%q)) = %q (%v), want %q", c.Raw, text, err, *c.Serialized)
			}
		}
	}
}

func TestPredicateVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.RelPaths {
		if got := IsSafeRepoRelPath(c.In); got != c.Out {
			t.Errorf("IsSafeRepoRelPath(%q) = %v, want %v", c.In, got, c.Out)
		}
	}
	for _, c := range v.NodeRanges {
		if got := SatisfiesNodeRange(c.Actual, c.Spec); got != c.Out {
			t.Errorf("SatisfiesNodeRange(%q, %q) = %v, want %v", c.Actual, c.Spec, got, c.Out)
		}
	}
	for _, c := range v.Patterns {
		if got := IsSafePattern(c.In); got != c.Out {
			t.Errorf("IsSafePattern(%q) = %v, want %v", c.In, got, c.Out)
		}
	}
	for _, c := range v.LinkURLs {
		if got := IsSafeLinkURL(c.In); got != c.Out {
			t.Errorf("IsSafeLinkURL(%q) = %v, want %v", c.In, got, c.Out)
		}
	}
}
