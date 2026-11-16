package setup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name        string
		manual      map[string]bool
		derived     Derived
		wantDone    []bool // shop, prices, stock, bill
		wantCount   int
		wantPercent int
		wantMinutes int
		wantDoneAll bool
	}{
		{"new shop: only the shop step", nil, Derived{}, []bool{true, false, false, false}, 1, 25, 6, false},
		{"prices derived", nil, Derived{Prices: true}, []bool{true, true, false, false}, 2, 50, 4, false},
		{"prices and stock derived", nil, Derived{Prices: true, Stock: true}, []bool{true, true, true, false}, 3, 75, 2, false},
		{"everything derived", nil, Derived{true, true, true}, []bool{true, true, true, true}, 4, 100, 0, true},
		{"manual tick counts", map[string]bool{"stock": true}, Derived{}, []bool{true, false, true, false}, 2, 50, 4, false},
		{"manual and derived together", map[string]bool{"prices": true, "stock": true, "bill": true}, Derived{Prices: true}, []bool{true, true, true, true}, 4, 100, 0, true},
		{"manual false does not undo a derived step", map[string]bool{"prices": false}, Derived{Prices: true}, []bool{true, true, false, false}, 2, 50, 4, false},
		{"manual shop=false cannot undo the shop step", map[string]bool{"shop": false}, Derived{}, []bool{true, false, false, false}, 1, 25, 6, false},
		{"cashier and whatsapp ticks are not counted", map[string]bool{"cashier": true, "whatsapp": true}, Derived{}, []bool{true, false, false, false}, 1, 25, 6, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Build(tt.manual, tt.derived)
			if len(r.Steps) != 4 || r.Total != 4 {
				t.Fatalf("steps = %d, total = %d, want 4 and 4", len(r.Steps), r.Total)
			}
			for i, id := range []string{"shop", "prices", "stock", "bill"} {
				if r.Steps[i].ID != id || r.Steps[i].Done != tt.wantDone[i] {
					t.Errorf("step %d = %+v, want %s done=%v", i, r.Steps[i], id, tt.wantDone[i])
				}
			}
			if r.DoneCount != tt.wantCount || r.Percent != tt.wantPercent || r.MinutesLeft != tt.wantMinutes || r.Complete != tt.wantDoneAll {
				t.Errorf("done=%d percent=%d minutes=%d complete=%v, want %d %d %d %v",
					r.DoneCount, r.Percent, r.MinutesLeft, r.Complete, tt.wantCount, tt.wantPercent, tt.wantMinutes, tt.wantDoneAll)
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
	r.With(auth.RequireOwner).Patch("/setup/steps/{id}", h.Patch)
	return r
}

func TestPatch(t *testing.T) {
	tests := []struct {
		name, role, step, body string
		want                   int
		errContains            string
	}{
		{"cashier cannot tick", "cashier", "prices", `{"done":true}`, 403, "owner only"},
		{"farm manager cannot tick", "farm_manager", "prices", `{"done":true}`, 403, "owner only"},
		{"unknown step", "owner", "nope", `{"done":true}`, 400, "unknown step"},
		{"shop cannot change", "owner", "shop", `{"done":true}`, 400, "shop step cannot be changed"},
		{"shop cannot change (false)", "owner", "shop", `{"done":false}`, 400, "shop step cannot be changed"},
		{"missing done", "owner", "prices", `{}`, 400, `done`},
		{"done wrong type", "owner", "prices", `{"done":"yes"}`, 400, `done`},
		{"unknown field", "owner", "prices", `{"done":true,"x":1}`, 400, `done`},
		{"bad json", "owner", "prices", `{`, 400, `done`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			routes(tt.role).ServeHTTP(rec, httptest.NewRequest("PATCH", "/setup/steps/"+tt.step, strings.NewReader(tt.body)))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.errContains) {
				t.Errorf("body %s does not contain %q", rec.Body, tt.errContains)
			}
		})
	}
}
