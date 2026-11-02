package secure

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Port of test/secureBundle.test.ts and the bundle half of
// test/secureTeam.test.ts: round trips through the real (random) PackSections.

var bundleFiles = []BundleFile{
	{Path: ".env", Content: []byte("API_KEY=sk-live-abc123\nDB=postgres://u:p@h/db\n")},
	{Path: "app/.env.prod", Content: []byte("NODE_ENV=production\n")},
	{Path: "certs/dev.pem", Content: []byte{0, 1, 2, 250, 251, 252}}, // binary-ish
	{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Executable: true},
}

func mustPackBundle(t *testing.T, project string, files []BundleFile, pw string) string {
	t.Helper()
	text, err := PackBundle(project, files, pw)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || errCode(err) != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func TestBundleRoundTripsFiles(t *testing.T) {
	bundle := mustPackBundle(t, "myproject", bundleFiles, "correct horse battery")
	out, err := UnpackBundle(bundle, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(bundleFiles) {
		t.Fatalf("got %d files", len(out))
	}
	for i, f := range bundleFiles {
		if out[i].Path != f.Path || !bytes.Equal(out[i].Content, f.Content) || out[i].Executable != f.Executable {
			t.Errorf("file %d = %+v, want %+v", i, out[i], f)
		}
	}
}

func TestBundleEnvelopeOnlyWithoutPassword(t *testing.T) {
	bundle := mustPackBundle(t, "myproject", bundleFiles, "correct horse battery")
	h, err := ReadBundleHeader(bundle)
	if err != nil || h == nil {
		t.Fatalf("header: %v", err)
	}
	if h.Project != "myproject" || h.FileCount != 4 || h.Version != 2 || h.CreatedAt <= 0 {
		t.Errorf("header = %+v", h)
	}
	sameJSON(t, "sections", h.Sections, json.RawMessage(`[{"kind":"repo","project":"myproject","folder":"myproject","fileCount":4}]`))
	// No plaintext leakage: neither file paths nor contents appear in the JSON.
	if strings.Contains(bundle, "API_KEY") || strings.Contains(bundle, ".env") {
		t.Error("plaintext leaked into the envelope")
	}
}

func TestBundleRejectsWrongPassword(t *testing.T) {
	bundle := mustPackBundle(t, "myproject", bundleFiles, "correct horse battery")
	_, err := UnpackBundle(bundle, "wrong password")
	wantCode(t, err, ErrBadPassword)
}

func TestBundleRejectsTamperedPayload(t *testing.T) {
	bundle := mustPackBundle(t, "myproject", bundleFiles, "correct horse battery")
	var raw map[string]any
	if err := json.Unmarshal([]byte(bundle), &raw); err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(raw["payload"].(string))
	payload[0] ^= 0xff
	raw["payload"] = base64.StdEncoding.EncodeToString(payload)
	tampered, _ := json.Marshal(raw)
	_, err := UnpackBundle(string(tampered), "correct horse battery")
	wantCode(t, err, ErrBadPassword)
}

func TestBundleRejectsNonBundleAndFutureVersion(t *testing.T) {
	for _, text := range []string{"not json at all", `{"format":"something-else"}`} {
		if h, err := ReadBundleHeader(text); h != nil || err != nil {
			t.Errorf("ReadBundleHeader(%q) = %v, %v", text, h, err)
		}
	}
	_, err := ReadBundleHeader(`{"format":"gitcito-secure-bundle","version":99}`)
	wantCode(t, err, ErrUnsupportedVersion)
	_, err = UnpackBundle("{}", "x")
	wantCode(t, err, ErrInvalid)
}

func multiSections() []Section {
	return []Section{
		{Kind: KindRepo, Project: "api", Folder: "api", Remote: str("git@github.com:acme/api.git"),
			Files: []BundleFile{{Path: ".env", Content: []byte("API_KEY=sk-1\n")}}},
		{Kind: KindRepo, Project: "web", Folder: "web-frontend",
			Files: []BundleFile{{Path: ".env.local", Content: []byte("NEXT_PUBLIC=1\n")}}},
		{Kind: KindVault, Entries: []BundleVaultEntry{
			{Key: "STRIPE", Value: "sk_live_x", Note: str("prod")},
			{Key: "SENTRY", Value: "https://dsn"},
		}},
	}
}

func TestMultiSectionEnvelopeSummaries(t *testing.T) {
	bundle, err := PackSections("Workspace", multiSections(), "a good long password")
	if err != nil {
		t.Fatal(err)
	}
	h, err := ReadBundleHeader(bundle)
	if err != nil || h == nil {
		t.Fatal(err)
	}
	if h.Version != 2 || h.FileCount != 2 { // repo files only, vault excluded
		t.Errorf("header = %+v", h)
	}
	sameJSON(t, "sections", h.Sections, json.RawMessage(`[
		{"kind":"repo","project":"api","folder":"api","remote":"git@github.com:acme/api.git","fileCount":1},
		{"kind":"repo","project":"web","folder":"web-frontend","fileCount":1},
		{"kind":"vault","entryCount":2}]`))
	// Secret values never appear in the plaintext envelope.
	if strings.Contains(bundle, "sk_live_x") || strings.Contains(bundle, "API_KEY") {
		t.Error("secret leaked into the envelope")
	}
}

func TestMultiSectionRoundTrip(t *testing.T) {
	bundle, err := PackSections("Workspace", multiSections(), "a good long password")
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnpackSections(bundle, "a good long password")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].Folder != "api" || *out[0].Remote != "git@github.com:acme/api.git" || out[1].Folder != "web-frontend" {
		t.Fatalf("sections = %+v", out)
	}
	if !reflect.DeepEqual(out[2].Entries, multiSections()[2].Entries) {
		t.Errorf("vault = %+v", out[2].Entries)
	}
	files, err := UnpackBundle(bundle, "a good long password")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != ".env" || files[1].Path != ".env.local" {
		t.Errorf("flattened = %+v", files)
	}
}

func TestLegacySingleRepoBundleReadsAsOneSection(t *testing.T) {
	v1 := mustPackBundle(t, "legacy", []BundleFile{{Path: ".env", Content: []byte("X=1\n")}}, "pw pw pw pw")
	h, err := ReadBundleHeader(v1)
	if err != nil || h == nil {
		t.Fatal(err)
	}
	sameJSON(t, "section", h.Sections[0], json.RawMessage(`{"kind":"repo","project":"legacy","folder":"legacy","fileCount":1}`))
	sections, err := UnpackSections(v1, "pw pw pw pw")
	if err != nil || len(sections) != 1 || sections[0].Kind != KindRepo {
		t.Fatalf("sections = %+v, %v", sections, err)
	}
}

func TestIsSafeRelPath(t *testing.T) {
	for _, p := range []string{".env", "app/.env.prod", "deep/nested/dir/file.txt"} {
		if !IsSafeRelPath(p) {
			t.Errorf("%q should be safe", p)
		}
	}
	for _, p := range []string{"../outside", "a/../../outside", "/etc/passwd", `C:\windows\system32`, `a\..\b`, "", "a//b", "./a"} {
		if IsSafeRelPath(p) {
			t.Errorf("%q should be unsafe", p)
		}
	}
}

func TestIsSecretFile(t *testing.T) {
	for p, want := range map[string]bool{
		".env": true, "app/.env.prod": true, "certs/dev.pem": true, ".env.example": false, "src/index.ts": false,
	} {
		if isSecretFile(p) != want {
			t.Errorf("isSecretFile(%q) != %v", p, want)
		}
	}
}

func teamSections(t *testing.T) []Section {
	return []Section{
		{Kind: KindWorkspace, Name: "Team", Tabs: mustParse(t,
			`[{"kind":"repo","repo":{"name":"api","folder":"api","remote":"git@x.test:acme/api.git"}},`+
				`{"kind":"group","name":"Front","color":"#f00","repos":[{"name":"web","folder":"web"}]}]`)},
		{Kind: KindNotes, Folder: "api", Ref: NotesRef, Notes: []BundleNote{{Sha: strings.Repeat("a", 40), Body: "ship it"}}},
	}
}

func TestV3WorkspaceAndNotesRoundTrip(t *testing.T) {
	sections := teamSections(t)
	text, err := PackSections("Team", sections, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	h, err := ReadBundleHeader(text)
	if err != nil || h == nil {
		t.Fatal(err)
	}
	if h.Version != 3 {
		t.Errorf("version = %v, want 3", h.Version)
	}
	sameJSON(t, "summaries", h.Sections, json.RawMessage(`[
		{"kind":"workspace","name":"Team","tabCount":2,"repoCount":2},
		{"kind":"notes","folder":"api","ref":"refs/notes/commits","noteCount":1}]`))
	back, err := UnpackSections(text, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if string(stringifyJS(encodeForVector(back[0]), "")) != string(stringifyJS(encodeForVector(sections[0]), "")) ||
		!reflect.DeepEqual(back[1].Notes, sections[1].Notes) || back[1].Ref != NotesRef {
		t.Errorf("round trip = %+v", back)
	}
}

func TestRepoAndVaultOnlyStaysV2(t *testing.T) {
	text, err := PackSections("p", []Section{{Kind: KindVault, Entries: []BundleVaultEntry{{Key: "K", Value: "v"}}}}, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := ReadBundleHeader(text)
	if h == nil || h.Version != 2 {
		t.Errorf("header = %+v", h)
	}
}

func TestPackFailsOnTabsTheTSCouldNotCount(t *testing.T) {
	// `t.repos.length` on a group without repos threw in the TS; exportV2
	// reported that as write-failed.
	_, err := PackSections("p", []Section{{Kind: KindWorkspace, Name: "w", Tabs: mustParse(t, `[{"kind":"group"}]`)}}, "x")
	if err == nil {
		t.Fatal("want an error")
	}
	text, err := PackSections("p", []Section{{Kind: KindWorkspace, Name: "w", Tabs: mustParse(t, `[{"kind":"group","repos":5}]`)}}, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"repoCount": null`) {
		t.Errorf("NaN repoCount should serialise as null:\n%s", text)
	}
}

func TestJSNumberFormatting(t *testing.T) {
	for f, want := range map[float64]string{
		0: "0", 1: "1", -42.5: "-42.5", 0.1: "0.1", 1e21: "1e+21", 1e20: "100000000000000000000",
		1.5e-7: "1.5e-7", 5e-7: "5e-7", 0.000001: "0.000001", 123456789012345680000: "123456789012345680000",
		1.7976931348623157e308: "1.7976931348623157e+308",
	} {
		if got := jsNumber(f); got != want {
			t.Errorf("jsNumber(%v) = %s, want %s", f, got, want)
		}
	}
}
