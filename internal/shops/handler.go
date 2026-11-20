package shops

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/profile"
	"mohisab-api/internal/rewards"
	"mohisab-api/internal/stock"
)

type Handler struct {
	DB *pgxpool.Pool
	// LogoPrefix, when set, is the only allowed start of logo_url (our storage bucket).
	LogoPrefix string
	TrialDays  int // length of the free trial created with the shop (0 = 14)
}

type shopOut struct {
	ID         uuid.UUID     `json:"id"`
	Name       string        `json:"name"`
	Role       string        `json:"role"`
	LogoURL    *string       `json:"logo_url"`
	Categories []string      `json:"categories"`
	Locations  []locationOut `json:"locations"`
}

// What the shop can sell. Matches items.category.
var allowedCategories = map[string]bool{
	"chicken": true, "fish": true, "eggs": true, "mutton": true, "dairy": true,
}

// GET /v1/me — the user and the shops they belong to (with each shop's locations).
func (h Handler) Me(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := auth.UserID(ctx)
	var phone string
	var name, photo *string
	lang := "en"
	err := h.DB.QueryRow(ctx, `
		select coalesce(a.phone, ''), p.name, p.photo_url, coalesce(p.language, 'en')
		from auth.users a left join profiles p on p.user_id = a.id where a.id = $1`, uid).Scan(&phone, &name, &photo, &lang)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	// A staff invite waits for this phone to log in; accept it now.
	if err := profile.AttachInvites(ctx, h.DB, uid, phone); err != nil {
		log.Printf("attach invites: %v", err)
	}
	rows, err := h.DB.Query(ctx, `
		select s.id, s.name, m.role, s.logo_url, s.categories
		from members m join shops s on s.id = m.shop_id
		where m.user_id = $1 and m.status = 'active' order by s.created_at`, uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	shops := []shopOut{}
	for rows.Next() {
		var s shopOut
		if err := rows.Scan(&s.ID, &s.Name, &s.Role, &s.LogoURL, &s.Categories); err != nil {
			rows.Close()
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		s.Locations = []locationOut{}
		shops = append(shops, s)
	}
	rows.Close()
	for i := range shops {
		lr, err := h.DB.Query(ctx, `
			select id, name, type, lat, lng, active from locations
			where shop_id = $1 and ($2::text <> 'farm_manager' or type = 'farm') order by type, created_at`, shops[i].ID, shops[i].Role)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		for lr.Next() {
			var l locationOut
			if err := lr.Scan(&l.ID, &l.Name, &l.Type, &l.Lat, &l.Lng, &l.Active); err != nil {
				lr.Close()
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
			shops[i].Locations = append(shops[i].Locations, l)
		}
		lr.Close()
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"user_id": uid,
		"user":    map[string]any{"id": uid, "name": name, "phone": phone, "photo_url": photo, "language": lang},
		"shops":   shops,
	})
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

	// Optional: the "Reward your buyers" and "Opening stock" answers from onboarding.
	Rewards *rewardsIn `json:"rewards"`
	Stock   []stockIn  `json:"stock"`
}

type rewardsIn struct {
	SpendPerPoint *int     `json:"spend_per_point"`
	PointValue    *float64 `json:"point_value"`
	ExpiryMonths  *int     `json:"expiry_months"`
	MaxPayPercent *int     `json:"max_pay_percent"`
}

// stockIn is one opening stock line. location is "farm" or "shop" (default: the shop, else the farm).
type stockIn struct {
	Location string   `json:"location"`
	Name     string   `json:"name"`
	Unit     string   `json:"unit"`
	Quantity float64  `json:"quantity"`
	Rate     *float64 `json:"rate"`
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
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Lat    *float64  `json:"lat"`
	Lng    *float64  `json:"lng"`
	Active bool      `json:"active"`
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

	if in.Rewards != nil {
		if msg := rewards.Check(in.Rewards.SpendPerPoint, in.Rewards.PointValue, in.Rewards.ExpiryMonths, in.Rewards.MaxPayPercent); msg != "" {
			httpx.Error(w, http.StatusBadRequest, "rewards: "+msg)
			return
		}
	}
	stockItems := make([]stock.Item, len(in.Stock))
	stockLoc := make([]string, len(in.Stock))
	for i, si := range in.Stock {
		loc := strings.ToLower(strings.TrimSpace(si.Location))
		if loc == "" {
			loc = "shop"
			if !hasShop {
				loc = "farm"
			}
		}
		if (loc != "farm" || !in.HasFarm) && (loc != "shop" || !hasShop) {
			httpx.Error(w, http.StatusBadRequest, "stock: location must be farm or shop, and that location must be switched on")
			return
		}
		stockLoc[i] = loc
		stockItems[i] = stock.Item{Name: si.Name, Unit: si.Unit, Quantity: si.Quantity, Rate: si.Rate}
	}
	if len(stockItems) > 0 {
		if msg := stock.ValidateItems(stockItems); msg != "" {
			httpx.Error(w, http.StatusBadRequest, "stock: "+msg)
			return
		}
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

	days := h.TrialDays
	if days <= 0 {
		days = 14
	}
	if _, err := tx.Exec(ctx, `insert into subscriptions (shop_id, plan, trial_ends_at) values ($1, 'trial', now() + make_interval(days => $2::int))`,
		shopID, days); err != nil {
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
			returning id, name, type, lat, lng, active`,
			shopID, name, typ, lat, lng).Scan(&l.ID, &l.Name, &l.Type, &l.Lat, &l.Lng, &l.Active); err != nil {
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
	if r := in.Rewards; r != nil {
		if err := rewards.Save(ctx, tx, shopID, *r.SpendPerPoint, *r.PointValue, *r.ExpiryMonths, *r.MaxPayPercent); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
	}
	for _, loc := range []string{"farm", "shop"} {
		var batch []stock.Item
		for i, it := range stockItems {
			if stockLoc[i] == loc {
				batch = append(batch, it)
			}
		}
		if len(batch) == 0 {
			continue
		}
		var locID uuid.UUID
		for _, l := range locations {
			if l.Type == loc {
				locID = l.ID
			}
		}
		if err := stock.AddItems(ctx, tx, shopID, locID, auth.UserID(ctx), "form", batch); err != nil {
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
