// Package idem makes POST routes safe to retry: the same Idempotency-Key in the same shop
// returns the first successful response instead of running the request again.
package idem

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mohisab-api/internal/auth"
	"mohisab-api/internal/httpx"
)

const maxKeyLen = 128

type Store struct {
	DB *pgxpool.Pool

	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

// lock serialises requests that carry the same key (within this server process), so two
// quick retries cannot both run.
func (s *Store) lock(k string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*keyLock{}
	}
	l := s.locks[k]
	if l == nil {
		l = &keyLock{}
		s.locks[k] = l
	}
	l.refs++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.locks, k)
		}
		s.mu.Unlock()
	}
}

// Middleware must run after auth.RequireShop. Requests without the header pass straight through.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > maxKeyLen {
			httpx.Error(w, http.StatusBadRequest, "Idempotency-Key is too long")
			return
		}
		shopID := auth.MemberOf(r.Context()).ShopID
		unlock := s.lock(shopID.String() + "|" + key)
		defer unlock()

		var status int
		var body, path string
		err := s.DB.QueryRow(r.Context(),
			`select status, response, path from idempotency_keys where shop_id = $1 and key = $2`,
			shopID, key).Scan(&status, &body, &path)
		switch {
		case err == nil:
			if path != r.URL.Path {
				httpx.Error(w, http.StatusConflict, "Idempotency-Key was already used for a different request")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replay", "true")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		case !errors.Is(err, pgx.ErrNoRows):
			httpx.Fail(w, err)
			return
		}

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Only successes are remembered, so a failed attempt can be retried with the same key.
		if rec.status >= 200 && rec.status < 300 {
			_, _ = s.DB.Exec(r.Context(), `
				insert into idempotency_keys (shop_id, key, method, path, status, response)
				values ($1, $2, $3, $4, $5, $6) on conflict do nothing`,
				shopID, key, r.Method, r.URL.Path, rec.status, rec.body.String())
		}
	})
}

type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.body.Len() < 1<<20 {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b)
}
