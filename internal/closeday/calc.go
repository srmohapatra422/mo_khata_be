// Package closeday is the end-of-day count: cash in the drawer and stock on the shelf.
package closeday

import "math"

// tolerance: differences smaller than half a paisa are rounding, not a real difference.
const tolerance = 0.005

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// ExpectedCash is the cash that should be in the drawer at the end of the day:
// cash bills + cash collected on credit + the opening float - expenses.
func ExpectedCash(cashBills, cashCreditCollected, openingFloat, expenses float64) float64 {
	return round2(cashBills + cashCreditCollected + openingFloat - expenses)
}

// Difference is counted minus expected. Negative means the drawer is short.
func Difference(counted, expected float64) float64 {
	return round2(counted - expected)
}

// Status says what the difference means: short, extra or matched.
func Status(difference float64) string {
	switch {
	case difference < -tolerance:
		return "short"
	case difference > tolerance:
		return "extra"
	default:
		return "matched"
	}
}

// Wastage is what went missing from the shelf. Only weighed (kg) items count as wastage:
// meat and fish lose weight from trimming. A short count of birds, trays or pieces is a stock
// correction, not wastage.
func Wastage(unit string, expected, actual float64) float64 {
	if unit != "kg" {
		return 0
	}
	return math.Round(math.Max(expected-actual, 0)*1000) / 1000
}

// Split divides a stock change (actual - expected) into the wastage movement and the
// adjustment movement, so the two always add up to the change:
//
//	(-wastage) + adjustment == actual - expected
func Split(unit string, expected, actual float64) (wastage, adjustment float64) {
	wastage = Wastage(unit, expected, actual)
	adjustment = math.Round((actual-expected+wastage)*1000) / 1000
	return wastage, adjustment
}
