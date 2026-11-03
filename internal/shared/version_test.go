package shared

import "testing"

// Ports test/version.test.ts.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
	}{
		// orders by major/minor/patch
		{"orders by major/minor/patch", "1.0.0", "1.0.1", -1},
		{"orders by major/minor/patch", "1.2.0", "1.1.9", 1},
		{"orders by major/minor/patch", "2.0.0", "1.9.9", 1},
		{"orders by major/minor/patch", "1.0.0", "1.0.0", 0},
		// tolerates a leading v and missing segments
		{"tolerates a leading v and missing segments", "v1.1.0", "1.0.0", 1},
		{"tolerates a leading v and missing segments", "1.2", "1.2.0", 0},
		// ranks a prerelease below the final release
		{"ranks a prerelease below the final release", "1.0.0", "1.0.0-rc.1", 1},
		{"ranks a prerelease below the final release", "1.0.0-rc.1", "1.0.0-rc.2", -1},
		// parseInt semantics
		{"parseInt semantics", "1.2x.0", "1.2.0", 0},
		{"parseInt semantics", "1..0", "1.0.0", 0},
		{"parseInt semantics", "V2", "1.9", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s: CompareVersions(%q, %q) = %d, want %d", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewerVersion(t *testing.T) {
	// is true only for strictly newer candidates
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.1.0", "1.0.0", true}, {"1.0.0", "1.0.0", false}, {"0.9.0", "1.0.0", false}, {"v1.0.1", "v1.0.0", true}} {
		if got := IsNewerVersion(c.a, c.b); got != c.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
