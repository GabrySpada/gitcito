package secure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// The other direction of the compatibility contract: bundles written by Go,
// committed under testdata/vectors/secure-go-produced/, which the TypeScript
// opens in test/port/secure.test.ts ("TS opens Go-produced bundles").
//
// PORT_WRITE_VECTORS=1 regenerates them; otherwise this test re-packs each one
// and requires the committed bytes, and requires Go to read them back as
// expected.json says.

type goProducedCase struct {
	name     string
	project  string
	password string
	sections func(t *testing.T) []Section
}

func str(s string) *string { return &s }

func mustParse(t *testing.T, raw string) []any {
	t.Helper()
	v, err := parseJS([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.([]any)
}

var goProducedCases = []goProducedCase{
	{
		name:     "go-v2-repo-vault",
		project:  "go-project",
		password: "correct horse battery",
		sections: func(t *testing.T) []Section {
			return []Section{
				{Kind: KindRepo, Project: "api", Folder: "api", Remote: str("git@github.com:acme/api.git"), Files: []BundleFile{
					{Path: ".env", Content: []byte("API_KEY=sk-go-1\n")},
					{Path: "bin/run.sh", Content: []byte("#!/bin/sh\necho go\n"), Executable: true},
					{Path: "certs/key.pem", Content: []byte{0, 1, 2, 0xfa, 0xfb, 0xfc, 0xff}},
				}},
				{Kind: KindVault, Entries: []BundleVaultEntry{
					{Key: "STRIPE", Value: "sk_live_go", Note: str("prod")},
					{Key: "SENTRY", Value: "https://dsn"},
				}},
			}
		},
	},
	{
		name:     "go-v3-workspace-notes",
		project:  "Team",
		password: "pässwörd 🔑",
		sections: func(t *testing.T) []Section {
			return []Section{
				{Kind: KindWorkspace, Name: "Team 🧪", Tabs: mustParse(t,
					`[{"kind":"repo","repo":{"name":"api","folder":"api","remote":"git@x.test:acme/api.git"}},`+
						`{"kind":"group","name":"Front","color":"#f00","repos":[{"name":"web","folder":"web"}]}]`)},
				{Kind: KindNotes, Folder: "api", Ref: NotesRef, Notes: []BundleNote{
					{Sha: "0123456789abcdef0123456789abcdef01234567", Body: "ship it\n\nfrom go"},
				}},
			}
		},
	},
	{
		name:     "go-escapes",
		project:  "Proj <x> & \"y\"    \u0007 é 😀 \\ \t",
		password: "",
		sections: func(t *testing.T) []Section {
			return []Section{
				{Kind: KindRepo, Project: "ñandú <&>", Folder: "dir with spaces", Files: []BundleFile{
					{Path: "dir/ñ.env", Content: []byte("KEY=\"välue\" # 😀\n")},
					{Path: "empty", Content: []byte{}},
				}},
				{Kind: KindVault, Entries: []BundleVaultEntry{{Key: "TAB\tKEY", Value: "line1\nline2 ", Note: str("n<o>te")}}},
			}
		},
	},
}

type goProducedExpected struct {
	File     string          `json:"file"`
	Password string          `json:"password"`
	Header   json.RawMessage `json:"header"`
	Sections json.RawMessage `json:"sections"`
}

func goProducedParams(i int) packParams {
	salt := make([]byte, saltLen)
	iv := make([]byte, ivLen)
	for k := range salt {
		salt[k] = byte(200 + i*13 + k*5)
	}
	for k := range iv {
		iv[k] = byte(31 + i*17 + k*3)
	}
	return packParams{salt: salt, iv: iv, createdAt: 1798761600000 + int64(i)}
}

func TestGoProducedBundles(t *testing.T) {
	dir := filepath.Join(testutil.MustRepoRoot(t), "testdata", "vectors", "secure-go-produced")
	write := os.Getenv("PORT_WRITE_VECTORS") == "1"
	expected := []goProducedExpected{}
	for i, c := range goProducedCases {
		sections := c.sections(t)
		text, err := packWith(c.project, sections, c.password, goProducedParams(i))
		if err != nil {
			t.Fatal(err)
		}
		file := c.name + ".gitcito"
		header, err := ReadBundleHeader(text)
		if err != nil || header == nil {
			t.Fatalf("%s: header %v", c.name, err)
		}
		back, err := UnpackSections(text, c.password)
		if err != nil {
			t.Fatalf("%s: unpack %v", c.name, err)
		}
		enc := []any{}
		for _, s := range back {
			enc = append(enc, encodeForVector(s))
		}
		hj, _ := json.Marshal(header)
		sj, _ := json.Marshal(enc)
		expected = append(expected, goProducedExpected{File: file, Password: c.password, Header: hj, Sections: sj})
		if write {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		committed, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatalf("%s missing — run with PORT_WRITE_VECTORS=1: %v", file, err)
		}
		if string(committed) != text {
			t.Fatalf("%s: Go no longer produces the committed bytes", file)
		}
	}
	data, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, "expected.json")
	if write {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "expected.json", got, committed)
}
