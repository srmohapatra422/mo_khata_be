// Package devauth is a DEV-ONLY phone login: any phone number and any non-empty code
// logs in. It is registered only when DEV_AUTH_SECRET is set. Replace it with Supabase
// phone OTP (or a real provider) before going live, and unset DEV_AUTH_SECRET.
package devauth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/httpx"
	"mohisab-api/internal/otp"
)

const tokenTTL = 30 * 24 * time.Hour

type Handler struct {
	DB       *pgxpool.Pool
	Provider otp.Provider
	Secret   string // DEV_AUTH_SECRET
	Issuer   string // <SUPABASE_URL>/auth/v1
}

type sendIn struct {
	Phone string `json:"phone"`
}

type verifyIn struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

// POST /auth/otp/send
func (h Handler) Send(w http.ResponseWriter, r *http.Request) {
	var in sendIn
	if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Phone) == "" {
		httpx.Error(w, http.StatusBadRequest, "phone is required")
		return
	}
	if err := h.Provider.Send(r.Context(), strings.TrimSpace(in.Phone)); err != nil {
		httpx.Error(w, http.StatusBadGateway, "could not send OTP")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sent": true, "dev_mode": true})
}

// POST /auth/otp/verify — returns an access token for the /v1 routes.
func (h Handler) Verify(w http.ResponseWriter, r *http.Request) {
	var in verifyIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	phone := strings.TrimSpace(in.Phone)
	if phone == "" || strings.TrimSpace(in.Code) == "" {
		httpx.Error(w, http.StatusBadRequest, "phone and code are required")
		return
	}
	ok, err := h.Provider.Check(r.Context(), phone, in.Code)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "could not check OTP")
		return
	}
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "wrong code")
		return
	}

	// Find or create the user row (members.user_id references auth.users).
	var uid uuid.UUID
	err = h.DB.QueryRow(r.Context(), `select id from auth.users where phone = $1`, phone).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		err = h.DB.QueryRow(r.Context(), `
			insert into auth.users (id, instance_id, aud, role, phone, phone_confirmed_at, created_at, updated_at)
			values (gen_random_uuid(), '00000000-0000-0000-0000-000000000000', 'authenticated', 'authenticated',
			        $1, now(), now(), now())
			on conflict (phone) do update set updated_at = now()
			returning id`, phone).Scan(&uid)
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}

	exp := time.Now().Add(tokenTTL)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   uid.String(),
		"aud":   "authenticated",
		"iss":   h.Issuer,
		"role":  "authenticated",
		"phone": phone,
		"iat":   time.Now().Unix(),
		"exp":   exp.Unix(),
	})
	signed, err := tok.SignedString([]byte(h.Secret))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not sign token")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"access_token": signed,
		"token_type":   "bearer",
		"expires_at":   exp,
		"user_id":      uid,
		"dev_mode":     true,
	})
}
