package customers

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
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
