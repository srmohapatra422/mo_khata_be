package voice

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"mohisab-api/internal/names"
)

// RuleParser understands Hinglish (and English, Hindi and Odia) number words, units, rupee words
// and item names, by rules. It needs no network and no model.
type RuleParser struct{}

type kind int

const (
	kWord kind = iota
	kNum
	kMult
	kUnit
	kRupee
	kRate
	kBreak
	kStop
)

type token struct {
	kind     kind
	text     string  // as spoken
	val      float64 // kNum, kMult
	unit     unitInfo
	isDigits bool // the number was written with digits
}

var thousands = regexp.MustCompile(`(\d),(\d{3})`)

// ---------- tokenising ----------

func tokenize(s string) []token {
	s = asciiDigits(s)
	for i := 0; i < 3; i++ { // 1,23,45,678 style groupings
		s = thousands.ReplaceAllString(s, "$1$2")
	}
	var out []token
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, classify(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case unicode.IsSpace(r):
			flush()
			if r == '\n' {
				out = append(out, token{kind: kBreak})
			}
		case r == ',' || r == ';' || r == '!' || r == '?' || r == '।' || r == ':':
			flush()
			out = append(out, token{kind: kBreak})
		case r == '.':
			prevDigit := i > 0 && unicode.IsDigit(rs[i-1])
			nextDigit := i+1 < len(rs) && unicode.IsDigit(rs[i+1])
			if prevDigit && nextDigit {
				cur = append(cur, r)
				continue
			}
			flush()
			if i+1 >= len(rs) || unicode.IsSpace(rs[i+1]) {
				out = append(out, token{kind: kBreak})
			}
		case r == '₹':
			flush()
			out = append(out, token{kind: kRupee, text: "₹"})
		case unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r):
			// "30kg" is "30" and "kg"
			if n := len(cur); n > 0 && !unicode.IsMark(r) {
				last := cur[n-1]
				// the dot of a decimal ("12.5") belongs to the number
				if (unicode.IsDigit(last) || last == '.') != unicode.IsDigit(r) {
					flush()
				}
			}
			cur = append(cur, r)
		default: // / - ( ) " ' and so on
			flush()
		}
	}
	flush()
	return mergeNumbers(out)
}

func classify(text string) token {
	lw := strings.ToLower(text)
	if v, err := strconv.ParseFloat(lw, 64); err == nil && unicode.IsDigit([]rune(lw)[0]) {
		return token{kind: kNum, text: text, val: v, isDigits: true}
	}
	switch {
	case rupeeWords[lw]:
		return token{kind: kRupee, text: text}
	case rateWords[lw]:
		return token{kind: kRate, text: text}
	case breakWords[lw]:
		return token{kind: kBreak, text: text}
	}
	if v, ok := multiplierWords[lw]; ok {
		return token{kind: kMult, text: text, val: v}
	}
	if v, ok := numberWords[lw]; ok {
		return token{kind: kNum, text: text, val: v}
	}
	if u, ok := unitWords[lw]; ok {
		return token{kind: kUnit, text: text, unit: u}
	}
	if stopWords[lw] {
		return token{kind: kStop, text: text}
	}
	return token{kind: kWord, text: text}
}

// mergeNumbers joins number words into one number: "do sau pachas" = 250, "ek hazaar" = 1000,
// "dhai sau" = 250, "2 sau" = 200, "twenty five" = 25. Two plain numbers next to each other
// stay separate ("10 20 kilo" is two numbers) unless a multiplier or a tens + units pair joins them.
func mergeNumbers(in []token) []token {
	var out []token
	for i := 0; i < len(in); {
		t := in[i]
		if t.kind != kNum && t.kind != kMult {
			out = append(out, t)
			i++
			continue
		}
		var total, current float64
		var parts []string
		prev := token{kind: kWord}
		j := i
		for ; j < len(in); j++ {
			tj := in[j]
			if tj.kind == kNum {
				if j > i {
					tensThenUnit := prev.kind == kNum && !prev.isDigits && !tj.isDigits &&
						prev.val >= 20 && prev.val < 100 && int(prev.val)%10 == 0 && tj.val >= 1 && tj.val <= 9
					if prev.kind != kMult && !tensThenUnit {
						break
					}
				}
				current += tj.val
			} else if tj.kind == kMult {
				if j > i && prev.kind == kMult {
					break
				}
				if current == 0 {
					current = 1
				}
				if tj.val >= 1000 {
					total += current * tj.val
					current = 0
				} else {
					current *= tj.val
				}
			} else {
				break
			}
			parts = append(parts, tj.text)
			prev = tj
		}
		if j == i+1 && t.kind == kNum {
			out = append(out, t)
		} else {
			out = append(out, token{kind: kNum, text: strings.Join(parts, " "), val: total + current})
		}
		i = j
	}
	return out
}

// ---------- mentions ----------

type mention struct {
	rate       bool
	val        float64
	unit       string // quantity mentions only; "" when no unit was said
	start, end int    // token positions, inclusive
}

// findMentions walks one segment (no "aur" or comma inside) and finds quantities and rates.
func findMentions(seg []token, mode string) []mention {
	var ms []mention
	for k := 0; k < len(seg); {
		t := seg[k]
		switch {
		case t.kind == kNum && k+1 < len(seg) && seg[k+1].kind == kRupee: // "140 rupaye (kilo)"
			end := k + 1
			if end+1 < len(seg) && seg[end+1].kind == kUnit {
				end++
			}
			ms = append(ms, mention{rate: true, val: t.val, start: k, end: end})
			k = end + 1
		case (t.kind == kRupee || t.kind == kRate) && k+1 < len(seg) && seg[k+1].kind == kNum: // "rs 140", "rate 140"
			end := k + 1
			if end+1 < len(seg) && seg[end+1].kind == kUnit {
				end++
			}
			ms = append(ms, mention{rate: true, val: seg[k+1].val, start: k, end: end})
			k = end + 1
		case t.kind == kNum && mode != ModeExpense && k+1 < len(seg) && seg[k+1].kind == kUnit: // "30 kilo"
			u := seg[k+1].unit
			ms = append(ms, mention{val: t.val * u.factor, unit: u.unit, start: k, end: k + 1})
			k += 2
		case t.kind == kNum && mode != ModeExpense: // "10 ande"
			ms = append(ms, mention{val: t.val, start: k, end: k})
			k++
		default:
			k++
		}
	}
	return ms
}

// gapWords are the item-name words between two token positions (exclusive).
func gapWords(seg []token, from, to int) []string {
	var w []string
	for i := from + 1; i < to && i < len(seg); i++ {
		if seg[i].kind == kWord {
			w = append(w, seg[i].text)
		}
	}
	return w
}

// ---------- parse ----------

func (RuleParser) Parse(_ context.Context, in Input) (Output, error) {
	toks := tokenize(in.Transcript)
	var segments [][]token
	var cur []token
	for _, t := range toks {
		if t.kind == kBreak {
			if len(cur) > 0 {
				segments = append(segments, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		segments = append(segments, cur)
	}

	if in.Mode == ModeExpense {
		return Output{Expenses: parseExpenses(segments)}, nil
	}

	type pendingRate struct {
		words []string
		rate  float64
	}
	var items []Item
	var pending []pendingRate
	nameFirst, styleKnown := false, false

	for _, seg := range segments {
		ms := findMentions(seg, in.Mode)
		var qty []int // indexes into ms
		for i, m := range ms {
			if !m.rate {
				qty = append(qty, i)
			}
		}
		if len(qty) == 0 {
			// Only rates here ("rohu 140 rupaye kilo"): they belong to an item named elsewhere.
			var words []string
			for _, t := range seg {
				if t.kind == kWord {
					words = append(words, t.text)
				}
			}
			for _, m := range ms {
				if m.rate && len(words) > 0 {
					pending = append(pending, pendingRate{words, m.val})
				}
			}
			continue
		}

		// gap before mention i, and after it
		before := func(i int) []string {
			if i == 0 {
				return gapWords(seg, -1, ms[0].start)
			}
			return gapWords(seg, ms[i-1].end, ms[i].start)
		}
		after := func(i int) []string {
			if i == len(ms)-1 {
				return gapWords(seg, ms[i].end, len(seg))
			}
			return gapWords(seg, ms[i].end, ms[i+1].start)
		}
		if !styleKnown {
			// The first quantity decides: words before it mean "name, then quantity" (rohu 30 kilo).
			nameFirst = len(before(qty[0])) > 0
			styleKnown = true
		}
		// A gap of words is claimed by the first quantity that wants it.
		claimedBefore := map[int]bool{} // gap before mention i
		claimedAfter := map[int]bool{}  // gap after mention i
		segItems := make([]*Item, len(ms))
		for _, i := range qty {
			var words []string
			take := func(preferBefore bool) {
				order := []bool{preferBefore, !preferBefore}
				for _, b := range order {
					if b {
						// the gap before i is the same gap as "after i-1"
						if claimedBefore[i] || (i > 0 && claimedAfter[i-1]) {
							continue
						}
						if w := before(i); len(w) > 0 {
							words = w
							claimedBefore[i] = true
							return
						}
					} else {
						if claimedAfter[i] || (i+1 < len(ms) && claimedBefore[i+1]) {
							continue
						}
						if w := after(i); len(w) > 0 {
							words = w
							claimedAfter[i] = true
							return
						}
					}
				}
			}
			take(nameFirst)
			if len(words) == 0 {
				continue // a quantity with no item name is useless
			}
			it := &Item{Heard: strings.Join(words, " "), Quantity: ms[i].val}
			if ms[i].unit != "" {
				u := ms[i].unit
				it.Unit = &u
			}
			segItems[i] = it
		}
		// Attach each rate to the nearest quantity before it, else the first one after it.
		for i, m := range ms {
			if !m.rate {
				continue
			}
			var target *Item
			for j := i - 1; j >= 0 && target == nil; j-- {
				target = segItems[j]
			}
			for j := i + 1; j < len(ms) && target == nil; j++ {
				target = segItems[j]
			}
			if target != nil && target.Rate == nil {
				v := m.val
				target.Rate = &v
			}
		}
		for _, it := range segItems {
			if it != nil {
				items = append(items, *it)
			}
		}
	}

	// A rate said on its own ("rohu 140 rupaye kilo") goes to the item with that name.
	for _, p := range pending {
		pn := names.NormalizeName(strings.Join(p.words, " "))
		for i := range items {
			if items[i].Rate == nil && sameName(names.NormalizeName(items[i].Heard), pn) {
				v := p.rate
				items[i].Rate = &v
				break
			}
		}
	}

	out := items[:0]
	for _, it := range items {
		if it.Quantity <= 0 {
			continue
		}
		matchCatalog(&it, in)
		out = append(out, it)
	}
	return Output{Items: out}, nil
}

func sameName(a, b string) bool { return a != "" && b != "" && (a == b || names.Near(a, b)) }

func parseExpenses(segments [][]token) []ExpenseItem {
	var out []ExpenseItem
	for _, seg := range segments {
		amount, found := 0.0, false
		var words []string
		for _, t := range seg {
			switch t.kind {
			case kNum:
				if !found {
					amount, found = t.val, true
				}
			case kWord:
				words = append(words, t.text)
			}
		}
		if !found || amount <= 0 {
			continue
		}
		heard := strings.Join(words, " ")
		name := "Expense"
		if heard != "" {
			name = capitalize(heard)
		}
		out = append(out, ExpenseItem{Name: name, Heard: heard, Amount: amount})
	}
	return out
}

// ---------- matching the shop's own items ----------

func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

func tokenSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, t := range strings.Fields(s) {
		m[t] = true
	}
	return m
}

// score says how well a spoken name fits a shop item (lower is better, -1 = no fit):
// 0 same name, 1 every spoken word is a word of the item name ("rohu" in "Rohu fish"),
// 2 a near typo of the whole name, 3 words that start or nearly match.
func score(spoken, item string) int {
	switch {
	case spoken == item:
		return 0
	case spoken == "":
		return -1
	}
	items := tokenSet(item)
	all := true
	for t := range tokenSet(spoken) {
		if !items[t] {
			all = false
			break
		}
	}
	switch {
	case all:
		return 1
	case names.Near(item, spoken):
		return 2
	case names.Matches(item, spoken):
		return 3
	}
	return -1
}

func matchCatalog(it *Item, in Input) {
	spoken := names.NormalizeName(it.Heard)
	type cand struct {
		c     CatalogItem
		score int
		words int
	}
	var cands []cand
	for _, c := range in.Catalog {
		n := names.NormalizeName(c.Name)
		if s := score(spoken, n); s >= 0 {
			cands = append(cands, cand{c, s, len(strings.Fields(n))})
		}
	}
	if len(cands) == 0 {
		it.Name = capitalize(it.Heard)
		return
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		if cands[i].words != cands[j].words {
			return cands[i].words < cands[j].words
		}
		return cands[i].c.Name < cands[j].c.Name
	})
	best := cands[0].c
	it.Name, it.Matched = best.Name, true
	if it.Unit == nil && best.Unit != "" {
		u := best.Unit
		it.Unit = &u
	}
	// A sale with no price said uses the shop's price.
	if in.Mode == ModeSale && it.Rate == nil && best.Price != nil {
		p := *best.Price
		it.Rate = &p
	}
}
