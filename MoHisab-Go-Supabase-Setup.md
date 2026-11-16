# Mo Hisab API — Go + Supabase project setup

Backend for **Mo Hisab** (billing, stock and loyalty points for poultry, meat, fish and egg shops).
Stack: **Go 1.22+**, **chi** router, **pgx v5** (PostgreSQL driver), **goose** migrations, **Supabase** (Postgres + Auth + Storage).

> Rule for the whole project: the Flutter app uses Supabase **only to log in**. Every read and write of business data goes through this Go API. Business rules (bill totals, stock, points) live only here.

---

## 1. Supabase setup (once)

1. Create two projects on supabase.com: `mohisab-dev` and `mohisab-prod`. Region: **Mumbai (ap-south-1)**.
2. **Authentication → Providers → Phone**: enable phone login and connect an SMS provider (or plan WhatsApp OTP later).
3. **Project Settings → Database → Connection string**, copy two URLs:
   - **Direct / session pooler** (port `5432`) → used for migrations (`MIGRATE_DATABASE_URL`).
   - **Transaction pooler** (port `6543`) → used by the running API (`DATABASE_URL`).
4. **Project Settings → API**: copy the **Project URL** (`SUPABASE_URL`).
   - If your project uses the new asymmetric **JWT signing keys**, the API verifies tokens with the public JWKS automatically (nothing else to copy).
   - If it still uses the **legacy JWT secret**, copy it into `SUPABASE_JWT_SECRET`.
5. Never put the database URL, service key or JWT secret in the Flutter app.

---

## 2. Create the project

```bash
mkdir mohisab-api && cd mohisab-api
go mod init github.com/<your-github>/mohisab-api

go get github.com/go-chi/chi/v5
go get github.com/go-chi/chi/v5/middleware
go get github.com/jackc/pgx/v5
go get github.com/golang-jwt/jwt/v5
go get github.com/MicahParks/keyfunc/v3
go get github.com/google/uuid
go get github.com/joho/godotenv

go install github.com/pressly/goose/v3/cmd/goose@latest
```

### Folder structure

```text
mohisab-api/
├── cmd/
│   └── api/
│       └── main.go              # HTTP server entry point
├── internal/
│   ├── config/config.go         # env variables
│   ├── db/db.go                 # pgx pool
│   ├── httpx/httpx.go           # JSON helpers
│   ├── auth/auth.go             # Supabase JWT check + shop membership
│   ├── shops/handler.go         # /v1/me, /v1/shops
│   ├── catalogue/handler.go     # /v1/items, /v1/prices/today
│   ├── customers/handler.go     # /v1/customers
│   └── billing/handler.go       # /v1/bills (stock + points in one transaction)
├── migrations/
│   └── 00001_init.sql
├── .env.example
├── Dockerfile
└── Makefile
```

Later (see backend plan): `internal/points`, `internal/stock`, `internal/voice`, `internal/whatsapp`, `cmd/worker` with River jobs.

---

## 3. Environment

`.env.example` (copy to `.env`, never commit `.env`):

```dotenv
PORT=8080
# Transaction pooler (port 6543) for the API
DATABASE_URL=postgresql://postgres.<ref>:<password>@aws-0-ap-south-1.pooler.supabase.com:6543/postgres
# Session pooler / direct (port 5432) for migrations
MIGRATE_DATABASE_URL=postgresql://postgres.<ref>:<password>@aws-0-ap-south-1.pooler.supabase.com:5432/postgres
SUPABASE_URL=https://<ref>.supabase.co
# Only if your project still uses the legacy HS256 JWT secret; leave empty to use JWKS
SUPABASE_JWT_SECRET=
```

Copy the exact URLs from your Supabase dashboard; the host names above are examples.

---

## 4. Database migration

`migrations/00001_init.sql`

```sql
-- +goose Up
create table shops (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  points_per_rupees numeric not null default 10,   -- 1 point per ₹10
  point_value numeric not null default 0.25,       -- 1 point = ₹0.25
  points_expiry_months int default 12,             -- null = never
  max_points_pct int not null default 50,          -- max % of a bill paid by points
  language text not null default 'or',
  created_at timestamptz not null default now()
);

create table members (
  shop_id uuid not null references shops(id) on delete cascade,
  user_id uuid not null references auth.users(id) on delete cascade,
  role text not null check (role in ('owner','cashier','farm_manager')),
  created_at timestamptz not null default now(),
  primary key (shop_id, user_id)
);

create table locations (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  name text not null,
  type text not null check (type in ('farm','shop')),
  created_at timestamptz not null default now()
);

create table items (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  category text not null check (category in ('chicken','fish','mutton','eggs','other')),
  name text not null,                 -- 'Broiler · Boneless', 'Rohu', 'Mutton'
  unit text not null check (unit in ('kg','piece','tray','dozen','bird')),
  aliases text[] not null default '{}',
  active boolean not null default true,
  created_at timestamptz not null default now()
);

create table item_prices (
  item_id uuid not null references items(id) on delete cascade,
  day date not null,
  price numeric(10,2) not null check (price >= 0),
  updated_by uuid,
  updated_at timestamptz not null default now(),
  primary key (item_id, day)
);

create table customers (
  id uuid primary key default gen_random_uuid(),
  shop_id uuid not null references shops(id) on delete cascade,
  phone text not null,
  name text,
  whatsapp_opt_in boolean not null default false,
  created_at timestamptz not null default now(),
  unique (shop_id, phone)
);

create table bills (
  id uuid primary key,                -- generated on the phone (offline-safe)
  shop_id uuid not null references shops(id),
  location_id uuid not null references locations(id),
  customer_id uuid references customers(id),
  total numeric(12,2) not null,
  points_used int not null default 0,
  points_discount numeric(12,2) not null default 0,
  points_earned int not null default 0,
  payable numeric(12,2) not null,
  payment_mode text not null check (payment_mode in ('cash','upi','card','credit')),
  status text not null default 'saved',
  created_by uuid not null,
  created_at timestamptz not null,    -- time on the phone
  synced_at timestamptz not null default now()
);

create table bill_lines (
  id uuid primary key default gen_random_uuid(),
  bill_id uuid not null references bills(id) on delete cascade,
  item_id uuid references items(id),  -- null for manual entry
  name text not null,
  qty numeric(10,3) not null check (qty > 0),
  rate numeric(10,2) not null check (rate >= 0),
  amount numeric(12,2) not null,
  price_edited boolean not null default false,
  note text
);

create table stock_ledger (
  id bigserial primary key,
  shop_id uuid not null references shops(id),
  location_id uuid not null references locations(id),
  item_id uuid not null references items(id),
  qty numeric(10,3) not null,         -- + in, - out
  kind text not null check (kind in ('purchase','production','transfer','sale','wastage','adjust')),
  ref_id uuid,
  created_by uuid,
  created_at timestamptz not null default now()
);

create table points_ledger (
  id bigserial primary key,
  shop_id uuid not null references shops(id),
  customer_id uuid not null references customers(id),
  points int not null,                -- +48 earned, -400 redeemed
  kind text not null check (kind in ('earn','redeem','expire','reverse','bonus')),
  bill_id uuid references bills(id),
  expires_on date,
  created_at timestamptz not null default now()
);

create index bills_shop_created on bills (shop_id, created_at desc);
create index stock_shop_item on stock_ledger (shop_id, location_id, item_id);
create index points_customer on points_ledger (customer_id);
create index customers_shop_phone on customers (shop_id, phone);

-- Safety net: RLS on, no public policies. Only the API (database role) reads/writes.
alter table shops enable row level security;
alter table members enable row level security;
alter table locations enable row level security;
alter table items enable row level security;
alter table item_prices enable row level security;
alter table customers enable row level security;
alter table bills enable row level security;
alter table bill_lines enable row level security;
alter table stock_ledger enable row level security;
alter table points_ledger enable row level security;

-- +goose Down
drop table if exists points_ledger, stock_ledger, bill_lines, bills, customers,
  item_prices, items, locations, members, shops cascade;
```

Run it:

```bash
set -a; source .env; set +a
goose -dir migrations postgres "$MIGRATE_DATABASE_URL" up
```

> The API connects as the `postgres` role from the connection string, which bypasses RLS. RLS stays on so that the public `anon` key (used by the app for login) can never read these tables directly.

---

## 5. Code

### `internal/config/config.go`

```go
package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port              string
	DatabaseURL       string
	SupabaseURL       string
	SupabaseJWTSecret string
}

func Load() Config {
	_ = godotenv.Load() // reads .env in local dev; ignored in production

	c := Config{
		Port:              getenv("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		SupabaseURL:       os.Getenv("SUPABASE_URL"),
		SupabaseJWTSecret: os.Getenv("SUPABASE_JWT_SECRET"),
	}
	if c.DatabaseURL == "" || c.SupabaseURL == "" {
		log.Fatal("DATABASE_URL and SUPABASE_URL are required")
	}
	return c
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

### `internal/db/db.go`

```go
package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	// Supabase transaction pooler (port 6543) does not support prepared statements.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.MaxConns = 10
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return pool, nil
}
```

### `internal/httpx/httpx.go`

```go
package httpx

import (
	"encoding/json"
	"net/http"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
```

### `internal/auth/auth.go`

```go
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/<your-github>/mohisab-api/internal/httpx"
)

type ctxKey int

const (
	userKey ctxKey = iota
	memberKey
)

type Member struct {
	ShopID uuid.UUID
	Role   string // owner | cashier | farm_manager
}

type Verifier struct {
	keyfunc jwt.Keyfunc
	issuer  string
}

// NewVerifier checks Supabase access tokens.
// With a legacy secret it uses HS256; otherwise it fetches the project's public JWKS.
func NewVerifier(ctx context.Context, supabaseURL, secret string) (*Verifier, error) {
	issuer := strings.TrimRight(supabaseURL, "/") + "/auth/v1"
	if secret != "" {
		return &Verifier{
			issuer: issuer,
			keyfunc: func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
				}
				return []byte(secret), nil
			},
		}, nil
	}
	k, err := keyfunc.NewDefaultCtx(ctx, []string{issuer + "/.well-known/jwks.json"})
	if err != nil {
		return nil, err
	}
	return &Verifier{issuer: issuer, keyfunc: k.Keyfunc}, nil
}

// RequireUser verifies the Bearer token and puts the user id in the context.
func (v *Verifier) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			httpx.Error(w, http.StatusUnauthorized, "missing token")
			return
		}
		tok, err := jwt.Parse(raw, v.keyfunc,
			jwt.WithIssuer(v.issuer),
			jwt.WithAudience("authenticated"),
			jwt.WithExpirationRequired(),
		)
		if err != nil || !tok.Valid {
			httpx.Error(w, http.StatusUnauthorized, "invalid token")
			return
		}
		sub, err := tok.Claims.GetSubject()
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "invalid token")
			return
		}
		uid, err := uuid.Parse(sub)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, uid)))
	})
}

// RequireShop reads the X-Shop-ID header and checks the user is a member of that shop.
func RequireShop(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			shopID, err := uuid.Parse(r.Header.Get("X-Shop-ID"))
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, "X-Shop-ID header required")
				return
			}
			var role string
			err = pool.QueryRow(r.Context(),
				`select role from members where shop_id = $1 and user_id = $2`,
				shopID, UserID(r.Context())).Scan(&role)
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusForbidden, "not a member of this shop")
				return
			}
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
			ctx := context.WithValue(r.Context(), memberKey, Member{ShopID: shopID, Role: role})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireOwner blocks cashiers and farm managers (prices, points rules, deletes).
func RequireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if MemberOf(r.Context()).Role != "owner" {
			httpx.Error(w, http.StatusForbidden, "owner only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func UserID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(userKey).(uuid.UUID)
	return id
}

func MemberOf(ctx context.Context) Member {
	m, _ := ctx.Value(memberKey).(Member)
	return m
}
```

### `internal/shops/handler.go`

```go
package shops

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/<your-github>/mohisab-api/internal/auth"
	"github.com/<your-github>/mohisab-api/internal/httpx"
)

type Handler struct{ DB *pgxpool.Pool }

type shopOut struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role"`
}

// GET /v1/me — shops this user belongs to.
func (h Handler) Me(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `
		select s.id, s.name, m.role
		from members m join shops s on s.id = m.shop_id
		where m.user_id = $1 order by s.created_at`, auth.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()
	shops := []shopOut{}
	for rows.Next() {
		var s shopOut
		if err := rows.Scan(&s.ID, &s.Name, &s.Role); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		shops = append(shops, s)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user_id": auth.UserID(r.Context()), "shops": shops})
}

type createShopIn struct {
	Name      string `json:"name"`
	HasFarm   bool   `json:"has_farm"`
	ShopLabel string `json:"shop_label"` // e.g. "Main shop"
}

// POST /v1/shops — onboarding: creates the shop, makes the caller owner, adds locations.
func (h Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in createShopIn
	if err := httpx.Decode(r, &in); err != nil || in.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if in.ShopLabel == "" {
		in.ShopLabel = "Main shop"
	}
	ctx := r.Context()
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)

	var shopID uuid.UUID
	if err := tx.QueryRow(ctx, `insert into shops (name) values ($1) returning id`, in.Name).Scan(&shopID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	if _, err := tx.Exec(ctx, `insert into members (shop_id, user_id, role) values ($1, $2, 'owner')`,
		shopID, auth.UserID(ctx)); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	if _, err := tx.Exec(ctx, `insert into locations (shop_id, name, type) values ($1, $2, 'shop')`,
		shopID, in.ShopLabel); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	if in.HasFarm {
		if _, err := tx.Exec(ctx, `insert into locations (shop_id, name, type) values ($1, 'Poultry farm', 'farm')`,
			shopID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": shopID, "name": in.Name})
}
```

### `internal/catalogue/handler.go`

```go
package catalogue

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/<your-github>/mohisab-api/internal/auth"
	"github.com/<your-github>/mohisab-api/internal/httpx"
)

type Handler struct{ DB *pgxpool.Pool }

type itemOut struct {
	ID       uuid.UUID `json:"id"`
	Category string    `json:"category"`
	Name     string    `json:"name"`
	Unit     string    `json:"unit"`
	Price    *float64  `json:"price_today"` // null if not set today
}

// GET /v1/prices/today — active items with today's price (falls back to latest price).
func (h Handler) Today(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	rows, err := h.DB.Query(r.Context(), `
		select i.id, i.category, i.name, i.unit,
		       (select p.price::float8 from item_prices p
		         where p.item_id = i.id and p.day <= current_date
		         order by p.day desc limit 1)
		from items i
		where i.shop_id = $1 and i.active
		order by i.category, i.name`, m.ShopID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()
	out := []itemOut{}
	for rows.Next() {
		var it itemOut
		if err := rows.Scan(&it.ID, &it.Category, &it.Name, &it.Unit, &it.Price); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		out = append(out, it)
	}
	httpx.JSON(w, http.StatusOK, out)
}

type createItemIn struct {
	Category string   `json:"category"`
	Name     string   `json:"name"`
	Unit     string   `json:"unit"`
	Price    *float64 `json:"price"` // optional: sets today's price too
}

// POST /v1/items — "add item with price one by one" from Profile › Today's prices.
func (h Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
	var in createItemIn
	if err := httpx.Decode(r, &in); err != nil || in.Name == "" || in.Unit == "" || in.Category == "" {
		httpx.Error(w, http.StatusBadRequest, "category, name and unit are required")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)

	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into items (shop_id, category, name, unit) values ($1, $2, $3, $4) returning id`,
		m.ShopID, in.Category, in.Name, in.Unit).Scan(&id); err != nil {
		httpx.Error(w, http.StatusBadRequest, "could not create item")
		return
	}
	if in.Price != nil {
		if _, err := tx.Exec(ctx, `
			insert into item_prices (item_id, day, price, updated_by) values ($1, current_date, $2, $3)`,
			id, *in.Price, auth.UserID(ctx)); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id})
}

type priceIn struct {
	ItemID uuid.UUID `json:"item_id"`
	Price  float64   `json:"price"`
}

// PUT /v1/prices/today — owner sets today's prices (body: list of item_id + price).
func (h Handler) SetToday(w http.ResponseWriter, r *http.Request) {
	var in []priceIn
	if err := httpx.Decode(r, &in); err != nil || len(in) == 0 {
		httpx.Error(w, http.StatusBadRequest, "list of {item_id, price} required")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)
	for _, p := range in {
		// The join on items makes sure the item belongs to this shop.
		tag, err := tx.Exec(ctx, `
			insert into item_prices (item_id, day, price, updated_by)
			select i.id, current_date, $2, $3 from items i where i.id = $1 and i.shop_id = $4
			on conflict (item_id, day) do update
			  set price = excluded.price, updated_by = excluded.updated_by, updated_at = now()`,
			p.ItemID, p.Price, auth.UserID(ctx), m.ShopID)
		if err != nil || tag.RowsAffected() == 0 {
			httpx.Error(w, http.StatusBadRequest, "unknown item "+p.ItemID.String())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"updated": len(in)})
}
```

### `internal/customers/handler.go`

```go
package customers

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/<your-github>/mohisab-api/internal/auth"
	"github.com/<your-github>/mohisab-api/internal/httpx"
)

type Handler struct{ DB *pgxpool.Pool }

type customerOut struct {
	ID     uuid.UUID `json:"id"`
	Phone  string    `json:"phone"`
	Name   *string   `json:"name"`
	Points int       `json:"points"`
}

// GET /v1/customers?q=ravi  (name or phone search, with points balance)
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	m := auth.MemberOf(r.Context())
	q := "%" + r.URL.Query().Get("q") + "%"
	rows, err := h.DB.Query(r.Context(), `
		select c.id, c.phone, c.name,
		       coalesce((select sum(points) from points_ledger p where p.customer_id = c.id), 0)::int
		from customers c
		where c.shop_id = $1 and (c.phone ilike $2 or coalesce(c.name, '') ilike $2)
		order by c.created_at desc
		limit 50`, m.ShopID, q)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()
	out := []customerOut{}
	for rows.Next() {
		var c customerOut
		if err := rows.Scan(&c.ID, &c.Phone, &c.Name, &c.Points); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		out = append(out, c)
	}
	httpx.JSON(w, http.StatusOK, out)
}
```

### `internal/billing/handler.go` — the important one

One transaction: bill + lines + stock out + points earned/redeemed. Safe to retry with the same bill id.

```go
package billing

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/<your-github>/mohisab-api/internal/auth"
	"github.com/<your-github>/mohisab-api/internal/httpx"
)

type Handler struct{ DB *pgxpool.Pool }

type lineIn struct {
	ItemID      *uuid.UUID `json:"item_id"` // null for manual entry
	Name        string     `json:"name"`
	Qty         float64    `json:"qty"`
	Rate        float64    `json:"rate"`
	PriceEdited bool       `json:"price_edited"`
	Note        string     `json:"note"`
}

type billIn struct {
	ID            uuid.UUID `json:"id"` // made on the phone (UUID v7)
	LocationID    uuid.UUID `json:"location_id"`
	CustomerPhone string    `json:"customer_phone"` // empty = walk-in
	PaymentMode   string    `json:"payment_mode"`   // cash | upi | card | credit
	PointsUsed    int       `json:"points_used"`
	CreatedAt     time.Time `json:"created_at"`
	Lines         []lineIn  `json:"lines"`
}

type billOut struct {
	ID             uuid.UUID `json:"id"`
	Total          float64   `json:"total"`
	PointsDiscount float64   `json:"points_discount"`
	Payable        float64   `json:"payable"`
	PointsEarned   int       `json:"points_earned"`
	PointsBalance  *int      `json:"points_balance"`
	AlreadySaved   bool      `json:"already_saved"`
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// POST /v1/bills
func (h Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in billIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.ID == uuid.Nil || in.LocationID == uuid.Nil || len(in.Lines) == 0 {
		httpx.Error(w, http.StatusBadRequest, "id, location_id and at least one line are required")
		return
	}
	switch in.PaymentMode {
	case "cash", "upi", "card", "credit":
	default:
		httpx.Error(w, http.StatusBadRequest, "payment_mode must be cash, upi, card or credit")
		return
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}

	ctx := r.Context()
	m := auth.MemberOf(ctx)
	userID := auth.UserID(ctx)

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)

	// 1. Idempotency: same bill id sent again after a network drop → return the saved bill.
	var existing billOut
	err = tx.QueryRow(ctx, `
		select id, total::float8, points_discount::float8, payable::float8, points_earned
		from bills where id = $1 and shop_id = $2`, in.ID, m.ShopID).
		Scan(&existing.ID, &existing.Total, &existing.PointsDiscount, &existing.Payable, &existing.PointsEarned)
	if err == nil {
		existing.AlreadySaved = true
		httpx.JSON(w, http.StatusOK, existing)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}

	// 2. Location must belong to this shop.
	var ok bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from locations where id = $1 and shop_id = $2)`,
		in.LocationID, m.ShopID).Scan(&ok); err != nil || !ok {
		httpx.Error(w, http.StatusBadRequest, "unknown location")
		return
	}

	// 3. Shop points rules.
	var perRupees, pointValue float64
	var maxPct int
	var expiryMonths *int
	if err := tx.QueryRow(ctx, `
		select points_per_rupees::float8, point_value::float8, max_points_pct, points_expiry_months
		from shops where id = $1`, m.ShopID).Scan(&perRupees, &pointValue, &maxPct, &expiryMonths); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}

	// 4. Totals are always recalculated on the server.
	total := 0.0
	for _, l := range in.Lines {
		if l.Qty <= 0 || l.Rate < 0 || strings.TrimSpace(l.Name) == "" {
			httpx.Error(w, http.StatusBadRequest, "each line needs name, qty > 0 and rate >= 0")
			return
		}
		total += round2(l.Qty * l.Rate)
	}
	total = round2(total)

	// 5. Customer (created on first bill) and points.
	var customerID *uuid.UUID
	var balance *int
	discount := 0.0
	earned := 0
	phone := strings.TrimSpace(in.CustomerPhone)
	if phone != "" {
		var cid uuid.UUID
		if err := tx.QueryRow(ctx, `
			insert into customers (shop_id, phone) values ($1, $2)
			on conflict (shop_id, phone) do update set phone = excluded.phone
			returning id`, m.ShopID, phone).Scan(&cid); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		customerID = &cid

		// Lock this customer's points while we check the balance.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, cid.String()); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}
		var bal int
		if err := tx.QueryRow(ctx, `select coalesce(sum(points), 0)::int from points_ledger where customer_id = $1`,
			cid).Scan(&bal); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "database error")
			return
		}

		if in.PointsUsed > 0 {
			// TODO: verify the redemption OTP before allowing this (see /v1/points/redeem-otp).
			if in.PointsUsed > bal {
				httpx.Error(w, http.StatusConflict, "not enough points")
				return
			}
			discount = round2(float64(in.PointsUsed) * pointValue)
			if discount > total*float64(maxPct)/100 {
				httpx.Error(w, http.StatusConflict, "points exceed the allowed share of this bill")
				return
			}
			bal -= in.PointsUsed
		}
		if perRupees > 0 {
			earned = int(math.Floor((total - discount) / perRupees))
		}
		bal += earned
		balance = &bal
	} else if in.PointsUsed > 0 {
		httpx.Error(w, http.StatusBadRequest, "walk-in bills cannot use points")
		return
	}
	payable := round2(total - discount)

	// 6. Bill header.
	if _, err := tx.Exec(ctx, `
		insert into bills (id, shop_id, location_id, customer_id, total, points_used, points_discount,
		                   points_earned, payable, payment_mode, created_by, created_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		in.ID, m.ShopID, in.LocationID, customerID, total, in.PointsUsed, discount,
		earned, payable, in.PaymentMode, userID, in.CreatedAt); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}

	// 7. Lines + stock out.
	for _, l := range in.Lines {
		var note *string
		if n := strings.TrimSpace(l.Note); n != "" {
			note = &n
		}
		if _, err := tx.Exec(ctx, `
			insert into bill_lines (bill_id, item_id, name, qty, rate, amount, price_edited, note)
			values ($1,$2,$3,$4,$5,$6,$7,$8)`,
			in.ID, l.ItemID, l.Name, l.Qty, l.Rate, round2(l.Qty*l.Rate), l.PriceEdited, note); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid line: "+l.Name)
			return
		}
		if l.ItemID != nil {
			if _, err := tx.Exec(ctx, `
				insert into stock_ledger (shop_id, location_id, item_id, qty, kind, ref_id, created_by)
				select $1, $2, i.id, $4, 'sale', $5, $6 from items i where i.id = $3 and i.shop_id = $1`,
				m.ShopID, in.LocationID, *l.ItemID, -l.Qty, in.ID, userID); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
		}
	}

	// 8. Points ledger.
	if customerID != nil {
		if in.PointsUsed > 0 {
			if _, err := tx.Exec(ctx, `
				insert into points_ledger (shop_id, customer_id, points, kind, bill_id)
				values ($1, $2, $3, 'redeem', $4)`, m.ShopID, *customerID, -in.PointsUsed, in.ID); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
		}
		if earned > 0 {
			var expires *time.Time
			if expiryMonths != nil {
				t := in.CreatedAt.AddDate(0, *expiryMonths, 0)
				expires = &t
			}
			if _, err := tx.Exec(ctx, `
				insert into points_ledger (shop_id, customer_id, points, kind, bill_id, expires_on)
				values ($1, $2, $3, 'earn', $4, $5::date)`, m.ShopID, *customerID, earned, in.ID, expires); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}

	// TODO: queue the WhatsApp bill message (River job) after commit.
	httpx.JSON(w, http.StatusCreated, billOut{
		ID: in.ID, Total: total, PointsDiscount: discount, Payable: payable,
		PointsEarned: earned, PointsBalance: balance,
	})
}

```

### `cmd/api/main.go`

```go
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/<your-github>/mohisab-api/internal/auth"
	"github.com/<your-github>/mohisab-api/internal/billing"
	"github.com/<your-github>/mohisab-api/internal/catalogue"
	"github.com/<your-github>/mohisab-api/internal/config"
	"github.com/<your-github>/mohisab-api/internal/customers"
	"github.com/<your-github>/mohisab-api/internal/db"
	"github.com/<your-github>/mohisab-api/internal/httpx"
	"github.com/<your-github>/mohisab-api/internal/shops"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	verifier, err := auth.NewVerifier(ctx, cfg.SupabaseURL, cfg.SupabaseJWTSecret)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	shopH := shops.Handler{DB: pool}
	catH := catalogue.Handler{DB: pool}
	custH := customers.Handler{DB: pool}
	billH := billing.Handler{DB: pool}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(verifier.RequireUser)

		r.Get("/me", shopH.Me)
		r.Post("/shops", shopH.Create)

		// Everything below needs X-Shop-ID and membership.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireShop(pool))

			r.Get("/prices/today", catH.Today)
			r.Get("/customers", custH.List)
			r.Post("/bills", billH.Create)

			// Owner-only settings.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireOwner)
				r.Post("/items", catH.CreateItem)
				r.Put("/prices/today", catH.SetToday)
			})
		})
	})

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("Mo Hisab API listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
```

---

## 6. Run, build, deploy

`Makefile`

```makefile
include .env
export

run:
	go run ./cmd/api

migrate-up:
	goose -dir migrations postgres "$(MIGRATE_DATABASE_URL)" up

migrate-new:
	goose -dir migrations create $(name) sql

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -o bin/api ./cmd/api
```

`Dockerfile`

```dockerfile
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/api /api
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
```

Deploy the image to **Google Cloud Run (asia-south1, Mumbai)**, Fly.io, Railway or a small server in Mumbai. Put `DATABASE_URL`, `SUPABASE_URL` and `SUPABASE_JWT_SECRET` (if used) in the host's secret settings, not in the image.

---

## 7. Try it

1. Log in from the Flutter app with Supabase phone OTP, then copy the access token (`supabase.auth.currentSession!.accessToken`).
2. Call the API:

```bash
TOKEN=<access token>

# Who am I + my shops
curl -s localhost:8080/v1/me -H "Authorization: Bearer $TOKEN"

# Create my shop (onboarding)
curl -s -X POST localhost:8080/v1/shops -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"My Shop","has_farm":true}'

SHOP=<shop id from the response>

# Add an item with today's price
curl -s -X POST localhost:8080/v1/items -H "Authorization: Bearer $TOKEN" -H "X-Shop-ID: $SHOP" \
  -H "Content-Type: application/json" \
  -d '{"category":"chicken","name":"Broiler · Boneless","unit":"kg","price":320}'

# Save a bill (run it twice: the second call returns already_saved=true)
curl -s -X POST localhost:8080/v1/bills -H "Authorization: Bearer $TOKEN" -H "X-Shop-ID: $SHOP" \
  -H "Content-Type: application/json" \
  -d '{
    "id":"0193a1e2-7c4b-7d11-9a2e-5b8f3c1d2e4f",
    "location_id":"<shop location id>",
    "customer_phone":"9848012345",
    "payment_mode":"upi",
    "points_used":0,
    "created_at":"2026-11-13T12:30:00+05:30",
    "lines":[
      {"item_id":"<item id>","name":"Broiler · Boneless","qty":1.5,"rate":320},
      {"name":"Mutton","qty":0.5,"rate":750,"note":"curry pieces"}
    ]
  }'
```

### Flutter call (Dio example)

```dart
final token = Supabase.instance.client.auth.currentSession?.accessToken;
final res = await dio.post(
  '$apiBase/v1/bills',
  data: bill.toJson(),               // id generated on the phone (uuid v7)
  options: Options(headers: {
    'Authorization': 'Bearer $token',
    'X-Shop-ID': currentShopId,
  }),
);
```

---

## 8. Next steps (in this order)

1. `GET /v1/locations`, `POST /v1/locations`, `PATCH /v1/shops/{id}` (points rules, language) for the Profile screens.
2. Redemption OTP: `POST /v1/points/redeem-otp`, then require a valid code in `POST /v1/bills` when `points_used` is above the owner's limit.
3. Stock endpoints: purchases, transfers farm → shop, wastage; stock balance = `sum(qty)` from `stock_ledger`.
4. `cmd/worker` with **River** (Postgres job queue): WhatsApp bill message, points expiry (nightly `expire` rows), nightly owner summary.
5. `POST /v1/voice/parse`: speech-to-text + LLM with the shop's items and aliases → rows for the confirm popup.
6. Sync: `GET /v1/sync/pull?since=` for prices, items and customers changed after a time.
7. Tests: one integration test per endpoint against the `mohisab-dev` Supabase project, including a cross-shop access test that must return 403.
