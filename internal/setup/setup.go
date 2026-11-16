// Package setup is the new-shop checklist: shop, prices, stock, first bill.
package setup

import (
	"context"
	"math"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
)

// Returned lists the steps the app shows, in order. cashier and whatsapp are in the database
// enum for later, but are not returned yet.
var Returned = []string{"shop", "prices", "stock", "bill"}

var knownSteps = map[string]bool{
	"shop": true, "prices": true, "stock": true, "bill": true, "cashier": true, "whatsapp": true,
}

const minutesPerStep = 2

// Derived is what the shop's data already proves.
type Derived struct {
	Prices bool // at least one price saved
	Stock  bool // at least one stock-in
	Bill   bool // at least one bill
}

type Step struct {
	ID   string `json:"id"`
	Done bool   `json:"done"`
}

type Response struct {
	Steps       []Step `json:"steps"`
	DoneCount   int    `json:"done_count"`
	Total       int    `json:"total"`
	Percent     int    `json:"percent"`
	MinutesLeft int    `json:"minutes_left"`
	Complete    bool   `json:"complete"`
}

// Build combines what the data proves with what the owner ticked. A step is done when either
// says so, so un-ticking cannot undo a step the data proves. The shop step is always done.
func Build(manual map[string]bool, d Derived) Response {
	derived := map[string]bool{"shop": true, "prices": d.Prices, "stock": d.Stock, "bill": d.Bill}
	r := Response{Steps: make([]Step, 0, len(Returned)), Total: len(Returned)}
	for _, id := range Returned {
		done := derived[id] || manual[id]
		if done {
			r.DoneCount++
		}
		r.Steps = append(r.Steps, Step{ID: id, Done: done})
	}
	r.Percent = int(math.Round(float64(r.DoneCount) / float64(r.Total) * 100))
	r.MinutesLeft = (r.Total - r.DoneCount) * minutesPerStep
	r.Complete = r.DoneCount == r.Total
	return r
}

type Handler struct{ DB *pgxpool.Pool }

func load(ctx context.Context, q db.Querier, shopID uuid.UUID) (Response, error) {
	var d Derived
	var ticked []string
	err := q.QueryRow(ctx, `
		select exists(select 1 from item_prices p join items i on i.id = p.item_id where i.shop_id = $1),
		       exists(select 1 from stock_movements where shop_id = $1 and kind = 'in'),
		       exists(select 1 from bills where shop_id = $1),
		       coalesce((select array_agg(step) from setup_steps where shop_id = $1 and done), '{}')`,
		shopID).Scan(&d.Prices, &d.Stock, &d.Bill, &ticked)
	if err != nil {
		return Response{}, err
	}
	manual := map[string]bool{}
	for _, s := range ticked {
		manual[s] = true
	}
	return Build(manual, d), nil
}

// GET /v1/setup
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	resp, err := load(r.Context(), h.DB, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// PATCH /v1/setup/steps/{id} (owner) — tick or un-tick a step by hand. Returns the same body as GET.
func (h Handler) Patch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !knownSteps[id] {
		httpx.Error(w, http.StatusBadRequest, "unknown step")
		return
	}
	if id == "shop" {
		httpx.Error(w, http.StatusBadRequest, "the shop step cannot be changed")
		return
	}
	var in struct {
		Done *bool `json:"done"`
	}
	if err := httpx.Decode(r, &in); err != nil || in.Done == nil {
		httpx.Error(w, http.StatusBadRequest, `send {"done": true} or {"done": false}`)
		return
	}
	ctx := r.Context()
	shopID := auth.MemberOf(ctx).ShopID
	if _, err := h.DB.Exec(ctx, `
		insert into setup_steps (shop_id, step, done) values ($1, $2, $3)
		on conflict (shop_id, step) do update set done = excluded.done, updated_at = now()`,
		shopID, id, *in.Done); err != nil {
		httpx.Fail(w, err)
		return
	}
	resp, err := load(ctx, h.DB, shopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, resp)
}
