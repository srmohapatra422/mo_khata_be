package customers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/shoptime"
)

type pointsRowOut struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"` // earned | redeemed | expired | adjusted
	Points      int       `json:"points"`
	CreatedAt   time.Time `json:"created_at"`
	BillID      *string   `json:"bill_id"`
	BillPayable *float64  `json:"bill_payable"`
	Note        *string   `json:"note"`
}

// kindOut maps the ledger kind to the name the app shows.
func kindOut(k string) string {
	switch k {
	case "earn":
		return "earned"
	case "redeem":
		return "redeemed"
	case "expire":
		return "expired"
	}
	return "adjusted" // reverse, bonus
}

// GET /v1/customers/{id}/points?limit=50&before=<cursor>
// Newest first. before is the X-Next-Cursor of the previous page.
func (h Handler) Points(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, ok := atoi(v)
		if !ok || n < 1 || n > 100 {
			httpx.Error(w, http.StatusBadRequest, "limit must be 1 to 100")
			return
		}
		limit = n
	}
	var before *int64
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			httpx.Error(w, http.StatusBadRequest, "invalid before cursor")
			return
		}
		before = &n
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	loc := m.Location()

	var one int
	if err := h.DB.QueryRow(ctx, `select 1 from customers where id = $1 and shop_id = $2`, id, m.ShopID).Scan(&one); err == pgx.ErrNoRows {
		httpx.Error(w, http.StatusNotFound, "customer not found")
		return
	} else if err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := h.sweep(ctx, m.ShopID, &id, time.Now().In(loc).Format(shoptime.DateLayout)); err != nil {
		httpx.Fail(w, err)
		return
	}

	// One extra row tells us whether there is a next page. The ledger id grows with time.
	rows, err := h.DB.Query(ctx, `
		select p.id, p.kind, p.points, p.created_at, p.bill_id, b.payable::float8, p.note
		from points_ledger p left join bills b on b.id = p.bill_id
		where p.shop_id = $1 and p.customer_id = $2 and ($3::bigint is null or p.id < $3::bigint)
		order by p.id desc limit $4`, m.ShopID, id, before, limit+1)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer rows.Close()
	out := []pointsRowOut{}
	var lastID int64
	for rows.Next() {
		var (
			pid  int64
			kind string
			o    pointsRowOut
		)
		var bid *string
		if err := rows.Scan(&pid, &kind, &o.Points, &o.CreatedAt, &bid, &o.BillPayable, &o.Note); err != nil {
			httpx.Fail(w, err)
			return
		}
		if len(out) == limit {
			w.Header().Set("X-Next-Cursor", strconv.FormatInt(lastID, 10))
			break
		}
		o.ID, o.Kind, o.BillID = strconv.FormatInt(pid, 10), kindOut(kind), bid
		o.CreatedAt = o.CreatedAt.In(loc)
		out = append(out, o)
		lastID = pid
	}
	if err := rows.Err(); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
