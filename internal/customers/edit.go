package customers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

// idParam reads {id}. A malformed id can never belong to the shop, so it is a 404.
func idParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, httpx.NotFound("customer not found")
	}
	return id, nil
}

// optString is a JSON field that can be absent, null or a string.
type optString struct {
	Set   bool
	Value *string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

// CleanName trims a customer name: 1 to 80 characters.
func CleanName(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", httpx.BadRequest("name cannot be empty (send null to clear it)")
	}
	if utf8.RuneCountInString(s) > 80 {
		return "", httpx.BadRequest("name must be 80 characters or fewer")
	}
	return s, nil
}

type patchIn struct {
	Name       optString `json:"name"`
	WhatsApp   *bool     `json:"whatsapp_opted_in"`
	IsBusiness *bool     `json:"is_business"`
}

// PATCH /v1/customers/{id}  (owner, cashier; is_business owner only)
func (h Handler) Patch(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var in patchIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !in.Name.Set && in.WhatsApp == nil && in.IsBusiness == nil {
		httpx.Error(w, http.StatusBadRequest, "send name, whatsapp_opted_in or is_business")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	if in.IsBusiness != nil && m.Role != "owner" {
		httpx.Error(w, http.StatusForbidden, "only the owner can change is_business")
		return
	}
	var name *string
	if in.Name.Set && in.Name.Value != nil {
		n, err := CleanName(*in.Name.Value)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		name = &n
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var optedIn bool
	err = tx.QueryRow(ctx, `select whatsapp_opt_in from customers where id = $1 and shop_id = $2 for update`,
		id, m.ShopID).Scan(&optedIn)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "customer not found")
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if in.Name.Set {
		if _, err := tx.Exec(ctx, `update customers set name = $3 where id = $1 and shop_id = $2`, id, m.ShopID, name); err != nil {
			httpx.Fail(w, err)
			return
		}
	}
	if in.IsBusiness != nil {
		if _, err := tx.Exec(ctx, `update customers set is_business = $3 where id = $1 and shop_id = $2`,
			id, m.ShopID, *in.IsBusiness); err != nil {
			httpx.Fail(w, err)
			return
		}
	}
	// Remember who changed the WhatsApp consent, and when.
	if in.WhatsApp != nil && *in.WhatsApp != optedIn {
		if _, err := tx.Exec(ctx, `
			update customers set whatsapp_opt_in = $3, whatsapp_opt_in_at = now(), whatsapp_opt_in_by = $4
			where id = $1 and shop_id = $2`, id, m.ShopID, *in.WhatsApp, auth.UserID(ctx)); err != nil {
			httpx.Fail(w, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	d, err := h.detail(r, id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

type paymentIn struct {
	Amount     float64    `json:"amount"`
	Method     string     `json:"method"`
	Note       string     `json:"note"`
	LocationID *uuid.UUID `json:"location_id"`
}

// POST /v1/customers/{id}/payments  (owner, cashier). Records money collected against credit.
func (h Handler) Pay(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var in paymentIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	amount := round2(in.Amount)
	if amount <= 0 {
		httpx.Error(w, http.StatusBadRequest, "amount must be more than 0")
		return
	}
	switch in.Method {
	case "cash", "upi", "card":
	default:
		httpx.Error(w, http.StatusBadRequest, "method must be cash, upi or card")
		return
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > 200 {
		httpx.Error(w, http.StatusBadRequest, "note must be 200 characters or fewer")
		return
	}
	var noteCol *string
	if note != "" {
		noteCol = &note
	}

	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)

	var one int
	if err := tx.QueryRow(ctx, `select 1 from customers where id = $1 and shop_id = $2`, id, m.ShopID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "customer not found")
		return
	} else if err != nil {
		httpx.Fail(w, err)
		return
	}

	// The counter the money came in at: the one given, else the shop location, else any location.
	// Close-day counts credit payments per location.
	var loc uuid.UUID
	if in.LocationID != nil {
		err = tx.QueryRow(ctx, `select id from locations where id = $1 and shop_id = $2`, *in.LocationID, m.ShopID).Scan(&loc)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "unknown location")
			return
		}
	} else {
		err = tx.QueryRow(ctx, `select id from locations where shop_id = $1
			order by (type = 'shop') desc, created_at limit 1`, m.ShopID).Scan(&loc)
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	// One payment at a time per customer, so two taps cannot both pass the "more than the due" check.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "credit:"+id.String()); err != nil {
		httpx.Fail(w, err)
		return
	}
	var billed, paid float64
	if err := tx.QueryRow(ctx, `
		select coalesce((select sum(payable) from bills where shop_id = $1 and customer_id = $2 and payment_mode = 'credit'), 0)::float8,
		       coalesce((select sum(amount) from credit_payments where shop_id = $1 and customer_id = $2), 0)::float8`,
		m.ShopID, id).Scan(&billed, &paid); err != nil {
		httpx.Fail(w, err)
		return
	}
	due := Due(billed, paid)
	if amount > due {
		httpx.Error(w, http.StatusConflict, "amount is more than the due")
		return
	}
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into credit_payments (shop_id, customer_id, location_id, amount, method, note, created_by)
		values ($1, $2, $3, $4, $5, $6, $7) returning id`,
		m.ShopID, id, loc, amount, in.Method, noteCol, auth.UserID(ctx)).Scan(&pid); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": pid, "due_after": round2(due - amount)})
}
