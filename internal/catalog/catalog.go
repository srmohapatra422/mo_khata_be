// Package catalog holds what pricing, stock and billing share: categories and item names.
package catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mohisab-api/internal/db"
	"mohisab-api/internal/names"
)

// Standard are the five categories every shop can sell, in display order.
var Standard = []struct{ Key, Name string }{
	{"chicken", "Chicken"}, {"fish", "Fish"}, {"eggs", "Eggs"}, {"mutton", "Mutton"}, {"dairy", "Dairy"},
}

var (
	ErrUnknownCategory = errors.New("unknown category")
	ErrCategoryExists  = errors.New("category already exists")
)

type Category struct {
	ID   uuid.UUID `json:"id"`
	Key  *string   `json:"key"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
}

// Seed adds the standard categories to a shop the first time they are needed.
func Seed(ctx context.Context, q db.Querier, shopID uuid.UUID) error {
	keys := make([]string, len(Standard))
	nm := make([]string, len(Standard))
	norm := make([]string, len(Standard))
	for i, s := range Standard {
		keys[i], nm[i], norm[i] = s.Key, s.Name, names.NormalizeName(s.Name)
	}
	_, err := q.Exec(ctx, `
		insert into categories (shop_id, key, name, name_norm, kind)
		select $1, k, n, nn, 'standard' from unnest($2::text[], $3::text[], $4::text[]) as t(k, n, nn)
		on conflict (shop_id, name_norm) do nothing`, shopID, keys, nm, norm)
	return err
}

// List returns the standard categories first (fixed order), then custom ones.
func List(ctx context.Context, q db.Querier, shopID uuid.UUID) ([]Category, error) {
	if err := Seed(ctx, q, shopID); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		select c.id, c.key, c.name, c.kind from categories c join shops s on s.id = c.shop_id
		where c.shop_id = $1 and (c.key is null or c.key = any(s.categories))
		order by array_position(array['chicken','fish','eggs','mutton','dairy'], c.key), c.created_at`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Key, &c.Name, &c.Kind); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Create adds a custom category. It returns ErrCategoryExists when the name (normalised) is taken.
func Create(ctx context.Context, q db.Querier, shopID uuid.UUID, name string) (Category, error) {
	name = strings.TrimSpace(name)
	norm := names.NormalizeName(name)
	var c Category
	if norm == "" {
		return c, ErrUnknownCategory
	}
	if err := Seed(ctx, q, shopID); err != nil {
		return c, err
	}
	var exists bool
	if err := q.QueryRow(ctx, `select exists(select 1 from categories where shop_id = $1 and name_norm = $2)`,
		shopID, norm).Scan(&exists); err != nil {
		return c, err
	}
	if exists {
		return c, ErrCategoryExists
	}
	err := q.QueryRow(ctx, `
		insert into categories (shop_id, key, name, name_norm, kind) values ($1, null, $2, $3, 'custom')
		returning id, key, name, kind`, shopID, name, norm).Scan(&c.ID, &c.Key, &c.Name, &c.Kind)
	return c, err
}

// Resolve finds a category by id, or by name (creating a custom one when the name is new).
func Resolve(ctx context.Context, q db.Querier, shopID uuid.UUID, id *uuid.UUID, name string) (Category, error) {
	var c Category
	if err := Seed(ctx, q, shopID); err != nil {
		return c, err
	}
	if id != nil {
		err := q.QueryRow(ctx, `select id, key, name, kind from categories where id = $1 and shop_id = $2`,
			*id, shopID).Scan(&c.ID, &c.Key, &c.Name, &c.Kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return c, ErrUnknownCategory
		}
		return c, err
	}
	norm := names.NormalizeName(name)
	if norm == "" {
		return c, ErrUnknownCategory
	}
	err := q.QueryRow(ctx, `select id, key, name, kind from categories where shop_id = $1 and name_norm = $2`,
		shopID, norm).Scan(&c.ID, &c.Key, &c.Name, &c.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Create(ctx, q, shopID, name)
	}
	return c, err
}

// ByKey finds a standard category (or the custom "Other" one for key "other").
func ByKey(ctx context.Context, q db.Querier, shopID uuid.UUID, key string) (Category, error) {
	if key == "other" {
		return Resolve(ctx, q, shopID, nil, "Other")
	}
	if err := Seed(ctx, q, shopID); err != nil {
		return Category{}, err
	}
	var c Category
	err := q.QueryRow(ctx, `select id, key, name, kind from categories where shop_id = $1 and key = $2`,
		shopID, key).Scan(&c.ID, &c.Key, &c.Name, &c.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrUnknownCategory
	}
	return c, err
}

// AddToShop records that the shop sells a standard category (shops.categories).
func AddToShop(ctx context.Context, q db.Querier, shopID uuid.UUID, key string) error {
	_, err := q.Exec(ctx, `
		update shops set categories = array_append(categories, $2::text)
		where id = $1 and not ($2::text = any(categories))`, shopID, key)
	return err
}

// ItemCategoryValue is what goes into items.category (the legacy fixed list).
func ItemCategoryValue(c Category) string {
	if c.Key != nil {
		return *c.Key
	}
	return "other"
}

// BackfillItems links items created before categories existed (category_id / name_norm).
func BackfillItems(ctx context.Context, q db.Querier) (int, error) {
	rows, err := q.Query(ctx, `select id, shop_id, category, name from items where category_id is null or name_norm = ''`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id, shop uuid.UUID
		cat, nm  string
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.shop, &r.cat, &r.nm); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	for _, r := range todo {
		c, err := ByKey(ctx, q, r.shop, r.cat)
		if err != nil {
			return 0, err
		}
		if _, err := q.Exec(ctx, `update items set category_id = $2, name_norm = $3 where id = $1`,
			r.id, c.ID, names.NormalizeName(r.nm)); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}
