package names

import "testing"

func TestNormalizeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Broiler Chicken!", "broiler chicken"},
		{"  Katla   Fish ", "katla fish"},
		{"Eggs", "egg"},
		{"Brown eggs", "brown egg"},
		{"Mutton-Curry cut", "mutton curry cut"},
		{"anda", "egg"},
		{"Ande", "egg"},
		{"अंडे", "egg"},
		{"अंडा", "egg"},
		{"ଅଣ୍ଡା", "egg"},
		{"Murga", "chicken"},
		{"मुर्गा", "chicken"},
		{"Machhi", "fish"},
		{"मछली", "fish"},
		{"ମାଛ", "fish"},
		{"Bakra", "mutton"},
		{"Goat", "mutton"},
		{"ଛେଳି", "mutton"},
		{"Doodh", "milk"},
		{"दूध", "milk"},
		{"gas", "gas"}, // 3 letters: the trailing s stays
		{"", ""},
		{"  !! ", ""},
	}
	for _, tt := range tests {
		if got := NormalizeName(tt.in); got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeName_SameForVariants(t *testing.T) {
	groups := [][]string{
		{"Egg", "eggs", "EGGS", "Anda", "ande"},
		{"Fish", "machli", "Machhi", "FISH"},
		{"Broiler", "broiler ", "BROILER."},
	}
	for _, g := range groups {
		want := NormalizeName(g[0])
		for _, s := range g[1:] {
			if got := NormalizeName(s); got != want {
				t.Errorf("NormalizeName(%q) = %q, want same as %q (%q)", s, got, g[0], want)
			}
		}
	}
}

func TestDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"abc", "abc", 0}, {"", "abc", 3}, {"abc", "", 3},
		{"kitten", "sitting", 3}, {"broiler", "broilar", 1}, {"fish", "fist", 1},
		{"ମାଛ", "ମାଛି", 1}, {"flaw", "lawn", 2},
	}
	for _, tt := range tests {
		if got := Distance(tt.a, tt.b); got != tt.want {
			t.Errorf("Distance(%q,%q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestAllowedTypos(t *testing.T) {
	tests := []struct{ n, want int }{{0, 0}, {1, 0}, {3, 0}, {4, 1}, {7, 1}, {8, 2}, {20, 2}}
	for _, tt := range tests {
		if got := AllowedTypos(tt.n); got != tt.want {
			t.Errorf("AllowedTypos(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

func TestNear(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"broiler", "broilar", true},
		{"broiler", "broiler", true},
		{"katla", "katle", true},
		{"rohu", "roha", true},
		{"fish", "fist", true},
		{"cat", "bat", false}, // short words allow no typo
		{"egg", "eg", false},
		{"fish", "fresh", false},
		{"chicken curry cut", "chiken curry cut", true}, // long names allow two typos
		{"chicken curry cut", "chickn curr cut", true},
		{"chicken curry cut", "mutton curry cut", false},
	}
	for _, tt := range tests {
		if got := Near(tt.a, tt.b); got != tt.want {
			t.Errorf("Near(%q,%q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		name, query string
		want        bool
	}{
		{"broiler boneless", "", true},
		{"broiler boneless", "bone", true},
		{"broiler boneless", "broilar", true},
		{"broiler boneless", "boneles broiler", true},
		{"katla", "fish", false},
		{"rohu fish", "fish", true},
		{"rohu fish", "xyz", false},
	}
	for _, tt := range tests {
		if got := Matches(tt.name, tt.query); got != tt.want {
			t.Errorf("Matches(%q,%q) = %v, want %v", tt.name, tt.query, got, tt.want)
		}
	}
}
