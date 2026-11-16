package closeday

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func TestExpectedCash(t *testing.T) {
	tests := []struct {
		name                                          string
		cashBills, creditCash, float_, expenses, want float64
	}{
		{"cash bills only", 9000, 0, 0, 0, 9000},
		{"plus credit collected in cash", 9000, 800, 0, 0, 9800},
		{"minus expenses", 9000, 800, 0, 600, 9200},
		{"with an opening float", 9000, 0, 500, 0, 9500},
		{"expenses larger than cash is negative", 100, 0, 0, 250, -150},
		{"rounds to paisa", 100.004, 0.003, 0, 0, 100.01},
		{"nothing happened", 0, 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExpectedCash(tt.cashBills, tt.creditCash, tt.float_, tt.expenses); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDifferenceAndStatus(t *testing.T) {
	tests := []struct {
		name              string
		counted, expected float64
		wantDiff          float64
		wantStatus        string
	}{
		{"the spec example: counted 9050 of 9200", 9050, 9200, -150, "short"},
		{"extra cash", 9210, 9200, 10, "extra"},
		{"exact", 9200, 9200, 0, "matched"},
		{"a paisa short is real", 9199.99, 9200, -0.01, "short"},
		{"empty drawer", 0, 500, -500, "short"},
		{"nothing expected, nothing counted", 0, 0, 0, "matched"},
		{"expected is negative (more expenses than cash)", 0, -150, 150, "extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Difference(tt.counted, tt.expected)
			if d != tt.wantDiff {
				t.Errorf("difference = %v, want %v", d, tt.wantDiff)
			}
			if s := Status(d); s != tt.wantStatus {
				t.Errorf("status = %s, want %s", s, tt.wantStatus)
			}
		})
	}
}

func TestStatusTolerance(t *testing.T) {
	for d, want := range map[float64]string{0.004: "matched", -0.004: "matched", 0.006: "extra", -0.006: "short"} {
		if got := Status(d); got != want {
			t.Errorf("Status(%v) = %s, want %s", d, got, want)
		}
	}
}

func TestWastage(t *testing.T) {
	tests := []struct {
		name             string
		unit             string
		expected, actual float64
		want             float64
	}{
		{"the spec example: curry cut 6.2 -> 5.9 kg", "kg", 6.2, 5.9, 0.3},
		{"nothing missing", "kg", 5, 5, 0},
		{"more than expected is not wastage", "kg", 5, 6, 0},
		{"all gone", "kg", 2.5, 0, 2.5},
		{"birds are not wastage", "birds", 100, 90, 0},
		{"trays are not wastage", "trays", 10, 8, 0},
		{"pieces are not wastage", "pieces", 10, 8, 0},
		{"floating point noise is rounded", "kg", 0.3, 0.1, 0.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Wastage(tt.unit, tt.expected, tt.actual); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// The wastage movement and the adjustment movement must always add up to the stock change,
// or the movement log would not match the stock quantity.
func TestSplitAddsUpToTheChange(t *testing.T) {
	tests := []struct {
		unit             string
		expected, actual float64
		wantW, wantA     float64
	}{
		{"kg", 6.2, 5.9, 0.3, 0},
		{"kg", 5, 6, 0, 1},
		{"kg", 5, 5, 0, 0},
		{"birds", 100, 90, 0, -10},
		{"trays", 4, 6, 0, 2},
		{"kg", 0.3, 0.1, 0.2, 0},
		{"kg", 10, 0, 10, 0},
	}
	for _, tt := range tests {
		w, a := Split(tt.unit, tt.expected, tt.actual)
		if w != tt.wantW || a != tt.wantA {
			t.Errorf("Split(%s,%v,%v) = %v,%v want %v,%v", tt.unit, tt.expected, tt.actual, w, a, tt.wantW, tt.wantA)
		}
		if math.Abs((-w+a)-(tt.actual-tt.expected)) > 1e-9 {
			t.Errorf("Split(%s,%v,%v): -%v + %v does not equal the change %v", tt.unit, tt.expected, tt.actual, w, a, tt.actual-tt.expected)
		}
	}
}

func f(v float64) *float64 { return &v }

func TestCloseInValidate(t *testing.T) {
	loc, item := uuid.New(), uuid.New()
	ok := func() closeIn {
		return closeIn{LocationID: loc, CountedCash: f(9050), Reason: "coins missing", Stock: []stockIn{{ItemID: item, Actual: 5.9}}}
	}
	tests := []struct {
		name   string
		mutate func(*closeIn)
		want   string
	}{
		{"valid", func(c *closeIn) {}, ""},
		{"valid without stock or reason", func(c *closeIn) { c.Stock = nil; c.Reason = "" }, ""},
		{"zero counted cash is allowed", func(c *closeIn) { c.CountedCash = f(0) }, ""},
		{"no location", func(c *closeIn) { c.LocationID = uuid.Nil }, "location_id is required"},
		{"no counted cash", func(c *closeIn) { c.CountedCash = nil }, "counted_cash is required"},
		{"negative counted cash", func(c *closeIn) { c.CountedCash = f(-1) }, "counted_cash must be 0 or more"},
		{"negative actual", func(c *closeIn) { c.Stock[0].Actual = -0.1 }, "stock 1: actual must be 0 or more"},
		{"zero actual is allowed", func(c *closeIn) { c.Stock[0].Actual = 0 }, ""},
		{"stock without item id", func(c *closeIn) { c.Stock[0].ItemID = uuid.Nil }, "stock 1: item_id is required"},
		{"repeated item", func(c *closeIn) { c.Stock = append(c.Stock, c.Stock[0]) }, "stock 2: item_id is repeated"},
		{"reason too long", func(c *closeIn) { c.Reason = strings.Repeat("x", 201) }, "reason is too long (200 characters at most)"},
		{"too many stock rows", func(c *closeIn) {
			c.Stock = nil
			for i := 0; i < 101; i++ {
				c.Stock = append(c.Stock, stockIn{ItemID: uuid.New()})
			}
		}, "send at most 100 stock rows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ok()
			tt.mutate(&c)
			if got := c.validate(); got != tt.want {
				t.Errorf("validate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func routes(role string) http.Handler {
	h := Handler{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithUser(r.Context(), uuid.New())
			ctx = auth.WithMember(ctx, auth.Member{ShopID: uuid.New(), Role: role})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRoles("owner", "cashier"))
		r.Get("/close-day/preview", h.Preview)
		r.Post("/close-day", h.Close)
	})
	return r
}

func TestRolesAndValidation(t *testing.T) {
	loc := uuid.NewString()
	tests := []struct {
		name, role, method, path, body string
		want                           int
		errContains                    string
	}{
		{"farm manager cannot preview", "farm_manager", "GET", "/close-day/preview?location_id=" + loc, "", 403, "your role cannot do this"},
		{"farm manager cannot close", "farm_manager", "POST", "/close-day", `{}`, 403, "your role cannot do this"},
		{"preview: no location", "cashier", "GET", "/close-day/preview", "", 400, "location_id is required"},
		{"preview: bad location", "owner", "GET", "/close-day/preview?location_id=nope", "", 400, "invalid location_id"},
		{"preview: bad date", "owner", "GET", "/close-day/preview?location_id=" + loc + "&date=16-11-2026", "", 400, "date must be YYYY-MM-DD"},
		{"preview: future date", "owner", "GET", "/close-day/preview?location_id=" + loc + "&date=2999-01-01", "", 400, "date cannot be in the future"},
		{"close: bad json", "cashier", "POST", "/close-day", `{`, 400, "invalid body"},
		{"close: unknown field", "cashier", "POST", "/close-day", `{"location_id":"` + loc + `","counted_cash":1,"x":1}`, 400, "invalid body"},
		{"close: no counted cash", "cashier", "POST", "/close-day", `{"location_id":"` + loc + `"}`, 400, "counted_cash is required"},
		{"close: negative cash", "owner", "POST", "/close-day", `{"location_id":"` + loc + `","counted_cash":-5}`, 400, "counted_cash must be 0 or more"},
		{"close: negative actual", "owner", "POST", "/close-day", `{"location_id":"` + loc + `","counted_cash":5,"stock":[{"item_id":"` + loc + `","actual":-1}]}`, 400, "actual must be 0 or more"},
		{"close: bad date", "owner", "POST", "/close-day", `{"location_id":"` + loc + `","counted_cash":5,"date":"soon"}`, 400, "date must be YYYY-MM-DD"},
		{"close: future date", "owner", "POST", "/close-day", `{"location_id":"` + loc + `","counted_cash":5,"date":"2999-01-01"}`, 400, "date cannot be in the future"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			routes(tt.role).ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.errContains) {
				t.Errorf("body %s does not contain %q", rec.Body, tt.errContains)
			}
		})
	}
}
