package dashboard

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"mohisab-api/internal/db"
)

// Runs against a real database. Set TEST_DATABASE_URL to run it; it creates and deletes its own shop.
func TestIntegration_TotalsAndBuckets(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := Handler{DB: pool}

	var shop, loc, otherLoc, c1, c2 uuid.UUID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `insert into shops (name) values ('dashboard-itest') returning id`).Scan(&shop))
	defer func() {
		_, _ = pool.Exec(ctx, `delete from bills where shop_id = $1`, shop)
		_, _ = pool.Exec(ctx, `delete from shops where id = $1`, shop)
	}()
	must(pool.QueryRow(ctx, `insert into locations (shop_id, name, type) values ($1, 'Shop', 'shop') returning id`, shop).Scan(&loc))
	must(pool.QueryRow(ctx, `insert into locations (shop_id, name, type) values ($1, 'Farm', 'farm') returning id`, shop).Scan(&otherLoc))
	must(pool.QueryRow(ctx, `insert into customers (shop_id, phone) values ($1, '1') returning id`, shop).Scan(&c1))
	must(pool.QueryRow(ctx, `insert into customers (shop_id, phone) values ($1, '2') returning id`, shop).Scan(&c2))

	type bill struct {
		at       time.Time
		amount   float64
		mode     string
		cust     *uuid.UUID
		earned   int
		used     int
		location uuid.UUID
	}
	bills := []bill{
		// current week (Mon 16 Nov .. Sun 22 Nov 2026)
		{at(2026, 11, 16, 10, 0, 0), 100, "cash", &c1, 10, 0, loc},
		{at(2026, 11, 17, 12, 0, 0), 200, "upi", &c1, 20, 5, loc},
		{at(2026, 11, 18, 12, 0, 0), 300, "credit", &c2, 30, 0, loc},
		{at(2026, 11, 19, 12, 0, 0), 400, "card", nil, 0, 0, loc},
		{at(2026, 11, 20, 12, 0, 0), 500, "cash", nil, 0, 0, loc},
		{at(2026, 11, 21, 12, 0, 0), 600, "upi", nil, 0, 0, loc},
		{at(2026, 11, 22, 7, 0, 0), 50, "cash", nil, 0, 0, loc},
		{at(2026, 11, 22, 22, 30, 0), 700, "cash", nil, 0, 0, otherLoc},
		// last week, same elapsed span (up to Sun 15 Nov 23:00)
		{at(2026, 11, 9, 10, 0, 0), 1000, "cash", nil, 0, 0, loc},
		{at(2026, 11, 14, 10, 0, 0), 500, "cash", nil, 0, 0, loc},
		{at(2026, 11, 15, 23, 30, 0), 9999, "cash", nil, 0, 0, loc}, // after the same clock time: not compared
		// earlier this month and the month before
		{at(2026, 11, 3, 10, 0, 0), 11, "cash", nil, 0, 0, loc},
		{at(2026, 11, 10, 10, 0, 0), 22, "cash", nil, 0, 0, loc},
		{at(2026, 10, 5, 10, 0, 0), 500, "cash", nil, 0, 0, loc},
		// after "now": must not count
		{at(2026, 11, 23, 9, 0, 0), 77777, "cash", nil, 0, 0, loc},
	}
	for i, b := range bills {
		_, err := pool.Exec(ctx, `
			insert into bills (id, shop_id, location_id, customer_id, total, points_used, points_discount, points_earned,
			                   payable, payment_mode, created_by, created_at)
			values ($1, $2, $3, $4, $5, $6, 0, $7, $5, $8, $9, $10)`,
			uuid.New(), shop, b.location, b.cust, b.amount, b.used, b.earned, b.mode, uuid.New(), b.at)
		must(err)
		_ = i
	}

	now := at(2026, 11, 22, 23, 0, 0)
	run := func(period string, locs []string) (response, float64, Chart) {
		t.Helper()
		rng, _ := Ranges(period, now)
		var out response
		var prev float64
		must(h.totals(ctx, shop, locs, rng, &out, &prev))
		sums, err := h.buckets(ctx, shop, locs, rng, period, "Asia/Kolkata")
		must(err)
		return out, prev, BuildChart(period, daysInMonth(now), sums)
	}
	eq := func(name string, got, want any) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	// --- week, all locations
	out, prev, chart := run(Week, nil)
	eq("week sales", out.Sales, 2850.0) // 100+200+300+400+500+600+50+700
	eq("week bills", out.Bills, 8)
	eq("week customers", out.Customers, 2)
	eq("week cash", out.Payments.Cash, 1350.0) // 100+500+50+700 (not last week's)
	eq("week upi", out.Payments.UPI, 800.0)
	eq("week credit", out.Payments.Credit, 300.0)
	eq("week card", out.Payments.Card, 400.0)
	eq("points given", out.Points.Given, 60)
	eq("points redeemed", out.Points.Redeemed, 5)
	eq("week previous sales", prev, 1522.0)         // 1000 + 500 + 22 (10 Nov), not the 23:30 bill
	eq("week growth", *Growth(out.Sales, prev), 87) // (2850-1522)/1522 = 87.3%
	eq("week amounts", chart.Amounts, []float64{100, 200, 300, 400, 500, 600, 750})
	eq("week best", *chart.Best, Best{"S", 750})

	// --- week, one location
	out, prev, chart = run(Week, []string{otherLoc.String()})
	eq("farm only: sales", out.Sales, 700.0)
	eq("farm only: bills", out.Bills, 1)
	eq("farm only: previous", prev, 0.0)
	eq("farm only: growth", Growth(out.Sales, prev) == nil, true)
	eq("farm only: chart", chart.Amounts, []float64{0, 0, 0, 0, 0, 0, 700})
	out, _, _ = run(Week, []string{})
	eq("no allowed locations: sales", out.Sales, 0.0)

	// --- today (Sun 22 Nov): 07:00 -> first bucket, 22:30 -> last bucket
	out, prev, chart = run(Today, nil)
	eq("today sales", out.Sales, 750.0)
	eq("today amounts", chart.Amounts, []float64{50, 0, 0, 0, 0, 0, 700})
	eq("today previous (Sat up to 23:00)", prev, 600.0)

	// --- month (Nov): W1 days 1-7, W2 8-14, W3 15-21, W4 22+
	out, prev, chart = run(Month, nil)
	eq("month sales", out.Sales, 2850.0+11+22+1000+500+9999) // everything dated 1-22 Nov
	eq("month amounts", chart.Amounts, []float64{11, 1000 + 500 + 22, 9999 + 100 + 200 + 300 + 400 + 500 + 600, 750, 0})
	eq("month previous (Oct 1 - Oct 22 23:00)", prev, 500.0)
}
