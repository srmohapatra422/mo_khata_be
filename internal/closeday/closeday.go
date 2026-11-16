package closeday

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/access"
	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/outbox"
	"mohisab-api/internal/shoptime"
)

type Handler struct{ DB *pgxpool.Pool }

const openingFloat = 0 // the shop does not record an opening float yet

// summary is everything money-related that happened at one location on one day.
type summary struct {
	Bills               int
	Sales               float64 // all bills, every payment mode
	CashBills           float64
	UPI                 float64
	CreditGiven         float64 // bills paid by credit
	CreditCollected     float64 // credit payments, every method
	CreditCollectedCash float64
	Expenses            float64
}

func loadSummary(ctx context.Context, q db.Querier, shopID, locID uuid.UUID, start, end time.Time) (summary, error) {
	var s summary
	err := q.QueryRow(ctx, `
		select b.cnt, b.sales, b.cash, b.upi, b.credit, c.total, c.cash, e.total
		from (select count(*)::int cnt,
		             coalesce(sum(payable), 0)::float8 sales,
		             coalesce(sum(payable) filter (where payment_mode = 'cash'), 0)::float8 cash,
		             coalesce(sum(payable) filter (where payment_mode = 'upi'), 0)::float8 upi,
		             coalesce(sum(payable) filter (where payment_mode = 'credit'), 0)::float8 credit
		      from bills where shop_id = $1 and location_id = $2 and created_at >= $3 and created_at < $4) b,
		     (select coalesce(sum(amount), 0)::float8 total,
		             coalesce(sum(amount) filter (where method = 'cash'), 0)::float8 cash
		      from credit_payments where shop_id = $1 and location_id = $2 and created_at >= $3 and created_at < $4) c,
		     (select coalesce(sum(amount), 0)::float8 total
		      from expenses where shop_id = $1 and location_id = $2 and created_at >= $3 and created_at < $4) e`,
		shopID, locID, start, end,
	).Scan(&s.Bills, &s.Sales, &s.CashBills, &s.UPI, &s.CreditGiven, &s.CreditCollected, &s.CreditCollectedCash, &s.Expenses)
	return s, err
}

func (s summary) expectedCash() float64 {
	return ExpectedCash(s.CashBills, s.CreditCollectedCash, openingFloat, s.Expenses)
}

// ---------- preview ----------

type stockRow struct {
	ItemID   uuid.UUID `json:"item_id"`
	Name     string    `json:"name"`
	Unit     string    `json:"unit"`
	Expected float64   `json:"expected"`
}

// GET /v1/close-day/preview?location_id=&date= (owner, cashier)
func (h Handler) Preview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	locID, err := requiredLocation(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	start, end, day, err := shoptime.DayRange(r.URL.Query().Get("date"), time.Now().In(m.Location()))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if _, err := access.Locations(ctx, h.DB, m, &locID); err != nil {
		httpx.Fail(w, err)
		return
	}
	s, err := loadSummary(ctx, h.DB, m.ShopID, locID, start, end)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var closed bool
	if err := h.DB.QueryRow(ctx, `select exists(select 1 from day_closings where shop_id = $1 and location_id = $2 and business_date = $3::date)`,
		m.ShopID, locID, day).Scan(&closed); err != nil {
		httpx.Fail(w, err)
		return
	}
	rows, err := h.DB.Query(ctx, `
		select s.id, s.name, s.unit, s.quantity::float8
		from stock_items s
		where s.shop_id = $1 and s.location_id = $2
		  and (s.quantity > 0 or exists (select 1 from stock_movements mv
		        where mv.stock_item_id = s.id and mv.kind = 'sale' and mv.created_at >= $3 and mv.created_at < $4))
		order by exists (select 1 from stock_movements mv
		        where mv.stock_item_id = s.id and mv.kind = 'sale' and mv.created_at >= $3 and mv.created_at < $4) desc,
		         s.quantity desc, s.name
		limit 20`, m.ShopID, locID, start, end)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer rows.Close()
	stock := []stockRow{}
	for rows.Next() {
		var sr stockRow
		if err := rows.Scan(&sr.ItemID, &sr.Name, &sr.Unit, &sr.Expected); err != nil {
			httpx.Fail(w, err)
			return
		}
		stock = append(stock, sr)
	}
	if err := rows.Err(); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"date": day, "location_id": locID, "bills": s.Bills, "already_closed": closed,
		"cash": map[string]any{"expected": s.expectedCash()},
		"other_money": map[string]any{
			"upi_received":     round2(s.UPI),
			"credit_given":     round2(s.CreditGiven),
			"credit_collected": round2(s.CreditCollected),
			"expenses":         round2(s.Expenses),
		},
		"stock": stock,
	})
}

func requiredLocation(r *http.Request) (uuid.UUID, error) {
	id, err := access.LocationParam(r)
	if err != nil {
		return uuid.Nil, err
	}
	if id == nil {
		return uuid.Nil, httpx.BadRequest("location_id is required")
	}
	return *id, nil
}

// ---------- close ----------

type stockIn struct {
	ItemID uuid.UUID `json:"item_id"`
	Actual float64   `json:"actual"`
}

type closeIn struct {
	LocationID  uuid.UUID `json:"location_id"`
	Date        string    `json:"date"`
	CountedCash *float64  `json:"counted_cash"`
	Reason      string    `json:"reason"`
	Stock       []stockIn `json:"stock"`
}

const maxStockRows = 100

// validate cleans the input and returns a message for the first problem, or "".
func (in *closeIn) validate() string {
	in.Reason = strings.TrimSpace(in.Reason)
	switch {
	case in.LocationID == uuid.Nil:
		return "location_id is required"
	case in.CountedCash == nil:
		return "counted_cash is required"
	case *in.CountedCash < 0 || *in.CountedCash > 1_000_000_000:
		return "counted_cash must be 0 or more"
	case utf8.RuneCountInString(in.Reason) > 200:
		return "reason is too long (200 characters at most)"
	case len(in.Stock) > maxStockRows:
		return fmt.Sprintf("send at most %d stock rows", maxStockRows)
	}
	seen := map[uuid.UUID]bool{}
	for i, s := range in.Stock {
		if s.ItemID == uuid.Nil {
			return fmt.Sprintf("stock %d: item_id is required", i+1)
		}
		if !(s.Actual >= 0) || s.Actual > 10_000_000 {
			return fmt.Sprintf("stock %d: actual must be 0 or more", i+1)
		}
		if seen[s.ItemID] {
			return fmt.Sprintf("stock %d: item_id is repeated", i+1)
		}
		seen[s.ItemID] = true
	}
	return ""
}

type wastageOut struct {
	Name     string  `json:"name"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
}

// POST /v1/close-day (owner, cashier)
func (h Handler) Close(w http.ResponseWriter, r *http.Request) {
	var in closeIn
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
	start, end, day, err := shoptime.DayRange(in.Date, time.Now().In(m.Location()))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
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

	var already bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from day_closings where shop_id = $1 and location_id = $2 and business_date = $3::date)`,
		m.ShopID, in.LocationID, day).Scan(&already); err != nil {
		httpx.Fail(w, err)
		return
	}
	if already {
		httpx.Error(w, http.StatusConflict, "this day is already closed")
		return
	}

	s, err := loadSummary(ctx, tx, m.ShopID, in.LocationID, start, end)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	expected := s.expectedCash()
	diff := Difference(*in.CountedCash, expected)
	status := Status(diff)

	// Lock and read the stock rows being counted.
	type counted struct {
		id               uuid.UUID
		name, unit       string
		expected, actual float64
	}
	var rowsToCount []counted
	if len(in.Stock) > 0 {
		ids := make([]string, len(in.Stock))
		actual := map[uuid.UUID]float64{}
		for i, sIn := range in.Stock {
			ids[i] = sIn.ItemID.String()
			actual[sIn.ItemID] = sIn.Actual
		}
		rows, err := tx.Query(ctx, `
			select id, name, unit, quantity::float8 from stock_items
			where id = any($1::uuid[]) and shop_id = $2 and location_id = $3
			order by name for update`, ids, m.ShopID, in.LocationID)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		for rows.Next() {
			var c counted
			if err := rows.Scan(&c.id, &c.name, &c.unit, &c.expected); err != nil {
				rows.Close()
				httpx.Fail(w, err)
				return
			}
			c.actual = actual[c.id]
			rowsToCount = append(rowsToCount, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			httpx.Fail(w, err)
			return
		}
		if len(rowsToCount) != len(in.Stock) {
			httpx.Error(w, http.StatusBadRequest, "unknown stock item for this location")
			return
		}
	}

	// Insert the closing first: the unique key turns a double close (even a racing one) into a 409.
	var reason *string
	if in.Reason != "" {
		reason = &in.Reason
	}
	var closingID uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into day_closings (shop_id, location_id, business_date, expected_cash, counted_cash, difference, reason, closed_by)
		values ($1, $2, $3::date, $4, $5, $6, $7, $8) returning id`,
		m.ShopID, in.LocationID, day, expected, *in.CountedCash, diff, reason, userID).Scan(&closingID)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		httpx.Error(w, http.StatusConflict, "this day is already closed")
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	wastage := []wastageOut{}
	for _, c := range rowsToCount {
		waste, adjust := Split(c.unit, c.expected, c.actual)
		if _, err := tx.Exec(ctx, `update stock_items set quantity = $2, updated_at = now() where id = $1`, c.id, c.actual); err != nil {
			httpx.Fail(w, err)
			return
		}
		if waste > 0 {
			if err := movement(ctx, tx, m.ShopID, in.LocationID, c.id, "wastage", -waste, closingID, userID); err != nil {
				httpx.Fail(w, err)
				return
			}
			wastage = append(wastage, wastageOut{Name: c.name, Quantity: waste, Unit: c.unit})
		}
		if adjust != 0 {
			if err := movement(ctx, tx, m.ShopID, in.LocationID, c.id, "adjustment", adjust, closingID, userID); err != nil {
				httpx.Fail(w, err)
				return
			}
		}
		if _, err := tx.Exec(ctx, `
			insert into day_closing_stock (closing_id, stock_item_id, expected, actual, wastage) values ($1, $2, $3, $4, $5)`,
			closingID, c.id, c.expected, c.actual, waste); err != nil {
			httpx.Fail(w, err)
			return
		}
	}

	sum := map[string]any{
		"sales": round2(s.Sales), "bills": s.Bills, "cash_bills": round2(s.CashBills), "upi_received": round2(s.UPI),
		"credit_given": round2(s.CreditGiven), "credit_collected": round2(s.CreditCollected),
		"expenses": round2(s.Expenses), "expected_cash": expected, "counted_cash": *in.CountedCash,
		"difference": diff, "status": status, "wastage": wastage,
	}
	if _, err := tx.Exec(ctx, `update day_closings set summary = $2::jsonb where id = $1`, closingID, jsonString(sum)); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := outbox.Enqueue(ctx, tx, m.ShopID, outbox.KindDaySummary, map[string]any{
		"date": day, "location_id": in.LocationID, "closing_id": closingID,
		"sales": round2(s.Sales), "bills": s.Bills, "difference": diff, "status": status, "wastage": wastage,
	}); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": closingID, "difference": diff, "status": status, "wastage": wastage})
}

func movement(ctx context.Context, tx pgx.Tx, shopID, locID, itemID uuid.UUID, kind string, qty float64, closingID, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		insert into stock_movements (shop_id, location_id, stock_item_id, kind, quantity, source, ref_id, created_by)
		values ($1, $2, $3, $4, $5, 'close_day', $6, $7)`, shopID, locID, itemID, kind, qty, closingID, userID)
	return err
}
