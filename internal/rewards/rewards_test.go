package rewards

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func ip(v int) *int         { return &v }
func fp(v float64) *float64 { return &v }

func good() putIn { return putIn{ip(10), fp(0.25), ip(12), ip(50)} }

func TestPutInValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*putIn)
		want   string
	}{
		{"the defaults", func(p *putIn) {}, ""},
		{"spend 1 is the lowest", func(p *putIn) { p.SpendPerPoint = ip(1) }, ""},
		{"spend 1000 is the highest", func(p *putIn) { p.SpendPerPoint = ip(1000) }, ""},
		{"spend 0", func(p *putIn) { p.SpendPerPoint = ip(0) }, "spend_per_point must be between 1 and 1000"},
		{"spend 1001", func(p *putIn) { p.SpendPerPoint = ip(1001) }, "spend_per_point must be between 1 and 1000"},
		{"value 0.05 is the lowest", func(p *putIn) { p.PointValue = fp(0.05) }, ""},
		{"value 5 is the highest", func(p *putIn) { p.PointValue = fp(5) }, ""},
		{"value 0.04", func(p *putIn) { p.PointValue = fp(0.04) }, "point_value must be between 0.05 and 5"},
		{"value 5.01", func(p *putIn) { p.PointValue = fp(5.01) }, "point_value must be between 0.05 and 5"},
		{"value 0", func(p *putIn) { p.PointValue = fp(0) }, "point_value must be between 0.05 and 5"},
		{"expiry 1", func(p *putIn) { p.ExpiryMonths = ip(1) }, ""},
		{"expiry 60", func(p *putIn) { p.ExpiryMonths = ip(60) }, ""},
		{"expiry 0", func(p *putIn) { p.ExpiryMonths = ip(0) }, "expiry_months must be between 1 and 60"},
		{"expiry 61", func(p *putIn) { p.ExpiryMonths = ip(61) }, "expiry_months must be between 1 and 60"},
		{"max pay 5", func(p *putIn) { p.MaxPayPercent = ip(5) }, ""},
		{"max pay 100", func(p *putIn) { p.MaxPayPercent = ip(100) }, ""},
		{"max pay 4", func(p *putIn) { p.MaxPayPercent = ip(4) }, "max_pay_percent must be between 5 and 100"},
		{"max pay 101", func(p *putIn) { p.MaxPayPercent = ip(101) }, "max_pay_percent must be between 5 and 100"},
		{"missing spend", func(p *putIn) { p.SpendPerPoint = nil }, "spend_per_point, point_value, expiry_months and max_pay_percent are all required"},
		{"missing value", func(p *putIn) { p.PointValue = nil }, "spend_per_point, point_value, expiry_months and max_pay_percent are all required"},
		{"missing expiry", func(p *putIn) { p.ExpiryMonths = nil }, "spend_per_point, point_value, expiry_months and max_pay_percent are all required"},
		{"missing max pay", func(p *putIn) { p.MaxPayPercent = nil }, "spend_per_point, point_value, expiry_months and max_pay_percent are all required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := good()
			tt.mutate(&p)
			if got := p.validate(); got != tt.want {
				t.Errorf("validate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestItemInValidate(t *testing.T) {
	tests := []struct {
		name string
		in   itemIn
		want string
	}{
		{"valid", itemIn{Name: "1 tray brown eggs", Points: ip(300)}, ""},
		{"no name", itemIn{Points: ip(300)}, "name is required"},
		{"punctuation only", itemIn{Name: "!!", Points: ip(300)}, "name is required"},
		{"name too long", itemIn{Name: strings.Repeat("a", 81), Points: ip(300)}, "name is too long (80 characters at most)"},
		{"no points", itemIn{Name: "Eggs"}, "points must be between 1 and 1000000"},
		{"zero points", itemIn{Name: "Eggs", Points: ip(0)}, "points must be between 1 and 1000000"},
		{"too many points", itemIn{Name: "Eggs", Points: ip(1_000_001)}, "points must be between 1 and 1000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.validate(); got != tt.want {
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
	r.With(auth.RequireOwner).Put("/rewards", h.Put)
	r.With(auth.RequireOwner).Post("/rewards/items", h.CreateItem)
	r.With(auth.RequireOwner).Delete("/rewards/items/{id}", h.DeleteItem)
	return r
}

func TestRolesAndValidation(t *testing.T) {
	tests := []struct {
		name, role, method, path, body string
		want                           int
		errContains                    string
	}{
		{"cashier cannot change settings", "cashier", "PUT", "/rewards", `{"spend_per_point":10,"point_value":0.25,"expiry_months":12,"max_pay_percent":50}`, 403, "owner only"},
		{"farm manager cannot add rewards", "farm_manager", "POST", "/rewards/items", `{"name":"x","points":1}`, 403, "owner only"},
		{"cashier cannot delete rewards", "cashier", "DELETE", "/rewards/items/" + uuid.NewString(), "", 403, "owner only"},
		{"put: bad json", "owner", "PUT", "/rewards", `{`, 400, "invalid body"},
		{"put: unknown field", "owner", "PUT", "/rewards", `{"spend_per_point":10,"point_value":0.25,"expiry_months":12,"max_pay_percent":50,"x":1}`, 400, "invalid body"},
		{"put: fractional spend", "owner", "PUT", "/rewards", `{"spend_per_point":10.5,"point_value":0.25,"expiry_months":12,"max_pay_percent":50}`, 400, "invalid body"},
		{"put: missing fields", "owner", "PUT", "/rewards", `{"spend_per_point":10}`, 400, "all required"},
		{"put: out of range", "owner", "PUT", "/rewards", `{"spend_per_point":10,"point_value":9,"expiry_months":12,"max_pay_percent":50}`, 400, "point_value must be between"},
		{"item: fractional points", "owner", "POST", "/rewards/items", `{"name":"x","points":1.5}`, 400, "invalid body"},
		{"item: no points", "owner", "POST", "/rewards/items", `{"name":"x"}`, 400, "points must be between"},
		{"delete: bad id", "owner", "DELETE", "/rewards/items/abc", "", 400, "invalid reward id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			routes(tt.role).ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.errContains) {
				t.Errorf("status %d body %s, want %d containing %q", rec.Code, rec.Body, tt.want, tt.errContains)
			}
		})
	}
}
