// Package names normalises item names so that spelling variants, plurals and common
// Hindi / Odia words compare equal, and measures typo distance between two names.
package names

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// aliases map a (plural-folded) token to its canonical word.
var aliases = map[string]string{
	// egg
	"anda": "egg", "ande": "egg", "andey": "egg", "अंडा": "egg", "अंडे": "egg", "ଅଣ୍ଡା": "egg",
	// chicken
	"murga": "chicken", "murgi": "chicken", "murg": "chicken", "chikan": "chicken", "chiken": "chicken",
	"चिकन": "chicken", "मुर्गा": "chicken", "मुर्गी": "chicken", "ଚିକେନ": "chicken",
	// fish
	"machhi": "fish", "machli": "fish", "macha": "fish", "machi": "fish",
	"मछली": "fish", "ମାଛ": "fish",
	// mutton
	"bakra": "mutton", "khasi": "mutton", "goat": "mutton", "बकरा": "mutton", "ଛେଳି": "mutton",
	// milk
	"doodh": "milk", "dudh": "milk", "दूध": "milk",
}

// NormalizeName lower-cases, turns punctuation into spaces, collapses spaces, strips a
// trailing "s" from words longer than 3 letters and maps known aliases to one word.
func NormalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	toks := strings.Fields(b.String())
	for i, t := range toks {
		toks[i] = fold(t)
	}
	return strings.Join(toks, " ")
}

func fold(t string) string {
	if utf8.RuneCountInString(t) > 3 && strings.HasSuffix(t, "s") {
		t = t[:len(t)-1]
	}
	if a, ok := aliases[t]; ok {
		return a
	}
	return t
}

// Distance is the Levenshtein edit distance, counted in characters (runes).
func Distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// AllowedTypos is how many typos a word of n characters may have:
// under 4 letters none, 4 to 7 letters one, 8 or more letters two.
func AllowedTypos(n int) int {
	switch {
	case n < 4:
		return 0
	case n < 8:
		return 1
	default:
		return 2
	}
}

// Near reports whether two normalised names are the same or differ by an allowed typo.
// The shorter name decides how many typos are allowed.
func Near(a, b string) bool {
	if a == b {
		return true
	}
	n := min(utf8.RuneCountInString(a), utf8.RuneCountInString(b))
	return Distance(a, b) <= AllowedTypos(n)
}

// Matches reports whether a normalised name matches a normalised search text: the text
// is contained in the name, or every search word starts a name word or is a near typo of one.
func Matches(name, query string) bool {
	if query == "" || strings.Contains(name, query) {
		return true
	}
	if Near(name, query) {
		return true
	}
	nameToks := strings.Fields(name)
	for _, qt := range strings.Fields(query) {
		found := false
		for _, nt := range nameToks {
			if strings.HasPrefix(nt, qt) || Near(nt, qt) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
