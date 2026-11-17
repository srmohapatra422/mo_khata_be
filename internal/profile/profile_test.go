package profile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

func ip(i int) *int         { return &i }
func fp(f float64) *float64 { return &f }
func bp(b bool) *bool       { return &b }
func sp(s string) *string   { return &s }

func TestTrialDaysLeft(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	cases := []struct {
		name string
		plan string
		end  *time.Time
		want int
	}{
		{"11 full days", "trial", at(11 * 24 * time.Hour), 11},
		{"part of a day rounds up", "trial", at(10*24*time.Hour + time.Minute), 11},
		{"ends in an hour", "trial", at(time.Hour), 1},
		{"already over", "trial", at(-time.Hour), 0},
		{"paid plan", "pro", at(5 * 24 * time.Hour), 0},
		{"no end date", "trial", nil, 0},
	}
	for _, c := range cases {
		if got := TrialDaysLeft(c.plan, c.end, now); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestOfferValidate(t *testing.T) {
	ok := []offerIn{
		{Enabled: bp(true), Type: "discount", DiscountAmount: ip(20), MinBill: ip(500)},
		{Enabled: bp(false), Type: "discount", DiscountAmount: ip(1000), MinBill: ip(100000)},
		{Enabled: bp(true), Type: "points", Product: sp("Chicken"), Multiplier: ip(3)},
		// the other type's fields are not checked
		{Enabled: bp(true), Type: "points", Product: sp("eggs"), Multiplier: ip(5), DiscountAmount: ip(7)},
	}
	for i, in := range ok {
		if _, err := in.validate(); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	bad := map[string]offerIn{
		"enabled missing":     {Type: "discount"},
		"bad type":            {Enabled: bp(true), Type: "bogo"},
		"discount 3":          {Enabled: bp(true), Type: "discount", DiscountAmount: ip(3), MinBill: ip(500)},
		"discount not x5":     {Enabled: bp(true), Type: "discount", DiscountAmount: ip(22), MinBill: ip(500)},
		"discount too big":    {Enabled: bp(true), Type: "discount", DiscountAmount: ip(1005), MinBill: ip(500)},
		"min bill 25":         {Enabled: bp(true), Type: "discount", DiscountAmount: ip(20), MinBill: ip(25)},
		"min bill not x50":    {Enabled: bp(true), Type: "discount", DiscountAmount: ip(20), MinBill: ip(520)},
		"min bill too big":    {Enabled: bp(true), Type: "discount", DiscountAmount: ip(20), MinBill: ip(100050)},
		"points no product":   {Enabled: bp(true), Type: "points", Multiplier: ip(2)},
		"points multiplier 4": {Enabled: bp(true), Type: "points", Product: sp("fish"), Multiplier: ip(4)},
	}
	for name, in := range bad {
		if _, err := in.validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestOfferDiscount(t *testing.T) {
	o := offerOut{Enabled: true, Type: "discount", DiscountAmount: 20, MinBill: 500}
	if OfferDiscount(o, 499.99) != 0 || OfferDiscount(o, 500) != 20 {
		t.Error("min bill boundary")
	}
	o.Enabled = false
	if OfferDiscount(o, 900) != 0 {
		t.Error("disabled offer must not apply")
	}
	if OfferDiscount(offerOut{Enabled: true, Type: "points", MinBill: 0}, 900) != 0 {
		t.Error("points offer gives no discount")
	}
}

func TestLocationsValidate(t *testing.T) {
	good := locationsIn{HasFarm: bp(true), HasShop: bp(true), FarmLocation: &geoIn{fp(20.29), fp(85.82)}}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]locationsIn{
		"none on":          {HasFarm: bp(false), HasShop: bp(false)},
		"flags missing":    {},
		"position, off":    {HasFarm: bp(false), HasShop: bp(true), FarmLocation: &geoIn{fp(1), fp(1)}},
		"shop pos, off":    {HasFarm: bp(true), HasShop: bp(false), ShopLocation: &geoIn{fp(1), fp(1)}},
		"lat only":         {HasFarm: bp(true), HasShop: bp(true), FarmLocation: &geoIn{Lat: fp(1)}},
		"lat out of range": {HasFarm: bp(true), HasShop: bp(true), FarmLocation: &geoIn{fp(91), fp(1)}},
		"lng out of range": {HasFarm: bp(true), HasShop: bp(true), ShopLocation: &geoIn{fp(1), fp(181)}},
	}
	for name, in := range bad {
		if err := in.validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := (locationsIn{HasFarm: bp(false), HasShop: bp(false)}).validate(); err == nil || err.Error() != "pick at least one location" {
		t.Errorf("message: %v", err)
	}
	// null / empty position means "no position"
	empty := locationsIn{HasFarm: bp(true), HasShop: bp(true), FarmLocation: &geoIn{}}
	if err := empty.validate(); err != nil {
		t.Errorf("empty position: %v", err)
	}
}

func TestLastOwnerRule(t *testing.T) {
	if !LastOwnerBlocked("owner", 1, "cashier") || !LastOwnerBlocked("owner", 1, "") {
		t.Error("the only owner must be protected")
	}
	if LastOwnerBlocked("owner", 2, "cashier") {
		t.Error("two owners: one may step down")
	}
	if LastOwnerBlocked("cashier", 1, "farm_manager") || LastOwnerBlocked("owner", 1, "owner") {
		t.Error("only demoting or removing the last owner is blocked")
	}
}

func TestInviteValidate(t *testing.T) {
	in := inviteIn{Phone: "+91 90000 00002", Role: "Cashier", Name: " Ravi "}
	if err := in.validate(); err != nil || in.Phone != "9000000002" || in.Role != "cashier" || in.Name != "Ravi" {
		t.Fatalf("%v %+v", err, in)
	}
	for name, in := range map[string]inviteIn{
		"short phone": {Phone: "12345", Role: "cashier"},
		"owner":       {Phone: "9000000002", Role: "owner"},
		"bad role":    {Phone: "9000000002", Role: "boss"},
		"long name":   {Phone: "9000000002", Role: "cashier", Name: strings.Repeat("a", 81)},
	} {
		if err := in.validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCleanCategories(t *testing.T) {
	got, err := cleanCategories([]string{"Chicken", " fish ", "chicken", "eggs"})
	if err != nil || strings.Join(got, ",") != "chicken,fish,eggs" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := cleanCategories([]string{}); err != nil || len(got) != 0 {
		t.Error("empty list is allowed")
	}
	if _, err := cleanCategories([]string{"chicken", "pizza"}); err == nil || err.Error() != "unknown category: pizza" {
		t.Errorf("got %v", err)
	}
}

func TestCleanURLAndName(t *testing.T) {
	prefix := "https://x.supabase.co/storage/v1/object/public/shop-logos/"
	if u, err := cleanURL(prefix+"a.png", prefix, "photo_url"); err != nil || u == "" {
		t.Error(err)
	}
	if _, err := cleanURL("https://evil.com/a.png", prefix, "photo_url"); err == nil {
		t.Error("foreign url must be rejected")
	}
	if _, err := cleanURL("ftp://x", "", "photo_url"); err == nil {
		t.Error("non-http url must be rejected")
	}
	if u, err := cleanURL("  ", prefix, "photo_url"); err != nil || u != "" {
		t.Error("blank means remove")
	}
	if _, err := cleanName("   ", "name"); err == nil {
		t.Error("empty name")
	}
	if _, err := cleanName(strings.Repeat("a", 81), "name"); err == nil {
		t.Error("long name")
	}
	if n, _ := cleanName("  Ravi Kumar ", "name"); n != "Ravi Kumar" {
		t.Error("trim")
	}
}

func TestLast10AndCSVSafe(t *testing.T) {
	for in, want := range map[string]string{"+919876543210": "9876543210", "9876543210": "9876543210", "098-765": "098765"} {
		if got := last10(in); got != want {
			t.Errorf("last10(%q)=%q", in, got)
		}
	}
	if csvSafe("=SUM(A1)") != "'=SUM(A1)" || csvSafe("-12.5") != "-12.5" || csvSafe("Rohu") != "Rohu" {
		t.Error("csvSafe")
	}
}

// ---------- handler tests: validation runs before any database call ----------

func call(h http.HandlerFunc, method, body string, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	ctx := auth.WithUser(context.Background(), uuid.New())
	ctx = auth.WithMember(ctx, auth.Member{ShopID: uuid.New(), Role: role})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestHandlerBadRequests(t *testing.T) {
	h := Handler{}
	cases := []struct {
		name string
		fn   http.HandlerFunc
		body string
		want string
	}{
		{"me empty", h.PatchMe, `{}`, "send name"},
		{"me blank name", h.PatchMe, `{"name":"  "}`, "name must be"},
		{"me bad language", h.PatchMe, `{"language":"fr"}`, "language must be"},
		{"me unknown field", h.PatchMe, `{"nick":"x"}`, "invalid body"},
		{"me foreign photo", h.PatchMe, `{"photo_url":"ftp://x"}`, "photo_url must be"},
		{"shop blank name", h.PatchShop, `{"name":""}`, "name must be"},
		{"shop empty", h.PatchShop, `{}`, "send name"},
		{"categories missing", h.PutCategories, `{}`, "categories is required"},
		{"categories unknown", h.PutCategories, `{"categories":["pizza"]}`, "unknown category: pizza"},
		{"locations none", h.PutLocations, `{"has_farm":false,"has_shop":false}`, "pick at least one location"},
		{"offer bad", h.PutOffer, `{"enabled":true,"type":"discount","discount_amount":3,"min_bill":500}`, "discount_amount"},
		{"invite bad phone", h.Invite, `{"phone":"123","role":"cashier"}`, "10 digits"},
		{"invite owner", h.Invite, `{"phone":"9000000002","role":"owner"}`, "owner cannot be invited"},
		{"device bad", h.PutDevice, `{"connected":true}`, "device must be"},
		{"export bad format", h.CreateExport, `{"format":"pdf"}`, "format must be csv"},
		{"preview bad", h.Preview, ``, "bill_total"},
	}
	for _, c := range cases {
		rec := call(c.fn, "POST", c.body, "owner")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if rec := call(h.Upgrade, "POST", ``, "owner"); rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "payments not enabled") {
		t.Errorf("upgrade: %d %s", rec.Code, rec.Body.String())
	}
}

// Mirrors the route groups in cmd/api/main.go.
func TestRoleMatrix(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, 200, map[string]bool{"ok": true}) }
	router := func(role string) http.Handler {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := auth.WithMember(auth.WithUser(req.Context(), uuid.New()), auth.Member{ShopID: uuid.New(), Role: role})
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Get("/profile", ok)
		r.With(auth.RequireRoles("owner", "cashier")).Get("/staff", ok)
		r.With(auth.RequireRoles("owner", "cashier")).Put("/devices/x", ok)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOwner)
			r.Put("/offer", ok)
			r.Get("/plan", ok)
			r.Post("/staff/invite", ok)
		})
		return r
	}
	type row struct{ method, path string }
	want := map[string]map[row]int{
		"owner":        {{"GET", "/profile"}: 200, {"GET", "/staff"}: 200, {"PUT", "/devices/x"}: 200, {"PUT", "/offer"}: 200, {"GET", "/plan"}: 200},
		"cashier":      {{"GET", "/profile"}: 200, {"GET", "/staff"}: 200, {"PUT", "/devices/x"}: 200, {"PUT", "/offer"}: 403, {"GET", "/plan"}: 403, {"POST", "/staff/invite"}: 403},
		"farm_manager": {{"GET", "/profile"}: 200, {"GET", "/staff"}: 403, {"PUT", "/devices/x"}: 403, {"PUT", "/offer"}: 403, {"GET", "/plan"}: 403},
	}
	for role, rows := range want {
		for rw, code := range rows {
			rec := httptest.NewRecorder()
			router(role).ServeHTTP(rec, httptest.NewRequest(rw.method, rw.path, nil))
			if rec.Code != code {
				t.Errorf("%s %s %s: got %d want %d", role, rw.method, rw.path, rec.Code, code)
			}
		}
	}
	rec := httptest.NewRecorder()
	router("cashier").ServeHTTP(rec, httptest.NewRequest("GET", "/plan", nil))
	if !strings.Contains(rec.Body.String(), "owner only") {
		t.Errorf("message: %s", rec.Body.String())
	}
}
