// Package dashboard serves GET /v1/dashboard: sales, bills, payments, points, growth,
// the sales chart, today's-rates flag and low stock, all in the shop's timezone.
package dashboard

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/access"
	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

// clockSkew lets a bill saved a moment ago count even if the phone's clock runs slightly fast.
const clockSkew = 5 * time.Minute

type Handler struct{ DB *pgxpool.Pool }

type payments struct {
	Cash   float64 `json:"cash"`
	UPI    float64 `json:"upi"`
	Credit float64 `json:"credit"`
	Card   float64 `json:"card"`
}

type points struct {
	Given    int `json:"given"`
	Redeemed int `json:"redeemed"`
}

type lowStock struct {
	ItemID       uuid.UUID `json:"item_id"`
	Name         string    `json:"name"`
	Quantity     float64   `json:"quantity"`
	Unit         string    `json:"unit"`
	ReorderLevel float64   `json:"reorder_level"`
}

type response struct {
	Period            string     `json:"period"`
	Sales             float64    `json:"sales"`
	Bills             int        `json:"bills"`
	Customers         int        `json:"customers"`
	Payments          payments   `json:"payments"`
	Points            points     `json:"points"`
	GrowthPercent     *int       `json:"growth_percent"`
	Chart             Chart      `json:"chart"`
	RatesUpdatedToday bool       `json:"rates_updated_today"`
	LowStock          []lowStock `json:"low_stock"`
}

// bucketExpr turns a bill's time into the number we group the chart by, in the shop's timezone.
var bucketExpr = map[string]string{
	Today: `extract(hour from created_at at time zone $2::text)::int`,
	Week:  `(extract(isodow from created_at at time zone $2::text)::int - 1)`,
	Month: `extract(day from created_at at time zone $2::text)::int`,
}

// parallel runs the queries at the same time and returns the first error.
func parallel(fns ...func() error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(fns))
	for i, f := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// GET /v1/dashboard?period=today|week|month&location_id=<uuid optional>
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)

	period := r.URL.Query().Get("period")
	if period == "" {
		period = Today
	}
	loc := m.Location()
	now := time.Now().In(loc)
	rng, ok := Ranges(period, now)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "period must be today, week or month")
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

	out := response{Period: period, LowStock: []lowStock{}}
	var prevSales float64
	var chartSums map[int]float64
	err = parallel(
		func() error { return h.totals(ctx, m.ShopID, locs, rng, &out, &prevSales) },
		func() error {
			var e error
			chartSums, e = h.buckets(ctx, m.ShopID, locs, rng, period, m.Location().String())
			return e
		},
		func() error { return h.lowStock(ctx, m.ShopID, locs, &out) },
		func() error { return h.ratesUpdated(ctx, m.ShopID, now.Format("2006-01-02"), &out) },
	)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out.Sales = round2(out.Sales)
	out.Payments = payments{round2(out.Payments.Cash), round2(out.Payments.UPI), round2(out.Payments.Credit), round2(out.Payments.Card)}
	out.GrowthPercent = Growth(out.Sales, round2(prevSales))
	out.Chart = BuildChart(period, daysInMonth(now), chartSums)
	httpx.JSON(w, http.StatusOK, out)
}

// totals reads the current and the previous period in one pass over the bills.
func (h Handler) totals(ctx context.Context, shopID uuid.UUID, locs []string, rng Range, out *response, prevSales *float64) error {
	return h.DB.QueryRow(ctx, `
		select
		  coalesce(sum(payable) filter (where cur), 0)::float8,
		  count(*) filter (where cur)::int,
		  count(distinct customer_id) filter (where cur)::int,
		  coalesce(sum(payable) filter (where cur and payment_mode = 'cash'), 0)::float8,
		  coalesce(sum(payable) filter (where cur and payment_mode = 'upi'), 0)::float8,
		  coalesce(sum(payable) filter (where cur and payment_mode = 'credit'), 0)::float8,
		  coalesce(sum(payable) filter (where cur and payment_mode = 'card'), 0)::float8,
		  coalesce(sum(points_earned) filter (where cur), 0)::int,
		  coalesce(sum(points_used) filter (where cur), 0)::int,
		  coalesce(sum(payable) filter (where not cur), 0)::float8
		from (
		  select payable, payment_mode, customer_id, points_earned, points_used,
		         created_at >= $2 as cur
		  from bills
		  where shop_id = $1
		    and ($6::uuid[] is null or location_id = any($6::uuid[]))
		    and ((created_at >= $2 and created_at < $3) or (created_at >= $4 and created_at < $5))
		) b`,
		shopID, rng.Start, rng.End.Add(clockSkew), rng.PrevStart, rng.PrevEnd, locs,
	).Scan(&out.Sales, &out.Bills, &out.Customers,
		&out.Payments.Cash, &out.Payments.UPI, &out.Payments.Credit, &out.Payments.Card,
		&out.Points.Given, &out.Points.Redeemed, prevSales)
}

// buckets sums sales per chart bucket for the current period.
func (h Handler) buckets(ctx context.Context, shopID uuid.UUID, locs []string, rng Range, period, tz string) (map[int]float64, error) {
	rows, err := h.DB.Query(ctx, `
		select `+bucketExpr[period]+` as k, sum(payable)::float8
		from bills
		where shop_id = $1 and created_at >= $3 and created_at < $4
		  and ($5::uuid[] is null or location_id = any($5::uuid[]))
		group by k`, shopID, tz, rng.Start, rng.End.Add(clockSkew), locs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	raw := map[int]float64{}
	for rows.Next() {
		var k int
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		raw[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Turn the grouping key (hour, weekday or day) into the bucket index.
	sums := map[int]float64{}
	for k, v := range raw {
		switch period {
		case Today:
			sums[BucketIndex(period, time.Date(2000, 1, 1, k, 0, 0, 0, time.UTC))] += v
		case Week:
			sums[k] += v
		default:
			sums[BucketIndex(period, time.Date(2000, 1, k, 0, 0, 0, 0, time.UTC))] += v
		}
	}
	return sums, nil
}

func (h Handler) lowStock(ctx context.Context, shopID uuid.UUID, locs []string, out *response) error {
	rows, err := h.DB.Query(ctx, `
		select id, name, quantity::float8, unit, reorder_level::float8
		from stock_items
		where shop_id = $1 and reorder_level is not null and quantity <= reorder_level
		  and ($2::uuid[] is null or location_id = any($2::uuid[]))
		order by case when reorder_level = 0 then 0 else quantity / reorder_level end, name
		limit 10`, shopID, locs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l lowStock
		if err := rows.Scan(&l.ItemID, &l.Name, &l.Quantity, &l.Unit, &l.ReorderLevel); err != nil {
			return err
		}
		out.LowStock = append(out.LowStock, l)
	}
	return rows.Err()
}

func (h Handler) ratesUpdated(ctx context.Context, shopID uuid.UUID, day string, out *response) error {
	return h.DB.QueryRow(ctx, `
		select exists(select 1 from item_prices p join items i on i.id = p.item_id
		              where i.shop_id = $1 and p.day = $2::date)`, shopID, day).Scan(&out.RatesUpdatedToday)
}
