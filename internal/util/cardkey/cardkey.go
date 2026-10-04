// Package cardkey implements the 雷电面板 (ui3344) card-key ("卡密") format.
//
// A card key is 12 effective characters of Crockford Base32, rendered for
// humans as LD-XXXX-XXXX-XXXX. The 12 characters are
//
//	11 data characters (55 random bits, drawn from crypto/rand)
//	 1 checksum character (5 bits, weighted mod-32 sum of the data)
//
// Design trade-offs:
//
//   - 60 usable bits is far more than the format needs, so spending 5 of them
//     on a checksum costs nothing an operator can observe: 55 random bits is
//     3.6e16 keys, while even a very large deployment hands out a few hundred
//     thousand cards. The checksum buys a definite "this is not a card key"
//     answer for the overwhelmingly common failure (a typo, a truncated paste),
//     which is a different message than "this card key does not exist" and can
//     be rejected before touching the database.
//   - The checksum is sum(i*w_i * value(d_i)) mod 32 over the data characters
//     with *odd* weights w_i = 2i+1. Mod-32 arithmetic is a ring, not a field,
//     so an even weight would have a zero divisor (2 * 16 == 0 mod 32) and
//     would let a single mistyped character slip through. Odd weights are
//     invertible mod 32, which makes every single-character error detectable;
//     distinct weights also catch adjacent transpositions.
//   - The canonical storage form is the bare 12 characters (no prefix, no
//     hyphens); Generate returns exactly that, so "store what Generate
//     returned" is correct by construction. Use Format for display.
package cardkey

import (
	"crypto/rand"
	"strings"
	"unicode"
)

// Alphabet is the Crockford Base32 alphabet: 0-9 then the unambiguous
// consonants. I, L, O and U are deliberately absent; I/L/O are accepted as
// confusables of 1/0 on input (see Normalize) and U is rejected outright.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	// DataLen is the number of random data characters in a card key.
	DataLen = 11
	// Length is the number of effective characters in a canonical card key.
	Length = DataLen + 1
	// Prefix is the display prefix of a card key.
	Prefix = "LD-"
	// GroupSize is the display group size; Length is a multiple of it, so a
	// formatted card key is exactly Prefix + three hyphen-separated groups.
	GroupSize = 4
)

// base is len(Alphabet) as an int, hoisted so the checksum reads clearly.
const base = 32

// valueOf maps a canonical alphabet byte to its 5-bit value, or -1 when the
// byte is not part of the alphabet. It is a byte table rather than a map
// because Normalize/IsWellFormed are called on every /c/<key> request.
var valueOf [256]int8

func init() {
	for i := range valueOf {
		valueOf[i] = -1
	}
	for i := 0; i < len(Alphabet); i++ {
		valueOf[Alphabet[i]] = int8(i)
	}
}

// Generate returns a fresh card key in canonical form: 12 Crockford Base32
// characters, no prefix and no hyphens, ready to be stored in clients.card_key.
// Render it for humans with Format.
//
// The 11 random characters come from crypto/rand. 256 is an exact multiple of
// the 32-character alphabet, so the modulo reduction below is uniform — there
// is no modulo bias to correct.
func Generate() string {
	var raw [DataLen]byte
	// crypto/rand.Read never fails: as of Go 1.24 it panics irrecoverably
	// rather than returning an error, and a card key must not silently
	// degrade to a predictable value.
	if _, err := rand.Read(raw[:]); err != nil {
		panic("cardkey: crypto/rand failed: " + err.Error())
	}
	data := make([]byte, DataLen)
	for i, b := range raw {
		data[i] = Alphabet[int(b)%base]
	}
	return encode(data)
}

// encode appends the checksum character to 11 canonical data characters.
func encode(data []byte) string {
	return string(data) + string(Alphabet[checkValue(data)])
}

// checkValue returns the 5-bit checksum of the canonical data characters.
// Inputs are assumed to be Length-1 canonical alphabet bytes; see
// IsWellFormed for the validated entry point.
func checkValue(data []byte) byte {
	sum := 0
	for i, ch := range data {
		weight := 2*i + 1 // odd => invertible mod 32 => detects any single-char error
		sum += weight * int(valueOf[ch])
	}
	return byte(sum % base)
}

// Normalize canonicalizes user input: case is ignored, hyphens and whitespace
// are dropped, a leading LD prefix is removed, and the Crockford confusables
// O -> 0 and I/L -> 1 are folded onto their digits.
//
// The result is the canonical form when the input happened to be a valid card
// key, and a best-effort cleanup otherwise: Normalize never reports failure, so
// callers must check the result with IsWellFormed (or use Parse) before trusting
// it. Only the shape Length is guaranteed for valid input — a normalized string
// of any other length is malformed by definition.
func Normalize(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		switch r {
		case '-', '_', '.', ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	out := b.String()

	// Drop the display prefix. Only ever stripped from an over-long input, so
	// the prefix can never eat the first two characters of a 12-char key.
	if len(out) > Length && strings.HasPrefix(out, "LD") {
		out = out[2:]
	}

	// Confusables, applied after prefix stripping: 'L' is itself a confusable,
	// so mapping first would turn "LD-..." into "1D-..." and hide the prefix.
	return strings.Map(func(r rune) rune {
		switch r {
		case 'O':
			return '0'
		case 'I', 'L':
			return '1'
		}
		return r
	}, out)
}

// IsWellFormed reports whether input resolves to a canonical card key of the
// right length, drawn from the alphabet, with a matching checksum. It accepts
// every input shape Normalize accepts, so a user may paste LD-XXXX-XXXX-XXXX,
// the bare 12 characters, or a lowercase/confusable variant of either.
func IsWellFormed(input string) bool {
	_, ok := Parse(input)
	return ok
}

// Parse normalizes input and validates it, returning the canonical form and
// whether it is a well-formed card key. The canonical form is returned even
// when ok is false whenever normalization produced something usable, so callers
// can log what the user actually typed.
func Parse(input string) (string, bool) {
	canonical := Normalize(input)
	if len(canonical) != Length {
		return canonical, false
	}
	data := []byte(canonical[:DataLen])
	for _, ch := range data {
		if valueOf[ch] < 0 {
			return canonical, false
		}
	}
	check := canonical[DataLen]
	if valueOf[check] < 0 {
		return canonical, false
	}
	return canonical, byte(valueOf[check]) == checkValue(data)
}

// Format renders a card key for display as LD-XXXX-XXXX-XXXX. Input is
// normalized first, so Format(Normalize(k)) == Format(k). A value that does not
// normalize to Length characters is returned as normalized, unchanged — Format
// never drops information; use IsWellFormed to decide whether to show it.
func Format(input string) string {
	canonical := Normalize(input)
	if len(canonical) != Length {
		return canonical
	}
	var b strings.Builder
	b.Grow(len(Prefix) + Length + Length/GroupSize - 1)
	b.WriteString(Prefix)
	for i := 0; i < Length; i++ {
		if i > 0 && i%GroupSize == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(canonical[i])
	}
	return b.String()
}
