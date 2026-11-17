package profile

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
)

type offerOut struct {
	Enabled        bool    `json:"enabled"`
	Type           string  `json:"type"`
	DiscountAmount int     `json:"discount_amount"`
	MinBill        int     `json:"min_bill"`
	Product        *string `json:"product"`
	Multiplier     int     `json:"multiplier"`
}

// loadOffer creates the shop's offer row with the defaults the first time it is read.
func loadOffer(ctx context.Context, q db.Querier, shopID uuid.UUID) (offerOut, error) {
	if _, err := q.Exec(ctx, `insert into offers (shop_id) values ($1) on conflict do nothing`, shopID); err != nil {
		return offerOut{}, err
	}
	var o offerOut
	err := q.QueryRow(ctx, `select enabled, type, discount_amount, min_bill, product, multiplier from offers where shop_id = $1`, shopID).
		Scan(&o.Enabled, &o.Type, &o.DiscountAmount, &o.MinBill, &o.Product, &o.Multiplier)
	return o, err
}

type offerIn struct {
	Enabled        *bool   `json:"enabled"`
	Type           string  `json:"type"`
	DiscountAmount *int    `json:"discount_amount"`
	MinBill        *int    `json:"min_bill"`
	Product        *string `json:"product"`
	Multiplier     *int    `json:"multiplier"`
}

// validate checks the offer. Fields of the other type are stored as given but not checked.
func (in offerIn) validate() (offerOut, error) {
	var o offerOut
	if in.Enabled == nil {
		return o, httpx.BadRequest("enabled is required")
	}
	o.Enabled, o.Type = *in.Enabled, strings.ToLower(strings.TrimSpace(in.Type))
	o.DiscountAmount, o.MinBill, o.Multiplier = 20, 500, 2
	if in.DiscountAmount != nil {
		o.DiscountAmount = *in.DiscountAmount
	}
	if in.MinBill != nil {
		o.MinBill = *in.MinBill
	}
	if in.Multiplier != nil {
		o.Multiplier = *in.Multiplier
	}
	if in.Product != nil {
		if p := strings.ToLower(strings.TrimSpace(*in.Product)); p != "" {
			o.Product = &p
		}
	}
	switch o.Type {
	case "discount":
		if o.DiscountAmount < 5 || o.DiscountAmount > 1000 || o.DiscountAmount%5 != 0 {
			return o, httpx.BadRequest("discount_amount must be between 5 and 1000, in steps of 5")
		}
		if o.MinBill < 50 || o.MinBill > 100000 || o.MinBill%50 != 0 {
			return o, httpx.BadRequest("min_bill must be between 50 and 100000, in steps of 50")
		}
	case "points":
		if o.Product == nil {
			return o, httpx.BadRequest("product is required for a points offer")
		}
		if len(*o.Product) > 80 {
			return o, httpx.BadRequest("product is too long")
		}
		if o.Multiplier != 2 && o.Multiplier != 3 && o.Multiplier != 5 {
			return o, httpx.BadRequest("multiplier must be 2, 3 or 5")
		}
	default:
		return o, httpx.BadRequest("type must be discount or points")
	}
	return o, nil
}

// OfferDiscount is the discount for a bill total: 0 unless a discount offer is on and the bill reaches min_bill.
func OfferDiscount(o offerOut, billTotal float64) int {
	if o.Enabled && o.Type == "discount" && billTotal >= float64(o.MinBill) {
		return o.DiscountAmount
	}
	return 0
}

// GET /v1/offer
func (h Handler) GetOffer(w http.ResponseWriter, r *http.Request) {
	o, err := loadOffer(r.Context(), h.DB, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

// PUT /v1/offer (owner) — full replace.
func (h Handler) PutOffer(w http.ResponseWriter, r *http.Request) {
	var in offerIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	o, err := in.validate()
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if _, err := h.DB.Exec(r.Context(), `
		insert into offers (shop_id, enabled, type, discount_amount, min_bill, product, multiplier)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (shop_id) do update set enabled = $2, type = $3, discount_amount = $4,
		  min_bill = $5, product = $6, multiplier = $7, updated_at = now()`,
		auth.MemberOf(r.Context()).ShopID, o.Enabled, o.Type, o.DiscountAmount, o.MinBill, o.Product, o.Multiplier); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

// GET /v1/offer/preview?bill_total=… — used by New bill.
func (h Handler) Preview(w http.ResponseWriter, r *http.Request) {
	total, err := strconv.ParseFloat(r.URL.Query().Get("bill_total"), 64)
	if err != nil || total < 0 {
		httpx.Error(w, http.StatusBadRequest, "bill_total must be a number")
		return
	}
	o, err := loadOffer(r.Context(), h.DB, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	d := OfferDiscount(o, total)
	httpx.JSON(w, http.StatusOK, map[string]any{"applies": d > 0, "discount": d, "type": o.Type, "multiplier": o.Multiplier, "product": o.Product})
}
