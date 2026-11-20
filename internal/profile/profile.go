// Package profile serves the Profile screen: the one-call GET /v1/profile, editing the user
// and shop, what the shop sells, locations, offer of the day, staff, devices and plan.
package profile

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/uploads"
)

type Handler struct {
	DB *pgxpool.Pool
	// LogoPrefix, when set, is the only allowed start of photo_url / logo_url (our storage bucket).
	LogoPrefix string
	TrialDays  int
	Storage    *uploads.Storage // nil when SUPABASE_SERVICE_KEY is not set (backups and exports then fail)
}

var (
	languages  = map[string]bool{"en": true, "hi": true, "or": true, "hinglish": true}
	categories = []string{"chicken", "fish", "eggs", "mutton", "dairy"}
)

func (h Handler) trialDays() int {
	if h.TrialDays > 0 {
		return h.TrialDays
	}
	return 14
}

// ---------- shared shapes ----------

type userOut struct {
	ID       uuid.UUID `json:"id"`
	Name     *string   `json:"name"`
	Phone    string    `json:"phone"`
	PhotoURL *string   `json:"photo_url"`
	Language string    `json:"language"`
}

type locationOut struct {
	ID     uuid.UUID `json:"id"`
	Type   string    `json:"type"`
	Name   string    `json:"name"`
	Lat    *float64  `json:"lat"`
	Lng    *float64  `json:"lng"`
	Active bool      `json:"active"`
}

type shopOut struct {
	ID         uuid.UUID     `json:"id"`
	Name       string        `json:"name"`
	LogoURL    *string       `json:"logo_url"`
	Language   string        `json:"language"`
	Role       string        `json:"role"`
	Categories []string      `json:"categories"`
	Locations  []locationOut `json:"locations"`
}

func loadUser(ctx context.Context, q db.Querier, id uuid.UUID) (userOut, error) {
	u := userOut{ID: id}
	err := q.QueryRow(ctx, `
		select coalesce(a.phone, ''), p.name, p.photo_url, coalesce(p.language, 'en')
		from auth.users a left join profiles p on p.user_id = a.id where a.id = $1`, id).
		Scan(&u.Phone, &u.Name, &u.PhotoURL, &u.Language)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, httpx.NotFound("user not found")
	}
	return u, err
}

// loadLocations returns the shop's locations. Farm managers only see the farm.
func loadLocations(ctx context.Context, q db.Querier, shopID uuid.UUID, role string) ([]locationOut, error) {
	rows, err := q.Query(ctx, `
		select id, type, name, lat, lng, active from locations
		where shop_id = $1 and ($2::text <> 'farm_manager' or type = 'farm')
		order by type, created_at`, shopID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []locationOut{}
	for rows.Next() {
		var l locationOut
		if err := rows.Scan(&l.ID, &l.Type, &l.Name, &l.Lat, &l.Lng, &l.Active); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func loadShop(ctx context.Context, q db.Querier, m auth.Member) (shopOut, error) {
	s := shopOut{ID: m.ShopID, Role: m.Role}
	if err := q.QueryRow(ctx, `select name, logo_url, language, categories from shops where id = $1`, m.ShopID).
		Scan(&s.Name, &s.LogoURL, &s.Language, &s.Categories); err != nil {
		return s, err
	}
	var err error
	s.Locations, err = loadLocations(ctx, q, m.ShopID, m.Role)
	return s, err
}

// ---------- pure helpers (unit tested) ----------

// TrialDaysLeft is max(0, ceil((trialEnds - now) / 24h)); 0 when the plan is not a trial.
func TrialDaysLeft(plan string, trialEnds *time.Time, now time.Time) int {
	if plan != "trial" || trialEnds == nil {
		return 0
	}
	d := math.Ceil(trialEnds.Sub(now).Hours() / 24)
	if d < 0 {
		return 0
	}
	return int(d)
}

// cleanURL checks a photo or logo url. "" means "remove" (null in the request).
func cleanURL(u, prefix, field string) (string, error) {
	u = strings.TrimSpace(u)
	if u == "" {
		return "", nil
	}
	if prefix != "" && !strings.HasPrefix(u, prefix) {
		return "", httpx.BadRequest(field + " must be the url returned by POST /v1/uploads/logo")
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return "", httpx.BadRequest(field + " must be an http(s) url")
	}
	return u, nil
}

func cleanName(n, field string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 80 {
		return "", httpx.BadRequest(field + " must be 1 to 80 characters")
	}
	return n, nil
}

// optString is a JSON field that can be absent, null or a string.
type optString struct {
	Set   bool
	Value *string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := jsonUnmarshal(b, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

// ---------- GET /v1/profile ----------

type profileOut struct {
	User   userOut `json:"user"`
	Shop   shopOut `json:"shop"`
	Prices struct {
		Count        int  `json:"count"`
		UpdatedToday bool `json:"updated_today"`
	} `json:"prices"`
	Offer   offerOut `json:"offer"`
	Rewards struct {
		SpendPerPoint int     `json:"spend_per_point"`
		PointValue    float64 `json:"point_value"`
		ExpiryMonths  *int    `json:"expiry_months"`
		MaxPayPercent int     `json:"max_pay_percent"`
	} `json:"rewards"`
	Staff struct {
		Count int `json:"count"`
	} `json:"staff"`
	Stock struct {
		Items int `json:"items"` // stock items (farm only for a farm manager)
		Low   int `json:"low"`   // at or below their reorder level
	} `json:"stock"`
	Plan    *planOut `json:"plan"`
	Devices struct {
		WhatsApp     bool `json:"whatsapp"`
		Printer      bool `json:"printer"`
		Scale        bool `json:"scale"`
		AllConnected bool `json:"all_connected"`
	} `json:"devices"`
}

// GET /v1/profile (any role) — everything the Profile tab shows. `plan` is null unless owner.
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	var out profileOut
	var err error
	if out.User, err = loadUser(ctx, h.DB, auth.UserID(ctx)); err != nil {
		httpx.Fail(w, err)
		return
	}
	if out.Shop, err = loadShop(ctx, h.DB, m); err != nil {
		httpx.Fail(w, err)
		return
	}
	day := time.Now().In(m.Location()).Format("2006-01-02")
	if err = h.DB.QueryRow(ctx, `
		select (select count(*) from items where shop_id = $1 and active)::int,
		       exists(select 1 from item_prices p join items i on i.id = p.item_id where i.shop_id = $1 and p.day = $2::date),
		       (select count(*) from members where shop_id = $1 and status = 'active')::int
		       + (select count(*) from staff_invites where shop_id = $1)::int`, m.ShopID, day).
		Scan(&out.Prices.Count, &out.Prices.UpdatedToday, &out.Staff.Count); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err = h.DB.QueryRow(ctx, `
		select count(*)::int, count(*) filter (where si.reorder_level is not null and si.quantity <= si.reorder_level)::int
		from stock_items si join locations l on l.id = si.location_id
		where si.shop_id = $1 and ($2::text <> 'farm_manager' or l.type = 'farm')`, m.ShopID, m.Role).
		Scan(&out.Stock.Items, &out.Stock.Low); err != nil {
		httpx.Fail(w, err)
		return
	}
	if out.Offer, err = loadOffer(ctx, h.DB, m.ShopID); err != nil {
		httpx.Fail(w, err)
		return
	}
	var perRupees float64
	if err = h.DB.QueryRow(ctx, `
		select points_per_rupees::float8, point_value::float8, points_expiry_months, max_points_pct from shops where id = $1`, m.ShopID).
		Scan(&perRupees, &out.Rewards.PointValue, &out.Rewards.ExpiryMonths, &out.Rewards.MaxPayPercent); err != nil {
		httpx.Fail(w, err)
		return
	}
	out.Rewards.SpendPerPoint = int(math.Round(perRupees))
	dev, err := loadDevices(ctx, h.DB, m.ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out.Devices.WhatsApp, out.Devices.Printer, out.Devices.Scale = dev.WhatsApp.Connected, dev.Printer.Connected, dev.Scale.Connected
	out.Devices.AllConnected = out.Devices.WhatsApp && out.Devices.Printer && out.Devices.Scale
	if m.Role == "owner" {
		p, err := h.loadPlan(ctx, h.DB, m.ShopID)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		out.Plan = &p
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---------- PATCH /v1/me ----------

type patchMeIn struct {
	Name     *string   `json:"name"`
	PhotoURL optString `json:"photo_url"`
	Language *string   `json:"language"`
}

// PATCH /v1/me (token) — all fields optional. Returns the user object.
func (h Handler) PatchMe(w http.ResponseWriter, r *http.Request) {
	var in patchMeIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	var name, photo, lang *string
	if in.Name != nil {
		n, err := cleanName(*in.Name, "name")
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		name = &n
	}
	if in.Language != nil {
		l := strings.ToLower(strings.TrimSpace(*in.Language))
		if !languages[l] {
			httpx.Error(w, http.StatusBadRequest, "language must be en, hi, or or hinglish")
			return
		}
		lang = &l
	}
	if in.PhotoURL.Set && in.PhotoURL.Value != nil {
		u, err := cleanURL(*in.PhotoURL.Value, h.LogoPrefix, "photo_url")
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		if u != "" {
			photo = &u
		}
	}
	if name == nil && lang == nil && !in.PhotoURL.Set {
		httpx.Error(w, http.StatusBadRequest, "send name, photo_url or language")
		return
	}
	ctx := r.Context()
	uid := auth.UserID(ctx)
	// photo_url: not sent = keep, null or "" = remove, url = set.
	if _, err := h.DB.Exec(ctx, `
		insert into profiles (user_id, name, photo_url, language)
		values ($1, $2, $3, coalesce($4, 'en'))
		on conflict (user_id) do update set
		  name = coalesce($2, profiles.name),
		  photo_url = case when $5::bool then $3 else profiles.photo_url end,
		  language = coalesce($4, profiles.language),
		  updated_at = now()`, uid, name, photo, lang, in.PhotoURL.Set); err != nil {
		httpx.Fail(w, err)
		return
	}
	u, err := loadUser(ctx, h.DB, uid)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, u)
}

// ---------- PATCH /v1/shops/current ----------

type patchShopIn struct {
	Name     *string   `json:"name"`
	LogoURL  optString `json:"logo_url"`
	Language *string   `json:"language"`
}

// PATCH /v1/shops/current (owner). Returns the shop object.
func (h Handler) PatchShop(w http.ResponseWriter, r *http.Request) {
	var in patchShopIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	var name, logo, lang *string
	if in.Name != nil {
		n, err := cleanName(*in.Name, "name")
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		name = &n
	}
	if in.Language != nil {
		l := strings.ToLower(strings.TrimSpace(*in.Language))
		if !languages[l] {
			httpx.Error(w, http.StatusBadRequest, "language must be en, hi, or or hinglish")
			return
		}
		lang = &l
	}
	if in.LogoURL.Set && in.LogoURL.Value != nil {
		u, err := cleanURL(*in.LogoURL.Value, h.LogoPrefix, "logo_url")
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		if u != "" {
			logo = &u
		}
	}
	if name == nil && lang == nil && !in.LogoURL.Set {
		httpx.Error(w, http.StatusBadRequest, "send name, logo_url or language")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	if _, err := h.DB.Exec(ctx, `
		update shops set name = coalesce($2, name),
		  logo_url = case when $4::bool then $3 else logo_url end,
		  language = coalesce($5, language)
		where id = $1`, m.ShopID, name, logo, in.LogoURL.Set, lang); err != nil {
		httpx.Fail(w, err)
		return
	}
	s, err := loadShop(ctx, h.DB, m)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

// ---------- PUT /v1/shops/current/categories ----------

type categoriesIn struct {
	Categories *[]string `json:"categories"`
}

// cleanCategories lowercases, dedupes and rejects unknown names.
func cleanCategories(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		ok := false
		for _, a := range categories {
			ok = ok || a == c
		}
		if !ok {
			return nil, httpx.BadRequest("unknown category: " + c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// PUT /v1/shops/current/categories (owner). Items and history are kept; the app hides the rest.
func (h Handler) PutCategories(w http.ResponseWriter, r *http.Request) {
	var in categoriesIn
	if err := httpx.Decode(r, &in); err != nil || in.Categories == nil {
		httpx.Error(w, http.StatusBadRequest, "categories is required")
		return
	}
	cats, err := cleanCategories(*in.Categories)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	if _, err := h.DB.Exec(ctx, `update shops set categories = $2 where id = $1`, m.ShopID, cats); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"categories": cats})
}

// ---------- locations ----------

// GET /v1/locations
func (h Handler) GetLocations(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	l, err := loadLocations(r.Context(), h.DB, m.ShopID, m.Role)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, l)
}

type geoIn struct {
	Lat *float64 `json:"lat"`
	Lng *float64 `json:"lng"`
}

type locationsIn struct {
	HasFarm      *bool  `json:"has_farm"`
	HasShop      *bool  `json:"has_shop"`
	FarmLocation *geoIn `json:"farm_location"`
	ShopLocation *geoIn `json:"shop_location"`
}

func (g *geoIn) empty() bool { return g == nil || (g.Lat == nil && g.Lng == nil) }

func (g *geoIn) check(label string) error {
	if g.empty() {
		return nil
	}
	switch {
	case g.Lat == nil || g.Lng == nil:
		return httpx.BadRequest(label + " needs both lat and lng")
	case *g.Lat < -90 || *g.Lat > 90:
		return httpx.BadRequest(label + ": lat must be between -90 and 90")
	case *g.Lng < -180 || *g.Lng > 180:
		return httpx.BadRequest(label + ": lng must be between -180 and 180")
	}
	return nil
}

// validate applies the Shops & farms rules.
func (in locationsIn) validate() error {
	if in.HasFarm == nil || in.HasShop == nil {
		return httpx.BadRequest("has_farm and has_shop are required")
	}
	if !*in.HasFarm && !*in.HasShop {
		return httpx.BadRequest("pick at least one location")
	}
	if !in.FarmLocation.empty() && !*in.HasFarm {
		return httpx.BadRequest("farm_location given but has_farm is false")
	}
	if !in.ShopLocation.empty() && !*in.HasShop {
		return httpx.BadRequest("shop_location given but has_shop is false")
	}
	if err := in.FarmLocation.check("farm_location"); err != nil {
		return err
	}
	return in.ShopLocation.check("shop_location")
}

// PUT /v1/locations (owner). Off = soft delete (active=false); on creates or re-activates.
func (h Handler) PutLocations(w http.ResponseWriter, r *http.Request) {
	var in locationsIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(w, err)
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	apply := func(typ, name string, on bool, g *geoIn) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `select id from locations where shop_id = $1 and type = $2 order by created_at limit 1 for update`,
			m.ShopID, typ).Scan(&id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if !on {
				return nil
			}
			var lat, lng *float64
			if !g.empty() {
				lat, lng = g.Lat, g.Lng
			}
			_, err = tx.Exec(ctx, `insert into locations (shop_id, name, type, lat, lng) values ($1, $2, $3, $4, $5)`,
				m.ShopID, name, typ, lat, lng)
			return err
		case err != nil:
			return err
		}
		if !on {
			_, err = tx.Exec(ctx, `update locations set active = false where shop_id = $1 and type = $2`, m.ShopID, typ)
			return err
		}
		// A position is only changed when one is sent; re-activating keeps the old one.
		if g.empty() {
			_, err = tx.Exec(ctx, `update locations set active = true where id = $1`, id)
		} else {
			_, err = tx.Exec(ctx, `update locations set active = true, lat = $2, lng = $3 where id = $1`, id, *g.Lat, *g.Lng)
		}
		return err
	}
	if err := apply("farm", "Poultry farm", *in.HasFarm, in.FarmLocation); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := apply("shop", "Shop", *in.HasShop, in.ShopLocation); err != nil {
		httpx.Fail(w, err)
		return
	}
	l, err := loadLocations(ctx, tx, m.ShopID, m.Role)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, l)
}
