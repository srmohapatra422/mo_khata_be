package customers

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/httpx"
	"mohisab-api/internal/points"
)

// Handler serves the customer screens. GoldSpend and SilverSpend are the tier thresholds (rupees
// over the last 12 months) and come from the config.
type Handler struct {
	DB          *pgxpool.Pool
	GoldSpend   float64
	SilverSpend float64
}

func (h Handler) thresholds() (gold, silver float64) {
	gold, silver = h.GoldSpend, h.SilverSpend
	if gold <= 0 {
		gold = 50000
	}
	if silver <= 0 {
		silver = 20000
	}
	return gold, silver
}

// stat is one customer with everything derived from bills, credit payments and the points ledger.
type stat struct {
	ID         uuid.UUID
	Phone      string
	Name       *string
	Points     int
	IsBusiness bool
	WhatsApp   bool
	CreatedAt  time.Time
	Visits     int
	FirstAt    *time.Time
	LastAt     *time.Time
	MonthSpend float64
	Spend12m   float64
	TotalSpend float64
	Due        float64
	Tier       string
}

// statsFrom is the one query behind the list, the summary and the details. Parameters:
// $1 shop, $2 customer id (null = all), $3/$4 this month [start,end), $5 start of the 12 months,
// $6 gold, $7 silver. Everything is aggregated per customer once (no N+1).
const statsFrom = `
with b as (
  select customer_id, count(*) as visits, min(created_at) as first_at, max(created_at) as last_at,
         coalesce(sum(payable) filter (where created_at >= $3 and created_at < $4), 0) as month_spend,
         coalesce(sum(payable) filter (where created_at >= $5), 0) as spend12,
         coalesce(sum(payable), 0) as total_spend,
         coalesce(sum(payable) filter (where payment_mode = 'credit'), 0) as credit_billed
  from bills
  where shop_id = $1 and customer_id is not null and ($2::uuid is null or customer_id = $2::uuid)
  group by customer_id),
cp as (
  select customer_id, sum(amount) as paid from credit_payments
  where shop_id = $1 and ($2::uuid is null or customer_id = $2::uuid) group by customer_id),
pt as (
  select customer_id, sum(points) as pts from points_ledger
  where shop_id = $1 and ($2::uuid is null or customer_id = $2::uuid) group by customer_id),
x as (
  select c.id, c.phone, c.name, coalesce(pt.pts, 0)::int as points, c.is_business, c.whatsapp_opt_in,
         c.created_at, coalesce(b.visits, 0)::int as visits, b.first_at, b.last_at,
         coalesce(b.month_spend, 0)::float8 as month_spend, coalesce(b.spend12, 0)::float8 as spend12,
         coalesce(b.total_spend, 0)::float8 as total_spend,
         greatest(coalesce(b.credit_billed, 0) - coalesce(cp.paid, 0), 0)::float8 as due,
         case when c.is_business then 'business'
              when coalesce(b.spend12, 0) >= $6::numeric then 'gold'
              when coalesce(b.spend12, 0) >= $7::numeric then 'silver'
              else 'regular' end as tier
  from customers c
  left join b on b.customer_id = c.id
  left join cp on cp.customer_id = c.id
  left join pt on pt.customer_id = c.id
  where c.shop_id = $1 and ($2::uuid is null or c.id = $2::uuid))
`

const statCols = `x.id, x.phone, x.name, x.points, x.is_business, x.whatsapp_opt_in, x.created_at, x.visits,
	x.first_at, x.last_at, x.month_spend, x.spend12, x.total_spend, x.due, x.tier`

// clock holds the shop-timezone values every stats query needs.
type clock struct {
	now                  time.Time
	monthStart, monthEnd time.Time
	year                 time.Time
}

func newClock(now time.Time) clock {
	ms, me := MonthRange(now)
	return clock{now: now, monthStart: ms, monthEnd: me, year: now.AddDate(-1, 0, 0)}
}

func (h Handler) args(shopID uuid.UUID, cid *uuid.UUID, c clock) []any {
	g, s := h.thresholds()
	return []any{shopID, cid, c.monthStart, c.monthEnd, c.year, g, s}
}

// sweep writes 'expire' rows for points that ran out (see points.ExpireDue) before a read.
func (h Handler) sweep(ctx context.Context, shopID uuid.UUID, cid *uuid.UUID, today string) error {
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := points.ExpireDue(ctx, tx, shopID, cid, today); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- filters and sorting (the SQL text is fixed here; user input only picks a key) ----

var filterSQL = map[string]string{
	"":                "true",
	"all":             "true",
	"gold":            "x.tier = 'gold'",
	"credit_due":      "x.due > 0",
	"not_visited_30d": "(x.last_at is null or x.last_at < $9)",
}

var sortSQL = map[string]string{
	"":       "x.created_at desc",
	"recent": "x.created_at desc",
	"name":   "lower(coalesce(x.name, x.phone)) asc",
	"points": "x.points desc",
	"due":    "x.due desc",
}

// ListParams is the validated query of GET /v1/customers.
type ListParams struct {
	Q      string
	Filter string
	Sort   string
	Limit  int
	Offset int
}

func parseListParams(get func(string) string) (ListParams, error) {
	p := ListParams{Q: strings.TrimSpace(get("q")), Filter: get("filter"), Sort: get("sort"), Limit: 50}
	if _, ok := filterSQL[p.Filter]; !ok {
		return p, httpx.BadRequest("filter must be all, gold, credit_due or not_visited_30d")
	}
	if _, ok := sortSQL[p.Sort]; !ok {
		return p, httpx.BadRequest("sort must be recent, name, points or due")
	}
	if v := get("limit"); v != "" {
		n, ok := atoi(v)
		if !ok || n < 1 || n > 100 {
			return p, httpx.BadRequest("limit must be 1 to 100")
		}
		p.Limit = n
	}
	if v := get("offset"); v != "" {
		n, ok := atoi(v)
		if !ok || n < 0 {
			return p, httpx.BadRequest("offset must be 0 or more")
		}
		p.Offset = n
	}
	return p, nil
}

func atoi(s string) (int, bool) {
	n := 0
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// likePattern turns the search text into a contains-pattern, escaping LIKE wildcards.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

func scanStat(row interface{ Scan(...any) error }, extra ...any) (stat, error) {
	var s stat
	dest := append([]any{&s.ID, &s.Phone, &s.Name, &s.Points, &s.IsBusiness, &s.WhatsApp, &s.CreatedAt, &s.Visits,
		&s.FirstAt, &s.LastAt, &s.MonthSpend, &s.Spend12m, &s.TotalSpend, &s.Due, &s.Tier}, extra...)
	return s, row.Scan(dest...)
}

func (h Handler) list(ctx context.Context, shopID uuid.UUID, c clock, p ListParams) ([]stat, int, error) {
	args := h.args(shopID, nil, c)
	args = append(args, likePattern(p.Q), c.now.AddDate(0, 0, -30), p.Limit, p.Offset)
	sql := statsFrom + `select ` + statCols + `, count(*) over ()
		from x where (x.phone ilike $8 or coalesce(x.name, '') ilike $8) and $9::timestamptz is not null and ` + filterSQL[p.Filter] + `
		order by ` + sortSQL[p.Sort] + `, x.id limit $10 offset $11`
	rows, err := h.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []stat{}
	total := 0
	for rows.Next() {
		s, err := scanStat(rows, &total)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

func (h Handler) one(ctx context.Context, shopID, id uuid.UUID, c clock) (*stat, error) {
	rows, err := h.DB.Query(ctx, statsFrom+`select `+statCols+` from x`, h.args(shopID, &id, c)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	s, err := scanStat(rows)
	if err != nil {
		return nil, err
	}
	return &s, nil
}
