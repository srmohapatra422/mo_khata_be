package profile

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
)

var phoneRe = regexp.MustCompile(`^[0-9]{10}$`)

type staffOut struct {
	ID     uuid.UUID  `json:"id"`
	UserID *uuid.UUID `json:"user_id"`
	Name   *string    `json:"name"`
	Phone  string     `json:"phone"`
	Role   string     `json:"role"`
	Status string     `json:"status"`
}

// last10 reduces "+919876543210" or "09876543210" to the 10 digits we match on.
func last10(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 10 {
		s = s[len(s)-10:]
	}
	return s
}

// AttachInvites turns pending invites for this phone into memberships. Call it when a user logs in.
func AttachInvites(ctx context.Context, q db.Querier, userID uuid.UUID, phone string) error {
	p := last10(phone)
	if len(p) != 10 {
		return nil
	}
	_, err := q.Exec(ctx, `
		with moved as (
		  delete from staff_invites where phone = $2 returning shop_id, role, name, invited_by
		)
		insert into members (shop_id, user_id, role, name, invited_by)
		select shop_id, $1, role, name, invited_by from moved
		on conflict (shop_id, user_id) do update set status = 'active', role = excluded.role
		  where members.status = 'removed'`, userID, p)
	return err
}

// GET /v1/staff (owner, cashier) — owners first, then active, then invited.
func (h Handler) ListStaff(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(r.Context(), `
		select * from (
		  select m.user_id as id, m.user_id, coalesce(m.name, p.name) as name, coalesce(a.phone, '') as phone,
		         m.role, 'active' as status, m.created_at
		  from members m join auth.users a on a.id = m.user_id left join profiles p on p.user_id = m.user_id
		  where m.shop_id = $1 and m.status = 'active'
		  union all
		  select id, null::uuid, name, phone, role, 'invited', created_at from staff_invites where shop_id = $1
		) t order by (role = 'owner') desc, (status = 'active') desc, created_at`, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer rows.Close()
	out := []staffOut{}
	for rows.Next() {
		var s staffOut
		var created any
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Phone, &s.Role, &s.Status, &created); err != nil {
			httpx.Fail(w, err)
			return
		}
		out = append(out, s)
	}
	httpx.JSON(w, http.StatusOK, out)
}

type inviteIn struct {
	Phone string `json:"phone"`
	Role  string `json:"role"`
	Name  string `json:"name"`
}

func (in *inviteIn) validate() error {
	in.Phone = last10(in.Phone)
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case !phoneRe.MatchString(in.Phone):
		return httpx.BadRequest("phone must be 10 digits")
	case in.Role == "owner":
		return httpx.BadRequest("an owner cannot be invited")
	case in.Role != "cashier" && in.Role != "farm_manager":
		return httpx.BadRequest("role must be cashier or farm_manager")
	case len([]rune(in.Name)) > 80:
		return httpx.BadRequest("name must be 80 characters at most")
	}
	return nil
}

// POST /v1/staff/invite (owner)
func (h Handler) Invite(w http.ResponseWriter, r *http.Request) {
	var in inviteIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := in.validate(); err != nil {
		httpx.Fail(w, err)
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var name *string
	if in.Name != "" {
		name = &in.Name
	}

	var uid uuid.UUID
	err = tx.QueryRow(ctx, `select id from auth.users where right(regexp_replace(coalesce(phone,''), '\D', '', 'g'), 10) = $1 limit 1`, in.Phone).Scan(&uid)
	out := staffOut{Phone: in.Phone, Role: in.Role, Name: name}
	switch {
	case err == nil:
		var status string
		e := tx.QueryRow(ctx, `select status from members where shop_id = $1 and user_id = $2`, m.ShopID, uid).Scan(&status)
		if e == nil && status == "active" {
			httpx.Error(w, http.StatusConflict, "already a member")
			return
		}
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			httpx.Fail(w, e)
			return
		}
		if _, err := tx.Exec(ctx, `
			insert into members (shop_id, user_id, role, name, invited_by) values ($1, $2, $3, $4, $5)
			on conflict (shop_id, user_id) do update set status = 'active', role = $3, name = coalesce($4, members.name)`,
			m.ShopID, uid, in.Role, name, auth.UserID(ctx)); err != nil {
			httpx.Fail(w, err)
			return
		}
		out.ID, out.UserID, out.Status = uid, &uid, "active"
	case errors.Is(err, pgx.ErrNoRows):
		err := tx.QueryRow(ctx, `
			insert into staff_invites (shop_id, phone, name, role, invited_by) values ($1, $2, $3, $4, $5)
			on conflict (shop_id, phone) do nothing returning id`, m.ShopID, in.Phone, name, in.Role, auth.UserID(ctx)).Scan(&out.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusConflict, "already a member")
			return
		}
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		out.Status = "invited"
	default:
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// LastOwnerBlocked reports whether changing a member would leave the shop without an owner.
func LastOwnerBlocked(targetRole string, activeOwners int, newRole string) bool {
	return targetRole == "owner" && activeOwners <= 1 && newRole != "owner"
}

type patchStaffIn struct {
	Role string `json:"role"`
}

// PATCH /v1/staff/{id} (owner) — change a role. {id} is the user_id (or the invite id).
func (h Handler) PatchStaff(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid staff id")
		return
	}
	var in patchStaffIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	if in.Role != "owner" && in.Role != "cashier" && in.Role != "farm_manager" {
		httpx.Error(w, http.StatusBadRequest, "role must be owner, cashier or farm_manager")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)

	var cur string
	err = tx.QueryRow(ctx, `select role from members where shop_id = $1 and user_id = $2 and status = 'active' for update`, m.ShopID, id).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.Role == "owner" {
			httpx.Error(w, http.StatusBadRequest, "an owner cannot be invited")
			return
		}
		tag, e := tx.Exec(ctx, `update staff_invites set role = $3 where id = $1 and shop_id = $2`, id, m.ShopID, in.Role)
		if e != nil {
			httpx.Fail(w, e)
			return
		}
		if tag.RowsAffected() == 0 {
			httpx.Error(w, http.StatusNotFound, "staff member not found")
			return
		}
		_ = tx.Commit(ctx)
		h.respondStaff(w, r, id)
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var owners int
	if err := tx.QueryRow(ctx, `select count(*) from members where shop_id = $1 and role = 'owner' and status = 'active'`, m.ShopID).Scan(&owners); err != nil {
		httpx.Fail(w, err)
		return
	}
	if LastOwnerBlocked(cur, owners, in.Role) {
		httpx.Error(w, http.StatusConflict, "the last owner cannot be changed")
		return
	}
	if _, err := tx.Exec(ctx, `update members set role = $3 where shop_id = $1 and user_id = $2`, m.ShopID, id, in.Role); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	h.respondStaff(w, r, id)
}

func (h Handler) respondStaff(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	var s staffOut
	err := h.DB.QueryRow(r.Context(), `
		select m.user_id, m.user_id, coalesce(m.name, p.name), coalesce(a.phone, ''), m.role, 'active'
		from members m join auth.users a on a.id = m.user_id left join profiles p on p.user_id = m.user_id
		where m.shop_id = $1 and m.user_id = $2`, auth.MemberOf(r.Context()).ShopID, id).
		Scan(&s.ID, &s.UserID, &s.Name, &s.Phone, &s.Role, &s.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = h.DB.QueryRow(r.Context(), `select id, null::uuid, name, phone, role, 'invited' from staff_invites where id = $1`, id).
			Scan(&s.ID, &s.UserID, &s.Name, &s.Phone, &s.Role, &s.Status)
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

// DELETE /v1/staff/{id} (owner) — removes access at once (membership is checked on every request).
func (h Handler) RemoveStaff(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid staff id")
		return
	}
	ctx := r.Context()
	m := auth.MemberOf(ctx)
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var cur string
	err = tx.QueryRow(ctx, `select role from members where shop_id = $1 and user_id = $2 and status = 'active' for update`, m.ShopID, id).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		tag, e := tx.Exec(ctx, `delete from staff_invites where id = $1 and shop_id = $2`, id, m.ShopID)
		if e != nil {
			httpx.Fail(w, e)
			return
		}
		if tag.RowsAffected() == 0 {
			httpx.Error(w, http.StatusNotFound, "staff member not found")
			return
		}
		_ = tx.Commit(ctx)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var owners int
	if err := tx.QueryRow(ctx, `select count(*) from members where shop_id = $1 and role = 'owner' and status = 'active'`, m.ShopID).Scan(&owners); err != nil {
		httpx.Fail(w, err)
		return
	}
	if LastOwnerBlocked(cur, owners, "") {
		httpx.Error(w, http.StatusConflict, "the last owner cannot be removed")
		return
	}
	if _, err := tx.Exec(ctx, `update members set status = 'removed' where shop_id = $1 and user_id = $2`, m.ShopID, id); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
