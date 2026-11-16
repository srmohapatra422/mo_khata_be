// Package stock keeps the quantity of every item per location and a log of every change.
package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/access"
	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/names"
)

type Handler struct{ DB *pgxpool.Pool }

const (
	maxInItems  = 100
	maxQuantity = 10_000_000
)

var unitAliases = map[string]string{
	"kg": "kg", "kgs": "kg", "kilo": "kg", "kilos": "kg", "kilogram": "kg", "kilograms": "kg",
	"bird": "birds", "birds": "birds",
	"tray": "trays", "trays": "trays",
	"piece": "pieces", "pieces": "pieces", "pc": "pieces", "pcs": "pieces",
}

// NormalizeUnit maps what the app sends (kg, bird, trays, pcs...) to the stored unit.
func NormalizeUnit(s string) (string, bool) {
	u, ok := unitAliases[strings.ToLower(strings.TrimSpace(s))]
	return u, ok
}

// UnitFromItemUnit maps a price-item unit (kg, bird, tray, piece, dozen) to a stock unit.
func UnitFromItemUnit(s string) string {
	if u, ok := NormalizeUnit(s); ok {
		return u
	}
	return ""
}

type itemOut struct {
	ID             uuid.UUID  `json:"id"`
	LocationID     uuid.UUID  `json:"location_id"`
	Name           string     `json:"name"`
	Quantity       float64    `json:"quantity"`
	Unit           string     `json:"unit"`
	ReorderLevel   *float64   `json:"reorder_level"`
	IsLow          bool       `json:"is_low"`
	LastInAt       *time.Time `json:"last_in_at"`
	LastInQuantity *float64   `json:"last_in_quantity"`
}

type row struct {
	itemOut
	norm string
}

// load reads stock items. locs and ids are optional filters (nil = no filter).
func load(ctx context.Context, q db.Querier, shopID uuid.UUID, locs, ids []string) ([]row, error) {
	rows, err := q.Query(ctx, `
		select s.id, s.location_id, s.name, s.name_norm, s.quantity::float8, s.unit, s.reorder_level::float8,
		       (s.reorder_level is not null and s.quantity <= s.reorder_level),
		       li.created_at, li.quantity::float8
		from stock_items s
		left join lateral (
		    select m.created_at, m.quantity from stock_movements m
		    where m.stock_item_id = s.id and m.kind = 'in' order by m.id desc limit 1) li on true
		where s.shop_id = $1
		  and ($2::uuid[] is null or s.location_id = any($2::uuid[]))
		  and ($3::uuid[] is null or s.id = any($3::uuid[]))
		order by s.name_norm, s.unit
		limit 1000`, shopID, locs, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.LocationID, &r.Name, &r.norm, &r.Quantity, &r.Unit, &r.ReorderLevel,
			&r.IsLow, &r.LastInAt, &r.LastInQuantity); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func items(rs []row) []itemOut {
	out := make([]itemOut, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.itemOut)
	}
	return out
}

// GET /v1/stock?location_id=&search=
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	req, err := access.LocationParam(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	locs, err := access.Locations(r.Context(), h.DB, m, req)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	rs, err := load(r.Context(), h.DB, m.ShopID, locs, nil)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if q := names.NormalizeName(r.URL.Query().Get("search")); q != "" {
		kept := rs[:0]
		for _, row := range rs {
			if names.Matches(row.norm, q) {
				kept = append(kept, row)
			}
		}
		rs = kept
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items(rs)})
}

// ---------- stock in ----------

type inItem struct {
	Name     string   `json:"name"`
	Unit     string   `json:"unit"`
	Quantity float64  `json:"quantity"`
	Rate     *float64 `json:"rate"`
}

type inReq struct {
	LocationID uuid.UUID `json:"location_id"`
	Source     string    `json:"source"`
	Items      []inItem  `json:"items"`
}

// validate cleans the request (trims names, maps units, defaults source) and returns a message for the first problem.
func (in *inReq) validate() string {
	if in.LocationID == uuid.Nil {
		return "location_id is required"
	}
	switch in.Source {
	case "":
		in.Source = "form"
	case "form", "voice":
	default:
		return "source must be form or voice"
	}
	if len(in.Items) == 0 || len(in.Items) > maxInItems {
		return fmt.Sprintf("send between 1 and %d items", maxInItems)
	}
	for i := range in.Items {
		it := &in.Items[i]
		it.Name = strings.TrimSpace(it.Name)
		n := i + 1
		if it.Name == "" || names.NormalizeName(it.Name) == "" {
			return fmt.Sprintf("item %d: name is required", n)
		}
		if utf8.RuneCountInString(it.Name) > 80 {
			return fmt.Sprintf("item %d: name is too long", n)
		}
		u, ok := NormalizeUnit(it.Unit)
		if !ok {
			return fmt.Sprintf("item %d: unit must be kg, birds, trays or pieces", n)
		}
		it.Unit = u
		if !(it.Quantity > 0) || it.Quantity > maxQuantity {
			return fmt.Sprintf("item %d: quantity must be more than 0", n)
		}
		if it.Rate != nil && (*it.Rate < 0 || *it.Rate > maxQuantity) {
			return fmt.Sprintf("item %d: rate must be 0 or more", n)
		}
	}
	return ""
}

// POST /v1/stock/in — the "Add stock" form and the voice save. One transaction.
func (h Handler) In(w http.ResponseWriter, r *http.Request) {
	var in inReq
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
	userID := auth.UserID(ctx)
	if _, err := access.Locations(ctx, h.DB, m, &in.LocationID); err != nil {
		httpx.Fail(w, err)
		return
	}
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)

	var ids []string
	seen := map[uuid.UUID]bool{}
	for _, it := range in.Items {
		var id uuid.UUID
		// An exact match (same name, same unit) is added to and keeps its first spelling.
		if err := tx.QueryRow(ctx, `
			insert into stock_items (shop_id, location_id, name, name_norm, unit, quantity)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (shop_id, location_id, name_norm, unit)
			do update set quantity = stock_items.quantity + excluded.quantity, updated_at = now()
			returning id`,
			m.ShopID, in.LocationID, it.Name, names.NormalizeName(it.Name), it.Unit, it.Quantity).Scan(&id); err != nil {
			httpx.Fail(w, err)
			return
		}
		if _, err := tx.Exec(ctx, `
			insert into stock_movements (shop_id, location_id, stock_item_id, kind, quantity, rate, source, created_by)
			values ($1, $2, $3, 'in', $4, $5, $6, $7)`,
			m.ShopID, in.LocationID, id, it.Quantity, it.Rate, in.Source, userID); err != nil {
			httpx.Fail(w, err)
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id.String())
		}
	}
	rs, err := load(ctx, tx, m.ShopID, nil, ids)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"saved": len(in.Items), "items": items(rs)})
}

// ---------- reorder level ----------

// PATCH /v1/stock/{id} (owner) — body {"reorder_level": 5}; null clears it.
func (h Handler) PatchReorder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid stock item id")
		return
	}
	var in struct {
		ReorderLevel json.RawMessage `json:"reorder_level"`
	}
	if err := httpx.Decode(r, &in); err != nil || len(in.ReorderLevel) == 0 {
		httpx.Error(w, http.StatusBadRequest, "send reorder_level (a number, or null to clear it)")
		return
	}
	var level *float64
	if string(in.ReorderLevel) != "null" {
		var v float64
		if err := json.Unmarshal(in.ReorderLevel, &v); err != nil || v < 0 || v > maxQuantity {
			httpx.Error(w, http.StatusBadRequest, "reorder_level must be 0 or more")
			return
		}
		level = &v
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tag, err := h.DB.Exec(ctx, `update stock_items set reorder_level = $3, updated_at = now() where id = $1 and shop_id = $2`,
		id, m.ShopID, level)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "stock item not found")
		return
	}
	rs, err := load(ctx, h.DB, m.ShopID, nil, []string{id.String()})
	if err != nil || len(rs) == 0 {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rs[0].itemOut)
}

// ---------- wastage ----------

type wastageIn struct {
	Quantity float64 `json:"quantity"`
	Note     string  `json:"note"`
}

// POST /v1/stock/{id}/wastage (owner, farm manager)
func (h Handler) Wastage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid stock item id")
		return
	}
	var in wastageIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if !(in.Quantity > 0) || in.Quantity > maxQuantity {
		httpx.Error(w, http.StatusBadRequest, "quantity must be more than 0")
		return
	}
	if utf8.RuneCountInString(in.Note) > 200 {
		httpx.Error(w, http.StatusBadRequest, "note is too long")
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

	var loc uuid.UUID
	var qty float64
	err = tx.QueryRow(ctx, `select location_id, quantity::float8 from stock_items where id = $1 and shop_id = $2 for update`,
		id, m.ShopID).Scan(&loc, &qty)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "stock item not found")
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if _, err := access.Locations(ctx, tx, m, &loc); err != nil {
		httpx.Fail(w, err)
		return
	}
	if in.Quantity > qty+1e-9 {
		httpx.Error(w, http.StatusConflict, "wastage is more than the stock available")
		return
	}
	if _, err := tx.Exec(ctx, `update stock_items set quantity = greatest(quantity - $2, 0), updated_at = now() where id = $1`,
		id, in.Quantity); err != nil {
		httpx.Fail(w, err)
		return
	}
	var note *string
	if in.Note != "" {
		note = &in.Note
	}
	if _, err := tx.Exec(ctx, `
		insert into stock_movements (shop_id, location_id, stock_item_id, kind, quantity, source, note, created_by)
		values ($1, $2, $3, 'wastage', $4, 'form', $5, $6)`,
		m.ShopID, loc, id, -in.Quantity, note, auth.UserID(ctx)); err != nil {
		httpx.Fail(w, err)
		return
	}
	rs, err := load(ctx, tx, m.ShopID, nil, []string{id.String()})
	if err != nil || len(rs) == 0 {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rs[0].itemOut)
}

// ---------- used by billing ----------

// RecordSale takes a sold quantity off the matching stock item (same name, and same unit when
// the unit is known) and logs a 'sale' movement. No match means nothing happens. The quantity
// never goes below 0 (the log records what was really taken), and a sale is never refused
// because of stock. It is one statement.
func RecordSale(ctx context.Context, q db.Querier, shopID, locationID, billID, userID uuid.UUID, name, unit string, qty float64) error {
	norm := names.NormalizeName(name)
	if norm == "" || !(qty > 0) {
		return nil
	}
	_, err := q.Exec(ctx, `
		with t as (
		    select id, quantity as old_qty from stock_items
		    where shop_id = $1::uuid and location_id = $2::uuid and name_norm = $3::text
		      and ($4::text = '' or unit = $4::text)
		    order by quantity desc limit 1 for update),
		u as (
		    update stock_items s set quantity = greatest(s.quantity - $5::numeric, 0), updated_at = now()
		    from t where s.id = t.id returning s.id, t.old_qty)
		insert into stock_movements (shop_id, location_id, stock_item_id, kind, quantity, source, ref_id, created_by)
		select $1::uuid, $2::uuid, u.id, 'sale', 0 - least(u.old_qty, $5::numeric), 'bill', $6::uuid, $7::uuid
		from u where u.old_qty > 0`,
		shopID, locationID, norm, unit, math.Round(qty*1000)/1000, billID, userID)
	return err
}
