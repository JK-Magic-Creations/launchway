package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// RandomString returns n random bytes, base64url encoded without padding.
func RandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash returns the hex SHA-256 of s. Session ids and API tokens are stored
// only as this; each has 256 random bits, so a fast hash is enough.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// NewKey returns a random 256-bit key, hex encoded.
func NewKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Sealer encrypts and authenticates small values with AES-256-GCM, so they
// can be given to a browser to keep and checked when they come back. Every
// server sharing a database uses the same key.
type Sealer struct {
	aead cipher.AEAD
	ad   []byte // binds a sealed value to its purpose
}

// NewSealer takes a hex key from NewKey and a purpose, which must be the
// same when opening.
func NewSealer(hexKey, purpose string) (*Sealer, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("the key must be 32 bytes, hex encoded")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead, ad: []byte(purpose)}, nil
}

// Seal encodes v as JSON and seals it.
func (s *Sealer) Seal(v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, s.ad)), nil
}

// Open checks and decrypts what Seal made, into v.
func (s *Sealer) Open(sealed string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return errors.New("not a sealed value")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], s.ad)
	if err != nil {
		return errors.New("the sealed value was altered or made with another key")
	}
	if err := json.Unmarshal(plain, v); err != nil {
		return fmt.Errorf("the sealed value is not understood: %w", err)
	}
	return nil
}
