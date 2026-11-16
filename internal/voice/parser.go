// Package voice turns what the owner says ("aaj 30 kilo rohu aur 10 tray ande aaye, rohu 140
// rupaye kilo") into rows for the confirm popup. Nothing is saved here: the app shows the rows
// and the user confirms, then the app calls the normal save endpoints.
package voice

import "context"

// Modes of a voice entry.
const (
	ModeStockIn = "stock_in"
	ModeSale    = "sale"
	ModeExpense = "expense"
)

// CatalogItem is an item the shop already has (a price item or a stock item).
type CatalogItem struct {
	Name  string
	Unit  string   // kg, tray, piece or bird
	Price *float64 // today's price, when known
}

type Input struct {
	Transcript string
	Mode       string // stock_in | sale | expense
	Language   string // en | hi | or
	Catalog    []CatalogItem
}

// Item is one row for stock_in and sale.
type Item struct {
	Name     string   `json:"name"`    // the shop's own name when matched, otherwise what was heard
	Heard    string   `json:"heard"`   // the words as spoken
	Matched  bool     `json:"matched"` // true when Name is an item the shop already has
	Quantity float64  `json:"quantity"`
	Unit     *string  `json:"unit"` // kg, tray, piece or bird; null when unknown
	Rate     *float64 `json:"rate"`
}

// ExpenseItem is one row for expense mode.
type ExpenseItem struct {
	Name   string  `json:"name"`
	Heard  string  `json:"heard"`
	Amount float64 `json:"amount"`
}

type Output struct {
	Items    []Item
	Expenses []ExpenseItem
}

// Parser is the seam for replacing the rule-based parser with an LLM later.
type Parser interface {
	Parse(ctx context.Context, in Input) (Output, error)
}
