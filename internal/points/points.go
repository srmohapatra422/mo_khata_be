// Package points holds the rules for customer points that more than one handler needs:
// first-in-first-out use of earned points, and expiry.
//
// The balance is never stored: it is SUM(points) of the customer's points_ledger rows.
// Earn rows carry expires_on. Redeem / expire rows are negative and use up the OLDEST earn rows first.
package points

import (
	"context"
	"time"

	"github.com/google/uuid"

	"mohisab-api/internal/db"
)

// Earn is one earn row of the ledger.
type Earn struct {
	Points    int
	ExpiresOn *time.Time // date only; nil = never expires
}

// Remaining spreads outflow (points redeemed, expired or reversed, as a positive number) over the
// earn rows, oldest first (earns must already be ordered by time), and returns what is left of each.
func Remaining(earns []Earn, outflow int) []int {
	left := make([]int, len(earns))
	for i, e := range earns {
		take := min(e.Points, max(outflow, 0))
		outflow -= take
		left[i] = e.Points - take
	}
	return left
}

// ExpiringSoon returns the points that are still unused and expire between today and today+days
// (both inclusive), and the earliest such expiry date (nil when there are none). today is the shop's
// calendar day; only the date part of ExpiresOn matters.
func ExpiringSoon(earns []Earn, outflow int, today time.Time, days int) (int, *time.Time) {
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	from := day(today)
	to := from.AddDate(0, 0, days)
	total := 0
	var first *time.Time
	for i, left := range Remaining(earns, outflow) {
		e := earns[i]
		if left == 0 || e.ExpiresOn == nil {
			continue
		}
		d := day(*e.ExpiresOn)
		if d.Before(from) || d.After(to) {
			continue
		}
		total += left
		if first == nil || d.Before(*first) {
			dd := d
			first = &dd
		}
	}
	return total, first
}

// ExpireDue writes an 'expire' row for every earned point that is unused and past its expires_on
// (before today, in the shop's timezone). cid = nil sweeps the whole shop. Call it inside a
// transaction: it takes a per-shop lock so two sweeps cannot expire the same points twice.
// It returns how many customers had points expired.
func ExpireDue(ctx context.Context, q db.Querier, shopID uuid.UUID, cid *uuid.UUID, today string) (int, error) {
	if _, err := q.Exec(ctx, `select pg_advisory_xact_lock(hashtext('points-expiry:' || $1::text))`, shopID.String()); err != nil {
		return 0, err
	}
	tag, err := q.Exec(ctx, `
		insert into points_ledger (shop_id, customer_id, points, kind, note)
		select shop_id, customer_id, -sum(rem)::int, 'expire', 'points expired'
		from (
		  select e.shop_id, e.customer_id, e.expires_on,
		         greatest(0, least(e.points,
		           sum(e.points) over (partition by e.customer_id order by e.created_at, e.id) - coalesce(o.used, 0)))::int as rem
		  from points_ledger e
		  left join (select customer_id, -sum(points) as used from points_ledger
		             where shop_id = $1 and points < 0 group by customer_id) o on o.customer_id = e.customer_id
		  where e.shop_id = $1 and e.kind = 'earn' and ($2::uuid is null or e.customer_id = $2::uuid)
		) t
		where expires_on < $3::date and rem > 0
		group by shop_id, customer_id`, shopID, cid, today)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
