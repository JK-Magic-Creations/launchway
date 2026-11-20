// Package runnerauth is how a runner proves who it is to a Launchway server,
// shared by both ends.
//
// A runner enrols once with a join token that a person made for a pool. It
// generates an Ed25519 key pair, keeps the private key, and registers the
// public key; the server gives it a runner ID. From then on it asks for
// short-lived access tokens with assertions signed by its key: compact JWS
// (RFC 7515) with the EdDSA algorithm, in the shape of RFC 7523's client
// assertions. The proof is in the request body, not in the TLS layer, so it
// passes through proxies that inspect TLS, where client certificates do not.
package runnerauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Prefixes of the secrets, so that each kind can be recognised (and found by
// secret scanners) wherever it leaks.
const (
	JoinPrefix   = "lwj_" // a join token, made by a person for one pool
	AccessPrefix = "lwr_" // a runner's access token
)

// Lifetimes and tolerances.
const (
	// AssertionMaxLife is the longest an assertion may be valid for.
	AssertionMaxLife = 60 * time.Second
	// MaxSkew is how far a runner's clock may be from the server's.
	// Runners correct their clocks by the server's (see Clock), so this
	// only has to cover the time a request takes and coarse corrections.
	MaxSkew = 2 * time.Minute
	// AccessTTL is how long an access token lasts.
	AccessTTL = 15 * time.Minute
	// JoinTTL is how long a join token lasts by default, and MaxJoinTTL the
	// longest it may.
	JoinTTL    = time.Hour
	MaxJoinTTL = 7 * 24 * time.Hour
)

// Audiences: the path of the endpoint an assertion is for. An assertion for
// one cannot be used at the other.
const (
	AudienceToken  = "/api/runner/v2/token"
	AudienceRotate = "/api/runner/v2/rotate"
)

// Claims are what an assertion says.
type Claims struct {
	Issuer   string `json:"iss"` // the runner ID
	Subject  string `json:"sub"` // the runner ID again, as RFC 7523 asks
	Audience string `json:"aud"` // the server's URL and the endpoint's path
	IssuedAt int64  `json:"iat"` // Unix seconds
	Expires  int64  `json:"exp"` // Unix seconds
	ID       string `json:"jti"` // unique; the server accepts each once
	// NewKey, in a rotation's assertion, is the public key the runner
	// moves to, so that the old key vouches for exactly that key.
	NewKey string `json:"new_key,omitempty"`
}

var b64 = base64.RawURLEncoding

const header = `{"alg":"EdDSA","typ":"JWT"}`

// Sign makes an assertion.
func Sign(key ed25519.PrivateKey, c Claims) (string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signed := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString(body)
	return signed + "." + b64.EncodeToString(ed25519.Sign(key, []byte(signed))), nil
}

// NewAssertion makes an assertion for an endpoint, valid from now for
// AssertionMaxLife.
func NewAssertion(key ed25519.PrivateKey, runnerID, server, audience string, now time.Time) (string, error) {
	return Sign(key, Claims{Issuer: runnerID, Subject: runnerID, Audience: strings.TrimRight(server, "/") + audience,
		IssuedAt: now.Unix(), Expires: now.Add(AssertionMaxLife).Unix(), ID: Random(16)})
}

// Errors from Verify. ErrSkew means the assertion's times are too far from
// the server's clock; the server says its time so the runner can correct.
var (
	ErrMalformed = errors.New("the assertion is not a signed JWT")
	ErrSignature = errors.New("the assertion's signature does not match the runner's key")
	ErrAudience  = errors.New("the assertion is for another endpoint")
	ErrSkew      = errors.New("the assertion's times are too far from the server's clock")
	ErrLifetime  = errors.New("the assertion is valid for longer than 60 seconds")
)

// Peek reads an assertion's claims without checking anything, to find the
// runner whose key verifies it.
func Peek(token string) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, ErrMalformed
	}
	h, err := b64.DecodeString(parts[0])
	if err != nil {
		return c, ErrMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(h, &hdr) != nil || hdr.Alg != "EdDSA" {
		return c, fmt.Errorf("%w: the algorithm must be EdDSA", ErrMalformed)
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		return c, ErrMalformed
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return c, ErrMalformed
	}
	if c.Issuer == "" || c.Issuer != c.Subject || c.ID == "" || len(c.ID) > 128 {
		return c, fmt.Errorf("%w: iss, sub and jti are required, and iss must equal sub", ErrMalformed)
	}
	return c, nil
}

// Verify checks an assertion against a runner's public key: the signature,
// that it is for audience (an endpoint path; the server's own address is
// not compared, since proxies may change how it is reached, and the key is
// known to this installation only), and its times against now.
func Verify(token string, key ed25519.PublicKey, audience string, now time.Time) (Claims, error) {
	c, err := Peek(token)
	if err != nil {
		return c, err
	}
	i := strings.LastIndex(token, ".")
	sig, err := b64.DecodeString(token[i+1:])
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte(token[:i]), sig) {
		return c, ErrSignature
	}
	if !strings.HasSuffix(c.Audience, audience) {
		return c, ErrAudience
	}
	skew := int64(MaxSkew / time.Second)
	n := now.Unix()
	switch {
	case c.Expires-c.IssuedAt > int64(AssertionMaxLife/time.Second) || c.Expires <= c.IssuedAt:
		return c, ErrLifetime
	case c.IssuedAt > n+skew, c.Expires < n-skew:
		return c, ErrSkew
	}
	return c, nil
}

// KeepJTIUntil is how long a server must remember an assertion's ID to
// refuse it a second time: until no clock within MaxSkew could accept it.
func KeepJTIUntil(c Claims) time.Time {
	return time.Unix(c.Expires, 0).Add(MaxSkew)
}

// Random returns n random bytes, URL-safe base64 encoded.
func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b64.EncodeToString(b)
}

// NewSecret returns a new join or access token: the prefix and 32 random
// bytes.
func NewSecret(prefix string) string { return prefix + Random(32) }

// Hash is how join and access tokens are stored: their SHA-256, in hex.
func Hash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// EncodeKey and DecodeKey write and read a public key: URL-safe base64.
func EncodeKey(k ed25519.PublicKey) string { return b64.EncodeToString(k) }

func DecodeKey(s string) (ed25519.PublicKey, error) {
	b, err := b64.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("the public key is not 32 bytes of URL-safe base64")
	}
	return ed25519.PublicKey(b), nil
}

// Fingerprint names a public key briefly, for lists and logs.
func Fingerprint(k string) string {
	h := sha256.Sum256([]byte(k))
	return "SHA256:" + b64.EncodeToString(h[:])[:16]
}

// JoinProof shows, when joining, that the runner holds the private key of
// the public key it registers. It signs the join token, so the proof cannot
// be used for any other join.
func JoinProof(key ed25519.PrivateKey, joinToken string) string {
	return b64.EncodeToString(ed25519.Sign(key, proofMessage("join", joinToken, EncodeKey(key.Public().(ed25519.PublicKey)))))
}

// VerifyJoinProof checks a JoinProof.
func VerifyJoinProof(key ed25519.PublicKey, joinToken, proof string) bool {
	sig, err := b64.DecodeString(proof)
	return err == nil && ed25519.Verify(key, proofMessage("join", joinToken, EncodeKey(key)), sig)
}

// RotateProof shows, when rotating, that the runner holds the new key; it
// signs the assertion that the old key signed.
func RotateProof(newKey ed25519.PrivateKey, assertion string) string {
	return b64.EncodeToString(ed25519.Sign(newKey, proofMessage("rotate", assertion, EncodeKey(newKey.Public().(ed25519.PublicKey)))))
}

// VerifyRotateProof checks a RotateProof.
func VerifyRotateProof(newKey ed25519.PublicKey, assertion, proof string) bool {
	sig, err := b64.DecodeString(proof)
	return err == nil && ed25519.Verify(newKey, proofMessage("rotate", assertion, EncodeKey(newKey)), sig)
}

func proofMessage(kind, bound, key string) []byte {
	return []byte("launchway-runner-" + kind + "\n" + Hash(bound) + "\n" + key)
}
