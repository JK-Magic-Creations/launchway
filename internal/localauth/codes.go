package localauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// An alphabet without the characters that are easy to confuse when read
// aloud or copied by hand (no 0, 1, I, L, O).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func randomFrom(alphabet string, n int) string {
	out := make([]byte, n)
	buf := make([]byte, n*2)
	i := 0
	for i < n {
		if _, err := rand.Read(buf); err != nil {
			panic("no randomness: " + err.Error())
		}
		for _, b := range buf {
			// Reject values that would make some characters likelier.
			if int(b) >= 256-256%len(alphabet) {
				continue
			}
			out[i] = alphabet[int(b)%len(alphabet)]
			i++
			if i == n {
				break
			}
		}
	}
	return string(out)
}

// NewSetupCode returns a one-time setup code: 25 characters from a 31
// character alphabet (about 123 bits) written in five groups of five.
func NewSetupCode() string {
	c := randomFrom(codeAlphabet, 25)
	parts := make([]string, 0, 5)
	for i := 0; i < 25; i += 5 {
		parts = append(parts, c[i:i+5])
	}
	return strings.Join(parts, "-")
}

// NormalizeCode upper-cases a typed code and drops spaces and dashes, so the
// code can be typed with or without its grouping.
func NormalizeCode(code string) string {
	code = strings.ToUpper(code)
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, code)
}

// HashCode is how a one-time code is stored: SHA-256 of the normalized code.
// The codes have more than 100 bits of randomness, so a fast hash suffices.
func HashCode(code string) string {
	h := sha256.Sum256([]byte(NormalizeCode(code)))
	return hex.EncodeToString(h[:])
}

// SameCode compares a typed code with a stored hash in constant time.
func SameCode(typed, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashCode(typed)), []byte(storedHash)) == 1
}

// NewRecoveryCodes returns n single-use recovery codes, each ten characters
// in two groups of five (about 50 bits each: they are limited by the sign-in
// throttle, and each is single use).
func NewRecoveryCodes(n int) []string {
	out := make([]string, n)
	for i := range out {
		c := randomFrom(codeAlphabet, 10)
		out[i] = c[:5] + "-" + c[5:]
	}
	return out
}

// NewInviteToken returns the secret part of a one-time link: 32 characters.
func NewInviteToken() string { return randomFrom(codeAlphabet, 32) }
