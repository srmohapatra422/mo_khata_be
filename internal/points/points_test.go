package points

import (
	"reflect"
	"testing"
	"time"
)

func d(s string) *time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return &t
}

func TestRemaining_FIFO(t *testing.T) {
	earns := []Earn{{100, nil}, {50, nil}, {30, nil}}
	tests := []struct {
		name string
		out  int
		want []int
	}{
		{"nothing used", 0, []int{100, 50, 30}},
		{"inside the first", 40, []int{60, 50, 30}},
		{"exactly the first", 100, []int{0, 50, 30}},
		{"spills into the second", 120, []int{0, 30, 30}},
		{"uses everything", 180, []int{0, 0, 0}},
		{"more than there is", 500, []int{0, 0, 0}},
		{"negative is ignored", -5, []int{100, 50, 30}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Remaining(earns, tt.out); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestExpiringSoon(t *testing.T) {
	today := time.Date(2026, 11, 20, 23, 0, 0, 0, time.FixedZone("IST", 19800))
	earns := []Earn{
		{100, d("2026-11-25")}, // oldest, expires in 5 days
		{50, d("2026-12-20")},  // exactly 30 days out
		{40, d("2026-12-21")},  // 31 days: outside
		{10, nil},              // never
		{70, d("2026-11-19")},  // already past
	}
	tests := []struct {
		name      string
		out       int
		wantPts   int
		wantFirst string
	}{
		{"nothing used", 0, 100 + 50 + 70*0, "2026-11-25"}, // the past row is the sweep's job, not "soon"
		{"first partly used", 30, 70 + 50, "2026-11-25"},
		{"first fully used", 100, 50, "2026-12-20"},
		{"all used", 1000, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pts, first := ExpiringSoon(earns, tt.out, today, 30)
			if pts != tt.wantPts {
				t.Fatalf("points %d want %d", pts, tt.wantPts)
			}
			if tt.wantFirst == "" {
				if first != nil {
					t.Fatalf("want nil date, got %v", first)
				}
			} else if first == nil || first.Format("2006-01-02") != tt.wantFirst {
				t.Fatalf("date %v want %s", first, tt.wantFirst)
			}
		})
	}
}
