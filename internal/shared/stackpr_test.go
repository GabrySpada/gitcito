package shared

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/vectors/stackPr.json is written by test/port/hosting.test.ts from
// src/shared/stackPr.ts (buildStackSection supplies the sections).
type stackVectors struct {
	MergeStackSection []struct {
		Body    string `json:"body"`
		Section string `json:"section"`
		Out     string `json:"out"`
	} `json:"mergeStackSection"`
}

func loadStackVectors(t *testing.T) stackVectors {
	t.Helper()
	raw, err := os.ReadFile(vectorPath("stackPr.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v stackVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVectorMergeStackSection(t *testing.T) {
	for _, c := range loadStackVectors(t).MergeStackSection {
		if got := MergeStackSection(c.Body, c.Section); got != c.Out {
			t.Errorf("MergeStackSection(%q, %q):\n got  %q\n want %q", c.Body, c.Section, got, c.Out)
		}
	}
}

// test/pureLogic.test.ts › stackPr: the first two vectors are its sections.
func TestMergeStackSection(t *testing.T) {
	v := loadStackVectors(t).MergeStackSection
	section1, section2 := v[0].Section, v[1].Section

	t.Run("mergeStackSection appends once, then replaces in place", func(t *testing.T) {
		body1 := MergeStackSection("My description.", section1)
		if !strings.Contains(body1, "My description.") || !strings.Contains(body1, "#1") {
			t.Errorf("body1 = %q", body1)
		}
		body2 := MergeStackSection(body1, section2)
		if !strings.Contains(body2, "My description.") || !strings.Contains(body2, "#2") {
			t.Errorf("body2 = %q", body2)
		}
		if n := strings.Count(body2, StackSectionStart); n != 1 {
			t.Errorf("%d stack sections", n)
		}
	})
	t.Run("mergeStackSection on an empty body is just the section", func(t *testing.T) {
		if got := MergeStackSection("", section1); got != section1 {
			t.Errorf("got %q", got)
		}
	})
}
