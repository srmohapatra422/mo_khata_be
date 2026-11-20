package customers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

func get(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseListParams(t *testing.T) {
	good := []struct {
		name string
		in   map[string]string
		want ListParams
	}{
		{"defaults keep the old behaviour", nil, ListParams{Limit: 50}},
		{"all", map[string]string{"q": " ravi ", "filter": "gold", "sort": "points", "limit": "100", "offset": "20"},
			ListParams{Q: "ravi", Filter: "gold", Sort: "points", Limit: 100, Offset: 20}},
		{"credit due by due", map[string]string{"filter": "credit_due", "sort": "due"}, ListParams{Filter: "credit_due", Sort: "due", Limit: 50}},
		{"not visited", map[string]string{"filter": "not_visited_30d", "sort": "name"}, ListParams{Filter: "not_visited_30d", Sort: "name", Limit: 50}},
	}
	for _, tt := range good {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseListParams(get(tt.in))
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v want %+v", got, err, tt.want)
			}
		})
	}
	bad := map[string]map[string]string{
		"unknown filter": {"filter": "vip"},
		"unknown sort":   {"sort": "random"},
		"limit 0":        {"limit": "0"},
		"limit 101":      {"limit": "101"},
		"limit text":     {"limit": "ten"},
		"offset -1":      {"offset": "-1"},
		"offset text":    {"offset": "x"},
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := parseListParams(get(in))
			var he *httpx.Err
			if err == nil || !asErr(err, &he) || he.Status != 400 {
				t.Fatalf("want a 400, got %v", err)
			}
		})
	}
}

func asErr(err error, target **httpx.Err) bool {
	he, ok := err.(*httpx.Err)
	*target = he
	return ok
}

func TestLikePattern(t *testing.T) {
	for in, want := range map[string]string{"ravi": "%ravi%", "": "%%", "50%": `%50\%%`, "a_b": `%a\_b%`, `a\b`: `%a\\b%`} {
		if got := likePattern(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestCleanName(t *testing.T) {
	if n, err := CleanName("  Ravi   Kumar "); err != nil || n != "Ravi Kumar" {
		t.Fatalf("got %q, %v", n, err)
	}
	if _, err := CleanName("   "); err == nil {
		t.Fatal("empty name must fail")
	}
	if _, err := CleanName(strings.Repeat("a", 81)); err == nil {
		t.Fatal("81 characters must fail")
	}
	if _, err := CleanName(strings.Repeat("అ", 80)); err != nil {
		t.Fatalf("80 characters of any script is fine: %v", err)
	}
}

func TestKindOut(t *testing.T) {
	for in, want := range map[string]string{"earn": "earned", "redeem": "redeemed", "expire": "expired", "reverse": "adjusted", "bonus": "adjusted"} {
		if got := kindOut(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

// The first four fields are the original contract of GET /v1/customers: an old client that
// only knows them must still parse the new response.
func TestListResponseStillParsesForOldClients(t *testing.T) {
	name := "Ravi"
	b, _ := json.Marshal([]customerOut{{ID: uuid.New(), Phone: "9848012345", Name: &name, Points: 85, Tier: "gold", Visits: 3}})
	var old []struct {
		ID     uuid.UUID `json:"id"`
		Phone  string    `json:"phone"`
		Name   *string   `json:"name"`
		Points int       `json:"points"`
	}
	if err := json.Unmarshal(b, &old); err != nil || len(old) != 1 || old[0].Points != 85 || *old[0].Name != "Ravi" {
		t.Fatalf("old shape broke: %v %+v", err, old)
	}
	for _, k := range []string{"tier", "last_visit_at", "last_visit_days", "visits", "month_spend", "due", "whatsapp_opted_in"} {
		if !strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("missing field %s", k)
		}
	}
}

// ---- handlers that reject the request before touching the database ----

func call(h http.HandlerFunc, role, method, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(method, "/customers/{id}", h)
	r.Method(method, "/customers/{id}/payments", h)
	r.Method(method, "/customers/{id}/points", h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := auth.WithUser(req.Context(), uuid.New())
	ctx = auth.WithMember(ctx, auth.Member{ShopID: uuid.New(), Role: role})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestPatchRejects(t *testing.T) {
	h := Handler{}
	id := uuid.New().String()
	tests := []struct {
		name, role, path, body string
		want                   int
	}{
		{"bad id is a 404", "owner", "/customers/nope", `{"name":"A"}`, 404},
		{"unknown field", "owner", "/customers/" + id, `{"nickname":"A"}`, 400},
		{"nothing to change", "owner", "/customers/" + id, `{}`, 400},
		{"empty name", "owner", "/customers/" + id, `{"name":"  "}`, 400},
		{"long name", "cashier", "/customers/" + id, `{"name":"` + strings.Repeat("a", 81) + `"}`, 400},
		{"cashier cannot set is_business", "cashier", "/customers/" + id, `{"is_business":true}`, 403},
		{"not json", "owner", "/customers/" + id, `x`, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := call(h.Patch, tt.role, http.MethodPatch, tt.path, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status %d want %d: %s", rec.Code, tt.want, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatalf("body is not an error object: %s", rec.Body)
			}
		})
	}
}

func TestPayRejects(t *testing.T) {
	h := Handler{}
	p := "/customers/" + uuid.New().String() + "/payments"
	tests := []struct {
		name, path, body string
		want             int
	}{
		{"bad id", "/customers/x/payments", `{"amount":5,"method":"cash"}`, 404},
		{"zero amount", p, `{"amount":0,"method":"cash"}`, 400},
		{"negative amount", p, `{"amount":-5,"method":"cash"}`, 400},
		{"bad method", p, `{"amount":5,"method":"credit"}`, 400},
		{"unknown field", p, `{"amount":5,"method":"cash","tip":1}`, 400},
		{"long note", p, `{"amount":5,"method":"cash","note":"` + strings.Repeat("n", 201) + `"}`, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := call(h.Pay, "owner", http.MethodPost, tt.path, tt.body); rec.Code != tt.want {
				t.Fatalf("status %d want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestPointsRejects(t *testing.T) {
	id := uuid.New().String()
	for name, path := range map[string]string{
		"limit 0":    "/customers/" + id + "/points?limit=0",
		"limit 101":  "/customers/" + id + "/points?limit=101",
		"bad cursor": "/customers/" + id + "/points?before=abc",
	} {
		t.Run(name, func(t *testing.T) {
			if rec := call(Handler{}.Points, "owner", http.MethodGet, path, ""); rec.Code != 400 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
		})
	}
	if rec := call(Handler{}.Points, "owner", http.MethodGet, "/customers/zzz/points", ""); rec.Code != 404 {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}
