package runnerauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAssertions(t *testing.T) {
	pk, k, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	a, err := NewAssertion(k, "rnr_1", "https://ci.example.com/", AudienceToken, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(a, pk, AudienceToken, now)
	if err != nil || c.Issuer != "rnr_1" || c.Audience != "https://ci.example.com"+AudienceToken {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Verify(a, pk, AudienceRotate, now); !errors.Is(err, ErrAudience) {
		t.Fatalf("audience: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Verify(a, other, AudienceToken, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("other key: %v", err)
	}
	if _, err := Verify(a, pk, AudienceToken, now.Add(5*time.Minute)); !errors.Is(err, ErrSkew) {
		t.Fatalf("late: %v", err)
	}
	if _, err := Verify(a, pk, AudienceToken, now.Add(-5*time.Minute)); !errors.Is(err, ErrSkew) {
		t.Fatalf("early: %v", err)
	}
	// A body changed after signing.
	parts := strings.Split(a, ".")
	tampered := parts[0] + "." + b64.EncodeToString([]byte(`{"iss":"rnr_2","sub":"rnr_2","aud":"x/api/runner/v2/token","iat":1800000000,"exp":1800000030,"jti":"j"}`)) + "." + parts[2]
	if _, err := Verify(tampered, pk, AudienceToken, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered: %v", err)
	}
	// Too long a life, and another algorithm.
	long, _ := Sign(k, Claims{Issuer: "r", Subject: "r", Audience: AudienceToken, IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix(), ID: "j"})
	if _, err := Verify(long, pk, AudienceToken, now); !errors.Is(err, ErrLifetime) {
		t.Fatalf("long: %v", err)
	}
	none := b64.EncodeToString([]byte(`{"alg":"none"}`)) + "." + parts[1] + "."
	if _, err := Verify(none, pk, AudienceToken, now); !errors.Is(err, ErrMalformed) {
		t.Fatalf("alg none: %v", err)
	}
}

func TestProofs(t *testing.T) {
	pk, k, _ := ed25519.GenerateKey(rand.Reader)
	tok := NewSecret(JoinPrefix)
	p := JoinProof(k, tok)
	if !VerifyJoinProof(pk, tok, p) || VerifyJoinProof(pk, tok+"x", p) {
		t.Fatal("join proof")
	}
	if !VerifyRotateProof(pk, "assertion", RotateProof(k, "assertion")) || VerifyRotateProof(pk, "other", RotateProof(k, "assertion")) {
		t.Fatal("rotate proof")
	}
	if Hash(tok) == tok || len(Hash(tok)) != 64 {
		t.Fatal("hash")
	}
}
