package profile

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
	"mohisab-api/internal/outbox"
)

type waOut struct {
	Connected bool    `json:"connected"`
	Phone     *string `json:"phone"`
}

type hwOut struct {
	Connected bool    `json:"connected"`
	Name      *string `json:"name"`
}

type devicesOut struct {
	WhatsApp waOut `json:"whatsapp"`
	Printer  hwOut `json:"printer"`
	Scale    hwOut `json:"scale"`
}

func loadDevices(ctx context.Context, q db.Querier, shopID uuid.UUID) (devicesOut, error) {
	var out devicesOut
	rows, err := q.Query(ctx, `select device, connected, details from device_status where shop_id = $1`, shopID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var dev string
		var on bool
		var raw []byte
		if err := rows.Scan(&dev, &on, &raw); err != nil {
			return out, err
		}
		var d struct {
			Name  *string `json:"name"`
			Phone *string `json:"phone"`
		}
		_ = json.Unmarshal(raw, &d)
		switch dev {
		case "whatsapp":
			out.WhatsApp = waOut{on, d.Phone}
		case "printer":
			out.Printer = hwOut{on, d.Name}
		case "scale":
			out.Scale = hwOut{on, d.Name}
		}
	}
	return out, rows.Err()
}

// GET /v1/devices
func (h Handler) GetDevices(w http.ResponseWriter, r *http.Request) {
	d, err := loadDevices(r.Context(), h.DB, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

type deviceIn struct {
	Connected *bool   `json:"connected"`
	Name      *string `json:"name"`
	Phone     *string `json:"phone"`
}

// PUT /v1/devices/{device} (owner, cashier). The phone is the source of truth; we only remember it.
func (h Handler) PutDevice(w http.ResponseWriter, r *http.Request) {
	dev := strings.ToLower(chi.URLParam(r, "device"))
	if dev != "whatsapp" && dev != "printer" && dev != "scale" {
		httpx.Error(w, http.StatusBadRequest, "device must be whatsapp, printer or scale")
		return
	}
	var in deviceIn
	if err := httpx.Decode(r, &in); err != nil || in.Connected == nil {
		httpx.Error(w, http.StatusBadRequest, "connected is required")
		return
	}
	details := map[string]any{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if len([]rune(n)) > 80 {
			httpx.Error(w, http.StatusBadRequest, "name must be 80 characters at most")
			return
		}
		details["name"] = n
	}
	if in.Phone != nil {
		details["phone"] = last10(*in.Phone)
	}
	raw, _ := json.Marshal(details)
	ctx := r.Context()
	shopID := auth.MemberOf(ctx).ShopID
	if _, err := h.DB.Exec(ctx, `
		insert into device_status (shop_id, device, connected, details) values ($1, $2, $3, $4::jsonb)
		on conflict (shop_id, device) do update set connected = $3, details = $4::jsonb, updated_at = now()`,
		shopID, dev, *in.Connected, string(raw)); err != nil {
		httpx.Fail(w, err)
		return
	}
	d, err := loadDevices(ctx, h.DB, shopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// POST /v1/devices/whatsapp/test (owner) — queues a test message to the owner's phone.
func (h Handler) TestWhatsApp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := loadUser(ctx, h.DB, auth.UserID(ctx))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := outbox.Enqueue(ctx, h.DB, auth.MemberOf(ctx).ShopID, "whatsapp_test", map[string]string{"phone": last10(u.Phone)}); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"queued": true})
}
