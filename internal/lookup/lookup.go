// Package lookup answers "is this item already there?" for autocomplete and "Did you mean...?".
package lookup

import (
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/access"
	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/names"
)

type Handler struct{ DB *pgxpool.Pool }

type Cand struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Unit string    `json:"unit,omitempty"`
}

type Result struct {
	Kind        string `json:"kind"` // exact | near | none
	Item        *Cand  `json:"item"`
	Suggestions []Cand `json:"suggestions"`
}

const (
	maxSuggestions   = 5
	emptyQuerySample = 6
)

// Rank compares a typed name with the existing names.
//   - exact: the normalised names are equal
//   - near:  within the allowed typo distance
//   - none:  neither (suggestions may still list prefix and contains matches)
//
// An empty query returns the first few items as suggestions.
func Rank(query string, cands []Cand) Result {
	res := Result{Kind: "none", Suggestions: []Cand{}}
	sorted := append([]Cand(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Unit < sorted[j].Unit
	})
	q := names.NormalizeName(query)
	if q == "" {
		for i := 0; i < len(sorted) && i < emptyQuerySample; i++ {
			res.Suggestions = append(res.Suggestions, sorted[i])
		}
		return res
	}

	type scored struct {
		c     Cand
		score int
	}
	var matches []scored
	bestDist := -1
	for _, c := range sorted {
		n := names.NormalizeName(c.Name)
		score := -1
		switch {
		case n == q:
			score = 0
			if res.Kind != "exact" {
				res.Kind, res.Item = "exact", &Cand{ID: c.ID, Name: c.Name, Unit: c.Unit}
			}
		case strings.HasPrefix(n, q):
			score = 1
		case strings.Contains(n, q):
			score = 2
		case names.Matches(n, q):
			score = 3
		}
		if n != q && names.Near(n, q) {
			if d := names.Distance(n, q); res.Kind != "exact" && (bestDist < 0 || d < bestDist) {
				bestDist = d
				res.Kind, res.Item = "near", &Cand{ID: c.ID, Name: c.Name, Unit: c.Unit}
			}
			if score < 0 {
				score = 3
			}
		}
		if score >= 0 {
			matches = append(matches, scored{c, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score < matches[j].score })
	for _, m := range matches {
		if len(res.Suggestions) == maxSuggestions {
			break
		}
		res.Suggestions = append(res.Suggestions, m.c)
	}
	return res
}

// GET /v1/items/match?name=&scope=price|stock&category_id=&location_id=
func (h Handler) Match(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := q.Get("scope")
	if scope != "price" && scope != "stock" {
		httpx.Error(w, http.StatusBadRequest, "scope must be price or stock")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)

	var rows interface {
		Next() bool
		Scan(...any) error
		Err() error
		Close()
	}
	var err error
	if scope == "price" {
		var cat *uuid.UUID
		if raw := q.Get("category_id"); raw != "" {
			id, perr := uuid.Parse(raw)
			if perr != nil {
				httpx.Error(w, http.StatusBadRequest, "invalid category_id")
				return
			}
			cat = &id
		}
		rows, err = h.DB.Query(ctx, `
			select id, name, unit from items
			where shop_id = $1 and active and ($2::uuid is null or category_id = $2::uuid)
			order by name limit 1000`, m.ShopID, cat)
	} else {
		req, perr := access.LocationParam(r)
		if perr != nil {
			httpx.Fail(w, perr)
			return
		}
		locs, perr := access.Locations(ctx, h.DB, m, req)
		if perr != nil {
			httpx.Fail(w, perr)
			return
		}
		rows, err = h.DB.Query(ctx, `
			select id, name, unit from stock_items
			where shop_id = $1 and ($2::uuid[] is null or location_id = any($2::uuid[]))
			order by name limit 1000`, m.ShopID, locs)
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer rows.Close()
	var cands []Cand
	for rows.Next() {
		var c Cand
		if err := rows.Scan(&c.ID, &c.Name, &c.Unit); err != nil {
			httpx.Fail(w, err)
			return
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, Rank(q.Get("name"), cands))
}
