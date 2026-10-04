package model

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// GlazeMatch is a known glaze name found in a piece's glaze text.
type GlazeMatch struct {
	Start, End int    // byte offsets in the text
	Name       string // the glaze's name as recorded (canonical case)
}

// MatchGlazes finds the known glaze names in free text, e.g. "Pink with dabs
// of Red" → Pink, Red. Matching ignores case, only counts whole words, and
// prefers longer names ("Floating Blue" over "Blue"). Matches don't overlap
// and are returned in text order. The browser runs the same rules for its
// live preview (app.js).
func MatchGlazes(text string, names []string) []GlazeMatch {
	byLength := append([]string(nil), names...)
	sort.SliceStable(byLength, func(i, j int) bool { return len(byLength[i]) > len(byLength[j]) })
	taken := make([]bool, len(text))
	var matches []GlazeMatch
	for _, name := range byLength {
		n := len(name)
		if strings.TrimSpace(name) == "" {
			continue
		}
		for i := 0; i+n <= len(text); i++ {
			if !strings.EqualFold(text[i:i+n], name) || !wordStart(text, i) || !wordEnd(text, i+n) || anyTaken(taken[i:i+n]) {
				continue
			}
			for k := i; k < i+n; k++ {
				taken[k] = true
			}
			matches = append(matches, GlazeMatch{i, i + n, name})
			i += n - 1
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Start < matches[j].Start })
	return matches
}

// GlazesIn returns the distinct known glazes mentioned in text, in order.
func GlazesIn(text string, names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range MatchGlazes(text, names) {
		if k := strings.ToLower(m.Name); !seen[k] {
			seen[k] = true
			out = append(out, m.Name)
		}
	}
	return out
}

// ReplaceGlaze rewrites every whole-word, case-insensitive mention of one
// glaze name with another (used when a glaze is renamed).
func ReplaceGlaze(text, old, new string) string {
	var b strings.Builder
	last := 0
	for _, m := range MatchGlazes(text, []string{old}) {
		b.WriteString(text[last:m.Start])
		b.WriteString(new)
		last = m.End
	}
	b.WriteString(text[last:])
	return b.String()
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func wordStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !isWordRune(r)
}

func wordEnd(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return !isWordRune(r)
}

func anyTaken(b []bool) bool {
	for _, t := range b {
		if t {
			return true
		}
	}
	return false
}
