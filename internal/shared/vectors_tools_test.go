package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Replays testdata/vectors/{editors,diffTools,sshKeys}.json, written from the
// TypeScript originals by test/port/tools.test.ts.

func readToolsVectors(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// sameJSON compares got (marshalled by Go) with want (as TS wrote it), both
// normalised through a generic decode so key order and spacing do not matter.
func sameJSON(t *testing.T, label string, got any, want json.RawMessage) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(g, &a)
	_ = json.Unmarshal(want, &b)
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if string(ga) != string(gb) {
		t.Errorf("%s:\n got  %s\n want %s", label, ga, gb)
	}
}

func TestEditorsVectors(t *testing.T) {
	var v struct {
		Presets json.RawMessage `json:"presets"`
		Args    []struct {
			Template string          `json:"template"`
			Vars     EditorVars      `json:"vars"`
			Out      json.RawMessage `json:"out"`
		} `json:"args"`
		SupportsLine []struct {
			Setting EditorSetting   `json:"setting"`
			Out     json.RawMessage `json:"out"`
		} `json:"supportsLine"`
		Launch []struct {
			Setting EditorSetting   `json:"setting"`
			Target  EditorTarget    `json:"target"`
			Out     json.RawMessage `json:"out"`
		} `json:"launch"`
	}
	readToolsVectors(t, "editors.json", &v)
	if len(v.Args) == 0 || len(v.Launch) == 0 || len(v.SupportsLine) == 0 {
		t.Fatal("no vectors")
	}
	sameJSON(t, "presets", EditorPresets, v.Presets)
	for _, c := range v.Args {
		sameJSON(t, "editorArgs("+c.Template+")", EditorArgs(c.Template, c.Vars), c.Out)
	}
	for _, c := range v.SupportsLine {
		sameJSON(t, "supportsLine("+c.Setting.Name+")", SupportsLine(c.Setting), c.Out)
	}
	for _, c := range v.Launch {
		sameJSON(t, "editorLaunch("+c.Setting.Name+", "+c.Target.Path+")", EditorLaunch(c.Setting, c.Target), c.Out)
	}
}

func TestDiffToolsVectors(t *testing.T) {
	var v struct {
		Parse []struct {
			Stdout string          `json:"stdout"`
			Out    json.RawMessage `json:"out"`
		} `json:"parse"`
		Sort []struct {
			Tools []DiffToolInfo  `json:"tools"`
			Out   json.RawMessage `json:"out"`
		} `json:"sort"`
	}
	readToolsVectors(t, "diffTools.json", &v)
	if len(v.Parse) == 0 || len(v.Sort) == 0 {
		t.Fatal("no vectors")
	}
	for i, c := range v.Parse {
		sameJSON(t, "parseToolHelp #"+itoa(i), ParseToolHelp(c.Stdout), c.Out)
	}
	for i, c := range v.Sort {
		sameJSON(t, "sortTools #"+itoa(i), SortTools(c.Tools), c.Out)
	}
}

func TestSshKeysVectors(t *testing.T) {
	var v struct {
		ParseKeygenLine []struct {
			Line string          `json:"line"`
			Out  json.RawMessage `json:"out"`
		} `json:"parseKeygenLine"`
		AgentFingerprints []struct {
			Stdout string          `json:"stdout"`
			Out    json.RawMessage `json:"out"`
		} `json:"agentFingerprints"`
		PublicKeyFiles []struct {
			Entries []string        `json:"entries"`
			Out     json.RawMessage `json:"out"`
		} `json:"publicKeyFiles"`
		PrivateKeyName []struct {
			Name string          `json:"name"`
			Out  json.RawMessage `json:"out"`
		} `json:"privateKeyName"`
		ReadSshTest []struct {
			Output string          `json:"output"`
			Out    json.RawMessage `json:"out"`
		} `json:"readSshTest"`
	}
	readToolsVectors(t, "sshKeys.json", &v)
	if len(v.ParseKeygenLine) == 0 || len(v.ReadSshTest) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.ParseKeygenLine {
		sameJSON(t, "parseKeygenLine("+c.Line+")", ParseKeygenLine(c.Line), c.Out)
	}
	for _, c := range v.AgentFingerprints {
		sameJSON(t, "agentFingerprints("+c.Stdout+")", AgentFingerprints(c.Stdout), c.Out)
	}
	for i, c := range v.PublicKeyFiles {
		sameJSON(t, "publicKeyFiles #"+itoa(i), PublicKeyFiles(c.Entries), c.Out)
	}
	for _, c := range v.PrivateKeyName {
		sameJSON(t, "privateKeyName("+c.Name+")", PrivateKeyName(c.Name), c.Out)
	}
	for _, c := range v.ReadSshTest {
		sameJSON(t, "readSshTest("+c.Output+")", ReadSshTest(c.Output), c.Out)
	}
}
