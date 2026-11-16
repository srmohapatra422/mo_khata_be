// Package shoptime handles calendar days in the shop's timezone.
package shoptime

import (
	"time"

	"mohisab-api/internal/httpx"
)

const DateLayout = "2006-01-02"

// DayRange returns [start, end) of a business day in the shop's timezone. An empty date means
// today. The date must be YYYY-MM-DD and not in the future.
func DayRange(date string, now time.Time) (start, end time.Time, day string, err error) {
	loc := now.Location()
	if date == "" {
		date = now.Format(DateLayout)
	}
	start, perr := time.ParseInLocation(DateLayout, date, loc)
	if perr != nil {
		return start, end, "", httpx.BadRequest("date must be YYYY-MM-DD")
	}
	if start.After(now) {
		return start, end, "", httpx.BadRequest("date cannot be in the future")
	}
	return start, start.AddDate(0, 0, 1), start.Format(DateLayout), nil
}
