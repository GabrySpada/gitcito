package textconv

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// ─── vectors (test/port/registry.test.ts) ───────────────────────────────────

func TestJSONAndStringsVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "textconv.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		JSON []struct {
			In     string  `json:"in"`
			Out    *string `json:"out"`
			Throws bool    `json:"throws"`
		} `json:"json"`
		Strings []struct {
			Name  string  `json:"name"`
			B64   string  `json:"b64"`
			Out   *string `json:"out"`
			Error string  `json:"error"`
		} `json:"strings"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.JSON) == 0 || len(v.Strings) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range v.JSON {
		got, err := JSONToStable(c.In)
		switch {
		case c.Throws && err == nil:
			t.Errorf("JSONToStable(%q) = %q, want an error", c.In, got)
		case !c.Throws && (err != nil || got != *c.Out):
			t.Errorf("JSONToStable(%q) = %q, %v; want %q", c.In, got, err, *c.Out)
		}
	}
	for _, c := range v.Strings {
		buf, _ := base64.StdEncoding.DecodeString(c.B64)
		got, err := StringsToText(buf)
		switch {
		case c.Error != "":
			if err == nil || err.Error() != c.Error {
				t.Errorf("%s: err = %v, want %q", c.Name, err, c.Error)
			}
		case err != nil || got != *c.Out:
			t.Errorf("%s: StringsToText = %q, %v; want %q", c.Name, got, err, *c.Out)
		}
	}
}

func TestDocumentVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "documents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		File   string  `json:"file"`
		Out    *string `json:"out"`
		Throws bool    `json:"throws"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range cases {
		got, err := ConvertFile(filepath.Join("testdata", c.File))
		switch {
		case c.Throws && err == nil:
			t.Errorf("%s: got %q, want an error", c.File, got)
		case !c.Throws && (err != nil || got != *c.Out):
			t.Errorf("%s:\n got %q (%v)\nwant %q", c.File, got, err, *c.Out)
		}
	}
}

// ─── test/textconv.test.ts ──────────────────────────────────────────────────

// jsonToStable > sorts keys recursively and indents consistently
func TestJSONToStableSortsKeys(t *testing.T) {
	a, _ := JSONToStable(`{"b":1,"a":{"z":2,"y":[{"q":1,"p":2}]}}`)
	b, _ := JSONToStable(`{"a":{"y":[{"p":2,"q":1}],"z":2},"b":1}`)
	want := "{\n  \"a\": {\n    \"y\": [\n      {\n        \"p\": 2,\n        \"q\": 1\n      }\n    ],\n    \"z\": 2\n  },\n  \"b\": 1\n}\n"
	if a != b || a != want {
		t.Fatalf("a = %q, b = %q", a, b)
	}
}

// jsonToStable > keeps array order — arrays are ordered data, not key sets
func TestJSONToStableKeepsArrayOrder(t *testing.T) {
	if got, _ := JSONToStable("[3,1,2]"); got != "[\n  3,\n  1,\n  2\n]\n" {
		t.Fatalf("got %q", got)
	}
}

// jsonToStable > throws on invalid JSON so git shows the raw file instead of lying
func TestJSONToStableRejectsInvalid(t *testing.T) {
	if _, err := JSONToStable("{nope"); err == nil {
		t.Fatal("no error")
	}
}

// xlsxToCsv > renders every sheet as a labeled CSV block
func TestXlsxRendersEverySheet(t *testing.T) {
	buf, err := os.ReadFile(filepath.Join("testdata", "two-sheets.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := XlsxToCsv(buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## First", "name,qty", "ok,2", "## Second"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// convertFile > dispatches .json end to end from a real file
func TestConvertFileJSON(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(f, []byte(`{"b":1,"a":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ConvertFile(f); err != nil || got != "{\n  \"a\": 2,\n  \"b\": 1\n}\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// convertFile > refuses an extension it does not understand
func TestConvertFileRefusesUnknown(t *testing.T) {
	_, err := ConvertFile("/tmp/whatever.bin")
	if err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("err = %v", err)
	}
}

// convertFile > fails loudly on a corrupt document rather than printing garbage
func TestConvertFileCorruptDocx(t *testing.T) {
	f := filepath.Join(t.TempDir(), "broken.docx")
	if err := os.WriteFile(f, []byte("this is not a zip archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertFile(f); err == nil {
		t.Fatal("no error")
	}
}

// bundled converter wiring > resolves the shipped shim and reports it executable
// (the converterAvailable half lives in gitsvc / gitx).
func TestBundledPath(t *testing.T) {
	shim := BundledPath()
	if !strings.HasSuffix(shim, "gitcito-textconv") {
		t.Fatalf("shim = %s", shim)
	}
	info, err := os.Stat(shim)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("shim not executable: %v", err)
	}
}

func utf16le(s string) []byte {
	out := []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func utf16be(s string) []byte {
	out := []byte{0xfe, 0xff}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}

const stringsSrc = "/* Greeting */\n\"hello\" = \"Hola\";\n"

// stringsToText > decodes the UTF-16 that git refuses to diff at all
func TestStringsDecodesUTF16(t *testing.T) {
	for _, buf := range [][]byte{utf16le(stringsSrc), utf16be(stringsSrc)} {
		if got, err := StringsToText(buf); err != nil || got != stringsSrc {
			t.Fatalf("got %q, %v", got, err)
		}
	}
}

// stringsToText > leaves a modern UTF-8 file alone instead of turning it into mojibake
func TestStringsLeavesUTF8(t *testing.T) {
	for _, buf := range [][]byte{[]byte(stringsSrc), append([]byte{0xef, 0xbb, 0xbf}, stringsSrc...)} {
		if got, _ := StringsToText(buf); got != stringsSrc {
			t.Fatalf("got %q", got)
		}
	}
}

// stringsToText > keeps non-ASCII intact through the decode
func TestStringsKeepsNonASCII(t *testing.T) {
	accented := "\"greet\" = \"Buenos días — ¿qué tal?\";\n"
	if got, _ := StringsToText(utf16le(accented)); got != accented {
		t.Fatalf("got %q", got)
	}
}

// stringsToText > ends with a newline so the last line diffs like the others
func TestStringsEndsWithNewline(t *testing.T) {
	if got, _ := StringsToText([]byte(`"a" = "b";`)); got != "\"a\" = \"b\";\n" {
		t.Fatalf("got %q", got)
	}
}

// stringsToText > refuses a truncated UTF-16 file rather than emitting half a character
func TestStringsRefusesTruncated(t *testing.T) {
	_, err := StringsToText([]byte{0xfe, 0xff, 0x00, 0x61, 0x00})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("err = %v", err)
	}
}

// Apple localization files end to end > converts a .strings and a .xcstrings from disk
func TestAppleLocalizationFiles(t *testing.T) {
	dir := t.TempDir()
	strs := filepath.Join(dir, "Localizable.strings")
	if err := os.WriteFile(strs, utf16le("\"k\" = \"v\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ConvertFile(strs); err != nil || got != "\"k\" = \"v\";\n" {
		t.Fatalf("strings: %q, %v", got, err)
	}
	catalog := filepath.Join(dir, "Localizable.xcstrings")
	if err := os.WriteFile(catalog, []byte(`{"version":"1.0","sourceLanguage":"en"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ConvertFile(catalog); err != nil || got != "{\n  \"sourceLanguage\": \"en\",\n  \"version\": \"1.0\"\n}\n" {
		t.Fatalf("xcstrings: %q, %v", got, err)
	}
}

// ─── textconvCli.ts ─────────────────────────────────────────────────────────

func TestRunCLI(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.json")
	if err := os.WriteFile(f, []byte(`{"b":1,"a":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args           []string
		code           int
		stdout, stderr string
	}{
		{nil, 2, "", "usage: gitcito-textconv <file>\n"},
		{[]string{""}, 2, "", "usage: gitcito-textconv <file>\n"},
		{[]string{"--help"}, 2, "", "usage: gitcito-textconv <file>\n"},
		{[]string{f}, 0, "{\n  \"a\": 2,\n  \"b\": 1\n}\n", ""},
		{[]string{"/tmp/x.bin"}, 1, "", "gitcito-textconv: unsupported file type '.bin' (supported: .docx, .xlsx, .xls, .json, .xcstrings, .strings)\n"},
		{[]string{filepath.Join(t.TempDir(), "missing.json")}, 1, "", ""},
	} {
		var out, errb bytes.Buffer
		code := RunCLI(c.args, &out, &errb)
		if code != c.code || out.String() != c.stdout || (c.stderr != "" && errb.String() != c.stderr) || (c.code != 0 && errb.Len() == 0) {
			t.Errorf("RunCLI(%q) = %d, %q, %q", c.args, code, out.String(), errb.String())
		}
	}
}

// ─── number formatting ──────────────────────────────────────────────────────

func TestJSNumberHelpers(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{toFixed(1.005, 2), "1.00"}, // 1.005 is 1.00499999… in binary
		{toFixed(2.5, 0), "3"},      // an exact tie rounds up, unlike strconv
		{toFixed(-2.5, 0), "-3"},
		{toFixed(0.000001, 3), "0.000"},
		{toFixed(1e21, 2), "1e+21"},
		{toPrecision(123456, 2), "1.2e+5"},
		{toPrecision(0.000123, 2), "0.00012"},
		{toPrecision(1e-7, 1), "1e-7"},
		{toExponential(12345, 2), "1.23e+4"},
		{toExponential(0, 1), "0.0e+0"},
		{numberString(1e21), "1e+21"},
		{numberString(1.5e-7), "1.5e-7"},
		{numberString(0.000001), "0.000001"},
		{numberString(-0.5), "-0.5"},
		{generalNumber(1 / 3.0), "0.333333333"},
		{generalNumber(123456789012), "1.23457E+11"},
		{generalNumber(1e10), "10000000000"},
		{ssfFormat("0.00", 1234.5), "1234.50"},
		{ssfFormat("#,##0", 1234.5), "1,235"},
		{ssfFormat("0%", 0.256), "26%"},
		{ssfFormat("0.00E+00", 1234.5), "1.23E+03"},
		{ssfFormat(`"$"#,##0.00_);\("$"#,##0.00\)`, -1234.5), "($1,234.50)"},
		{ssfFormat("000-0000", 5551234), "555-1234"},
		{ssfFormat("yyyy-mm-dd", 45000), "2023-03-15"},
		{ssfFormat("dddd, mmmm d", 45000), "Wednesday, March 15"},
		{ssfFormat("[h]:mm:ss", 1.5), "36:00:00"},
		{ssfFormat("h:mm:ss.00", 0.5000001), "12:00:00.01"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
