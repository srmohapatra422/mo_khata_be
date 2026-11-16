// Package access decides which locations a request may use.
// Owners and cashiers see every location. Farm managers only see the farm.
package access

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
)

// LocationParam reads the optional ?location_id= query value.
func LocationParam(r *http.Request) (*uuid.UUID, error) {
	raw := r.URL.Query().Get("location_id")
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, httpx.BadRequest("invalid location_id")
	}
	return &id, nil
}

// Locations returns the allowed location ids as strings for "= any($n::uuid[])" in SQL.
// A nil result means every location of the shop. A requested location must belong to the shop
// (400 otherwise); a farm manager asking for a non-farm location gets 403.
func Locations(ctx context.Context, q db.Querier, m auth.Member, requested *uuid.UUID) ([]string, error) {
	if requested != nil {
		var typ string
		err := q.QueryRow(ctx, `select type from locations where id = $1 and shop_id = $2`, *requested, m.ShopID).Scan(&typ)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.BadRequest("unknown location")
		}
		if err != nil {
			return nil, err
		}
		if m.Role == "farm_manager" && typ != "farm" {
			return nil, httpx.Forbidden("farm managers can only use the farm location")
		}
		return []string{requested.String()}, nil
	}
	if m.Role != "farm_manager" {
		return nil, nil
	}
	rows, err := q.Query(ctx, `select id::text from locations where shop_id = $1 and type = 'farm'`, m.ShopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
