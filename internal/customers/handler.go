package customers

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/points"
	"mohisab-api/internal/shoptime"
)

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func tzTime(t *time.Time, loc *time.Location) *time.Time {
	if t == nil {
		return nil
	}
	l := t.In(loc)
	return &l
}

// customerOut: the first four fields are the original response, the rest were added.
type customerOut struct {
	ID            uuid.UUID  `json:"id"`
	Phone         string     `json:"phone"`
	Name          *string    `json:"name"`
	Points        int        `json:"points"`
	Tier          string     `json:"tier"`
	LastVisitAt   *time.Time `json:"last_visit_at"`
	LastVisitDays *int       `json:"last_visit_days"`
	Visits        int        `json:"visits"`
	MonthSpend    float64    `json:"month_spend"`
	Due           float64    `json:"due"`
	WhatsApp      bool       `json:"whatsapp_opted_in"`
}

// GET /v1/customers?q=&filter=all|gold|credit_due|not_visited_30d&sort=recent|name|points|due&limit=&offset=
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	p, err := parseListParams(r.URL.Query().Get)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	loc := m.Location()
	c := newClock(time.Now().In(loc))
	if err := h.sweep(r.Context(), m.ShopID, nil, c.now.Format(shoptime.DateLayout)); err != nil {
		httpx.Fail(w, err)
		return
	}
	rows, total, err := h.list(r.Context(), m.ShopID, c, p)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out := make([]customerOut, 0, len(rows))
	for _, s := range rows {
		out = append(out, customerOut{
			ID: s.ID, Phone: s.Phone, Name: s.Name, Points: s.Points, Tier: s.Tier,
			LastVisitAt: tzTime(s.LastAt, loc), LastVisitDays: LastVisitDays(s.LastAt, c.now),
			Visits: s.Visits, MonthSpend: round2(s.MonthSpend), Due: round2(s.Due), WhatsApp: s.WhatsApp,
		})
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	httpx.JSON(w, http.StatusOK, out)
}

// GET /v1/customers/summary
func (h Handler) Summary(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	c := newClock(time.Now().In(m.Location()))
	if err := h.sweep(r.Context(), m.ShopID, nil, c.now.Format(shoptime.DateLayout)); err != nil {
		httpx.Fail(w, err)
		return
	}
	var out struct {
		Customers      int     `json:"customers"`
		PointsUnused   int     `json:"points_unredeemed"`
		CreditDueTotal float64 `json:"credit_due_total"`
		CreditDueCount int     `json:"credit_due_count"`
		Gold           int     `json:"gold"`
		NotVisited30d  int     `json:"not_visited_30d"`
	}
	args := append(h.args(m.ShopID, nil, c), c.now.AddDate(0, 0, -30))
	err := h.DB.QueryRow(r.Context(), statsFrom+`
		select count(*)::int, coalesce(sum(greatest(x.points, 0)), 0)::int, coalesce(sum(x.due), 0)::float8,
		       (count(*) filter (where x.due > 0))::int, (count(*) filter (where x.tier = 'gold'))::int,
		       (count(*) filter (where x.last_at is null or x.last_at < $8))::int
		from x`, args...).
		Scan(&out.Customers, &out.PointsUnused, &out.CreditDueTotal, &out.CreditDueCount, &out.Gold, &out.NotVisited30d)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out.CreditDueTotal = round2(out.CreditDueTotal)
	httpx.JSON(w, http.StatusOK, out)
}

type detailOut struct {
	ID            uuid.UUID  `json:"id"`
	Phone         string     `json:"phone"`
	Name          *string    `json:"name"`
	Tier          string     `json:"tier"`
	IsBusiness    bool       `json:"is_business"`
	WhatsApp      bool       `json:"whatsapp_opted_in"`
	Points        int        `json:"points"`
	PointsWorth   float64    `json:"points_worth"`
	ExpiringPts   int        `json:"expiring_points"`
	ExpiryDate    *string    `json:"expiry_date"`
	Due           float64    `json:"due"`
	Visits        int        `json:"visits"`
	MonthSpend    float64    `json:"month_spend"`
	TotalSpend    float64    `json:"total_spend"`
	FirstVisitAt  *time.Time `json:"first_visit_at"`
	LastVisitAt   *time.Time `json:"last_visit_at"`
	LastVisitDays *int       `json:"last_visit_days"`
	UsuallyBuys   []string   `json:"usually_buys"`
	CreatedAt     time.Time  `json:"created_at"`
}

// detail loads the full details object for one customer of the shop (404 if it is not in the shop).
func (h Handler) detail(r *http.Request, id uuid.UUID) (*detailOut, error) {
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	loc := m.Location()
	c := newClock(time.Now().In(loc))
	today := c.now.Format(shoptime.DateLayout)

	if err := h.sweep(ctx, m.ShopID, &id, today); err != nil {
		return nil, err
	}
	s, err := h.one(ctx, m.ShopID, id, c)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, httpx.NotFound("customer not found")
	}

	var pointValue float64
	if err := h.DB.QueryRow(ctx, `select point_value::float8 from shops where id = $1`, m.ShopID).Scan(&pointValue); err != nil {
		return nil, err
	}

	// Earn rows (oldest first) and what has been used, to find the points that expire soon.
	rows, err := h.DB.Query(ctx, `
		select points, expires_on from points_ledger
		where shop_id = $1 and customer_id = $2 and kind = 'earn' order by created_at, id`, m.ShopID, id)
	if err != nil {
		return nil, err
	}
	var earns []points.Earn
	for rows.Next() {
		var e points.Earn
		if err := rows.Scan(&e.Points, &e.ExpiresOn); err != nil {
			rows.Close()
			return nil, err
		}
		earns = append(earns, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var used int
	if err := h.DB.QueryRow(ctx, `select coalesce(-sum(points), 0)::int from points_ledger
		where shop_id = $1 and customer_id = $2 and points < 0`, m.ShopID, id).Scan(&used); err != nil {
		return nil, err
	}
	expiring, first := points.ExpiringSoon(earns, used, c.now, 30)
	var expiryDate *string
	if first != nil {
		d := first.Format(shoptime.DateLayout)
		expiryDate = &d
	}

	// Top 3 item names by number of bill lines in the last 90 days (manual lines count by name).
	buys := []string{}
	brows, err := h.DB.Query(ctx, `
		select (array_agg(bl.name order by b.created_at desc))[1]
		from bill_lines bl join bills b on b.id = bl.bill_id
		where b.shop_id = $1 and b.customer_id = $2 and b.created_at >= $3
		group by lower(btrim(bl.name))
		order by count(*) desc, max(b.created_at) desc, lower(btrim(bl.name))
		limit 3`, m.ShopID, id, c.now.AddDate(0, 0, -90))
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var n string
		if err := brows.Scan(&n); err != nil {
			return nil, err
		}
		buys = append(buys, n)
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}

	return &detailOut{
		ID: s.ID, Phone: s.Phone, Name: s.Name, Tier: s.Tier, IsBusiness: s.IsBusiness, WhatsApp: s.WhatsApp,
		Points: s.Points, PointsWorth: round2(float64(s.Points) * pointValue),
		ExpiringPts: expiring, ExpiryDate: expiryDate,
		Due: round2(s.Due), Visits: s.Visits, MonthSpend: round2(s.MonthSpend), TotalSpend: round2(s.TotalSpend),
		FirstVisitAt: tzTime(s.FirstAt, loc), LastVisitAt: tzTime(s.LastAt, loc),
		LastVisitDays: LastVisitDays(s.LastAt, c.now), UsuallyBuys: buys, CreatedAt: s.CreatedAt,
	}, nil
}

// GET /v1/customers/{id}
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	d, err := h.detail(r, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}
