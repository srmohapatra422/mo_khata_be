package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestTokenHash(t *testing.T) {
	if TokenHash("a") != TokenHash("a") || TokenHash("a") == TokenHash("b") || len(TokenHash("a")) != 64 {
		t.Fatal("hash must be stable, distinct and hex sha256")
	}
}

// A logged-out token is rejected with 401, a different valid token still works.
func TestRevokedTokenRejected(t *testing.T) {
	v, err := NewVerifier(context.Background(), "https://x.supabase.co", "secret", "")
	if err != nil {
		t.Fatal(err)
	}
	mk := func() string {
		s, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": uuid.NewString(), "aud": "authenticated", "iss": "https://x.supabase.co/auth/v1",
			"exp": time.Now().Add(time.Hour).Unix(), "n": uuid.NewString(),
		}).SignedString([]byte("secret"))
		return s
	}
	dead, live := mk(), mk()
	v.revoked = func(_ context.Context, h string) (bool, error) { return h == TokenHash(dead), nil }
	h := v.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for tok, want := range map[string]int{dead: 401, live: 200} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("got %d want %d", rec.Code, want)
		}
	}
}
