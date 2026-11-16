package voice

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/access"
	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/names"
)

type Handler struct {
	DB     *pgxpool.Pool
	Parser Parser
}

const maxTranscript = 1000

type parseIn struct {
	Transcript string `json:"transcript"`
	Mode       string `json:"mode"`
	Language   string `json:"language"`
}

// validate cleans the input and returns a message for the first problem, or "".
func (in *parseIn) validate() string {
	in.Transcript = strings.TrimSpace(in.Transcript)
	switch {
	case in.Transcript == "":
		return "transcript is required"
	case utf8.RuneCountInString(in.Transcript) > maxTranscript:
		return "transcript is too long (1000 characters at most)"
	}
	switch in.Mode {
	case ModeStockIn, ModeSale, ModeExpense:
	default:
		return "mode must be stock_in, sale or expense"
	}
	switch in.Language {
	case "":
		in.Language = "en"
	case "en", "hi", "or":
	default:
		return "language must be en, hi or or"
	}
	return ""
}

// stock units are stored as kg, birds, trays, pieces; the app speaks kg, bird, tray, piece
var stockToAppUnit = map[string]string{"kg": "kg", "birds": "bird", "trays": "tray", "pieces": "piece"}

// catalog returns the shop's items the spoken names are matched against.
func (h Handler) catalog(r *http.Request, mode string) ([]CatalogItem, error) {
	if mode == ModeExpense {
		return nil, nil
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	day := time.Now().In(m.Location()).Format("2006-01-02")

	var cat []CatalogItem
	seen := map[string]bool{}
	add := func(c CatalogItem) {
		if n := names.NormalizeName(c.Name); n != "" && !seen[n] {
			seen[n] = true
			cat = append(cat, c)
		}
	}
	rows, err := h.DB.Query(ctx, `
		select i.name, i.unit,
		       (select p.price::float8 from item_prices p where p.item_id = i.id and p.day <= $2::date order by p.day desc limit 1)
		from items i where i.shop_id = $1 and i.active order by i.name limit 1000`, m.ShopID, day)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c CatalogItem
		if err := rows.Scan(&c.Name, &c.Unit, &c.Price); err != nil {
			rows.Close()
			return nil, err
		}
		if c.Unit == "dozen" {
			c.Unit = "piece"
		}
		add(c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if mode == ModeStockIn {
		locs, err := access.Locations(ctx, h.DB, m, nil)
		if err != nil {
			return nil, err
		}
		srows, err := h.DB.Query(ctx, `
			select name, unit from stock_items
			where shop_id = $1 and ($2::uuid[] is null or location_id = any($2::uuid[]))
			order by name limit 1000`, m.ShopID, locs)
		if err != nil {
			return nil, err
		}
		defer srows.Close()
		for srows.Next() {
			var c CatalogItem
			var unit string
			if err := srows.Scan(&c.Name, &unit); err != nil {
				return nil, err
			}
			c.Unit = stockToAppUnit[unit]
			add(c)
		}
		if err := srows.Err(); err != nil {
			return nil, err
		}
	}
	return cat, nil
}

// POST /v1/voice/parse — turns a transcript into rows for the confirm popup. Saves nothing.
func (h Handler) Parse(w http.ResponseWriter, r *http.Request) {
	var in parseIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if msg := in.validate(); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	cat, err := h.catalog(r, in.Mode)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out, err := h.Parser.Parse(r.Context(), Input{Transcript: in.Transcript, Mode: in.Mode, Language: in.Language, Catalog: cat})
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if in.Mode == ModeExpense {
		rows := out.Expenses
		if rows == nil {
			rows = []ExpenseItem{}
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
		return
	}
	rows := out.Items
	if rows == nil {
		rows = []Item{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}
