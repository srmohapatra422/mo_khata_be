package billing

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mohisab-api/internal/access"
	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/shoptime"
)

const maxRangeDays = 366

type billCustomer struct {
	ID    uuid.UUID `json:"id"`
	Name  *string   `json:"name"`
	Phone string    `json:"phone"`
}

type billRow struct {
	ID             uuid.UUID     `json:"id"`
	CreatedAt      time.Time     `json:"created_at"`
	Customer       *billCustomer `json:"customer"`
	PaymentMode    string        `json:"payment_mode"`
	Total          float64       `json:"total"`
	PointsDiscount float64       `json:"points_discount"`
	Payable        float64       `json:"payable"`
	PointsUsed     int           `json:"points_used"`
	PointsEarned   int           `json:"points_earned"`
	ItemsCount     int           `json:"items_count"`
}

type lineOut struct {
	ItemID      *uuid.UUID `json:"item_id"`
	Name        string     `json:"name"`
	Qty         float64    `json:"qty"`
	Unit        *string    `json:"unit"`
	Rate        float64    `json:"rate"`
	Total       float64    `json:"total"`
	PriceEdited bool       `json:"price_edited"`
	Note        *string    `json:"note"`
}

type billDetail struct {
	billRow
	LocationID uuid.UUID `json:"location_id"`
	Lines      []lineOut `json:"lines"`
}

const billCols = `b.id, b.created_at, b.payment_mode, b.total::float8, b.points_discount::float8, b.payable::float8,
	b.points_used, b.points_earned, (select count(*) from bill_lines l where l.bill_id = b.id)::int,
	b.location_id, c.id, c.name, c.phone`

func scanBill(row interface{ Scan(...any) error }, loc *time.Location) (billRow, uuid.UUID, error) {
	var (
		b      billRow
		locID  uuid.UUID
		cid    *uuid.UUID
		cname  *string
		cphone *string
	)
	err := row.Scan(&b.ID, &b.CreatedAt, &b.PaymentMode, &b.Total, &b.PointsDiscount, &b.Payable,
		&b.PointsUsed, &b.PointsEarned, &b.ItemsCount, &locID, &cid, &cname, &cphone)
	if err != nil {
		return b, locID, err
	}
	b.CreatedAt = b.CreatedAt.In(loc)
	if cid != nil && cphone != nil {
		b.Customer = &billCustomer{ID: *cid, Name: cname, Phone: *cphone}
	}
	return b, locID, nil
}

// cursor is "created_at|id", url-safe base64, so the app treats it as opaque.
func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(s string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	ts, ids, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	id, err := uuid.Parse(ids)
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	return t, id, nil
}

// DateRange turns ?from=&to= (YYYY-MM-DD, shop timezone, "to" inclusive) into [start, end).
// Either end may be left out. The range may not be longer than 366 days.
func DateRange(from, to string, loc *time.Location) (start, end *time.Time, err error) {
	parse := func(v, name string) (*time.Time, error) {
		if v == "" {
			return nil, nil
		}
		t, err := time.ParseInLocation(shoptime.DateLayout, v, loc)
		if err != nil {
			return nil, httpx.BadRequest(name + " must be YYYY-MM-DD")
		}
		return &t, nil
	}
	if start, err = parse(from, "from"); err != nil {
		return nil, nil, err
	}
	t, err := parse(to, "to")
	if err != nil {
		return nil, nil, err
	}
	if t != nil {
		e := t.AddDate(0, 0, 1)
		end = &e
	}
	if start != nil && end != nil {
		if !end.After(*start) {
			return nil, nil, httpx.BadRequest("to cannot be before from")
		}
		if end.Sub(*start) > maxRangeDays*24*time.Hour+time.Hour { // +1h: a DST shift must not cut a 366-day range
			return nil, nil, httpx.BadRequest("the range can be at most 366 days")
		}
	}
	return start, end, nil
}

// GET /v1/bills?customer_id=&from=&to=&limit=50&before=<cursor>
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	loc := m.Location()
	q := r.URL.Query()

	var cust *uuid.UUID
	if v := q.Get("customer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid customer_id")
			return
		}
		cust = &id
	}
	start, end, err := DateRange(q.Get("from"), q.Get("to"), loc)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n := 0
		for _, ch := range v {
			if ch < '0' || ch > '9' || n > 1000 {
				n = -1
				break
			}
			n = n*10 + int(ch-'0')
		}
		if n < 1 || n > 100 {
			httpx.Error(w, http.StatusBadRequest, "limit must be 1 to 100")
			return
		}
		limit = n
	}
	var curT *time.Time
	var curID *uuid.UUID
	if v := q.Get("before"); v != "" {
		t, id, err := decodeCursor(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid before cursor")
			return
		}
		curT, curID = &t, &id
	}
	// A farm manager only sees bills of the farm location.
	locs, err := access.Locations(ctx, h.DB, m, nil)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	rows, err := h.DB.Query(ctx, `
		select `+billCols+`
		from bills b left join customers c on c.id = b.customer_id
		where b.shop_id = $1
		  and ($2::uuid is null or b.customer_id = $2::uuid)
		  and ($3::timestamptz is null or b.created_at >= $3::timestamptz)
		  and ($4::timestamptz is null or b.created_at < $4::timestamptz)
		  and ($5::text[] is null or b.location_id = any($5::uuid[]))
		  and ($6::timestamptz is null or (b.created_at, b.id) < ($6::timestamptz, $7::uuid))
		order by b.created_at desc, b.id desc
		limit $8`, m.ShopID, cust, start, end, locs, curT, curID, limit+1)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer rows.Close()
	out := []billRow{}
	for rows.Next() {
		b, _, err := scanBill(rows, loc)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		if len(out) == limit {
			last := out[len(out)-1]
			w.Header().Set("X-Next-Cursor", encodeCursor(last.CreatedAt, last.ID))
			break
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// GET /v1/bills/{id}
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	loc := m.Location()
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "bill not found")
		return
	}
	locs, err := access.Locations(ctx, h.DB, m, nil)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	b, locID, err := scanBill(h.DB.QueryRow(ctx, `
		select `+billCols+`
		from bills b left join customers c on c.id = b.customer_id
		where b.id = $1 and b.shop_id = $2 and ($3::text[] is null or b.location_id = any($3::uuid[]))`,
		id, m.ShopID, locs), loc)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "bill not found")
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	rows, err := h.DB.Query(ctx, `
		select item_id, name, qty::float8, unit, rate::float8, amount::float8, price_edited, note
		from bill_lines where bill_id = $1 order by id`, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer rows.Close()
	lines := []lineOut{}
	for rows.Next() {
		var l lineOut
		if err := rows.Scan(&l.ItemID, &l.Name, &l.Qty, &l.Unit, &l.Rate, &l.Total, &l.PriceEdited, &l.Note); err != nil {
			httpx.Fail(w, err)
			return
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, billDetail{billRow: b, LocationID: locID, Lines: lines})
}
