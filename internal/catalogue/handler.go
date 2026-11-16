package catalogue

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/catalog"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/names"
)

type Handler struct{ DB *pgxpool.Pool }

type itemOut struct {
	ID       uuid.UUID `json:"id"`
	Category string    `json:"category"`
	Name     string    `json:"name"`
	Unit     string    `json:"unit"`
	Price    *float64  `json:"price_today"` // null if not set today
}

// GET /v1/prices/today — active items with today's price (falls back to latest price).
func (h Handler) Today(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	rows, err := h.DB.Query(r.Context(), `
		select i.id, i.category, i.name, i.unit,
		       (select p.price::float8 from item_prices p
		         where p.item_id = i.id and p.day <= current_date
		         order by p.day desc limit 1)
		from items i
		where i.shop_id = $1 and i.active
		order by i.category, i.name`, m.ShopID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()
	out := []itemOut{}
	for rows.Next() {
		var it itemOut
		if err := rows.Scan(&it.ID, &it.Category, &it.Name, &it.Unit, &it.Price); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		out = append(out, it)
	}
	httpx.JSON(w, http.StatusOK, out)
}

type createItemIn struct {
	Category string   `json:"category"`
	Name     string   `json:"name"`
	Unit     string   `json:"unit"`
	Price    *float64 `json:"price"` // optional: sets today's price too
}

// POST /v1/items — "add item with price one by one" from Profile › Today's prices.
func (h Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
	var in createItemIn
	if err := httpx.Decode(r, &in); err != nil || in.Name == "" || in.Unit == "" || in.Category == "" {
		httpx.Error(w, http.StatusBadRequest, "category, name and unit are required")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)

	// Link the item into the category catalog so the newer /v1/prices endpoints see it too.
	var catID *uuid.UUID
	if c, err := catalog.ByKey(ctx, tx, m.ShopID, in.Category); err == nil {
		catID = &c.ID
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into items (shop_id, category, category_id, name, name_norm, unit) values ($1, $2, $3, $4, $5, $6) returning id`,
		m.ShopID, in.Category, catID, in.Name, names.NormalizeName(in.Name), in.Unit).Scan(&id); err != nil {
		httpx.Error(w, http.StatusBadRequest, "could not create item")
		return
	}
	if in.Price != nil {
		if _, err := tx.Exec(ctx, `
			insert into item_prices (item_id, day, price, updated_by) values ($1, current_date, $2, $3)`,
			id, *in.Price, auth.UserID(ctx)); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id})
}

type priceIn struct {
	ItemID uuid.UUID `json:"item_id"`
	Price  float64   `json:"price"`
}

// PUT /v1/prices/today — owner sets today's prices (body: list of item_id + price).
func (h Handler) SetToday(w http.ResponseWriter, r *http.Request) {
	var in []priceIn
	if err := httpx.Decode(r, &in); err != nil || len(in) == 0 {
		httpx.Error(w, http.StatusBadRequest, "list of {item_id, price} required")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)
	for _, p := range in {
		// The join on items makes sure the item belongs to this shop.
		tag, err := tx.Exec(ctx, `
			insert into item_prices (item_id, day, price, updated_by)
			select i.id, current_date, $2, $3 from items i where i.id = $1 and i.shop_id = $4
			on conflict (item_id, day) do update
			  set price = excluded.price, updated_by = excluded.updated_by, updated_at = now()`,
			p.ItemID, p.Price, auth.UserID(ctx), m.ShopID)
		if err != nil || tag.RowsAffected() == 0 {
			httpx.Error(w, http.StatusBadRequest, "unknown item "+p.ItemID.String())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"updated": len(in)})
}
