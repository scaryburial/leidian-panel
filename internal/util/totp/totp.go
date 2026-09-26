// Package totp implements the small slice of RFC 6238 needed to gate the
// panel's domain feature behind the operator's authenticator app: verify a
// 6-digit code against a base32 secret, accepting the adjacent time steps to
// tolerate clock skew.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

const (
	digits = 6
	period = 30 * time.Second
)

// normalizeSecret upper-cases and strips padding/spaces so both padded and
// unpadded, spaced and unspaced secrets verify.
func normalizeSecret(secret string) string {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	s = strings.TrimRight(s, "=")
	if pad := len(s) % 8; pad != 0 {
		s += strings.Repeat("=", 8-pad)
	}
	return s
}

// codeAt returns the 6-digit code for the given counter.
func codeAt(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return fmt.Sprintf("%0*d", digits, value%1_000_000)
}

// Verify reports whether code is a valid current TOTP for secret.
func Verify(secret, code string) bool {
	normalized := normalizeSecret(secret)
	if normalized == "" {
		return false
	}
	key, err := base32.StdEncoding.DecodeString(normalized)
	if err != nil || len(key) == 0 {
		return false
	}
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return false
	}
	counter := uint64(time.Now().Unix() / int64(period.Seconds()))
	for _, delta := range []int64{0, -1, 1} {
		want := codeAt(key, uint64(int64(counter)+delta))
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
