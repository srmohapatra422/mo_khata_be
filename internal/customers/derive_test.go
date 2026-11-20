package customers

import (
	"testing"
	"time"
)

var ist = time.FixedZone("IST", 5*3600+1800)

func tp(t time.Time) *time.Time { return &t }

func TestLastVisitDays(t *testing.T) {
	// 00:30 IST on the 20th is still the 19th in UTC.
	now := time.Date(2026, 11, 20, 0, 30, 0, 0, ist)
	tests := []struct {
		name string
		last *time.Time
		want *int
	}{
		{"never", nil, nil},
		{"same day", tp(time.Date(2026, 11, 20, 0, 5, 0, 0, ist)), ip(0)},
		// 23:50 IST on the 19th: 40 minutes ago but a different calendar day.
		{"just before midnight is yesterday", tp(time.Date(2026, 11, 19, 23, 50, 0, 0, ist)), ip(1)},
		{"given in UTC, still shop days", tp(time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)), ip(0)}, // 01:30 IST on the 20th
		{"a week", tp(time.Date(2026, 11, 13, 12, 0, 0, 0, ist)), ip(7)},
		{"clock ahead", tp(time.Date(2026, 11, 21, 9, 0, 0, 0, ist)), ip(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LastVisitDays(tt.last, now)
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("got %v want %v", deref(got), deref(tt.want))
			}
		})
	}
}

func ip(i int) *int { return &i }
func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestMonthRange(t *testing.T) {
	tests := []struct {
		name       string
		now        time.Time
		start, end time.Time
	}{
		{"mid month", time.Date(2026, 11, 20, 10, 0, 0, 0, ist), time.Date(2026, 11, 1, 0, 0, 0, 0, ist), time.Date(2026, 12, 1, 0, 0, 0, 0, ist)},
		{"last second of the month", time.Date(2026, 11, 30, 23, 59, 59, 0, ist), time.Date(2026, 11, 1, 0, 0, 0, 0, ist), time.Date(2026, 12, 1, 0, 0, 0, 0, ist)},
		{"first minute of the month", time.Date(2026, 12, 1, 0, 1, 0, 0, ist), time.Date(2026, 12, 1, 0, 0, 0, 0, ist), time.Date(2027, 1, 1, 0, 0, 0, 0, ist)},
		{"december rolls the year", time.Date(2026, 12, 31, 12, 0, 0, 0, ist), time.Date(2026, 12, 1, 0, 0, 0, 0, ist), time.Date(2027, 1, 1, 0, 0, 0, 0, ist)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, e := MonthRange(tt.now)
			if !s.Equal(tt.start) || !e.Equal(tt.end) {
				t.Fatalf("got %v..%v want %v..%v", s, e, tt.start, tt.end)
			}
		})
	}
	// 00:30 IST on 1 Dec is still 30 Nov in UTC: the shop month must already be December.
	s, _ := MonthRange(time.Date(2026, 12, 1, 0, 30, 0, 0, ist))
	if s.Month() != time.December {
		t.Fatalf("month = %v, want December", s.Month())
	}
}

func TestTier(t *testing.T) {
	const gold, silver = 50000, 20000
	tests := []struct {
		name     string
		business bool
		spend    float64
		want     string
	}{
		{"none", false, 0, TierRegular},
		{"just under silver", false, 19999.99, TierRegular},
		{"silver edge", false, 20000, TierSilver},
		{"just under gold", false, 49999.99, TierSilver},
		{"gold edge", false, 50000, TierGold},
		{"business beats spend", true, 90000, TierBusiness},
		{"business with no spend", true, 0, TierBusiness},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Tier(tt.business, tt.spend, gold, silver); got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestDue(t *testing.T) {
	tests := []struct {
		name         string
		billed, paid float64
		want         float64
	}{
		{"no credit", 0, 0, 0},
		{"nothing paid", 800, 0, 800},
		{"partial", 800, 500, 300},
		{"fully paid", 800, 800, 0},
		{"overpaid never negative", 800, 900, 0},
		{"float noise", 0.3, 0.1 + 0.2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Due(tt.billed, tt.paid); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
