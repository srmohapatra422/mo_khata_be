package shops

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

type Handler struct {
	DB *pgxpool.Pool
	// LogoPrefix, when set, is the only allowed start of logo_url (our storage bucket).
	LogoPrefix string
}

type shopOut struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	LogoURL    *string   `json:"logo_url"`
	Categories []string  `json:"categories"`
}

// What the shop can sell. Matches items.category.
var allowedCategories = map[string]bool{
	"chicken": true, "fish": true, "eggs": true, "mutton": true, "dairy": true,
}

// GET /v1/me — shops this user belongs to.
func (h Handler) Me(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `
		select s.id, s.name, m.role, s.logo_url, s.categories
		from members m join shops s on s.id = m.shop_id
		where m.user_id = $1 order by s.created_at`, auth.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()
	shops := []shopOut{}
	for rows.Next() {
		var s shopOut
		if err := rows.Scan(&s.ID, &s.Name, &s.Role, &s.LogoURL, &s.Categories); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		shops = append(shops, s)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user_id": auth.UserID(r.Context()), "shops": shops})
}

type createShopIn struct {
	Name       string   `json:"name"`
	LogoURL    string   `json:"logo_url"`   // optional
	Categories []string `json:"categories"` // chicken, fish, eggs, mutton, dairy
	HasFarm    bool     `json:"has_farm"`   // "Poultry farm"
	HasShop    *bool    `json:"has_shop"`   // "Shop"; defaults to true when omitted
	ShopLabel  string   `json:"shop_label"` // name of the shop location, default "Shop"

	FarmLocation *geoIn `json:"farm_location"` // optional lat/lng of the farm
	ShopLocation *geoIn `json:"shop_location"` // optional lat/lng of the shop
}

// geoIn is a map position. Both values are needed together.
type geoIn struct {
	Lat *float64 `json:"lat"`
	Lng *float64 `json:"lng"`
}

// check returns an error message, or "" when the position is valid.
func (g *geoIn) check(label string) string {
	if g == nil {
		return ""
	}
	if g.Lat == nil || g.Lng == nil {
		return label + " needs both lat and lng"
	}
	if *g.Lat < -90 || *g.Lat > 90 {
		return label + ": lat must be between -90 and 90"
	}
	if *g.Lng < -180 || *g.Lng > 180 {
		return label + ": lng must be between -180 and 180"
	}
	return ""
}

type locationOut struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Type string    `json:"type"`
	Lat  *float64  `json:"lat"`
	Lng  *float64  `json:"lng"`
}

// POST /v1/shops — onboarding: creates the shop, makes the caller owner, adds locations.
func (h Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in createShopIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	hasShop := in.HasShop == nil || *in.HasShop
	if !in.HasFarm && !hasShop {
		httpx.Error(w, http.StatusBadRequest, "pick at least one location")
		return
	}
	// null, {} and {"lat":null,"lng":null} all mean "no position given".
	if in.FarmLocation != nil && in.FarmLocation.Lat == nil && in.FarmLocation.Lng == nil {
		in.FarmLocation = nil
	}
	if in.ShopLocation != nil && in.ShopLocation.Lat == nil && in.ShopLocation.Lng == nil {
		in.ShopLocation = nil
	}
	if in.FarmLocation != nil && !in.HasFarm {
		httpx.Error(w, http.StatusBadRequest, "farm_location given but has_farm is false")
		return
	}
	if in.ShopLocation != nil && !hasShop {
		httpx.Error(w, http.StatusBadRequest, "shop_location given but has_shop is false")
		return
	}
	if msg := in.FarmLocation.check("farm_location"); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	if msg := in.ShopLocation.check("shop_location"); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	if in.ShopLabel = strings.TrimSpace(in.ShopLabel); in.ShopLabel == "" {
		in.ShopLabel = "Shop"
	}
	categories := []string{}
	seen := map[string]bool{}
	for _, c := range in.Categories {
		c = strings.ToLower(strings.TrimSpace(c))
		if !allowedCategories[c] {
			httpx.Error(w, http.StatusBadRequest, "unknown category: "+c)
			return
		}
		if !seen[c] {
			seen[c] = true
			categories = append(categories, c)
		}
	}
	var logo *string
	if u := strings.TrimSpace(in.LogoURL); u != "" {
		if h.LogoPrefix != "" && !strings.HasPrefix(u, h.LogoPrefix) {
			httpx.Error(w, http.StatusBadRequest, "logo_url must be the url returned by POST /v1/uploads/logo")
			return
		}
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			httpx.Error(w, http.StatusBadRequest, "logo_url must be an http(s) url")
			return
		}
		logo = &u
	}

	ctx := r.Context()
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)

	var shopID uuid.UUID
	if err := tx.QueryRow(ctx, `insert into shops (name, logo_url, categories) values ($1, $2, $3) returning id`,
		in.Name, logo, categories).Scan(&shopID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	if _, err := tx.Exec(ctx, `insert into members (shop_id, user_id, role) values ($1, $2, 'owner')`,
		shopID, auth.UserID(ctx)); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}

	locations := []locationOut{}
	add := func(name, typ string, g *geoIn) error {
		var lat, lng *float64
		if g != nil {
			lat, lng = g.Lat, g.Lng
		}
		var l locationOut
		if err := tx.QueryRow(ctx, `
			insert into locations (shop_id, name, type, lat, lng) values ($1, $2, $3, $4, $5)
			returning id, name, type, lat, lng`,
			shopID, name, typ, lat, lng).Scan(&l.ID, &l.Name, &l.Type, &l.Lat, &l.Lng); err != nil {
			return err
		}
		locations = append(locations, l)
		return nil
	}
	if in.HasFarm {
		if err := add("Poultry farm", "farm", in.FarmLocation); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
	}
	if hasShop {
		if err := add(in.ShopLabel, "shop", in.ShopLocation); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"id": shopID, "name": in.Name, "logo_url": logo, "categories": categories, "locations": locations,
	})
}
