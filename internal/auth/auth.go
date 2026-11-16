package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/httpx"
)

type ctxKey int

const (
	userKey ctxKey = iota
	memberKey
)

type Member struct {
	ShopID uuid.UUID
	Role   string // owner | cashier | farm_manager
	TZ     string // the shop's IANA timezone; empty means Asia/Kolkata
}

// Location is the shop's timezone. "Today", "this week" and so on always use it, never UTC.
func (m Member) Location() *time.Location {
	if m.TZ != "" {
		if loc, err := time.LoadLocation(m.TZ); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation("Asia/Kolkata"); err == nil {
		return loc
	}
	return time.FixedZone("IST", 5*3600+1800)
}

type Verifier struct {
	keyfunc jwt.Keyfunc
	issuer  string
}

// NewVerifier checks Supabase access tokens.
// With a legacy secret it uses HS256; otherwise it fetches the project's public JWKS.
//
// devSecret, when not empty, also accepts HS256 tokens signed by the dev-only login
// (see internal/devauth). Leave it empty in production.
func NewVerifier(ctx context.Context, supabaseURL, secret, devSecret string) (*Verifier, error) {
	issuer := strings.TrimRight(supabaseURL, "/") + "/auth/v1"
	hmacKey := func(key string) jwt.Keyfunc {
		return func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
			}
			return []byte(key), nil
		}
	}
	var primary jwt.Keyfunc
	if secret != "" {
		primary = hmacKey(secret)
	} else {
		k, err := keyfunc.NewDefaultCtx(ctx, []string{issuer + "/.well-known/jwks.json"})
		if err != nil {
			return nil, err
		}
		primary = k.Keyfunc
	}
	if devSecret == "" {
		return &Verifier{issuer: issuer, keyfunc: primary}, nil
	}
	dev := hmacKey(devSecret)
	return &Verifier{issuer: issuer, keyfunc: func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); ok && secret == "" {
			return dev(t) // HS256 is never used by Supabase JWKS projects, so it is a dev token
		}
		if k, err := primary(t); err == nil {
			return k, nil
		}
		return dev(t)
	}}, nil
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
			var role, tz string
			err = pool.QueryRow(r.Context(),
				`select m.role, s.timezone from members m join shops s on s.id = m.shop_id
				 where m.shop_id = $1 and m.user_id = $2`,
				shopID, UserID(r.Context())).Scan(&role, &tz)
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusForbidden, "not a member of this shop")
				return
			}
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
			ctx := context.WithValue(r.Context(), memberKey, Member{ShopID: shopID, Role: role, TZ: tz})
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

// RequireRoles lets only the listed roles through (use after RequireShop).
func RequireRoles(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := MemberOf(r.Context()).Role
			for _, allowed := range roles {
				if role == allowed {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.Error(w, http.StatusForbidden, "your role cannot do this")
		})
	}
}

// WithUser and WithMember build a request context without a token (used by tests).
func WithUser(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userKey, id)
}

func WithMember(ctx context.Context, m Member) context.Context {
	return context.WithValue(ctx, memberKey, m)
}

func UserID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(userKey).(uuid.UUID)
	return id
}

func MemberOf(ctx context.Context) Member {
	m, _ := ctx.Value(memberKey).(Member)
	return m
}
