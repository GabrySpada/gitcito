package secure

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GabrySpada/gitcito/internal/testutil"
)

// Replays testdata/vectors/secure-bundle.json, captured from the TypeScript by
// test/port/secure.test.ts: every bundle the TS packed under a fixed salt, IV
// and clock must come out of packWith byte for byte, and every bundle — valid
// or crafted — must read, unpack and open exactly as the TS did.

type packVector struct {
	Name      string          `json:"name"`
	Project   string          `json:"project"`
	Password  string          `json:"password"`
	Salt      string          `json:"salt"`
	IV        string          `json:"iv"`
	CreatedAt int64           `json:"createdAt"`
	Key       string          `json:"key"`
	Sections  json.RawMessage `json:"sections"`
	Bundle    string          `json:"bundle"`
}

type decodedVector struct {
	Name        string          `json:"name"`
	Text        string          `json:"text"`
	Password    string          `json:"password"`
	Header      json.RawMessage `json:"header"`
	HeaderError string          `json:"headerError"`
	Sections    json.RawMessage `json:"sections"`
	Files       json.RawMessage `json:"files"`
	Opened      json.RawMessage `json:"opened"`
	Error       string          `json:"error"`
}

type bundleVectors struct {
	Scrypt struct {
		N, R, P, KeyLen int
	} `json:"scrypt"`
	Pack        []packVector    `json:"pack"`
	Decoded     []decodedVector `json:"decoded"`
	Crafted     []decodedVector `json:"crafted"`
	SafeRelPath []struct {
		Path string `json:"path"`
		Safe bool   `json:"safe"`
	} `json:"safeRelPath"`
	SecretFile []struct {
		Path   string `json:"path"`
		Secret bool   `json:"secret"`
	} `json:"secretFile"`
}

func loadBundleVectors(t *testing.T) bundleVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testutil.MustRepoRoot(t), "testdata", "vectors", "secure-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v bundleVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// vectorSections decodes sections written in encodeSection's shape (content
// base64), which is exactly the payload's shape.
func vectorSections(t *testing.T, raw json.RawMessage) []Section {
	t.Helper()
	parsed, err := parseJS(raw)
	if err != nil {
		t.Fatal(err)
	}
	arr, _ := parsed.([]any)
	sections, err := decodeSections(arr)
	if err != nil {
		t.Fatal(err)
	}
	return sections
}

// encodeForVector is the test file's encSection: the section as JSON with
// content base64, remote/note present whenever the section carries them.
func encodeForVector(s Section) any {
	var o jsObject
	switch s.Kind {
	case KindRepo:
		files := []any{}
		for _, f := range s.Files {
			fo := jsObject{{"path", f.Path}, {"content", base64.StdEncoding.EncodeToString(f.Content)}}
			if f.Executable {
				fo = append(fo, jsField{"executable", true})
			}
			files = append(files, fo)
		}
		o = jsObject{{"kind", s.Kind}, {"project", s.Project}, {"folder", s.Folder}, {"files", files}}
		if s.Remote != nil {
			o = append(o, jsField{"remote", *s.Remote})
		}
	case KindVault:
		entries := []any{}
		for _, e := range s.Entries {
			eo := jsObject{{"key", e.Key}, {"value", e.Value}}
			if e.Note != nil {
				eo = append(eo, jsField{"note", *e.Note})
			}
			entries = append(entries, eo)
		}
		o = jsObject{{"kind", s.Kind}, {"entries", entries}}
	case KindWorkspace:
		o = jsObject{{"kind", s.Kind}, {"name", s.Name}, {"tabs", tabsValue(s.Tabs)}}
	case KindNotes:
		notes := []any{}
		for _, n := range s.Notes {
			notes = append(notes, jsObject{{"sha", n.Sha}, {"body", n.Body}})
		}
		o = jsObject{{"kind", s.Kind}, {"folder", s.Folder}, {"ref", s.Ref}, {"notes", notes}}
		if s.Remote != nil {
			o = append(o, jsField{"remote", *s.Remote})
		}
	}
	return o
}

func encodeFilesForVector(files []BundleFile) any {
	out := []any{}
	for _, f := range files {
		fo := jsObject{{"path", f.Path}, {"content", base64.StdEncoding.EncodeToString(f.Content)}}
		if f.Executable {
			fo = append(fo, jsField{"executable", true})
		}
		out = append(out, fo)
	}
	return out
}

// sameJSON compares two values by their JSON meaning.
func sameJSON(t *testing.T, what string, got any, want json.RawMessage) {
	t.Helper()
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: marshal: %v", what, err)
	}
	var g, w any
	if err := json.Unmarshal(gotRaw, &g); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if len(want) == 0 {
		want = json.RawMessage("null")
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, gotRaw, want)
	}
}

func TestScryptParamsMatchVectors(t *testing.T) {
	v := loadBundleVectors(t)
	if v.Scrypt.N != scryptN || v.Scrypt.R != scryptR || v.Scrypt.P != scryptP || v.Scrypt.KeyLen != scryptKeyLen {
		t.Fatalf("scrypt params drifted: %+v", v.Scrypt)
	}
}

func TestPackMatchesTypeScriptByteForByte(t *testing.T) {
	v := loadBundleVectors(t)
	if len(v.Pack) == 0 {
		t.Fatal("no pack vectors")
	}
	for _, c := range v.Pack {
		t.Run(c.Name, func(t *testing.T) {
			salt, _ := base64.StdEncoding.DecodeString(c.Salt)
			iv, _ := base64.StdEncoding.DecodeString(c.IV)
			key, err := deriveKey(c.Password, salt)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(key) != c.Key {
				t.Fatalf("scrypt key = %x, want %s", key, c.Key)
			}
			sections := vectorSections(t, c.Sections)
			got, err := packWith(c.Project, sections, c.Password, packParams{salt: salt, iv: iv, createdAt: c.CreatedAt})
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Bundle {
				t.Fatalf("bundle differs from the TypeScript output\n got %s\nwant %s", got, c.Bundle)
			}
		})
	}
}

func replayDecoded(t *testing.T, c decodedVector) {
	t.Helper()
	header, err := ReadBundleHeader(c.Text)
	if c.HeaderError != "" {
		if err == nil || errCode(err) != c.HeaderError {
			t.Errorf("header error = %v, want %s", err, c.HeaderError)
		}
	} else {
		if err != nil {
			t.Fatalf("header: %v", err)
		}
		if header == nil {
			sameJSON(t, "header", nil, c.Header)
		} else {
			sameJSON(t, "header", header, c.Header)
		}
	}
	sections, err := UnpackSections(c.Text, c.Password)
	if c.Error != "" {
		if err == nil || errCode(err) != c.Error {
			t.Errorf("unpack error = %v (%s), want %s", err, errCode(err), c.Error)
		}
	} else {
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		enc := []any{}
		for _, s := range sections {
			enc = append(enc, encodeForVector(s))
		}
		sameJSON(t, "sections", enc, c.Sections)
		files, err := UnpackBundle(c.Text, c.Password)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, "files", encodeFilesForVector(files), c.Files)
	}
	file := filepath.Join(t.TempDir(), "b.gitcito")
	if err := os.WriteFile(file, []byte(c.Text), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Share{}
	sameJSON(t, "opened", s.OpenV2(file, c.Password), c.Opened)
}

func TestUnpackTypeScriptBundles(t *testing.T) {
	v := loadBundleVectors(t)
	for _, c := range v.Decoded {
		t.Run(c.Name, func(t *testing.T) {
			for _, p := range v.Pack {
				if p.Name == c.Name {
					c.Text, c.Password = p.Bundle, p.Password
				}
			}
			if c.Text == "" {
				t.Fatal("no bundle for this case")
			}
			replayDecoded(t, c)
		})
	}
}

func TestUnpackCraftedBundles(t *testing.T) {
	v := loadBundleVectors(t)
	if len(v.Crafted) == 0 {
		t.Fatal("no crafted vectors")
	}
	for _, c := range v.Crafted {
		t.Run(c.Name, func(t *testing.T) { replayDecoded(t, c) })
	}
}

func TestIsSafeRelPathVectors(t *testing.T) {
	v := loadBundleVectors(t)
	for _, c := range v.SafeRelPath {
		if got := IsSafeRelPath(c.Path); got != c.Safe {
			t.Errorf("IsSafeRelPath(%q) = %v, want %v", c.Path, got, c.Safe)
		}
	}
}

func TestIsSecretFileVectors(t *testing.T) {
	v := loadBundleVectors(t)
	for _, c := range v.SecretFile {
		if got := isSecretFile(c.Path); got != c.Secret {
			t.Errorf("isSecretFile(%q) = %v, want %v", c.Path, got, c.Secret)
		}
	}
}
