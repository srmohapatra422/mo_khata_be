// Package expenses records money the shop spent (electricity, feed, transport...).
package expenses

import (
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/access"
	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/shoptime"
)

type Handler struct{ DB *pgxpool.Pool }

const maxAmount = 1_000_000_000

type createIn struct {
	LocationID uuid.UUID `json:"location_id"`
	Amount     float64   `json:"amount"`
	Note       string    `json:"note"`
	Category   string    `json:"category"`
}

// validate cleans the input and returns a message for the first problem, or "".
func (in *createIn) validate() string {
	in.Note = strings.TrimSpace(in.Note)
	in.Category = strings.TrimSpace(in.Category)
	switch {
	case in.LocationID == uuid.Nil:
		return "location_id is required"
	case !(in.Amount > 0) || in.Amount > maxAmount:
		return "amount must be more than 0"
	case utf8.RuneCountInString(in.Note) > 200:
		return "note is too long (200 characters at most)"
	case utf8.RuneCountInString(in.Category) > 40:
		return "category is too long (40 characters at most)"
	}
	in.Amount = math.Round(in.Amount*100) / 100
	return ""
}

type expenseOut struct {
	ID         uuid.UUID `json:"id"`
	LocationID uuid.UUID `json:"location_id"`
	Amount     float64   `json:"amount"`
	Note       *string   `json:"note"`
	Category   *string   `json:"category"`
	CreatedAt  time.Time `json:"created_at"`
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// POST /v1/expenses (owner, cashier, farm manager on the farm)
func (h Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in createIn
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
	if _, err := access.Locations(ctx, h.DB, m, &in.LocationID); err != nil {
		httpx.Fail(w, err)
		return
	}
	var out expenseOut
	err := h.DB.QueryRow(ctx, `
		insert into expenses (shop_id, location_id, amount, note, category, created_by)
		values ($1, $2, $3, $4, $5, $6)
		returning id, location_id, amount::float8, note, category, created_at`,
		m.ShopID, in.LocationID, in.Amount, nilIfEmpty(in.Note), nilIfEmpty(in.Category), auth.UserID(ctx),
	).Scan(&out.ID, &out.LocationID, &out.Amount, &out.Note, &out.Category, &out.CreatedAt)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// GET /v1/expenses?date=YYYY-MM-DD&location_id= — the day's expenses (default: today).
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	start, end, day, err := shoptime.DayRange(r.URL.Query().Get("date"), time.Now().In(m.Location()))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	req, err := access.LocationParam(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	locs, err := access.Locations(ctx, h.DB, m, req)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	rows, err := h.DB.Query(ctx, `
		select id, location_id, amount::float8, note, category, created_at
		from expenses
		where shop_id = $1 and created_at >= $2 and created_at < $3
		  and ($4::uuid[] is null or location_id = any($4::uuid[]))
		order by created_at, id`, m.ShopID, start, end, locs)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer rows.Close()
	items := []expenseOut{}
	total := 0.0
	for rows.Next() {
		var e expenseOut
		if err := rows.Scan(&e.ID, &e.LocationID, &e.Amount, &e.Note, &e.Category, &e.CreatedAt); err != nil {
			httpx.Fail(w, err)
			return
		}
		total += e.Amount
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"date": day, "total": math.Round(total*100) / 100, "items": items})
}
