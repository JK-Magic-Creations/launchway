package notices

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// A mute link carries a token that names a person and a kind and is signed
// with a key only the installation knows, so that it needs no sign-in, cannot
// be made for someone else, and cannot be turned into anything but muting
// that one kind of mail for that one person.

// TokenLife is how long a mute link works: long, because mail is read late.
const TokenLife = 180 * 24 * time.Hour

// ErrBadToken is returned for a token that is not ours, was changed, or is old.
var ErrBadToken = errors.New("this link is not valid or has expired")

// SignMute makes the token for a person and a kind.
func SignMute(key []byte, person, kind string, now time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(person)) + "." + kind + "." + strconv.FormatInt(now.Add(TokenLife).Unix(), 36)
	return payload + "." + mac(key, payload)
}

func mac(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("launchway mute notice v1\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:20])
}

// VerifyMute checks a token and returns the person and the kind it mutes.
func VerifyMute(key []byte, token string, now time.Time) (person, kind string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return "", "", ErrBadToken
	}
	payload := strings.Join(parts[:3], ".")
	if !hmac.Equal([]byte(mac(key, payload)), []byte(parts[3])) {
		return "", "", ErrBadToken
	}
	exp, err := strconv.ParseInt(parts[2], 36, 64)
	if err != nil || now.Unix() > exp {
		return "", "", ErrBadToken
	}
	p, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", ErrBadToken
	}
	if _, ok := Info(parts[1]); !ok {
		return "", "", ErrBadToken
	}
	return string(p), parts[1], nil
}
