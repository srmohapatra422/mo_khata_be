package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

var ist = time.FixedZone("IST", 5*3600+1800)

func at(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, ist)
}

func TestRanges(t *testing.T) {
	tests := []struct {
		name   string
		period string
		now    time.Time
		want   Range
	}{
		{"today afternoon", Today, at(2026, 11, 16, 15, 30, 0),
			Range{at(2026, 11, 16, 0, 0, 0), at(2026, 11, 16, 15, 30, 0), at(2026, 11, 15, 0, 0, 0), at(2026, 11, 15, 15, 30, 0)}},
		{"today across new year", Today, at(2026, 1, 1, 10, 0, 0),
			Range{at(2026, 1, 1, 0, 0, 0), at(2026, 1, 1, 10, 0, 0), at(2025, 12, 31, 0, 0, 0), at(2025, 12, 31, 10, 0, 0)}},
		{"week on Monday", Week, at(2026, 11, 16, 10, 0, 0),
			Range{at(2026, 11, 16, 0, 0, 0), at(2026, 11, 16, 10, 0, 0), at(2026, 11, 9, 0, 0, 0), at(2026, 11, 9, 10, 0, 0)}},
		{"week on Wednesday", Week, at(2026, 11, 18, 12, 0, 0),
			Range{at(2026, 11, 16, 0, 0, 0), at(2026, 11, 18, 12, 0, 0), at(2026, 11, 9, 0, 0, 0), at(2026, 11, 11, 12, 0, 0)}},
		{"week on Sunday night", Week, at(2026, 11, 22, 23, 0, 0),
			Range{at(2026, 11, 16, 0, 0, 0), at(2026, 11, 22, 23, 0, 0), at(2026, 11, 9, 0, 0, 0), at(2026, 11, 15, 23, 0, 0)}},
		{"week across new year", Week, at(2027, 1, 1, 8, 0, 0), // a Friday
			Range{at(2026, 12, 28, 0, 0, 0), at(2027, 1, 1, 8, 0, 0), at(2026, 12, 21, 0, 0, 0), at(2026, 12, 25, 8, 0, 0)}},
		{"month mid", Month, at(2026, 11, 16, 9, 0, 0),
			Range{at(2026, 11, 1, 0, 0, 0), at(2026, 11, 16, 9, 0, 0), at(2026, 10, 1, 0, 0, 0), at(2026, 10, 16, 9, 0, 0)}},
		{"month, first seconds", Month, at(2026, 12, 1, 0, 0, 5),
			Range{at(2026, 12, 1, 0, 0, 0), at(2026, 12, 1, 0, 0, 5), at(2026, 11, 1, 0, 0, 0), at(2026, 11, 1, 0, 0, 5)}},
		{"month, previous month is shorter (31 Mar vs Feb)", Month, at(2026, 3, 31, 12, 0, 0),
			Range{at(2026, 3, 1, 0, 0, 0), at(2026, 3, 31, 12, 0, 0), at(2026, 2, 1, 0, 0, 0), at(2026, 3, 1, 0, 0, 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Ranges(tt.period, tt.now)
			if !ok {
				t.Fatal("period not recognised")
			}
			for name, pair := range map[string][2]time.Time{
				"Start": {got.Start, tt.want.Start}, "End": {got.End, tt.want.End},
				"PrevStart": {got.PrevStart, tt.want.PrevStart}, "PrevEnd": {got.PrevEnd, tt.want.PrevEnd},
			} {
				if !pair[0].Equal(pair[1]) {
					t.Errorf("%s = %s, want %s", name, pair[0], pair[1])
				}
			}
			if got.PrevEnd.After(got.Start) {
				t.Errorf("previous range runs past the start of the current one")
			}
		})
	}
}

func TestRanges_UsesShopMidnightNotUTC(t *testing.T) {
	// 00:30 IST on the 16th is still the 15th in UTC. "Today" must start at IST midnight.
	now := at(2026, 11, 16, 0, 30, 0)
	got, _ := Ranges(Today, now.UTC().In(ist))
	want := time.Date(2026, 11, 15, 18, 30, 0, 0, time.UTC)
	if !got.Start.Equal(want) {
		t.Errorf("Start = %s, want %s", got.Start.UTC(), want)
	}
}

func TestRanges_UnknownPeriod(t *testing.T) {
	for _, p := range []string{"", "year", "TODAY", "yesterday"} {
		if _, ok := Ranges(p, at(2026, 11, 16, 1, 0, 0)); ok {
			t.Errorf("Ranges(%q) accepted", p)
		}
	}
}

func TestBucketIndex_Today(t *testing.T) {
	want := map[int]int{0: 0, 5: 0, 7: 0, 8: 0, 9: 0, 10: 1, 11: 1, 12: 2, 13: 2, 14: 3, 16: 4, 18: 5, 19: 5, 20: 6, 21: 6, 22: 6, 23: 6}
	for h, idx := range want {
		if got := BucketIndex(Today, at(2026, 11, 16, h, 59, 0)); got != idx {
			t.Errorf("hour %d -> bucket %d, want %d", h, got, idx)
		}
	}
}

func TestBucketIndex_Week(t *testing.T) {
	// 16 Nov 2026 is a Monday.
	for i := 0; i < 7; i++ {
		if got := BucketIndex(Week, at(2026, 11, 16+i, 13, 0, 0)); got != i {
			t.Errorf("weekday %d -> bucket %d", i, got)
		}
	}
}

func TestBucketIndex_Month(t *testing.T) {
	want := map[int]int{1: 0, 7: 0, 8: 1, 14: 1, 15: 2, 21: 2, 22: 3, 28: 3, 29: 4, 30: 4, 31: 4}
	for day, idx := range want {
		if got := BucketIndex(Month, at(2026, 12, day, 9, 0, 0)); got != idx {
			t.Errorf("day %d -> bucket %d, want %d", day, got, idx)
		}
	}
}

func TestLabels(t *testing.T) {
	if got := strings.Join(Labels(Today, 30), ","); got != "8a,10a,12p,2p,4p,6p,8p" {
		t.Errorf("today labels = %s", got)
	}
	if got := strings.Join(Labels(Week, 30), ","); got != "M,T,W,T,F,S,S" {
		t.Errorf("week labels = %s", got)
	}
	for days, want := range map[int]string{28: "W1,W2,W3,W4", 29: "W1,W2,W3,W4,W5", 30: "W1,W2,W3,W4,W5", 31: "W1,W2,W3,W4,W5"} {
		if got := strings.Join(Labels(Month, days), ","); got != want {
			t.Errorf("month labels for %d days = %s, want %s", days, got, want)
		}
	}
}

func TestDaysInMonth(t *testing.T) {
	for _, tt := range []struct {
		t    time.Time
		want int
	}{
		{at(2026, 2, 10, 0, 0, 0), 28}, {at(2028, 2, 10, 0, 0, 0), 29}, {at(2026, 4, 1, 0, 0, 0), 30}, {at(2026, 12, 31, 0, 0, 0), 31},
	} {
		if got := daysInMonth(tt.t); got != tt.want {
			t.Errorf("daysInMonth(%s) = %d, want %d", tt.t.Format("2006-01"), got, tt.want)
		}
	}
}

func TestGrowth(t *testing.T) {
	i := func(v int) *int { return &v }
	tests := []struct {
		name      string
		cur, prev float64
		want      *int
	}{
		{"example from the spec", 112600, 107200, i(5)},
		{"previous is zero", 100, 0, nil},
		{"both zero", 0, 0, nil},
		{"fell by half", 50, 100, i(-50)},
		{"rose by half", 150, 100, i(50)},
		{"no change", 100, 100, i(0)},
		{"all gone", 0, 100, i(-100)},
		{"rounds half away from zero (down)", 103, 200, i(-49)}, // -48.5
		{"rounds half away from zero (up)", 201, 200, i(1)},     // 0.5
		{"doubled", 200, 100, i(100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Growth(tt.cur, tt.prev)
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("got %d, want nil", *got)
			case tt.want != nil && (got == nil || *got != *tt.want):
				t.Errorf("got %v, want %d", got, *tt.want)
			}
		})
	}
}

func TestBuildChart_WeekExample(t *testing.T) {
	sums := map[int]float64{0: 13500, 1: 12700, 2: 15000, 3: 12500, 4: 16900, 5: 21800, 6: 20200}
	c := BuildChart(Week, 30, sums)
	want := []float64{13500, 12700, 15000, 12500, 16900, 21800, 20200}
	for i, v := range want {
		if c.Amounts[i] != v {
			t.Errorf("amount[%d] = %v, want %v", i, c.Amounts[i], v)
		}
	}
	if c.Best == nil || c.Best.Label != "S" || c.Best.Amount != 21800 {
		t.Errorf("best = %+v, want S 21800", c.Best)
	}
}

func TestBuildChart_EveryBucketPresentAndBestNilWhenEmpty(t *testing.T) {
	for _, p := range []string{Today, Week, Month} {
		c := BuildChart(p, 30, nil)
		if len(c.Amounts) != len(c.Labels) || len(c.Labels) == 0 {
			t.Errorf("%s: %d amounts for %d labels", p, len(c.Amounts), len(c.Labels))
		}
		for _, a := range c.Amounts {
			if a != 0 {
				t.Errorf("%s: amount %v, want 0", p, a)
			}
		}
		if c.Best != nil {
			t.Errorf("%s: best = %+v, want nil", p, c.Best)
		}
	}
}

func TestBuildChart_TieTakesFirst_IgnoresOutOfRange_Rounds(t *testing.T) {
	c := BuildChart(Week, 30, map[int]float64{1: 500, 4: 500, 9: 99999, -1: 5, 0: 10.004})
	if c.Best == nil || c.Best.Label != "T" {
		t.Errorf("best = %+v, want the first of the tied buckets (T)", c.Best)
	}
	if c.Amounts[0] != 10 {
		t.Errorf("amount[0] = %v, want 10 (rounded)", c.Amounts[0])
	}
	for i, a := range c.Amounts {
		if i != 0 && i != 1 && i != 4 && a != 0 {
			t.Errorf("out of range index leaked into bucket %d", i)
		}
	}
}

func TestBuildChart_Month28DaysHasFourBuckets(t *testing.T) {
	if c := BuildChart(Month, 28, nil); len(c.Labels) != 4 {
		t.Errorf("got %d buckets, want 4", len(c.Labels))
	}
}

func TestGet_Validation400(t *testing.T) {
	h := Handler{}
	for _, q := range []string{"?period=year", "?period=TODAY", "?period=week&location_id=nope"} {
		req := httptest.NewRequest("GET", "/v1/dashboard"+q, nil)
		req = req.WithContext(auth.WithMember(req.Context(), auth.Member{ShopID: uuid.New(), Role: "owner"}))
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: status %d body %s, want 400 with an error", q, rec.Code, rec.Body)
		}
	}
}
