package otp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

// ValidFor is how long a verified phone can be used for a points redemption.
const ValidFor = 10 * time.Minute

// Provider sends and checks OTP codes.
// Replace DevProvider with a real SMS/WhatsApp provider when ready.
type Provider interface {
	Send(ctx context.Context, phone string) error
	Check(ctx context.Context, phone, code string) (bool, error)
}

// DevProvider is a stub: it sends nothing and accepts any code.
type DevProvider struct{}

func (DevProvider) Send(context.Context, string) error { return nil }

func (DevProvider) Check(_ context.Context, _, code string) (bool, error) {
	return strings.TrimSpace(code) != "", nil
}

type Handler struct {
	DB       *pgxpool.Pool
	Provider Provider
	DevMode  bool // reported to the app so it can show "OTP not enforced"
}

type phoneIn struct {
	Phone string `json:"phone"`
}

type verifyIn struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

// POST /v1/points/otp/send
func (h Handler) Send(w http.ResponseWriter, r *http.Request) {
	var in phoneIn
	if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Phone) == "" {
		httpx.Error(w, http.StatusBadRequest, "phone is required")
		return
	}
	if err := h.Provider.Send(r.Context(), strings.TrimSpace(in.Phone)); err != nil {
		httpx.Error(w, http.StatusBadGateway, "could not send OTP")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sent": true, "dev_mode": h.DevMode})
}

// POST /v1/points/otp/verify
func (h Handler) Verify(w http.ResponseWriter, r *http.Request) {
	var in verifyIn
	phone := ""
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	phone = strings.TrimSpace(in.Phone)
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
	m := auth.MemberOf(r.Context())
	expires := time.Now().Add(ValidFor)
	if _, err := h.DB.Exec(r.Context(), `
		insert into phone_verifications (shop_id, phone, verified_by, expires_at)
		values ($1, $2, $3, $4)`, m.ShopID, phone, auth.UserID(r.Context()), expires); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "database error")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"verified": true, "valid_until": expires, "dev_mode": h.DevMode})
}

// Consume marks one unused, unexpired verification for this phone as used by the bill.
// It returns false when the phone has not been verified recently.
func Consume(ctx context.Context, tx pgx.Tx, shopID any, phone string, billID any) (bool, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		update phone_verifications set used_at = now(), bill_id = $3
		where id = (
			select id from phone_verifications
			where shop_id = $1 and phone = $2 and used_at is null and expires_at > now()
			order by id desc limit 1 for update)
		returning id`, shopID, phone, billID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
