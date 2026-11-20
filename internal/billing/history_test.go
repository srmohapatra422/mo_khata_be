package billing

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var ist = time.FixedZone("IST", 5*3600+1800)

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 19, 18, 40, 5, 123456000, ist)
	id := uuid.New()
	gotT, gotID, err := decodeCursor(encodeCursor(at, id))
	if err != nil || !gotT.Equal(at) || gotID != id {
		t.Fatalf("got %v %v %v", gotT, gotID, err)
	}
	for _, bad := range []string{"", "abc", "!!!", encodeCursor(at, id)[:10]} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestDateRange(t *testing.T) {
	tests := []struct {
		name, from, to string
		ok             bool
		wantEnd        string // exclusive end, shop timezone
	}{
		{"nothing", "", "", true, ""},
		{"only from", "2026-11-01", "", true, ""},
		{"to is inclusive", "2026-11-01", "2026-11-01", true, "2026-11-02"},
		{"one year", "2026-01-01", "2026-12-31", true, "2027-01-01"},
		{"366 days", "2024-01-01", "2024-12-31", true, "2025-01-01"}, // leap year
		{"367 days", "2024-01-01", "2025-01-01", false, ""},
		{"to before from", "2026-11-05", "2026-11-04", false, ""},
		{"bad from", "01-11-2026", "", false, ""},
		{"bad to", "", "tomorrow", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, e, err := DateRange(tt.from, tt.to, ist)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				return
			}
			if tt.from != "" && s.Format("2006-01-02 15:04") != tt.from+" 00:00" {
				t.Errorf("start %v", s)
			}
			if tt.wantEnd == "" && e != nil {
				t.Errorf("end should be open, got %v", e)
			}
			if tt.wantEnd != "" && e.Format("2006-01-02") != tt.wantEnd {
				t.Errorf("end %v want %s", e, tt.wantEnd)
			}
		})
	}
}
