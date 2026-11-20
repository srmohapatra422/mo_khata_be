package customers

import "time"

// The derived values the customer screens show. They are pure functions so they can be unit tested;
// "today", "this month" and "days" always use the shop timezone (the location of now).

// LastVisitDays is the number of calendar days between the last bill and now in now's timezone:
// 0 = today, 1 = yesterday. nil when the customer has no bills.
func LastVisitDays(last *time.Time, now time.Time) *int {
	if last == nil {
		return nil
	}
	loc := now.Location()
	l := last.In(loc)
	a := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	b := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	d := int(b.Sub(a).Hours() / 24)
	if d < 0 {
		d = 0 // a bill dated slightly in the future (phone clock) counts as today
	}
	return &d
}

// MonthRange is [start, end) of the calendar month that now is in, in now's timezone.
func MonthRange(now time.Time) (start, end time.Time) {
	start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return start, start.AddDate(0, 1, 0)
}

const (
	TierRegular  = "regular"
	TierSilver   = "silver"
	TierGold     = "gold"
	TierBusiness = "business"
)

// Tier is not stored. A business customer is always "business"; otherwise it follows the spend over
// the last 12 months (silver and gold thresholds come from the config).
func Tier(isBusiness bool, spend12m, gold, silver float64) string {
	switch {
	case isBusiness:
		return TierBusiness
	case spend12m >= gold:
		return TierGold
	case spend12m >= silver:
		return TierSilver
	}
	return TierRegular
}

// Due is what the customer still owes: credit bills minus payments, never below zero.
func Due(creditBilled, paid float64) float64 {
	d := creditBilled - paid
	if d < 0.005 {
		return 0
	}
	return round2(d)
}
