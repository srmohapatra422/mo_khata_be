package lookup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"mohisab-api/internal/auth"
)

func c(name string) Cand { return Cand{ID: uuid.New(), Name: name} }

func suggestionNames(r Result) []string {
	out := []string{}
	for _, s := range r.Suggestions {
		out = append(out, s.Name)
	}
	return out
}

func TestRank(t *testing.T) {
	cands := []Cand{c("Broiler"), c("Broiler Boneless"), c("Rohu"), c("Katla"), c("Mutton"), c("Eggs"), c("Fish pickle")}
	tests := []struct {
		name        string
		query       string
		wantKind    string
		wantItem    string // "" = nil
		wantSuggest []string
	}{
		{"exact", "broiler", "exact", "Broiler", []string{"Broiler", "Broiler Boneless"}},
		{"exact ignores case and spaces", "  BROILER ", "exact", "Broiler", []string{"Broiler", "Broiler Boneless"}},
		{"exact by alias (anda = egg)", "anda", "exact", "Eggs", []string{"Eggs"}},
		{"exact by plural", "eggs", "exact", "Eggs", []string{"Eggs"}},
		{"near typo", "Broilar", "near", "Broiler", []string{"Broiler", "Broiler Boneless"}},
		{"near typo rohu", "roha", "near", "Rohu", []string{"Rohu"}},
		{"none but prefix suggestions", "bone", "none", "", []string{"Broiler Boneless"}},
		{"none, contains suggestion", "pickle", "none", "", []string{"Fish pickle"}},
		{"none, nothing close", "Hilsa", "none", "", []string{}},
		{"short words allow no typo", "Kat", "none", "", []string{"Katla"}},
		{"empty returns the first six", "", "none", "", []string{"Broiler", "Broiler Boneless", "Eggs", "Fish pickle", "Katla", "Mutton"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Rank(tt.query, cands)
			if got.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tt.wantKind)
			}
			switch {
			case tt.wantItem == "" && got.Item != nil:
				t.Errorf("item = %+v, want nil", got.Item)
			case tt.wantItem != "" && (got.Item == nil || got.Item.Name != tt.wantItem):
				t.Errorf("item = %+v, want %q", got.Item, tt.wantItem)
			}
			if s := strings.Join(suggestionNames(got), ","); s != strings.Join(tt.wantSuggest, ",") {
				t.Errorf("suggestions = %v, want %v", suggestionNames(got), tt.wantSuggest)
			}
		})
	}
}

func TestRank_AtMostFiveSuggestions(t *testing.T) {
	var cands []Cand
	for _, n := range []string{"Chicken A", "Chicken B", "Chicken C", "Chicken D", "Chicken E", "Chicken F", "Chicken G"} {
		cands = append(cands, c(n))
	}
	if got := Rank("chicken", cands); len(got.Suggestions) != 5 {
		t.Errorf("got %d suggestions, want 5", len(got.Suggestions))
	}
}

func TestRank_NoCandidates(t *testing.T) {
	got := Rank("anything", nil)
	if got.Kind != "none" || got.Item != nil || got.Suggestions == nil || len(got.Suggestions) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestRank_ExactWinsOverNear(t *testing.T) {
	cands := []Cand{c("Katle"), c("Katla")}
	got := Rank("katla", cands)
	if got.Kind != "exact" || got.Item.Name != "Katla" {
		t.Errorf("got %+v", got)
	}
}

func TestMatch_ScopeValidation(t *testing.T) {
	h := Handler{}
	for _, q := range []string{"", "?name=x", "?scope=bills", "?scope=price&category_id=nope"} {
		req := httptest.NewRequest("GET", "/v1/items/match"+q, nil)
		req = req.WithContext(auth.WithMember(req.Context(), auth.Member{ShopID: uuid.New(), Role: "owner"}))
		rec := httptest.NewRecorder()
		h.Match(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", q, rec.Code)
		}
	}
}
