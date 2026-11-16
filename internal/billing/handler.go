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

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/otp"
	"mohisab-api/internal/stock"
)

type Handler struct{ DB *pgxpool.Pool }

type lineIn struct {
	ItemID      *uuid.UUID `json:"item_id"` // null for manual entry
	Name        string     `json:"name"`
	Qty         float64    `json:"qty"`
	Rate        float64    `json:"rate"`
	PriceEdited bool       `json:"price_edited"`
	Note        string     `json:"note"`
	Unit        string     `json:"unit"` // optional: kg, bird, tray, piece. Used to match stock
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
			// The customer's phone must have been verified (see /v1/points/otp/verify).
			verified, err := otp.Consume(ctx, tx, m.ShopID, phone, in.ID)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
			if !verified {
				httpx.Error(w, http.StatusForbidden, "customer phone not verified: send and verify OTP first")
				return
			}
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
		// The unit comes from the price item when there is one, otherwise from the line.
		unit := strings.TrimSpace(l.Unit)
		if l.ItemID != nil {
			var itemUnit string
			err := tx.QueryRow(ctx, `select unit from items where id = $1 and shop_id = $2`, *l.ItemID, m.ShopID).Scan(&itemUnit)
			if err == nil {
				unit = itemUnit
			} else if !errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusInternalServerError, "database error")
				return
			}
		}
		var unitCol *string
		if unit != "" {
			unitCol = &unit
		}
		if _, err := tx.Exec(ctx, `
			insert into bill_lines (bill_id, item_id, name, qty, rate, amount, price_edited, note, unit)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			in.ID, l.ItemID, l.Name, l.Qty, l.Rate, round2(l.Qty*l.Rate), l.PriceEdited, note, unitCol); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid line: "+l.Name)
			return
		}
		// Take the sold quantity off the matching stock item, if there is one.
		if err := stock.RecordSale(ctx, tx, m.ShopID, in.LocationID, in.ID, userID, l.Name, stock.UnitFromItemUnit(unit), l.Qty); err != nil {
			httpx.Fail(w, err)
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
