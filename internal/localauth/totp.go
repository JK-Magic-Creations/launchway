package localauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 authenticator apps all use HMAC-SHA1; it is a keyed MAC here, not a hash of anything public
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
)

// TOTP (RFC 6238, as every authenticator app does it): 6 digits, 30-second
// steps, HMAC-SHA1, a 160-bit secret. A code is accepted for the step before,
// the current one and the one after (clock drift), and a step, once used, is
// never accepted again (the caller keeps the last step used).
const (
	TOTPDigits  = 6
	TOTPPeriod  = 30 // seconds
	totpSecretN = 20 // bytes
	totpSkew    = 1  // steps either side
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a new random secret, base32 encoded.
func NewTOTPSecret() (string, error) {
	b := make([]byte, totpSecretN)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

func hotp(secret []byte, counter uint64) string {
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(c[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%0*d", TOTPDigits, v)
}

// TOTPCode returns the code for the secret at a time (Unix seconds); used by
// tests and by the command that proves a freshly set up second factor.
func TOTPCode(secret string, unix int64) (string, error) {
	raw, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", fmt.Errorf("the secret is not base32: %w", err)
	}
	return hotp(raw, uint64(unix/TOTPPeriod)), nil //nolint:gosec // a time after 1970
}

// CheckTOTP reports whether the code is right at the time, and for which
// step. The step must be greater than lastStep (the last one accepted for
// this account) or the code is a replay.
func CheckTOTP(secret, code string, unix, lastStep int64) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	raw, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return 0, false
	}
	now := unix / TOTPPeriod
	found := int64(-1)
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		st := now + d
		if st < 0 {
			continue
		}
		// Compare every candidate so the time taken does not say which matched.
		if subtle.ConstantTimeCompare([]byte(hotp(raw, uint64(st))), []byte(code)) == 1 && st > lastStep {
			found = st
		}
	}
	return found, found >= 0
}

// TOTPURI is the otpauth:// address an authenticator app reads from a QR
// code or accepts pasted.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {fmt.Sprint(TOTPDigits)}, "period": {fmt.Sprint(TOTPPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}
