package shared

import (
	"strings"
	"testing"
)

func TestIsSafeRepoPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"src/a.ts", true},
		{".github/workflows/ci.yml", true},
		{"  spaced.txt  ", true},
		{"", false},
		{"   ", false},
		{"/etc/passwd", false},
		{`\windows\system32`, false},
		{"C:/x", false},
		{"c:x", false},
		{"a/../b", false},
		{`a\..\b`, false},
		{"..", false},
		{"a/..b", true},
		{"a\x00b", false},
		{"\uFEFF/etc/passwd", false},
		{"\u0085x", true},
		{strings.Repeat("a", 400), true},
		{strings.Repeat("a", 401), false},
		// 200 two-unit code points = 400 UTF-16 units: accepted like in JS.
		{strings.Repeat("😀", 200), true},
		{strings.Repeat("😀", 201), false},
	}
	for _, c := range cases {
		if got := IsSafeRepoPath(c.in); got != c.want {
			t.Errorf("IsSafeRepoPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
