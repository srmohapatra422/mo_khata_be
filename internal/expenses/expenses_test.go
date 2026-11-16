package expenses

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func TestCreateInValidate(t *testing.T) {
	loc := uuid.New()
	tests := []struct {
		name string
		in   createIn
		want string
	}{
		{"valid", createIn{LocationID: loc, Amount: 1800, Note: "Electricity", Category: "power"}, ""},
		{"note and category are optional", createIn{LocationID: loc, Amount: 5}, ""},
		{"no location", createIn{Amount: 5}, "location_id is required"},
		{"zero amount", createIn{LocationID: loc}, "amount must be more than 0"},
		{"negative amount", createIn{LocationID: loc, Amount: -3}, "amount must be more than 0"},
		{"absurd amount", createIn{LocationID: loc, Amount: 1e12}, "amount must be more than 0"},
		{"note too long", createIn{LocationID: loc, Amount: 5, Note: strings.Repeat("n", 201)}, "note is too long (200 characters at most)"},
		{"category too long", createIn{LocationID: loc, Amount: 5, Category: strings.Repeat("c", 41)}, "category is too long (40 characters at most)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.validate(); got != tt.want {
				t.Errorf("validate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateInValidate_CleansAndRounds(t *testing.T) {
	in := createIn{LocationID: uuid.New(), Amount: 10.126, Note: "  fuel ", Category: " transport "}
	if msg := in.validate(); msg != "" {
		t.Fatal(msg)
	}
	if in.Amount != 10.13 || in.Note != "fuel" || in.Category != "transport" {
		t.Errorf("got %+v", in)
	}
}

func TestHandlers400(t *testing.T) {
	h := Handler{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithUser(r.Context(), uuid.New())
			ctx = auth.WithMember(ctx, auth.Member{ShopID: uuid.New(), Role: "cashier"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	r.Post("/expenses", h.Create)
	r.Get("/expenses", h.List)
	loc := uuid.NewString()
	tests := []struct {
		name, method, path, body, errContains string
	}{
		{"bad json", "POST", "/expenses", `{`, "invalid body"},
		{"unknown field", "POST", "/expenses", `{"location_id":"` + loc + `","amount":5,"colour":"red"}`, "invalid body"},
		{"no amount", "POST", "/expenses", `{"location_id":"` + loc + `"}`, "amount must be more than 0"},
		{"no location", "POST", "/expenses", `{"amount":5}`, "location_id is required"},
		{"list: bad date", "GET", "/expenses?date=nope", "", "date must be YYYY-MM-DD"},
		{"list: future date", "GET", "/expenses?date=2999-01-01", "", "date cannot be in the future"},
		{"list: bad location", "GET", "/expenses?location_id=nope", "", "invalid location_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tt.errContains) {
				t.Errorf("status %d body %s, want 400 containing %q", rec.Code, rec.Body, tt.errContains)
			}
		})
	}
}
