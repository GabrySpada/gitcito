package shared

// Minimal semver helpers shared by update detection and the What's-new
// callout — src/shared/version.ts. Tolerant of a leading `v` and of a
// `-prerelease` suffix, which ranks lower than the same release.

import (
	"strings"
)

// parseIntJS is parseInt(s, 10) || 0: leading whitespace, an optional sign,
// then as many digits as there are; anything unparsable is 0.
func parseIntJS(s string) int {
	s = JSTrim(s)
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n, digits := 0, 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		digits++
	}
	if digits == 0 {
		return 0
	}
	if neg {
		return -n
	}
	return n
}

type parsedVersion struct {
	nums []int
	pre  string
}

func parseVersion(v string) parsedVersion {
	cleaned := JSTrim(v)
	if len(cleaned) > 0 && (cleaned[0] == 'v' || cleaned[0] == 'V') {
		cleaned = cleaned[1:]
	}
	// split('-', 2): the first two pieces, the rest dropped.
	parts := strings.Split(cleaned, "-")
	core, pre := parts[0], ""
	if len(parts) > 1 {
		pre = parts[1]
	}
	nums := []int{}
	for _, n := range strings.Split(core, ".") {
		nums = append(nums, parseIntJS(n))
	}
	for len(nums) < 3 {
		nums = append(nums, 0)
	}
	return parsedVersion{nums: nums, pre: pre}
}

// CompareVersions is -1 if a < b, 0 if equal, 1 if a > b.
func CompareVersions(a, b string) int {
	pa, pb := parseVersion(a), parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa.nums[i] != pb.nums[i] {
			if pa.nums[i] < pb.nums[i] {
				return -1
			}
			return 1
		}
	}
	// A release with no prerelease tag outranks one that has it (1.0.0 > 1.0.0-rc).
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	case CompareUTF16(pa.pre, pb.pre) < 0:
		return -1
	}
	return 1
}

// IsNewerVersion is true when candidate is a strictly newer version than current.
func IsNewerVersion(candidate, current string) bool {
	return CompareVersions(candidate, current) > 0
}
