package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func f64(v float64) *float64 { return &v }

func TestNewItemInValidate(t *testing.T) {
	cat := uuid.New()
	tests := []struct {
		name string
		in   newItemIn
		want string // "" = valid
	}{
		{"ok by id", newItemIn{CategoryID: &cat, Name: "Katla", Unit: "kg", Price: f64(170)}, ""},
		{"ok by name", newItemIn{CategoryName: "Ready to cook", Name: "Wings", Unit: "KG", Price: f64(320)}, ""},
		{"no category", newItemIn{Name: "Katla", Unit: "kg", Price: f64(170)}, "category_id or category_name is required"},
		{"blank category name", newItemIn{CategoryName: "  ", Name: "Katla", Unit: "kg", Price: f64(170)}, "category_id or category_name is required"},
		{"no name", newItemIn{CategoryID: &cat, Unit: "kg", Price: f64(170)}, "name is required"},
		{"punctuation only name", newItemIn{CategoryID: &cat, Name: "!!", Unit: "kg", Price: f64(170)}, "name is required"},
		{"name too long", newItemIn{CategoryID: &cat, Name: strings.Repeat("a", 81), Unit: "kg", Price: f64(170)}, "name is too long"},
		{"bad unit", newItemIn{CategoryID: &cat, Name: "Katla", Unit: "litre", Price: f64(170)}, "unit must be kg, tray, piece or bird"},
		{"dozen is not a price unit", newItemIn{CategoryID: &cat, Name: "Katla", Unit: "dozen", Price: f64(170)}, "unit must be kg, tray, piece or bird"},
		{"no price", newItemIn{CategoryID: &cat, Name: "Katla", Unit: "kg"}, "price is required and must be more than 0"},
		{"zero price", newItemIn{CategoryID: &cat, Name: "Katla", Unit: "kg", Price: f64(0)}, "price is required and must be more than 0"},
		{"negative price", newItemIn{CategoryID: &cat, Name: "Katla", Unit: "kg", Price: f64(-5)}, "price is required and must be more than 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.validate(); got != tt.want {
				t.Errorf("validate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewItemInValidate_Normalises(t *testing.T) {
	in := newItemIn{CategoryName: "  Fish ", Name: "  Katla ", Unit: " KG ", Price: f64(1)}
	if msg := in.validate(); msg != "" {
		t.Fatal(msg)
	}
	if in.Name != "Katla" || in.Unit != "kg" || in.CategoryName != "Fish" {
		t.Errorf("got %q %q %q", in.Name, in.Unit, in.CategoryName)
	}
}

func TestRound2(t *testing.T) {
	for in, want := range map[float64]float64{170.004: 170, 170.005: 170.01, -4.996: -5, 5: 5} {
		if got := round2(in); got != want {
			t.Errorf("round2(%v) = %v, want %v", in, got, want)
		}
	}
}

// asRole injects a member so handlers can be tested without a token or a database.
func asRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithUser(r.Context(), uuid.New())
			ctx = auth.WithMember(ctx, auth.Member{ShopID: uuid.New(), Role: role})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// routes mirrors the access levels in main.go. The handler has no database: every case
// below must be answered (401, 403 or 400) before any query would run.
func routes(role string) http.Handler {
	h := Handler{}
	r := chi.NewRouter()
	r.Use(asRole(role))
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRoles("owner", "cashier"))
		r.Post("/prices/items", h.CreateItem)
		r.Put("/prices", h.SaveRates)
		r.Patch("/prices/items/{id}", h.PatchItem)
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireOwner)
		r.Post("/categories", h.CreateCategory)
		r.Post("/prices/items/bulk", h.BulkCreate)
	})
	return r
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoles(t *testing.T) {
	tests := []struct {
		role, method, path string
		want               int
	}{
		{"farm_manager", "POST", "/prices/items", http.StatusForbidden},
		{"farm_manager", "PUT", "/prices", http.StatusForbidden},
		{"farm_manager", "POST", "/categories", http.StatusForbidden},
		{"cashier", "POST", "/categories", http.StatusForbidden},
		{"cashier", "POST", "/prices/items/bulk", http.StatusForbidden},
		// allowed roles get past the role check and fail on the empty body instead
		{"cashier", "POST", "/prices/items", http.StatusBadRequest},
		{"cashier", "PUT", "/prices", http.StatusBadRequest},
		{"owner", "POST", "/prices/items", http.StatusBadRequest},
		{"owner", "POST", "/categories", http.StatusBadRequest},
		{"owner", "POST", "/prices/items/bulk", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.role+" "+tt.method+" "+tt.path, func(t *testing.T) {
			if got := do(routes(tt.role), tt.method, tt.path, "").Code; got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidation400(t *testing.T) {
	cat := uuid.NewString()
	tests := []struct {
		name, method, path, body, errContains string
	}{
		{"item: bad json", "POST", "/prices/items", `{`, "invalid body"},
		{"item: unknown field", "POST", "/prices/items", `{"category_id":"` + cat + `","name":"A","unit":"kg","price":1,"colour":"red"}`, "invalid body"},
		{"item: no category", "POST", "/prices/items", `{"name":"Katla","unit":"kg","price":170}`, "category_id or category_name"},
		{"item: bad unit", "POST", "/prices/items", `{"category_name":"Fish","name":"Katla","unit":"box","price":170}`, "unit must be"},
		{"item: zero price", "POST", "/prices/items", `{"category_name":"Fish","name":"Katla","unit":"kg","price":0}`, "price is required"},
		{"item: bad category uuid", "POST", "/prices/items", `{"category_id":"nope","name":"Katla","unit":"kg","price":1}`, "invalid body"},
		{"category: empty name", "POST", "/categories", `{"name":"  "}`, "name is required"},
		{"category: too long", "POST", "/categories", `{"name":"` + strings.Repeat("x", 61) + `"}`, "name is required"},
		{"bulk: empty list", "POST", "/prices/items/bulk", `{"items":[]}`, "between 1 and 200"},
		{"bulk: second item bad", "POST", "/prices/items/bulk", `{"items":[{"category_name":"Fish","name":"A","unit":"kg","price":1},{"category_name":"Fish","name":"B","unit":"kg","price":0}]}`, "item 2: price is required"},
		{"rates: empty", "PUT", "/prices", `{"prices":[]}`, "send a list"},
		{"rates: zero price", "PUT", "/prices", `{"prices":[{"item_id":"` + cat + `","price":0}]}`, "more than 0"},
		{"rates: unknown field", "PUT", "/prices", `{"prices":[{"item_id":"` + cat + `","price":5}],"x":1}`, "invalid body"},
		{"patch: bad id", "PATCH", "/prices/items/abc", `{"name":"X"}`, "invalid item id"},
		{"patch: empty body", "PATCH", "/prices/items/" + cat, `{}`, "send name, unit or active"},
		{"patch: bad unit", "PATCH", "/prices/items/" + cat, `{"unit":"box"}`, "unit must be"},
		{"patch: blank name", "PATCH", "/prices/items/" + cat, `{"name":"  "}`, "name must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(routes("owner"), tt.method, tt.path, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.errContains) {
				t.Errorf("body %s does not contain %q", rec.Body, tt.errContains)
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("error body must be {\"error\": ...}, got %s", rec.Body)
			}
		})
	}
}

// 401: a verifier built from an HS256 secret (no network needed).
func TestRequireUser401(t *testing.T) {
	const secret = "test-secret"
	v, err := auth.NewVerifier(context.Background(), "https://example.supabase.co", secret, "")
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := v.RequireUser(ok)

	token := func(claims jwt.MapClaims) string {
		s, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
		return s
	}
	good := jwt.MapClaims{
		"sub": uuid.NewString(), "aud": "authenticated",
		"iss": "https://example.supabase.co/auth/v1", "exp": time.Now().Add(time.Hour).Unix(),
	}
	expired := jwt.MapClaims{
		"sub": uuid.NewString(), "aud": "authenticated",
		"iss": "https://example.supabase.co/auth/v1", "exp": time.Now().Add(-time.Hour).Unix(),
	}
	tests := []struct {
		name, header string
		want         int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"not bearer", "Basic abc", http.StatusUnauthorized},
		{"garbage", "Bearer abc.def.ghi", http.StatusUnauthorized},
		{"expired", "Bearer " + token(expired), http.StatusUnauthorized},
		{"valid", "Bearer " + token(good), http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/me", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
