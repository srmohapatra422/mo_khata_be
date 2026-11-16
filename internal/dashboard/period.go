package dashboard

import (
	"math"
	"time"
)

// Periods the dashboard understands.
const (
	Today = "today"
	Week  = "week"
	Month = "month"
)

// Range is the current period and the earlier period it is compared with.
// Both ranges are half open: [Start, End) and [PrevStart, PrevEnd).
type Range struct {
	Start, End         time.Time
	PrevStart, PrevEnd time.Time
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// Ranges works out the period for "now". now must already be in the shop's timezone, so
// "today" starts at the shop's midnight, never UTC midnight.
//
//	today: 00:00 to now, compared with yesterday up to the same clock time
//	week:  Monday 00:00 to now, compared with last week's same elapsed span
//	month: the 1st 00:00 to now, compared with last month's same elapsed span
//
// The previous range never runs past the start of the current one (for example 31 March against
// a 28 day February).
func Ranges(period string, now time.Time) (Range, bool) {
	var start, prevStart time.Time
	switch period {
	case Today:
		start = midnight(now)
		prevStart = start.AddDate(0, 0, -1)
	case Week:
		weekday := (int(now.Weekday()) + 6) % 7 // Monday = 0
		start = midnight(now).AddDate(0, 0, -weekday)
		prevStart = start.AddDate(0, 0, -7)
	case Month:
		y, m, _ := now.Date()
		start = time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
		prevStart = start.AddDate(0, -1, 0)
	default:
		return Range{}, false
	}
	prevEnd := prevStart.Add(now.Sub(start))
	if prevEnd.After(start) {
		prevEnd = start
	}
	return Range{Start: start, End: now, PrevStart: prevStart, PrevEnd: prevEnd}, true
}

func daysInMonth(t time.Time) int {
	y, m, _ := t.Date()
	return time.Date(y, m+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// Labels are the chart buckets for a period. Every bucket is always returned.
func Labels(period string, daysInMonth int) []string {
	switch period {
	case Today:
		return []string{"8a", "10a", "12p", "2p", "4p", "6p", "8p"}
	case Week:
		return []string{"M", "T", "W", "T", "F", "S", "S"}
	default:
		l := []string{"W1", "W2", "W3", "W4"}
		if daysInMonth >= 29 {
			l = append(l, "W5")
		}
		return l
	}
}

// BucketIndex says which chart bucket a bill belongs to. t must be in the shop's timezone.
//
//	today: two hour buckets from 8am. Before 8am goes in the first, 8pm and later in the last.
//	week:  Monday = 0 ... Sunday = 6
//	month: days 1-7 are W1, 8-14 W2, 15-21 W3, 22-28 W4, 29 to the end W5
func BucketIndex(period string, t time.Time) int {
	switch period {
	case Today:
		h := t.Hour()
		if h < 8 {
			return 0
		}
		return min((h-8)/2, 6)
	case Week:
		return (int(t.Weekday()) + 6) % 7
	default:
		return min((t.Day()-1)/7, 4)
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Growth is the percent change from previous to current, rounded to a whole number
// (halves round away from zero). It is nil when there was nothing in the previous period.
func Growth(current, previous float64) *int {
	if previous == 0 {
		return nil
	}
	g := int(math.Round((current - previous) / previous * 100))
	return &g
}

type Best struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

type Chart struct {
	Labels  []string  `json:"labels"`
	Amounts []float64 `json:"amounts"`
	Best    *Best     `json:"best"`
}

// BuildChart puts per-bucket sums (index -> amount) into a chart with every bucket present.
// Best is the highest bucket (the first one on a tie) and nil when every bucket is 0.
func BuildChart(period string, daysInMonth int, sums map[int]float64) Chart {
	labels := Labels(period, daysInMonth)
	amounts := make([]float64, len(labels))
	for i, v := range sums {
		if i >= 0 && i < len(amounts) {
			amounts[i] += v
		}
	}
	c := Chart{Labels: labels, Amounts: amounts}
	bestIdx := -1
	for i, a := range amounts {
		amounts[i] = round2(a)
		if amounts[i] > 0 && (bestIdx < 0 || amounts[i] > amounts[bestIdx]) {
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		c.Best = &Best{Label: labels[bestIdx], Amount: amounts[bestIdx]}
	}
	return c
}
