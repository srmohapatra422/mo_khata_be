// Package rewards serves the "Reward your buyers" settings.
//
// The points rules are stored on shops (points_per_rupees, point_value, points_expiry_months,
// max_points_pct) because billing already reads them from there, so there is one source of truth.
// Changing them only affects bills saved afterwards.
package rewards

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/names"
)

type Handler struct{ DB *pgxpool.Pool }

type item struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Points int       `json:"points"`
}

type settings struct {
	SpendPerPoint int     `json:"spend_per_point"`
	PointValue    float64 `json:"point_value"`
	ExpiryMonths  *int    `json:"expiry_months"`
	MaxPayPercent int     `json:"max_pay_percent"`
	Items         []item  `json:"items"`
}

func load(ctx context.Context, q db.Querier, shopID uuid.UUID) (settings, error) {
	var s settings
	var perRupees float64
	err := q.QueryRow(ctx, `
		select points_per_rupees::float8, point_value::float8, points_expiry_months, max_points_pct
		from shops where id = $1`, shopID).Scan(&perRupees, &s.PointValue, &s.ExpiryMonths, &s.MaxPayPercent)
	if err != nil {
		return s, err
	}
	s.SpendPerPoint = int(math.Round(perRupees))
	s.Items = []item{}
	rows, err := q.Query(ctx, `select id, name, points from reward_items where shop_id = $1 and active order by points, name`, shopID)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Name, &it.Points); err != nil {
			return s, err
		}
		s.Items = append(s.Items, it)
	}
	return s, rows.Err()
}

// GET /v1/rewards
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	s, err := load(r.Context(), h.DB, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

type putIn struct {
	SpendPerPoint *int     `json:"spend_per_point"`
	PointValue    *float64 `json:"point_value"`
	ExpiryMonths  *int     `json:"expiry_months"`
	MaxPayPercent *int     `json:"max_pay_percent"`
}

// validate returns a message for the first problem, or "". All four values are required.
func (in putIn) validate() string {
	switch {
	case in.SpendPerPoint == nil || in.PointValue == nil || in.ExpiryMonths == nil || in.MaxPayPercent == nil:
		return "spend_per_point, point_value, expiry_months and max_pay_percent are all required"
	case *in.SpendPerPoint < 1 || *in.SpendPerPoint > 1000:
		return "spend_per_point must be between 1 and 1000"
	case !(*in.PointValue >= 0.05) || *in.PointValue > 5:
		return "point_value must be between 0.05 and 5"
	case *in.ExpiryMonths < 1 || *in.ExpiryMonths > 60:
		return "expiry_months must be between 1 and 60"
	case *in.MaxPayPercent < 5 || *in.MaxPayPercent > 100:
		return "max_pay_percent must be between 5 and 100"
	}
	return ""
}

// PUT /v1/rewards (owner). Returns the same body as GET.
func (h Handler) Put(w http.ResponseWriter, r *http.Request) {
	var in putIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if msg := in.validate(); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	ctx := r.Context()
	shopID := auth.MemberOf(ctx).ShopID
	if _, err := h.DB.Exec(ctx, `
		update shops set points_per_rupees = $2, point_value = $3, points_expiry_months = $4, max_points_pct = $5
		where id = $1`, shopID, *in.SpendPerPoint, math.Round(*in.PointValue*100)/100, *in.ExpiryMonths, *in.MaxPayPercent); err != nil {
		httpx.Fail(w, err)
		return
	}
	s, err := load(ctx, h.DB, shopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

type itemIn struct {
	Name   string `json:"name"`
	Points *int   `json:"points"`
}

func (in *itemIn) validate() string {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "" || names.NormalizeName(in.Name) == "":
		return "name is required"
	case utf8.RuneCountInString(in.Name) > 80:
		return "name is too long (80 characters at most)"
	case in.Points == nil || *in.Points < 1 || *in.Points > 1_000_000:
		return "points must be between 1 and 1000000"
	}
	return ""
}

// POST /v1/rewards/items (owner)
func (h Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
	var in itemIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if msg := in.validate(); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	var out item
	err := h.DB.QueryRow(r.Context(), `
		insert into reward_items (shop_id, name, name_norm, points) values ($1, $2, $3, $4)
		returning id, name, points`,
		auth.MemberOf(r.Context()).ShopID, in.Name, names.NormalizeName(in.Name), *in.Points).Scan(&out.ID, &out.Name, &out.Points)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		httpx.Error(w, http.StatusConflict, "reward already exists: "+in.Name)
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// DELETE /v1/rewards/items/{id} (owner) — hides the reward.
func (h Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid reward id")
		return
	}
	tag, err := h.DB.Exec(r.Context(), `update reward_items set active = false where id = $1 and shop_id = $2 and active`,
		id, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "reward not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
