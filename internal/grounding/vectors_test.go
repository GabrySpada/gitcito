package grounding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/GabrySpada/gitcito/internal/shared"
)

// Replays testdata/vectors/grounding.json, written by
// test/port/repochat.test.ts from src/main/grounding.ts.

type fnCase struct {
	Fn     string            `json:"fn"`
	Args   []json.RawMessage `json:"args"`
	Out    json.RawMessage   `json:"out"`
	Throws *string           `json:"throws"`
}

func loadCases(t *testing.T, name string) []fnCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var file struct {
		Cases []fnCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(file.Cases) == 0 {
		t.Fatalf("%s has no cases", name)
	}
	return file.Cases
}

func arg[T any](t *testing.T, c fnCase, i int) T {
	t.Helper()
	var v T
	if i < len(c.Args) {
		if err := json.Unmarshal(c.Args[i], &v); err != nil {
			t.Fatalf("%s arg %d: %v", c.Fn, i, err)
		}
	}
	return v
}

// anyArg decodes an argument the way the TS saw it (json.Number numbers).
func anyArg(t *testing.T, c fnCase, i int) any {
	t.Helper()
	v, err := shared.DecodeJSON(c.Args[i])
	if err != nil {
		t.Fatalf("%s arg %d: %v", c.Fn, i, err)
	}
	return v
}

func canon(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func label(i int, c fnCase) string {
	args, _ := json.Marshal(c.Args)
	s := string(args)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return c.Fn + "#" + strconv.Itoa(i) + " " + s
}

func assertCase(t *testing.T, i int, c fnCase, got any) {
	t.Helper()
	g, w := canon(t, got), canon(t, c.Out)
	if !reflect.DeepEqual(g, w) {
		gj, _ := json.Marshal(g)
		wj, _ := json.Marshal(w)
		t.Errorf("%s:\n got  %s\n want %s", label(i, c), gj, wj)
	}
}

func setOf(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

func TestGroundingVectors(t *testing.T) {
	counts := map[string]int{}
	for i, c := range loadCases(t, "grounding.json") {
		counts[c.Fn]++
		var got any
		switch c.Fn {
		case "buildDiffEvidence":
			got = BuildDiffEvidence(arg[string](t, c, 0), arg[DiffEvidenceOpts](t, c, 1))
		case "serializeEvidence":
			got = SerializeEvidence(BuildDiffEvidence(arg[string](t, c, 0), arg[DiffEvidenceOpts](t, c, 1)))
		case "validateReview":
			got = ValidateReview(anyArg(t, c, 0), arg[[]string](t, c, 1))
		case "groundFindings":
			idx := EvidenceIndex(BuildDiffEvidence(arg[string](t, c, 1), DiffEvidenceOpts{}))
			got = GroundFindings(anyArg(t, c, 0).([]any), idx)
		case "renderFindings":
			idx := EvidenceIndex(BuildDiffEvidence(arg[string](t, c, 1), DiffEvidenceOpts{}))
			got = RenderFindings(GroundFindings(anyArg(t, c, 0).([]any), idx), arg[string](t, c, 2))
		case "buildLineWindow":
			got = BuildLineWindow(arg[string](t, c, 0), arg[int](t, c, 1), arg[WindowOpts](t, c, 2))
		case "buildWindowFromLines":
			got = BuildWindowFromLines(arg[[]NumberedLine](t, c, 0), arg[int](t, c, 1), arg[WindowOpts](t, c, 2))
		case "validateHoverExplain":
			got = ValidateHoverExplain(anyArg(t, c, 0), arg[LineWindow](t, c, 1))
		case "validateAskPlan":
			got = ValidateAskPlan(anyArg(t, c, 0), setOf(arg[[]string](t, c, 1)))
		case "validateAskActions":
			got = ValidateAskActions(anyArg(t, c, 0), setOf(arg[[]string](t, c, 1)), arg[AskAllow](t, c, 2))
		case "validateRepoChatActions":
			cx := arg[struct {
				WorkingTreePaths   []string `json:"workingTreePaths"`
				EvidencePaths      []string `json:"evidencePaths"`
				CompletePaths      []string `json:"completePaths"`
				AllowFileActions   *bool    `json:"allowFileActions"`
				AllowRemoteActions bool     `json:"allowRemoteActions"`
			}](t, c, 1)
			got = ValidateRepoChatActions(anyArg(t, c, 0), RepoChatActionContext{
				WorkingTreePaths:   setOf(cx.WorkingTreePaths),
				EvidencePaths:      setOf(cx.EvidencePaths),
				CompletePaths:      setOf(cx.CompletePaths),
				AllowFileActions:   cx.AllowFileActions,
				AllowRemoteActions: cx.AllowRemoteActions,
			})
		case "parseLooseJson":
			v, ok := ParseLooseJSON(arg[string](t, c, 0))
			if ok {
				got = v
			}
		default:
			t.Fatalf("unknown fn %q", c.Fn)
		}
		assertCase(t, i, c, got)
	}
	for _, fn := range []string{"buildDiffEvidence", "validateReview", "groundFindings", "buildLineWindow", "validateHoverExplain", "validateAskPlan", "validateAskActions", "validateRepoChatActions", "parseLooseJson"} {
		if counts[fn] == 0 {
			t.Errorf("no vectors for %s", fn)
		}
	}
}
