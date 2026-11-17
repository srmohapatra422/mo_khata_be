// Package pricing serves categories, price items and "Today's rates".
//
// Price items are rows of the existing items table and every price is a row of item_prices
// (price history): today's price is the row for today, yesterday's is the latest row before today.
package pricing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/catalog"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/names"
	"mohisab-api/internal/outbox"
)

type Handler struct{ DB *pgxpool.Pool }

var allowedUnits = map[string]bool{"kg": true, "tray": true, "piece": true, "bird": true}

const (
	maxNameLen  = 80
	maxBulkSize = 200
)

// ---------- response shapes ----------

type categoryRef struct {
	ID   uuid.UUID `json:"id"`
	Key  *string   `json:"key"`
	Name string    `json:"name"`
}

type itemOut struct {
	ID        uuid.UUID   `json:"id"`
	Category  categoryRef `json:"category"`
	Name      string      `json:"name"`
	Unit      string      `json:"unit"`
	Price     *float64    `json:"price"`
	Yesterday *float64    `json:"yesterday"`
	Change    *float64    `json:"change"`
}

type pricesOut struct {
	Date         string    `json:"date"`
	UpdatedToday bool      `json:"updated_today"`
	OptedIn      int       `json:"opted_in_customers"`
	Items        []itemOut `json:"items"`
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func today(m auth.Member) string { return time.Now().In(m.Location()).Format("2006-01-02") }

// queryItems loads active items (or exactly one item, active or not) with today's and yesterday's price.
func queryItems(ctx context.Context, q db.Querier, shopID uuid.UUID, day string, onlyID *uuid.UUID) ([]itemOut, error) {
	rows, err := q.Query(ctx, `
		select i.id, c.id, c.key, c.name, i.name, i.unit,
		       (select p.price::float8 from item_prices p where p.item_id = i.id and p.day <= $2::date order by p.day desc limit 1),
		       (select p.price::float8 from item_prices p where p.item_id = i.id and p.day <  $2::date order by p.day desc limit 1)
		from items i join categories c on c.id = i.category_id join shops s on s.id = i.shop_id
		where i.shop_id = $1
		  and case when $3::uuid is null
		       then i.active and (c.key is null or c.key = any(s.categories))  -- categories switched off are hidden
		       else i.id = $3::uuid end
		order by array_position(array['chicken','fish','eggs','mutton','dairy'], c.key), c.name, i.name`,
		shopID, day, onlyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []itemOut{}
	for rows.Next() {
		var it itemOut
		if err := rows.Scan(&it.ID, &it.Category.ID, &it.Category.Key, &it.Category.Name, &it.Name, &it.Unit,
			&it.Price, &it.Yesterday); err != nil {
			return nil, err
		}
		if it.Price != nil && it.Yesterday != nil {
			c := round2(*it.Price - *it.Yesterday)
			it.Change = &c
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func pricesBody(ctx context.Context, q db.Querier, m auth.Member) (pricesOut, error) {
	day := today(m)
	out := pricesOut{Date: day}
	items, err := queryItems(ctx, q, m.ShopID, day, nil)
	if err != nil {
		return out, err
	}
	out.Items = items
	err = q.QueryRow(ctx, `
		select exists(select 1 from item_prices p join items i on i.id = p.item_id
		              where i.shop_id = $1 and p.day = $2::date),
		       (select count(*) from customers where shop_id = $1 and whatsapp_opt_in)::int`,
		m.ShopID, day).Scan(&out.UpdatedToday, &out.OptedIn)
	return out, err
}

func oneItem(ctx context.Context, q db.Querier, m auth.Member, id uuid.UUID) (itemOut, error) {
	items, err := queryItems(ctx, q, m.ShopID, today(m), &id)
	if err != nil {
		return itemOut{}, err
	}
	if len(items) == 0 {
		return itemOut{}, httpx.NotFound("item not found")
	}
	return items[0], nil
}

// ---------- categories ----------

// GET /v1/categories
func (h Handler) Categories(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	cats, err := catalog.List(r.Context(), h.DB, m.ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cats)
}

type createCategoryIn struct {
	Name string `json:"name"`
}

// POST /v1/categories (owner)
func (h Handler) CreateCategory(w http.ResponseWriter, r *http.Request) {
	var in createCategoryIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 60 || names.NormalizeName(in.Name) == "" {
		httpx.Error(w, http.StatusBadRequest, "name is required (up to 60 characters)")
		return
	}
	c, err := catalog.Create(r.Context(), h.DB, auth.MemberOf(r.Context()).ShopID, in.Name)
	switch {
	case errors.Is(err, catalog.ErrCategoryExists):
		httpx.Error(w, http.StatusConflict, "category already exists")
	case err != nil:
		httpx.Fail(w, err)
	default:
		httpx.JSON(w, http.StatusCreated, c)
	}
}

// ---------- reading prices ----------

// GET /v1/prices
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	body, err := pricesBody(r.Context(), h.DB, auth.MemberOf(r.Context()))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

// ---------- creating items ----------

type newItemIn struct {
	CategoryID   *uuid.UUID `json:"category_id"`
	CategoryName string     `json:"category_name"`
	Name         string     `json:"name"`
	Unit         string     `json:"unit"`
	Price        *float64   `json:"price"`
}

// validate returns a message for the first problem, or "".
func (in *newItemIn) validate() string {
	in.Name = strings.TrimSpace(in.Name)
	in.CategoryName = strings.TrimSpace(in.CategoryName)
	in.Unit = strings.ToLower(strings.TrimSpace(in.Unit))
	switch {
	case in.CategoryID == nil && in.CategoryName == "":
		return "category_id or category_name is required"
	case in.Name == "" || names.NormalizeName(in.Name) == "":
		return "name is required"
	case utf8.RuneCountInString(in.Name) > maxNameLen:
		return "name is too long"
	case !allowedUnits[in.Unit]:
		return "unit must be kg, tray, piece or bird"
	case in.Price == nil || *in.Price <= 0:
		return "price is required and must be more than 0"
	}
	return ""
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// createItem adds the item and today's price inside tx. Errors are *httpx.Err when the caller is at fault.
func createItem(ctx context.Context, tx pgx.Tx, m auth.Member, userID uuid.UUID, day string, in newItemIn) (uuid.UUID, error) {
	cat, err := catalog.Resolve(ctx, tx, m.ShopID, in.CategoryID, in.CategoryName)
	if errors.Is(err, catalog.ErrUnknownCategory) {
		return uuid.Nil, httpx.BadRequest("unknown category")
	}
	if err != nil {
		return uuid.Nil, err
	}
	norm := names.NormalizeName(in.Name)
	var dup bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from items where shop_id = $1 and category_id = $2 and name_norm = $3 and active)`,
		m.ShopID, cat.ID, norm).Scan(&dup); err != nil {
		return uuid.Nil, err
	}
	if dup {
		return uuid.Nil, httpx.Conflict("item already exists: " + in.Name)
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into items (shop_id, category, category_id, name, name_norm, unit)
		values ($1, $2, $3, $4, $5, $6) returning id`,
		m.ShopID, catalog.ItemCategoryValue(cat), cat.ID, in.Name, norm, in.Unit).Scan(&id)
	if isUniqueViolation(err) {
		return uuid.Nil, httpx.Conflict("item already exists: " + in.Name)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `insert into item_prices (item_id, day, price, updated_by) values ($1, $2::date, $3, $4)`,
		id, day, *in.Price, userID); err != nil {
		return uuid.Nil, err
	}
	if cat.Key != nil {
		if err := catalog.AddToShop(ctx, tx, m.ShopID, *cat.Key); err != nil {
			return uuid.Nil, err
		}
	}
	return id, nil
}

// POST /v1/prices/items (owner, cashier)
func (h Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
	var in newItemIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if msg := in.validate(); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
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
	id, err := createItem(ctx, tx, m, auth.UserID(ctx), today(m), in)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	item, err := oneItem(ctx, tx, m, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, item)
}

type bulkIn struct {
	Items []newItemIn `json:"items"`
}

// POST /v1/prices/items/bulk (owner) — the "Start from a ready list" screen. All or nothing.
func (h Handler) BulkCreate(w http.ResponseWriter, r *http.Request) {
	var in bulkIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(in.Items) == 0 || len(in.Items) > maxBulkSize {
		httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("send between 1 and %d items", maxBulkSize))
		return
	}
	for i := range in.Items {
		if msg := in.Items[i].validate(); msg != "" {
			httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("item %d: %s", i+1, msg))
			return
		}
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	day := today(m)
	ids := make([]uuid.UUID, 0, len(in.Items))
	for _, it := range in.Items {
		id, err := createItem(ctx, tx, m, auth.UserID(ctx), day, it)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		ids = append(ids, id)
	}
	out := make([]itemOut, 0, len(ids))
	for _, id := range ids {
		item, err := oneItem(ctx, tx, m, id)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		out = append(out, item)
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"created": len(out), "items": out})
}

// ---------- saving rates ----------

type ratesIn struct {
	Prices []struct {
		ItemID uuid.UUID `json:"item_id"`
		Price  float64   `json:"price"`
	} `json:"prices"`
	SendWhatsApp bool `json:"send_whatsapp"`
}

// PUT /v1/prices (owner, cashier) — "Save today's rates".
func (h Handler) SaveRates(w http.ResponseWriter, r *http.Request) {
	var in ratesIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(in.Prices) == 0 || len(in.Prices) > 1000 {
		httpx.Error(w, http.StatusBadRequest, "send a list of {item_id, price} in prices")
		return
	}
	for _, p := range in.Prices {
		if p.Price <= 0 {
			httpx.Error(w, http.StatusBadRequest, "every price must be more than 0")
			return
		}
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	day := today(m)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	for _, p := range in.Prices {
		// The join on items makes sure the item belongs to this shop and is active.
		tag, err := tx.Exec(ctx, `
			insert into item_prices (item_id, day, price, updated_by)
			select i.id, $2::date, $3, $4 from items i where i.id = $1 and i.shop_id = $5 and i.active
			on conflict (item_id, day) do update
			  set price = excluded.price, updated_by = excluded.updated_by, updated_at = now()`,
			p.ItemID, day, p.Price, auth.UserID(ctx), m.ShopID)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		if tag.RowsAffected() == 0 {
			httpx.Error(w, http.StatusBadRequest, "unknown item "+p.ItemID.String())
			return
		}
	}
	body, err := pricesBody(ctx, tx, m)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if in.SendWhatsApp {
		rates := make([]map[string]any, 0, len(body.Items))
		for _, it := range body.Items {
			if it.Price != nil {
				rates = append(rates, map[string]any{"name": it.Name, "unit": it.Unit, "price": *it.Price})
			}
		}
		if err := outbox.Enqueue(ctx, tx, m.ShopID, outbox.KindRatesBroadcast, map[string]any{
			"date": day, "rates": rates, "opted_in_customers": body.OptedIn,
		}); err != nil {
			httpx.Fail(w, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

// POST /v1/prices/keep-yesterday (owner, cashier)
func (h Handler) KeepYesterday(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	day := today(m)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		insert into item_prices (item_id, day, price, updated_by)
		select i.id, $2::date,
		       (select p.price from item_prices p where p.item_id = i.id and p.day < $2::date order by p.day desc limit 1), $3
		from items i
		where i.shop_id = $1 and i.active
		  and not exists (select 1 from item_prices p where p.item_id = i.id and p.day = $2::date)
		  and exists (select 1 from item_prices p where p.item_id = i.id and p.day < $2::date)`,
		m.ShopID, day, auth.UserID(ctx)); err != nil {
		httpx.Fail(w, err)
		return
	}
	body, err := pricesBody(ctx, tx, m)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

// ---------- editing items ----------

type patchItemIn struct {
	Name       *string    `json:"name"`
	Unit       *string    `json:"unit"`
	Active     *bool      `json:"active"`
	Category   *string    `json:"category"`    // key (chicken, fish, ..., other) or a category name
	CategoryID *uuid.UUID `json:"category_id"` // or the id from GET /v1/categories
}

// PATCH /v1/prices/items/{id} (owner, cashier)
func (h Handler) PatchItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid item id")
		return
	}
	var in patchItemIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Name == nil && in.Unit == nil && in.Active == nil && in.Category == nil && in.CategoryID == nil {
		httpx.Error(w, http.StatusBadRequest, "send name, unit, category or active")
		return
	}
	if in.Category != nil {
		c := strings.TrimSpace(*in.Category)
		if c == "" || utf8.RuneCountInString(c) > 60 {
			httpx.Error(w, http.StatusBadRequest, "category must be 1 to 60 characters")
			return
		}
		in.Category = &c
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || names.NormalizeName(n) == "" || utf8.RuneCountInString(n) > maxNameLen {
			httpx.Error(w, http.StatusBadRequest, "name must be 1 to 80 characters")
			return
		}
		in.Name = &n
	}
	if in.Unit != nil {
		u := strings.ToLower(strings.TrimSpace(*in.Unit))
		if !allowedUnits[u] {
			httpx.Error(w, http.StatusBadRequest, "unit must be kg, tray, piece or bird")
			return
		}
		in.Unit = &u
	}

	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)

	var curName, curUnit string
	var curActive bool
	var catID uuid.UUID
	err = tx.QueryRow(ctx, `select name, unit, active, category_id from items where id = $1 and shop_id = $2 for update`,
		id, m.ShopID).Scan(&curName, &curUnit, &curActive, &catID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "item not found")
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	newName, newUnit, newActive := curName, curUnit, curActive
	newCat, newCatKey, catChanged := catID, "", false
	if in.Category != nil || in.CategoryID != nil {
		var cat catalog.Category
		var cerr error
		switch {
		case in.CategoryID != nil:
			cat, cerr = catalog.Resolve(ctx, tx, m.ShopID, in.CategoryID, "")
		case slices.Contains([]string{"chicken", "fish", "eggs", "mutton", "dairy", "other"}, strings.ToLower(*in.Category)):
			cat, cerr = catalog.ByKey(ctx, tx, m.ShopID, strings.ToLower(*in.Category))
		default:
			cat, cerr = catalog.Resolve(ctx, tx, m.ShopID, nil, *in.Category) // a custom category name
		}
		if errors.Is(cerr, catalog.ErrUnknownCategory) {
			httpx.Error(w, http.StatusBadRequest, "unknown category")
			return
		}
		if cerr != nil {
			httpx.Fail(w, cerr)
			return
		}
		newCat, newCatKey, catChanged = cat.ID, catalog.ItemCategoryValue(cat), cat.ID != catID
		if cat.Key != nil && catChanged {
			// Moving an item into a category switches that category on.
			if err := catalog.AddToShop(ctx, tx, m.ShopID, *cat.Key); err != nil {
				httpx.Fail(w, err)
				return
			}
		}
	}
	if in.Name != nil {
		newName = *in.Name
	}
	if in.Unit != nil {
		newUnit = *in.Unit
	}
	if in.Active != nil {
		newActive = *in.Active
	}
	norm := names.NormalizeName(newName)
	if newActive && (newName != curName || !curActive || catChanged) {
		var dup bool
		if err := tx.QueryRow(ctx, `
			select exists(select 1 from items where shop_id = $1 and category_id = $2 and name_norm = $3 and active and id <> $4)`,
			m.ShopID, newCat, norm, id).Scan(&dup); err != nil {
			httpx.Fail(w, err)
			return
		}
		if dup {
			httpx.Error(w, http.StatusConflict, "item already exists: "+newName)
			return
		}
	}
	if _, err := tx.Exec(ctx, `
		update items set name = $2, name_norm = $3, unit = $4, active = $5,
		  category_id = $6::uuid, category = case when $7::text = '' then category else $7::text end
		where id = $1`,
		id, newName, norm, newUnit, newActive, newCat, newCatKey); err != nil {
		if isUniqueViolation(err) {
			httpx.Error(w, http.StatusConflict, "item already exists: "+newName)
			return
		}
		httpx.Fail(w, err)
		return
	}
	item, err := oneItem(ctx, tx, m, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, item)
}

// DELETE /v1/prices/items/{id} (owner) — soft delete: the item is hidden, its price history stays.
func (h Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid item id")
		return
	}
	tag, err := h.DB.Exec(r.Context(), `update items set active = false where id = $1 and shop_id = $2`,
		id, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "item not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
