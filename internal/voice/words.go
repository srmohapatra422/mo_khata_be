package voice

import "strings"

// Words are matched after lower-casing. Hindi and Odia words are in Latin spelling as people
// say them, plus the Devanagari and Odia script for the common ones.

var numberWords = map[string]float64{
	// English
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
	"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
	"seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
	"sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90, "half": 0.5,
	// Hindi
	"ek": 1, "do": 2, "teen": 3, "char": 4, "paanch": 5, "panch": 5, "chhe": 6, "chhah": 6, "chah": 6, "che": 6,
	"saat": 7, "aath": 8, "nau": 9, "das": 10, "dus": 10, "gyarah": 11, "barah": 12, "terah": 13, "chaudah": 14,
	"pandrah": 15, "solah": 16, "satrah": 17, "atharah": 18, "unnis": 19, "bees": 20, "ikkis": 21, "bais": 22,
	"teis": 23, "chaubis": 24, "pachchis": 25, "tees": 30, "tis": 30, "chalis": 40, "pachas": 50, "saath": 60,
	"sattar": 70, "assi": 80, "nabbe": 90, "adha": 0.5, "aadha": 0.5, "dedh": 1.5, "dhai": 2.5, "sawa": 1.25,
	// Devanagari
	"एक": 1, "दो": 2, "तीन": 3, "चार": 4, "पाँच": 5, "पांच": 5, "छह": 6, "छः": 6, "सात": 7, "आठ": 8, "नौ": 9,
	"दस": 10, "बीस": 20, "तीस": 30, "चालीस": 40, "पचास": 50, "आधा": 0.5, "डेढ़": 1.5, "ढाई": 2.5,
	// Odia
	"ଏକ": 1, "ଦୁଇ": 2, "ତିନି": 3, "ଚାରି": 4, "ପାଞ୍ଚ": 5, "ଛଅ": 6, "ସାତ": 7, "ଆଠ": 8, "ନଅ": 9, "ଦଶ": 10,
}

var multiplierWords = map[string]float64{
	"sau": 100, "hundred": 100, "सौ": 100, "ଶହେ": 100,
	"hazaar": 1000, "hazar": 1000, "hajar": 1000, "thousand": 1000, "हज़ार": 1000, "हजार": 1000, "ହଜାର": 1000,
}

type unitInfo struct {
	unit   string  // kg, tray, piece, bird
	factor float64 // quantity multiplier (500 gram = 0.5 kg, 1 dozen = 12 pieces)
}

var unitWords = map[string]unitInfo{
	"kg": {"kg", 1}, "kgs": {"kg", 1}, "kilo": {"kg", 1}, "kilos": {"kg", 1}, "kilogram": {"kg", 1},
	"kilograms": {"kg", 1}, "kilogramme": {"kg", 1}, "kilu": {"kg", 1}, "किलो": {"kg", 1}, "केजी": {"kg", 1}, "କିଲୋ": {"kg", 1},
	"gram": {"kg", 0.001}, "grams": {"kg", 0.001}, "gm": {"kg", 0.001}, "gms": {"kg", 0.001}, "ग्राम": {"kg", 0.001},
	"tray": {"tray", 1}, "trays": {"tray", 1}, "ट्रे": {"tray", 1},
	"piece": {"piece", 1}, "pieces": {"piece", 1}, "pc": {"piece", 1}, "pcs": {"piece", 1}, "peice": {"piece", 1},
	"nag": {"piece", 1}, "नग": {"piece", 1}, "पीस": {"piece", 1},
	"dozen": {"piece", 12}, "dozens": {"piece", 12}, "darjan": {"piece", 12}, "दर्जन": {"piece", 12}, "ଡଜନ": {"piece", 12},
	"bird": {"bird", 1}, "birds": {"bird", 1},
}

var rupeeWords = set("rupaye", "rupaya", "rupya", "rupiya", "rupees", "rupee", "rs", "inr", "₹", "रुपये", "रुपए", "रुपया", "ଟଙ୍କା")

var rateWords = set("rate", "bhav", "bhaav", "daam", "dam", "price", "भाव", "दाम")

var breakWords = set("aur", "and", "tatha", "plus", "और", "तथा", "ଓ", "ଏବଂ")

var stopWords = set(
	"aaj", "kal", "abhi", "aaye", "aaya", "aayi", "aayein", "aa", "liya", "li", "liye", "bheja", "bheji", "becha",
	"bechi", "bika", "bik", "bech", "gaya", "gayi", "gaye", "diya", "diye", "mila", "mile", "hua", "hui",
	"hai", "hain", "tha", "the", "thi", "ka", "ki", "ke", "ko", "ne", "se", "me", "mein", "par", "pe", "bhi",
	"ye", "wo", "per", "stock", "add", "kharcha", "kharch", "kiya", "kar", "karo", "please", "a", "an", "is",
	"are", "was", "of", "today", "came", "arrived", "received", "sold", "bought", "got", "have", "had", "i",
	"we", "maine", "humne", "hamne", "wala", "wali", "ek_", "लगे", "लगी", "lagi", "lage",
	"आज", "आए", "आया", "आई", "बेचा", "मिला", "लिया", "का", "की", "के", "को", "में", "से", "है", "हैं",
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// digitMap turns Devanagari and Odia digits into ASCII digits.
func asciiDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '०' && r <= '९':
			b.WriteRune('0' + (r - '०'))
		case r >= '୦' && r <= '୯':
			b.WriteRune('0' + (r - '୦'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
