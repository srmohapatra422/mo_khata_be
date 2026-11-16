package voice

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func fp(v float64) *float64 { return &v }

var catalog = []CatalogItem{
	{Name: "Rohu fish", Unit: "kg", Price: fp(140)},
	{Name: "Katla", Unit: "kg", Price: fp(170)},
	{Name: "Eggs", Unit: "tray", Price: fp(6)},
	{Name: "Brown eggs", Unit: "tray", Price: fp(7)},
	{Name: "Broiler chicken", Unit: "kg", Price: fp(180)},
	{Name: "Mutton", Unit: "kg", Price: fp(700)},
	{Name: "Milk", Unit: "piece", Price: fp(30)},
}

// row is "name|heard|matched|quantity|unit|rate" so a whole result reads as one line.
func row(it Item) string {
	unit, rate := "-", "-"
	if it.Unit != nil {
		unit = *it.Unit
	}
	if it.Rate != nil {
		rate = fmt.Sprint(*it.Rate)
	}
	return fmt.Sprintf("%s|%s|%v|%v|%s|%s", it.Name, it.Heard, it.Matched, it.Quantity, unit, rate)
}

func parse(t *testing.T, mode, text string, cat []CatalogItem) Output {
	t.Helper()
	out, err := RuleParser{}.Parse(context.Background(), Input{Transcript: text, Mode: mode, Language: "en", Catalog: cat})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParse_Items(t *testing.T) {
	tests := []struct {
		name, mode, text string
		catalog          []CatalogItem
		want             []string
	}{
		{"the example sentence", ModeStockIn,
			"Aaj 30 kilo rohu aur 10 tray ande aaye, rohu 140 rupaye kilo", catalog,
			[]string{"Rohu fish|rohu|true|30|kg|140", "Eggs|ande|true|10|tray|-"}},
		{"plain kg and item", ModeStockIn, "50 kg broiler", catalog, []string{"Broiler chicken|broiler|true|50|kg|-"}},
		{"name first, then quantity, then rate", ModeStockIn, "broiler 50 kilo 150 rupaye", catalog,
			[]string{"Broiler chicken|broiler|true|50|kg|150"}},
		{"dozen becomes pieces", ModeStockIn, "do dozen anda", catalog, []string{"Eggs|anda|true|24|piece|-"}},
		{"dhai kilo = 2.5", ModeStockIn, "dhai kilo mutton", catalog, []string{"Mutton|mutton|true|2.5|kg|-"}},
		{"adha kilo = 0.5", ModeStockIn, "adha kilo mutton", catalog, []string{"Mutton|mutton|true|0.5|kg|-"}},
		{"dedh kilo = 1.5", ModeStockIn, "dedh kilo mutton", catalog, []string{"Mutton|mutton|true|1.5|kg|-"}},
		{"grams become kg", ModeStockIn, "500 gram chicken", catalog, []string{"Broiler chicken|chicken|true|0.5|kg|-"}},
		{"murga is chicken", ModeStockIn, "teen sau kilo murga", catalog, []string{"Broiler chicken|murga|true|300|kg|-"}},
		{"machhi is fish", ModeStockIn, "10 kilo machhi", []CatalogItem{{Name: "Fish", Unit: "kg"}}, []string{"Fish|machhi|true|10|kg|-"}},
		{"bakra is mutton", ModeStockIn, "5 kilo bakra", catalog, []string{"Mutton|bakra|true|5|kg|-"}},
		{"Devanagari digits and words", ModeStockIn, "३० किलो रोहू", catalog, []string{"रोहू|रोहू|false|30|kg|-"}},
		{"Devanagari egg", ModeStockIn, "दस ट्रे अंडे", catalog, []string{"Eggs|अंडे|true|10|tray|-"}},
		{"Odia words", ModeStockIn, "ଦଶ କିଲୋ ମାଛ", []CatalogItem{{Name: "Fish", Unit: "kg"}}, []string{"Fish|ମାଛ|true|10|kg|-"}},
		{"unknown item keeps what was heard", ModeStockIn, "5 kilo hilsa", catalog, []string{"Hilsa|hilsa|false|5|kg|-"}},
		{"typo still matches", ModeStockIn, "10 kilo broilar", catalog, []string{"Broiler chicken|broilar|true|10|kg|-"}},
		{"unit taken from the item when not said", ModeStockIn, "10 ande", catalog, []string{"Eggs|ande|true|10|tray|-"}},
		{"unknown item and no unit", ModeStockIn, "10 xyz", catalog, []string{"Xyz|xyz|false|10|-|-"}},
		{"decimal quantity", ModeStockIn, "12.5 kilo broiler", catalog, []string{"Broiler chicken|broiler|true|12.5|kg|-"}},
		{"glued number and unit", ModeStockIn, "30kg rohu", catalog, []string{"Rohu fish|rohu|true|30|kg|-"}},
		{"english number words", ModeStockIn, "twenty five kilo broiler", catalog, []string{"Broiler chicken|broiler|true|25|kg|-"}},
		{"upper case and extra spaces", ModeStockIn, "  AAJ   30   KILO   ROHU  ", catalog, []string{"Rohu fish|ROHU|true|30|kg|-"}},
		{"stop words are not part of the name", ModeStockIn, "aaj 30 kilo rohu aaye hain", catalog, []string{"Rohu fish|rohu|true|30|kg|-"}},

		// rate formats
		{"rate word", ModeStockIn, "rohu 30 kilo rate 140", catalog, []string{"Rohu fish|rohu|true|30|kg|140"}},
		{"rupee sign", ModeStockIn, "rohu 30 kilo ₹140", catalog, []string{"Rohu fish|rohu|true|30|kg|140"}},
		{"rs", ModeStockIn, "rohu 30 kilo rs 140", catalog, []string{"Rohu fish|rohu|true|30|kg|140"}},
		{"rupees per kg", ModeStockIn, "30 kilo rohu 140 rupees per kg", catalog, []string{"Rohu fish|rohu|true|30|kg|140"}},
		{"rs with a dot", ModeStockIn, "rohu 30 kilo rs.140", catalog, []string{"Rohu fish|rohu|true|30|kg|140"}},
		{"number word rate", ModeStockIn, "30 kilo rohu ek sau chalis rupaye", catalog, []string{"Rohu fish|rohu|true|30|kg|140"}},
		{"the unit after rupaye is not a quantity", ModeStockIn, "30 kilo rohu 140 rupaye kilo", catalog, []string{"Rohu fish|rohu|true|30|kg|140"}},

		// several items
		{"two items, quantity first, no 'aur'", ModeStockIn, "30 kilo rohu 20 kilo katla", catalog,
			[]string{"Rohu fish|rohu|true|30|kg|-", "Katla|katla|true|20|kg|-"}},
		{"two items, name first", ModeStockIn, "rohu 30 kilo katla 20 kilo", catalog,
			[]string{"Rohu fish|rohu|true|30|kg|-", "Katla|katla|true|20|kg|-"}},
		{"two items with their own rates", ModeStockIn, "rohu 30 kilo 140 rupaye katla 20 kilo 170 rupaye", catalog,
			[]string{"Rohu fish|rohu|true|30|kg|140", "Katla|katla|true|20|kg|170"}},
		{"rates said afterwards for each item", ModeStockIn,
			"rohu 30 kilo aur katla 20 kilo, rohu 140 rupaye, katla 120 rupaye", catalog,
			[]string{"Rohu fish|rohu|true|30|kg|140", "Katla|katla|true|20|kg|120"}},
		{"same item twice stays two rows", ModeStockIn, "rohu 30 kilo aur rohu 10 kilo", catalog,
			[]string{"Rohu fish|rohu|true|30|kg|-", "Rohu fish|rohu|true|10|kg|-"}},
		{"a rate for an item that was not bought is ignored", ModeStockIn, "30 kilo rohu, katla 120 rupaye", catalog,
			[]string{"Rohu fish|rohu|true|30|kg|-"}},

		// nothing to save
		{"no numbers", ModeStockIn, "aaj kuch nahi aaya", catalog, nil},
		{"hello", ModeStockIn, "hello", catalog, nil},
		{"empty", ModeStockIn, "", catalog, nil},
		{"quantity without a name", ModeStockIn, "30 kilo", catalog, nil},
		{"zero quantity", ModeStockIn, "0 kilo rohu", catalog, nil},
		{"only punctuation", ModeStockIn, "., ;!", catalog, nil},

		// sale mode fills the shop's price when none was said
		{"sale: shop price filled", ModeSale, "2 kilo broiler", catalog, []string{"Broiler chicken|broiler|true|2|kg|180"}},
		{"sale: spoken rate wins", ModeSale, "2 kilo broiler 200 rupaye", catalog, []string{"Broiler chicken|broiler|true|2|kg|200"}},
		{"sale: unknown item has no price", ModeSale, "1 kilo hilsa", catalog, []string{"Hilsa|hilsa|false|1|kg|-"}},
		{"sale: two items", ModeSale, "2 kilo broiler aur 1 tray ande", catalog,
			[]string{"Broiler chicken|broiler|true|2|kg|180", "Eggs|ande|true|1|tray|6"}},
		{"stock in does not fill the price", ModeStockIn, "2 kilo broiler", catalog, []string{"Broiler chicken|broiler|true|2|kg|-"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := parse(t, tt.mode, tt.text, tt.catalog)
			var got []string
			for _, it := range out.Items {
				got = append(got, row(it))
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("transcript %q\n got:\n  %s\nwant:\n  %s", tt.text, strings.Join(got, "\n  "), strings.Join(tt.want, "\n  "))
			}
		})
	}
}

func TestParse_Expenses(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string // "name|heard|amount"
	}{
		{"electricity", "bijli ka bill 1800 rupaye", []string{"Bijli bill|bijli bill|1800"}},
		{"amount first", "1800 rupaye bijli", []string{"Bijli|bijli|1800"}},
		{"two expenses", "petrol 500 aur chai 40", []string{"Petrol|petrol|500", "Chai|chai|40"}},
		{"commas in the amount", "electricity 1,800 rupees", []string{"Electricity|electricity|1800"}},
		{"number words", "do sau pachas rupaye chai", []string{"Chai|chai|250"}},
		{"ek hazaar", "kiraya ek hazaar", []string{"Kiraya|kiraya|1000"}},
		{"no words", "kharcha 300", []string{"Expense||300"}},
		{"rupee sign", "₹450 transport", []string{"Transport|transport|450"}},
		{"no amount", "bijli ka bill aaya", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := parse(t, ModeExpense, tt.text, nil)
			var got []string
			for _, e := range out.Expenses {
				got = append(got, fmt.Sprintf("%s|%s|%v", e.Name, e.Heard, e.Amount))
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("transcript %q\n got:  %v\n want: %v", tt.text, got, tt.want)
			}
			if len(out.Items) != 0 {
				t.Errorf("expense mode returned stock rows: %v", out.Items)
			}
		})
	}
}

func TestMergeNumbers(t *testing.T) {
	tests := []struct {
		text string
		want []float64
	}{
		{"ek hazaar do sau pachas", []float64{1250}},
		{"teen sau", []float64{300}},
		{"dhai sau", []float64{250}},
		{"twenty five", []float64{25}},
		{"2 sau", []float64{200}},
		{"2 hazaar 500", []float64{2500}},
		{"sau", []float64{100}},
		{"pachas", []float64{50}},
		{"10 20", []float64{10, 20}},      // two separate numbers
		{"teen char", []float64{3, 4}},    // not 7
		{"30 kilo 20", []float64{30, 20}}, // the unit separates them
		{"ek sau pachas", []float64{150}},
		{"one hundred twenty", []float64{120}},
		{"1,800", []float64{1800}},
		{"12.5", []float64{12.5}},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			var got []float64
			for _, tk := range tokenize(tt.text) {
				if tk.kind == kNum {
					got = append(got, tk.val)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("numbers in %q = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestTokenize_Splitting(t *testing.T) {
	tests := []struct{ text, want string }{
		{"30kg rohu", "num unit word"},
		{"rs.140", "rupee num"},
		{"₹140", "rupee num"},
		{"140/-", "num"},
		{"30 kilo, rohu", "num unit break word"},
		{"rohu. katla", "word break word"},
		{"rohu 12.5", "word num"},
		{"aur", "break"},
	}
	names := map[kind]string{kWord: "word", kNum: "num", kMult: "mult", kUnit: "unit", kRupee: "rupee", kRate: "rate", kBreak: "break", kStop: "stop"}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			var got []string
			for _, tk := range tokenize(tt.text) {
				got = append(got, names[tk.kind])
			}
			if strings.Join(got, " ") != tt.want {
				t.Errorf("tokens of %q = %v, want %s", tt.text, got, tt.want)
			}
		})
	}
}

func TestParse_DoesNotMutateTheCatalog(t *testing.T) {
	cat := []CatalogItem{{Name: "Rohu fish", Unit: "kg", Price: fp(140)}}
	_ = parse(t, ModeSale, "2 kilo rohu", cat)
	if cat[0].Name != "Rohu fish" || *cat[0].Price != 140 {
		t.Errorf("catalog changed: %+v", cat[0])
	}
}

func TestMatchCatalog_PrefersTheClosestName(t *testing.T) {
	cat := []CatalogItem{
		{Name: "Country chicken", Unit: "kg"}, {Name: "Chicken", Unit: "kg"}, {Name: "Broiler chicken curry cut", Unit: "kg"},
	}
	out := parse(t, ModeStockIn, "10 kilo chicken", cat)
	if len(out.Items) != 1 || out.Items[0].Name != "Chicken" {
		t.Errorf("got %+v, want the exact name 'Chicken'", out.Items)
	}
	// no exact name: the shortest item containing the word wins, then alphabetical
	out = parse(t, ModeStockIn, "10 kilo murga", cat[:1])
	if len(out.Items) != 1 || out.Items[0].Name != "Country chicken" {
		t.Errorf("got %+v", out.Items)
	}
}
