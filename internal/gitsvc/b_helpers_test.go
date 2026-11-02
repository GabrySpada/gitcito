package gitsvc

import (
	"errors"
	"math"
	"sort"
	"testing"
)

func TestBDecodeUTF8MatchesWhatwg(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain ascii", "plain ascii"},
		{"caf\xc3\xa9", "café"},
		{"\xff", "�"},
		{"a\xe2\x82b", "a�b"},   // truncated 3-byte sequence: one replacement
		{"\xe2\x82", "�"},       // …also at the end
		{"\xed\xa0\x80", "���"}, // surrogate: ED A0 is not a valid prefix
		{"\xf0\x9f\x98", "�"},   // 3 of a 4-byte emoji
		{"\xc0\xaf", "��"},      // overlong lead
		{"\xef\xbf\xbd", "�"},   // a real U+FFFD stays one
	}
	for _, tt := range tests {
		if got := bDecodeUTF8(tt.in); got != tt.want {
			t.Errorf("bDecodeUTF8(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBJSNumber(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"", 0}, {"  ", 0}, {"42", 42}, {" 42\n", 42}, {"-3.5", -3.5}, {"1e3", 1000}, {".5", 0.5},
		{"0x1f", 31}, {"0b101", 5}, {"0o17", 15}, {"Infinity", math.Inf(1)}, {"-Infinity", math.Inf(-1)},
	}
	for _, tt := range tests {
		if got := bJSNumber(tt.in); got != tt.want {
			t.Errorf("Number(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	for _, nan := range []string{"abc", "12px", "0x", "inf", "1_000", "--1"} {
		if got := bJSNumber(nan); !math.IsNaN(got) {
			t.Errorf("Number(%q) = %v, want NaN", nan, got)
		}
	}
	if bJSInt("abc") != 0 || bJSInt("1700000000") != 1700000000 {
		t.Error("bJSInt")
	}
	if bJSNumberString("1700000000") != "1700000000" || bJSNumberString("x") != "NaN" {
		t.Error("bJSNumberString")
	}
}

func TestBLocaleCompareOrders(t *testing.T) {
	// The orders ICU's root collation gives (what Electron's localeCompare did).
	// Captured from Node: [...].reverse().sort((a, b) => a.localeCompare(b)).
	want := []string{
		"_private", "-dash", ".env", ".gitignore", "@scope", "#hash", "~tilde", "🚀 rocket notes.txt", "$dollar",
		"10.txt", "9.txt", "a.txt", "A.txt", "b.txt", "café-☕.txt", "Cafe.txt", "café.txt", "crlf-windows.txt",
		"data.bin", "dist", "docs", "notes.txt", "README.md", "src", "z", "مرحبا.txt", "日本語ファイル.md",
	}
	got := make([]string, len(want))
	for i, s := range want {
		got[len(want)-1-i] = s
	}
	sort.SliceStable(got, func(i, j int) bool { return bLocaleCompare(got[i], got[j]) < 0 })
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %q\nwant   %q", got, want)
		}
	}
	if bLocaleCompare("a", "a") != 0 || bLocaleCompare("a", "b") >= 0 || bLocaleCompare("b", "a") <= 0 {
		t.Error("basic comparisons")
	}
}

func TestBJSRegexpTranslation(t *testing.T) {
	tests := []struct {
		src        string
		ignoreCase bool
		in         string
		match      bool
	}{
		{`a.c`, false, "a\rc", false}, // JS . stops at \r
		{`a.c`, false, "abc", true},
		{`a\sb`, false, "a b", true}, // JS \s covers NBSP
		{`[\s]x`, false, " x", true}, // …inside a class too
		{`A`, false, "A", true},      // \uXXXX escapes
		{`todo`, true, "TODO", true},
		{`\bTOD\b`, false, "TODO", false},
		{`\w+@corp\.dev`, false, "bob@corp.dev", true},
	}
	for _, tt := range tests {
		re, err := bJSRegexp(tt.src, tt.ignoreCase)
		if err != nil {
			t.Errorf("%q: %v", tt.src, err)
			continue
		}
		if got := re.MatchString(tt.in); got != tt.match {
			t.Errorf("/%s/ on %q = %v, want %v", tt.src, tt.in, got, tt.match)
		}
	}
	// RE2 has no lookaround or backreferences: such a pattern is "invalid",
	// and the search methods answer [] for it.
	for _, bad := range []string{`(?=x)`, `(a)\1`, `(`} {
		if _, err := bJSRegexp(bad, false); err == nil {
			t.Errorf("%q compiled", bad)
		}
	}
	if bEscapeRegExp(`a.b*c(d)[e]{f}|g^h$i\j+k?`) != `a\.b\*c\(d\)\[e\]\{f\}\|g\^h\$i\\j\+k\?` {
		t.Error("bEscapeRegExp")
	}
}

func TestBSliceEnd(t *testing.T) {
	for _, tt := range []struct{ n, end, want int }{{5, 2, 2}, {5, 9, 5}, {5, -1, 4}, {5, -9, 0}, {0, 3, 0}} {
		if got := bSliceEnd(tt.n, tt.end); got != tt.want {
			t.Errorf("bSliceEnd(%d, %d) = %d, want %d", tt.n, tt.end, got, tt.want)
		}
	}
}

func TestBWithRerereCaptureRecordsReplays(t *testing.T) {
	err := bWithRerereCapture("/repo/b-test", func() error {
		return errors.New("Resolved 'a.txt' using previous resolution.\nResolved 'b.txt' using previous resolution.")
	})
	if err == nil {
		t.Fatal("the error must pass through")
	}
	bRerereReplayed.Lock()
	got := bRerereReplayed.m["/repo/b-test"]
	delete(bRerereReplayed.m, "/repo/b-test")
	bRerereReplayed.Unlock()
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "b.txt" {
		t.Fatalf("captured %v", got)
	}
}

func TestBNumstatRenames(t *testing.T) {
	tests := []struct{ line, path string }{
		{"1\t2\tsrc/a.js", "src/a.js"},
		{"0\t0\tlib/util.js => src/utils/util.js", "src/utils/util.js"},
		{"3\t1\tsrc/{old => new}/c.js", "src/new/c.js"},
		{"-\t-\tassets/{a.png => b.png}", "assets/b.png"},
	}
	for _, tt := range tests {
		if p, _, _, ok := bNumstatRow(tt.line); !ok || p != tt.path {
			t.Errorf("%q → %q", tt.line, p)
		}
	}
	if _, a, r, _ := bNumstatRow("-\t-\tbin"); a != 0 || r != 0 {
		t.Error("binary rows count no lines")
	}
}
