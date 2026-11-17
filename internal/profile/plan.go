package profile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/db"
	"mohisab-api/internal/httpx"
)

const (
	BackupBucket = "shop-backups"
	backupGap    = 5 * time.Minute
	signSeconds  = 15 * 60
)

type planOut struct {
	Plan             string     `json:"plan"`
	TrialDaysLeft    int        `json:"trial_days_left"`
	TrialEndsAt      *time.Time `json:"trial_ends_at"`
	CurrentPeriodEnd *time.Time `json:"current_period_end"`
	LastBackupAt     *time.Time `json:"last_backup_at"`
}

// loadPlan reads the subscription, starting the trial the first time (shops made before plans existed).
func (h Handler) loadPlan(ctx context.Context, q db.Querier, shopID uuid.UUID) (planOut, error) {
	if _, err := q.Exec(ctx, `
		insert into subscriptions (shop_id, plan, trial_ends_at) values ($1, 'trial', now() + make_interval(days => $2::int))
		on conflict do nothing`, shopID, h.trialDays()); err != nil {
		return planOut{}, err
	}
	var p planOut
	err := q.QueryRow(ctx, `
		select plan, trial_ends_at, current_period_end,
		       (select max(created_at) from backups where shop_id = $1 and status = 'done')
		from subscriptions where shop_id = $1`, shopID).Scan(&p.Plan, &p.TrialEndsAt, &p.CurrentPeriodEnd, &p.LastBackupAt)
	p.TrialDaysLeft = TrialDaysLeft(p.Plan, p.TrialEndsAt, time.Now())
	return p, err
}

// GET /v1/plan (owner)
func (h Handler) GetPlan(w http.ResponseWriter, r *http.Request) {
	p, err := h.loadPlan(r.Context(), h.DB, auth.MemberOf(r.Context()).ShopID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

// POST /v1/plan/upgrade (owner) — payments are not built yet.
func (h Handler) Upgrade(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, http.StatusNotImplemented, "payments not enabled")
}

// ---------- data collection ----------

// rowsCSV runs a query whose columns are all text and returns it as CSV with a header row.
func rowsCSV(ctx context.Context, q db.Querier, sql string, args ...any) ([]byte, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	var header []string
	for _, f := range rows.FieldDescriptions() {
		header = append(header, f.Name)
	}
	_ = cw.Write(header)
	for rows.Next() {
		vals := make([]*string, len(header))
		ptrs := make([]any, len(header))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := make([]string, len(vals))
		for i, v := range vals {
			if v != nil {
				rec[i] = csvSafe(*v)
			}
		}
		_ = cw.Write(rec)
	}
	cw.Flush()
	return buf.Bytes(), rows.Err()
}

// csvSafe stops a cell being read as a spreadsheet formula.
func csvSafe(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@') {
		if len(s) > 1 && s[0] == '-' && s[1] >= '0' && s[1] <= '9' {
			return s // a negative number
		}
		return "'" + s
	}
	return s
}

// The tables a backup / export holds. Everything is scoped to one shop ($1).
var dataSets = []struct{ file, sql string }{
	{"bills.csv", `select id::text, location_id::text, customer_id::text, total::text, points_used::text, points_earned::text,
	               payable::text, payment_mode, status, created_at::text from bills where shop_id = $1 order by created_at`},
	{"bill_lines.csv", `select l.bill_id::text, l.name, l.qty::text, l.unit, l.rate::text, l.amount::text
	               from bill_lines l join bills b on b.id = l.bill_id where b.shop_id = $1 order by b.created_at`},
	{"customers.csv", `select id::text, phone, name, whatsapp_opt_in::text, created_at::text from customers where shop_id = $1 order by created_at`},
	{"stock.csv", `select id::text, location_id::text, name, unit, quantity::text, reorder_level::text from stock_items where shop_id = $1 order by name`},
	{"prices.csv", `select i.name as item, i.category, i.unit, p.day::text, p.price::text
	               from item_prices p join items i on i.id = p.item_id where i.shop_id = $1 order by p.day, i.name`},
}

func zipShop(ctx context.Context, q db.Querier, shopID uuid.UUID) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, d := range dataSets {
		data, err := rowsCSV(ctx, q, d.sql, shopID)
		if err != nil {
			return nil, err
		}
		f, err := zw.Create(d.file)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------- backups ----------

// POST /v1/backups (owner) — 202; a background job writes the dump to private storage.
func (h Handler) CreateBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	shopID := auth.MemberOf(ctx).ShopID
	if h.Storage == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "backup storage is not configured")
		return
	}
	// One per 5 minutes per shop; the advisory lock keeps two parallel taps from both passing.
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1::text, 7))`, shopID); err != nil {
		httpx.Fail(w, err)
		return
	}
	var recent bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from backups where shop_id = $1 and created_at > now() - make_interval(secs => $2::int))`,
		shopID, int(backupGap.Seconds())).Scan(&recent); err != nil {
		httpx.Fail(w, err)
		return
	}
	if recent {
		w.Header().Set("Retry-After", "300")
		httpx.Error(w, http.StatusTooManyRequests, "a backup was started in the last 5 minutes")
		return
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `insert into backups (shop_id) values ($1) returning id`, shopID).Scan(&id); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Fail(w, err)
		return
	}
	go h.runJob("backup", id, shopID)
	httpx.JSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "queued"})
}

// runJob builds the zip and stores it. It runs after the response, so it uses its own context.
func (h Handler) runJob(kind string, id, shopID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	table, okStatus := "backups", "done"
	if kind == "export" {
		table, okStatus = "data_exports", "ready"
	}
	fail := func(err error) {
		log.Printf("%s %s failed: %v", kind, id, err)
		_, _ = h.DB.Exec(context.Background(), `update `+table+` set status = 'failed' where id = $1`, id)
	}
	if err := h.Storage.EnsurePrivateBucket(ctx, BackupBucket); err != nil {
		fail(err)
		return
	}
	data, err := zipShop(ctx, h.DB, shopID)
	if err != nil {
		fail(err)
		return
	}
	key := shopID.String() + "/" + kind + "-" + id.String() + ".zip"
	if err := h.Storage.PutObject(ctx, BackupBucket, key, "application/zip", data); err != nil {
		fail(err)
		return
	}
	if _, err := h.DB.Exec(ctx, `update `+table+` set status = $2, file_url = $3 where id = $1`, id, okStatus, key); err != nil {
		fail(err)
	}
}

// GET /v1/backups/latest (owner) — the newest finished backup, or 404.
func (h Handler) LatestBackup(w http.ResponseWriter, r *http.Request) {
	var status string
	var at time.Time
	err := h.DB.QueryRow(r.Context(), `select status, created_at from backups where shop_id = $1 order by created_at desc limit 1`,
		auth.MemberOf(r.Context()).ShopID).Scan(&status, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "no backup yet")
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": status, "created_at": at})
}

// ---------- exports ----------

type exportIn struct {
	Format string `json:"format"`
}

// POST /v1/exports (owner) — 202.
func (h Handler) CreateExport(w http.ResponseWriter, r *http.Request) {
	var in exportIn
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Format != "csv" {
		httpx.Error(w, http.StatusBadRequest, "format must be csv")
		return
	}
	if h.Storage == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "export storage is not configured")
		return
	}
	shopID := auth.MemberOf(r.Context()).ShopID
	var id uuid.UUID
	if err := h.DB.QueryRow(r.Context(), `insert into data_exports (shop_id) values ($1) returning id`, shopID).Scan(&id); err != nil {
		httpx.Fail(w, err)
		return
	}
	go h.runJob("export", id, shopID)
	httpx.JSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "queued"})
}

// GET /v1/exports/{id} (owner) — `url` is a signed link valid for 15 minutes once ready.
func (h Handler) GetExport(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid export id")
		return
	}
	var status string
	var key *string
	err = h.DB.QueryRow(r.Context(), `select status, file_url from data_exports where id = $1 and shop_id = $2`,
		id, auth.MemberOf(r.Context()).ShopID).Scan(&status, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "export not found")
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	out := map[string]any{"id": id, "status": status}
	if status == "ready" && key != nil && h.Storage != nil {
		u, err := h.Storage.SignedURL(r.Context(), BackupBucket, *key, signSeconds)
		if err != nil {
			log.Printf("sign export: %v", err)
			httpx.Error(w, http.StatusBadGateway, "could not create the download link")
			return
		}
		out["url"] = u
		out["expires_in"] = signSeconds
	}
	httpx.JSON(w, http.StatusOK, out)
}
