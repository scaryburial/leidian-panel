package cardkey

import (
	"strings"
	"testing"
)

func TestGenerateProducesWellFormedCanonicalKeys(t *testing.T) {
	seen := make(map[string]struct{}, 512)
	for i := 0; i < 512; i++ {
		key := Generate()
		if len(key) != Length {
			t.Fatalf("Generate() = %q, length %d, want %d", key, len(key), Length)
		}
		if !IsWellFormed(key) {
			t.Fatalf("Generate() = %q, not well formed", key)
		}
		if strings.Contains(key, "-") || strings.HasPrefix(key, "LD") {
			t.Fatalf("Generate() = %q, want the canonical storage form (no prefix, no hyphens)", key)
		}
		for _, ch := range key {
			if !strings.ContainsRune(Alphabet, ch) {
				t.Fatalf("Generate() = %q, character %q outside the alphabet", key, ch)
			}
		}
		if _, dup := seen[key]; dup {
			t.Fatalf("Generate() repeated %q within 512 draws", key)
		}
		seen[key] = struct{}{}
	}
}

// An operator copies the display form, so Format must round-trip through
// Normalize and stay well formed.
func TestFormatRoundTrips(t *testing.T) {
	key := Generate()
	display := Format(key)
	if len(display) != len(Prefix)+Length+2 {
		t.Fatalf("Format(%q) = %q, want LD- + 12 chars + 2 hyphens", key, display)
	}
	if !strings.HasPrefix(display, Prefix) {
		t.Fatalf("Format(%q) = %q, want the LD- prefix", key, display)
	}
	if got := Normalize(display); got != key {
		t.Fatalf("Normalize(Format(key)) = %q, want %q", got, key)
	}
	if !IsWellFormed(display) {
		t.Fatalf("Format(%q) = %q, display form rejected", key, display)
	}
}

// Format must not silently drop characters it cannot interpret.
func TestFormatKeepsMalformedInput(t *testing.T) {
	for _, in := range []string{"", "ABC", strings.Repeat("Z", 20)} {
		if got := Format(in); got != Normalize(in) {
			t.Fatalf("Format(%q) = %q, want the normalized input %q", in, got, Normalize(in))
		}
	}
}

func TestNormalizeConfusablesAndCase(t *testing.T) {
	cases := map[string]string{
		// O -> 0, I/L -> 1; case, hyphens, underscores and whitespace ignored.
		"ld-oi23-4567-89ab":  "0123456789AB",
		"LD-OI23-4567-89AB":  "0123456789AB",
		"  oi23 4567 89ab  ": "0123456789AB",
		"ld_oi23_4567_89ab":  "0123456789AB",
		"OI23-4567-89AB":     "0123456789AB",
		"0123456789ab":       "0123456789AB",
		"LD-0123-4567-89AB":  "0123456789AB",
		"ld0123456789ab":     "0123456789AB",
		"0l23-4567-89AB":     "0123456789AB",
		// Length is preserved when no prefix applies: every I and L becomes 1.
		"ILLEGAL-KEY-12":    "111EGA1KEY12",
		"oi23-4567-89ab-zz": "0123456789ABZZ",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Only a genuine LD- prefix is stripped: the confusable mapping must not run
// first, or "LD-..." would become "1D-..." and lose its prefix.
func TestNormalizeStripsOnlyTheDisplayPrefix(t *testing.T) {
	if got := Normalize("LD-1D23-4567-89AB"); got != "1D23456789AB" {
		t.Fatalf("Normalize(with prefix) = %q, want %q", got, "1D23456789AB")
	}
	if got := Normalize("1D23-4567-89AB"); got != "1D23456789AB" {
		t.Fatalf("Normalize(short key) = %q, want %q", got, "1D23456789AB")
	}
	// A bare 12-character key never has its first two characters eaten.
	if got := Normalize("1D23456789AB"); got != "1D23456789AB" {
		t.Fatalf("Normalize(canonical) = %q, want %q", got, "1D23456789AB")
	}
}

func TestNormalizeEmptyAndGarbageInput(t *testing.T) {
	for _, in := range []string{"", "   ", "---", "__", "\t\n"} {
		got := Normalize(in)
		if got != "" {
			t.Errorf("Normalize(%q) = %q, want empty", in, got)
		}
		if IsWellFormed(in) {
			t.Errorf("IsWellFormed(%q) = true, want false", in)
		}
	}
	// A bare prefix is not empty after cleanup — L is a confusable of 1 — but
	// it is nowhere near a usable key.
	for _, in := range []string{"LD-", "LD", "-LD-"} {
		if got := Normalize(in); got != "1D" {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, "1D")
		}
		if IsWellFormed(in) {
			t.Errorf("IsWellFormed(%q) = true, want false", in)
		}
	}
	// Non-alphabet characters survive Normalize (it is a cleanup, not a
	// filter) and are rejected by the validator. U is not in Crockford's
	// alphabet and is deliberately not folded onto V.
	for _, in := range []string{"U123456789AB", "ABCD-EFGH-JKMU", "卡密0123456789", "0123456789A!"} {
		if IsWellFormed(in) {
			t.Errorf("IsWellFormed(%q) = true, want false", in)
		}
	}
}

func TestIsWellFormedRejectsWrongLength(t *testing.T) {
	key := Generate()
	for _, in := range []string{key[:Length-1], key + "Z", strings.Repeat("0", 200), "LD-" + key + "-EXTRA"} {
		if IsWellFormed(in) {
			t.Errorf("IsWellFormed(%q) = true, want false", in)
		}
	}
	if got := Normalize(strings.Repeat("0", 200)); len(got) != 200 {
		t.Errorf("Normalize of an over-long input dropped characters: len=%d", len(got))
	}
}

// The point of the checksum: any single mistyped character is caught before the
// database is consulted. Every position is exercised — the 11 data characters
// (with the original check character kept) and the check character itself.
func TestChecksumDetectsEverySingleCharacterTypo(t *testing.T) {
	key := Generate()
	data := []byte(key[:DataLen])
	check := key[DataLen]
	for pos := 0; pos < Length; pos++ {
		for _, repl := range []byte(Alphabet) {
			var mutated string
			if pos < DataLen {
				if repl == data[pos] {
					continue
				}
				mutated = string(data[:pos]) + string(repl) + string(data[pos+1:]) + string(check)
			} else {
				if repl == check {
					continue
				}
				mutated = string(data) + string(repl)
			}
			if IsWellFormed(mutated) {
				t.Fatalf("IsWellFormed accepted %q, a single-character mutation of %q", mutated, key)
			}
		}
	}
	if !IsWellFormed(key) {
		t.Fatalf("IsWellFormed rejected the unmodified key %q", key)
	}
}

// Adjacent transpositions of two characters whose 5-bit values differ by
// anything other than 16 are detected by the odd-weight design. (A difference
// of exactly 16 is the one pattern a single-character mod-32 check sum cannot
// see: 2 * 16 == 0 mod 32. It is documented in the package comment; the
// practical effect is "not found" instead of "malformed", never a wrong key.)
func TestChecksumDetectsAdjacentTransposition(t *testing.T) {
	// Values chosen 1 apart so the transposition is provably detectable, at
	// every adjacent pair of positions, with a valid check character each time.
	for pos := 0; pos+1 < DataLen; pos++ {
		data := []byte(Generate()[:DataLen])
		data[pos] = Alphabet[3]
		data[pos+1] = Alphabet[4]
		transposed := make([]byte, len(data))
		copy(transposed, data)
		transposed[pos], transposed[pos+1] = transposed[pos+1], transposed[pos]

		// Keep the original check character: a swapped pair whose check
		// character was recomputed would be a different valid key, not a typo.
		check := string(Alphabet[checkValue(data)])
		original := string(data) + check
		if !IsWellFormed(original) {
			t.Fatalf("setup key %q is not well formed", original)
		}
		mutated := string(transposed) + check
		if IsWellFormed(mutated) {
			t.Fatalf("IsWellFormed accepted transposed key %q (from %q)", mutated, original)
		}
	}
}

// A key whose last character is not the checksum of its data is rejected even
// though every character is in the alphabet.
func TestIsWellFormedRejectsWrongCheckCharacter(t *testing.T) {
	data := []byte(Generate()[:DataLen])
	correct := Alphabet[checkValue(data)]
	for _, repl := range []byte(Alphabet) {
		if repl == correct {
			continue
		}
		if IsWellFormed(string(data) + string(repl)) {
			t.Fatalf("IsWellFormed(%q) = true, want false (check character %q, want %q)", string(data)+string(repl), repl, correct)
		}
	}
	if !IsWellFormed(string(data) + string(correct)) {
		t.Fatalf("IsWellFormed rejected a correctly checksummed key %q", string(data)+string(correct))
	}
}

func TestParseReportsCanonicalFormForMalformedInput(t *testing.T) {
	canonical, ok := Parse("ld-oi23-4567-89ab")
	if ok {
		t.Fatalf("Parse accepted a key with an invalid checksum: %q", canonical)
	}
	if canonical != "0123456789AB" {
		t.Fatalf("Parse returned canonical %q, want the best-effort cleanup 0123456789AB", canonical)
	}
	if canonical, ok := Parse("ld-oi23-4567-89a"); canonical != "0123456789A" || ok {
		t.Fatalf("Parse(short) = %q, %v; want the cleanup plus false", canonical, ok)
	}
}

// Every weight is odd, which is what makes single-character errors detectable
// in a mod-32 group that has zero divisors.
func TestChecksumWeightsAreOdd(t *testing.T) {
	for i := 0; i < DataLen; i++ {
		if (2*i+1)%2 == 0 {
			t.Fatalf("weight %d for position %d is even", 2*i+1, i)
		}
	}
}

// Parse must accept every equivalent spelling of one key, which is what lets
// /c/<key> be forgiving about what the customer pasted.
func TestParseAcceptsEquivalentSpellings(t *testing.T) {
	key := Generate()
	variants := []string{key, strings.ToLower(key), Format(key), strings.ToLower(Format(key))}
	// Swap a 0 for an O and a 1 for an l to exercise the confusables.
	confused := strings.NewReplacer("0", "O", "1", "l").Replace(key)
	variants = append(variants, confused, Format(confused))
	for _, v := range variants {
		canonical, ok := Parse(v)
		if !ok || canonical != key {
			t.Errorf("Parse(%q) = %q, %v; want %q, true", v, canonical, ok, key)
		}
	}
}
