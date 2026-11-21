package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

// Prefix starts every sealed value. A stored value without it is plaintext
// from before encryption at rest was turned on (see IsSealed).
const Prefix = "lwe1."

// Format of a sealed value, after the Prefix, base64url without padding:
//
//	byte 0     format version (1)
//	byte 1     algorithm (1: AES-256-GCM)
//	bytes 2-5  version of the data key, big endian
//	bytes 6-17 nonce (12 random bytes)
//	bytes 18-  ciphertext and tag
//
// The purpose and organization that choose the data key are not in the
// value: the caller supplies them, and they are bound by the authenticated
// data, so a value only opens for the purpose and organization it was
// sealed for. AES-256-GCM is the algorithm because the standard library
// provides it and it is in the FIPS 140-3 validated module; the algorithm
// byte is how another one is added without a flag day. A random 96-bit
// nonce is safe far below 2^32 sealings per key, and the secrets sealed
// here are few (a rotation of the data key starts a new count).
const (
	formatVersion = 1
	algAES256GCM  = 1
	headerLen     = 6
	nonceLen      = 12
	minSealedLen  = headerLen + nonceLen + 16
)

// IsSealed reports whether s is a value this package sealed (it has the
// version prefix; it is not proof that it opens).
func IsSealed(s string) bool { return strings.HasPrefix(s, Prefix) }

// Binding says what a sealed value is for. Every field is bound into the
// authenticated data. Table, Column and Row identify where it is stored; Org
// is the organization the row belongs to, "" for the whole installation.
type Binding struct {
	Purpose string
	Org     string
	Table   string
	Column  string
	Row     string
}

// Purposes name the data keys. Secrets are values a person gave or a
// service needs back (webhook secrets, host keys); Tokens are the server's
// own signing and sealing keys and shared tokens.
const (
	PurposeSecrets = "secrets"
	PurposeTokens  = "tokens"
)

// aad is the authenticated data for a binding sealed with data key version v:
// every field length-prefixed so that no two bindings share an encoding.
func (b Binding) aad(installation string, v uint32) []byte {
	out := []byte("launchway/seal/v1")
	for _, f := range []string{installation, b.Purpose, b.Org, b.Table, b.Column, b.Row} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(f)))
		out = append(out, f...)
	}
	return binary.BigEndian.AppendUint32(out, v)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("a key must be 32 bytes")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// sealWith seals plain under key, naming dekVersion in the header.
func sealWith(key []byte, dekVersion uint32, aad, plain []byte) (string, error) {
	g, err := newGCM(key)
	if err != nil {
		return "", err
	}
	buf := make([]byte, headerLen+nonceLen, headerLen+nonceLen+len(plain)+g.Overhead())
	buf[0], buf[1] = formatVersion, algAES256GCM
	binary.BigEndian.PutUint32(buf[2:], dekVersion)
	if _, err := rand.Read(buf[headerLen:]); err != nil {
		return "", err
	}
	buf = g.Seal(buf, buf[headerLen:headerLen+nonceLen], plain, aad)
	return Prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// parse reads the header of a sealed value.
func parse(sealed string) (dekVersion uint32, raw []byte, err error) {
	if !IsSealed(sealed) {
		return 0, nil, errors.New("not a sealed value")
	}
	raw, err = base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(sealed, Prefix))
	if err != nil || len(raw) < minSealedLen {
		return 0, nil, errors.New("a sealed value is damaged (too short or not encoded as expected)")
	}
	if raw[0] != formatVersion {
		return 0, nil, errors.New("a sealed value is of a newer format than this server understands; upgrade this server")
	}
	if raw[1] != algAES256GCM {
		return 0, nil, errors.New("a sealed value uses an algorithm this server does not know; upgrade this server")
	}
	return binary.BigEndian.Uint32(raw[2:]), raw, nil
}

// SealedVersion returns the data key version a sealed value names.
func SealedVersion(sealed string) (uint32, error) {
	v, _, err := parse(sealed)
	return v, err
}

func openWith(key []byte, aad, raw []byte) ([]byte, error) {
	g, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plain, err := g.Open(nil, raw[headerLen:headerLen+nonceLen], raw[headerLen+nonceLen:], aad)
	if err != nil {
		return nil, ErrAltered
	}
	return plain, nil
}

// ErrAltered is a value that did not open: it was changed, moved to another
// row, column or organization, or made with another key.
var ErrAltered = errors.New("a sealed value did not open: it was changed, moved to another row, column or organization, or made with another key")
