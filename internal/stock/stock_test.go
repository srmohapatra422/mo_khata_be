package stock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func fp(v float64) *float64 { return &v }

func TestNormalizeUnit(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"kg", "kg", true}, {"KG", "kg", true}, {" Kilo ", "kg", true}, {"kilogram", "kg", true},
		{"bird", "birds", true}, {"birds", "birds", true},
		{"tray", "trays", true}, {"Trays", "trays", true},
		{"piece", "pieces", true}, {"pcs", "pieces", true},
		{"dozen", "", false}, {"litre", "", false}, {"", "", false},
	}
	for _, tt := range tests {
		got, ok := NormalizeUnit(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("NormalizeUnit(%q) = %q,%v want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestUnitFromItemUnit(t *testing.T) {
	for in, want := range map[string]string{"kg": "kg", "bird": "birds", "tray": "trays", "piece": "pieces", "dozen": "", "": ""} {
		if got := UnitFromItemUnit(in); got != want {
			t.Errorf("UnitFromItemUnit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInReqValidate(t *testing.T) {
	loc := uuid.New()
	ok := func() inReq {
		return inReq{LocationID: loc, Items: []inItem{{Name: "Broiler", Unit: "kg", Quantity: 12.5, Rate: fp(150)}}}
	}
	tests := []struct {
		name   string
		mutate func(*inReq)
		want   string
	}{
		{"valid", func(r *inReq) {}, ""},
		{"valid voice", func(r *inReq) { r.Source = "voice" }, ""},
		{"no location", func(r *inReq) { r.LocationID = uuid.Nil }, "location_id is required"},
		{"bad source", func(r *inReq) { r.Source = "bill" }, "source must be form or voice"},
		{"no items", func(r *inReq) { r.Items = nil }, "send between 1 and 100 items"},
		{"too many", func(r *inReq) { r.Items = make([]inItem, 101) }, "send between 1 and 100 items"},
		{"no name", func(r *inReq) { r.Items[0].Name = " " }, "item 1: name is required"},
		{"bad unit", func(r *inReq) { r.Items[0].Unit = "litre" }, "item 1: unit must be kg, birds, trays or pieces"},
		{"zero qty", func(r *inReq) { r.Items[0].Quantity = 0 }, "item 1: quantity must be more than 0"},
		{"negative qty", func(r *inReq) { r.Items[0].Quantity = -2 }, "item 1: quantity must be more than 0"},
		{"huge qty", func(r *inReq) { r.Items[0].Quantity = 1e9 }, "item 1: quantity must be more than 0"},
		{"negative rate", func(r *inReq) { r.Items[0].Rate = fp(-1) }, "item 1: rate must be 0 or more"},
		{"zero rate ok", func(r *inReq) { r.Items[0].Rate = fp(0) }, ""},
		{"no rate ok", func(r *inReq) { r.Items[0].Rate = nil }, ""},
		{"second item bad", func(r *inReq) { r.Items = append(r.Items, inItem{Name: "Eggs", Unit: "tray"}) }, "item 2: quantity must be more than 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ok()
			tt.mutate(&r)
			if got := r.validate(); got != tt.want {
				t.Errorf("validate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInReqValidate_Cleans(t *testing.T) {
	r := inReq{LocationID: uuid.New(), Items: []inItem{{Name: "  Eggs ", Unit: " Trays ", Quantity: 2}}}
	if msg := r.validate(); msg != "" {
		t.Fatal(msg)
	}
	if r.Source != "form" || r.Items[0].Name != "Eggs" || r.Items[0].Unit != "trays" {
		t.Errorf("not cleaned: %+v", r)
	}
}

func asRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithUser(r.Context(), uuid.New())
			ctx = auth.WithMember(ctx, auth.Member{ShopID: uuid.New(), Role: role})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Handler with no database: every case must be answered before a query would run.
func routes(role string) http.Handler {
	h := Handler{}
	r := chi.NewRouter()
	r.Use(asRole(role))
	r.Post("/stock/in", h.In)
	r.With(auth.RequireRoles("owner", "farm_manager")).Post("/stock/{id}/wastage", h.Wastage)
	r.With(auth.RequireOwner).Patch("/stock/{id}", h.PatchReorder)
	return r
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestRolesAndValidation(t *testing.T) {
	id := uuid.NewString()
	loc := uuid.NewString()
	tests := []struct {
		name, role, method, path, body string
		want                           int
		errContains                    string
	}{
		{"cashier cannot record wastage", "cashier", "POST", "/stock/" + id + "/wastage", `{"quantity":1}`, 403, "your role cannot do this"},
		{"cashier cannot set reorder level", "cashier", "PATCH", "/stock/" + id, `{"reorder_level":5}`, 403, "owner only"},
		{"farm manager cannot set reorder level", "farm_manager", "PATCH", "/stock/" + id, `{"reorder_level":5}`, 403, "owner only"},
		{"stock in: bad json", "cashier", "POST", "/stock/in", `{`, 400, "invalid body"},
		{"stock in: unknown field", "cashier", "POST", "/stock/in", `{"location_id":"` + loc + `","items":[],"x":1}`, 400, "invalid body"},
		{"stock in: no items", "farm_manager", "POST", "/stock/in", `{"location_id":"` + loc + `","items":[]}`, 400, "between 1 and 100"},
		{"stock in: bad quantity", "owner", "POST", "/stock/in", `{"location_id":"` + loc + `","items":[{"name":"A","unit":"kg","quantity":0}]}`, 400, "quantity must be more than 0"},
		{"stock in: bad unit", "owner", "POST", "/stock/in", `{"location_id":"` + loc + `","items":[{"name":"A","unit":"box","quantity":1}]}`, 400, "unit must be"},
		{"stock in: bad location uuid", "owner", "POST", "/stock/in", `{"location_id":"nope","items":[{"name":"A","unit":"kg","quantity":1}]}`, 400, "invalid body"},
		{"wastage: bad id", "owner", "POST", "/stock/abc/wastage", `{"quantity":1}`, 400, "invalid stock item id"},
		{"wastage: zero quantity", "farm_manager", "POST", "/stock/" + id + "/wastage", `{"quantity":0}`, 400, "quantity must be more than 0"},
		{"wastage: note too long", "owner", "POST", "/stock/" + id + "/wastage", `{"quantity":1,"note":"` + strings.Repeat("x", 201) + `"}`, 400, "note is too long"},
		{"reorder: missing field", "owner", "PATCH", "/stock/" + id, `{}`, 400, "send reorder_level"},
		{"reorder: negative", "owner", "PATCH", "/stock/" + id, `{"reorder_level":-1}`, 400, "reorder_level must be 0 or more"},
		{"reorder: wrong type", "owner", "PATCH", "/stock/" + id, `{"reorder_level":"x"}`, 400, "reorder_level must be 0 or more"},
		{"reorder: bad id", "owner", "PATCH", "/stock/abc", `{"reorder_level":1}`, 400, "invalid stock item id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(routes(tt.role), tt.method, tt.path, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.errContains) {
				t.Errorf("body %s does not contain %q", rec.Body, tt.errContains)
			}
		})
	}
}
