package shared

import (
	"strings"
	"unicode"
)

// String.prototype.localeCompare for model ids: an approximation of ICU's root
// collation, copied from internal/gitsvc (b_helpers.go, unexported there).
// aimodelnames.go orders undated models by name with it.

// localeCompare approximates `a.localeCompare(b)` — ICU's root collation,
// which is what Electron's V8 used. It is not a full UCA, but it reproduces the
// orderings a file tree and a branch list actually show:
//   - whitespace, then punctuation and symbols (in DUCET order), then emoji,
//     then currency signs, then digits, then letters; digits compare as
//     characters, not numbers ("10" < "9");
//   - letters compare case- and accent-insensitively first, then by accent,
//     then lower case before upper case;
//   - emoji and other symbols sort with the symbols, other scripts after Latin
//     by code point.
func localeCompare(a, b string) int {
	ka, kb := collationKey(a), collationKey(b)
	for level := 0; level < 3; level++ {
		if c := compareInts(ka[level], kb[level]); c != 0 {
			return c
		}
	}
	return 0
}

func compareInts(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// punctOrder is DUCET's order for the ASCII variable characters.
const punctOrder = "\t\n\v\f\r _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~"

// Primary weight bands.
const (
	weightPunct    = 1_000
	weightSymbol   = 2_000
	weightCurrency = 2_500
	weightDigit    = 3_000
	weightLatin    = 4_000
	weightOther    = 10_000
)

var latinFold = map[rune]struct {
	base   string
	accent int
}{
	'à': {"a", 1}, 'á': {"a", 2}, 'â': {"a", 3}, 'ã': {"a", 4}, 'ä': {"a", 5}, 'å': {"a", 6},
	'ç': {"c", 7}, 'è': {"e", 1}, 'é': {"e", 2}, 'ê': {"e", 3}, 'ë': {"e", 5},
	'ì': {"i", 1}, 'í': {"i", 2}, 'î': {"i", 3}, 'ï': {"i", 5}, 'ñ': {"n", 4},
	'ò': {"o", 1}, 'ó': {"o", 2}, 'ô': {"o", 3}, 'õ': {"o", 4}, 'ö': {"o", 5}, 'ø': {"o", 8},
	'ù': {"u", 1}, 'ú': {"u", 2}, 'û': {"u", 3}, 'ü': {"u", 5}, 'ý': {"y", 2}, 'ÿ': {"y", 5},
	'ß': {"ss", 9}, 'æ': {"ae", 9}, 'œ': {"oe", 9},
}

func collationKey(s string) [3][]int {
	var k [3][]int
	add := func(p, sec, ter int) {
		k[0] = append(k[0], p)
		k[1] = append(k[1], sec)
		k[2] = append(k[2], ter)
	}
	for _, r := range s {
		ter := 0
		if unicode.IsUpper(r) {
			ter = 1
			r = unicode.ToLower(r)
		}
		if f, ok := latinFold[r]; ok {
			for i, c := range f.base {
				sec := 0
				if i == 0 {
					sec = f.accent
				}
				add(weightLatin+int(c-'a'), sec, ter)
			}
			continue
		}
		switch {
		case r >= 'a' && r <= 'z':
			add(weightLatin+int(r-'a'), 0, ter)
		case r >= '0' && r <= '9':
			add(weightDigit+int(r-'0'), 0, ter)
		case r < 0x80 && strings.ContainsRune(punctOrder, r):
			add(weightPunct+strings.IndexRune(punctOrder, r), 0, ter)
		case unicode.Is(unicode.Sc, r):
			// Currency symbols come after every other symbol, emoji included.
			add(weightCurrency+int(r)%500, 0, ter)
		case unicode.IsSpace(r) || unicode.IsPunct(r):
			add(weightPunct+len(punctOrder)+int(r)%500, 0, ter)
		case unicode.IsSymbol(r) || r >= 0x1F000 && r <= 0x1FAFF:
			add(weightSymbol+int(r)%500, 0, ter)
		case unicode.IsDigit(r):
			add(weightDigit+int(r)%10, 0, ter)
		default:
			add(weightOther+int(r), 0, ter)
		}
	}
	return k
}
