package shoptime

import (
	"net/http"
	"testing"
	"time"

	"mohisab-api/internal/httpx"
)

var ist = time.FixedZone("IST", 5*3600+1800)

func TestDayRange(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 30, 0, 0, ist) // 00:30 IST = still the 15th in UTC
	tests := []struct {
		name, in  string
		wantDay   string
		wantStart time.Time
		wantErr   string
	}{
		{"empty means today in the shop timezone", "", "2026-11-16", time.Date(2026, 11, 15, 18, 30, 0, 0, time.UTC), ""},
		{"today", "2026-11-16", "2026-11-16", time.Date(2026, 11, 15, 18, 30, 0, 0, time.UTC), ""},
		{"yesterday", "2026-11-15", "2026-11-15", time.Date(2026, 11, 14, 18, 30, 0, 0, time.UTC), ""},
		{"far past", "2020-02-29", "2020-02-29", time.Date(2020, 2, 28, 18, 30, 0, 0, time.UTC), ""},
		{"tomorrow", "2026-11-17", "", time.Time{}, "date cannot be in the future"},
		{"wrong format", "16-11-2026", "", time.Time{}, "date must be YYYY-MM-DD"},
		{"not a date", "yesterday", "", time.Time{}, "date must be YYYY-MM-DD"},
		{"impossible date", "2026-02-30", "", time.Time{}, "date must be YYYY-MM-DD"},
		{"with time", "2026-11-16T10:00:00", "", time.Time{}, "date must be YYYY-MM-DD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, day, err := DayRange(tt.in, now)
			if tt.wantErr != "" {
				he, ok := err.(*httpx.Err)
				if !ok || he.Status != http.StatusBadRequest || he.Msg != tt.wantErr {
					t.Fatalf("err = %v, want 400 %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if day != tt.wantDay || !start.Equal(tt.wantStart) {
				t.Errorf("day = %s start = %s, want %s %s", day, start.UTC(), tt.wantDay, tt.wantStart)
			}
			if !end.Equal(start.AddDate(0, 0, 1)) {
				t.Errorf("end = %s, want one day after start", end)
			}
		})
	}
}
