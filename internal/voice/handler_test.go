package voice

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func TestParseInValidate(t *testing.T) {
	tests := []struct {
		name string
		in   parseIn
		want string
	}{
		{"valid", parseIn{Transcript: "30 kilo rohu", Mode: "stock_in", Language: "hi"}, ""},
		{"language defaults to en", parseIn{Transcript: "x", Mode: "sale"}, ""},
		{"expense", parseIn{Transcript: "x", Mode: "expense", Language: "or"}, ""},
		{"no transcript", parseIn{Transcript: "   ", Mode: "sale"}, "transcript is required"},
		{"transcript too long", parseIn{Transcript: strings.Repeat("a", 1001), Mode: "sale"}, "transcript is too long (1000 characters at most)"},
		{"no mode", parseIn{Transcript: "x"}, "mode must be stock_in, sale or expense"},
		{"bad mode", parseIn{Transcript: "x", Mode: "purchase"}, "mode must be stock_in, sale or expense"},
		{"bad language", parseIn{Transcript: "x", Mode: "sale", Language: "fr"}, "language must be en, hi or or"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.validate(); got != tt.want {
				t.Errorf("validate() = %q, want %q", got, tt.want)
			}
		})
	}
	in := parseIn{Transcript: "x", Mode: "sale"}
	in.validate()
	if in.Language != "en" {
		t.Errorf("language = %q, want en", in.Language)
	}
}

func TestHandler400(t *testing.T) {
	h := Handler{Parser: RuleParser{}}
	tests := []struct{ name, body, errContains string }{
		{"bad json", `{`, "invalid body"},
		{"unknown field", `{"transcript":"x","mode":"sale","x":1}`, "invalid body"},
		{"no transcript", `{"mode":"sale"}`, "transcript is required"},
		{"bad mode", `{"transcript":"x","mode":"nope"}`, "mode must be"},
		{"bad language", `{"transcript":"x","mode":"sale","language":"zz"}`, "language must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/voice/parse", strings.NewReader(tt.body))
			req = req.WithContext(auth.WithMember(req.Context(), auth.Member{ShopID: uuid.New(), Role: "cashier"}))
			rec := httptest.NewRecorder()
			h.Parse(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tt.errContains) {
				t.Errorf("status %d body %s, want 400 containing %q", rec.Code, rec.Body, tt.errContains)
			}
		})
	}
}

// Expense mode needs no database, so it can be tested end to end here.
func TestHandler_ExpenseNeedsNoCatalog(t *testing.T) {
	h := Handler{Parser: RuleParser{}}
	req := httptest.NewRequest("POST", "/v1/voice/parse",
		strings.NewReader(`{"transcript":"bijli ka bill 1800 rupaye","mode":"expense","language":"hi"}`))
	req = req.WithContext(auth.WithMember(req.Context(), auth.Member{ShopID: uuid.New(), Role: "owner"}))
	rec := httptest.NewRecorder()
	h.Parse(rec, req)
	want := `{"items":[{"name":"Bijli bill","heard":"bijli bill","amount":1800}]}`
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("status %d body %s, want 200 %s", rec.Code, rec.Body, want)
	}
}

func TestHandler_ExpenseNothingHeardIsAnEmptyList(t *testing.T) {
	h := Handler{Parser: RuleParser{}}
	req := httptest.NewRequest("POST", "/v1/voice/parse", strings.NewReader(`{"transcript":"hello","mode":"expense"}`))
	req = req.WithContext(auth.WithMember(req.Context(), auth.Member{ShopID: uuid.New(), Role: "owner"}))
	rec := httptest.NewRecorder()
	h.Parse(rec, req)
	if strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Errorf("body = %s, want an empty list (not null)", rec.Body)
	}
}
