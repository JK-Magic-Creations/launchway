package localauth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHashRoundTripAndParameters(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("the stated parameters are not in the hash: %s", h)
	}
	if !VerifyPassword("correct horse battery staple", h) || VerifyPassword("correct horse battery stapl", h) {
		t.Fatal("verify")
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatal("two hashes of one password are equal: no salt")
	}
	if NeedsRehash(h) {
		t.Fatal("a fresh hash needs no rehash")
	}
	if !NeedsRehash("$argon2id$v=19$m=4096,t=1,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5") {
		t.Fatal("a weaker hash needs a rehash")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=999999999,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", "$bcrypt$x$y$z$w$v"} {
		if VerifyPassword("x", bad) {
			t.Errorf("verified against %q", bad)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	if CommonPasswordCount() < 150 {
		t.Fatalf("the blocklist has %d entries", CommonPasswordCount())
	}
	for _, bad := range []string{"short", "password1234", "Password123456", "qwertyuiop123", "aaaaaaaaaaaa", "abcdefghijkl", "launchway2026", "iloveyou1234"} {
		if CheckPassword(bad, "launchway") == "" {
			t.Errorf("%q was accepted", bad)
		}
	}
	if CheckPassword("alice-wonderland-12", "alice") == "" {
		t.Error("a password built from the user name was accepted")
	}
	// Length over composition: plain lowercase words are fine.
	for _, ok := range []string{"purple walrus drinks coffee", "twelve plain words", strings.Repeat("ab", 20) + "xyz"} {
		if msg := CheckPassword(ok, "bob"); msg != "" {
			t.Errorf("%q refused: %s", ok, msg)
		}
	}
	if CheckPassword(strings.Repeat("x1", 200)) == "" {
		t.Error("an over-long password was accepted")
	}
}

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 appendix B, SHA-1, secret "12345678901234567890", T=59 -> 94287082 (8 digits); six digits: 287082.
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	c, err := TOTPCode(secret, 59)
	if err != nil || c != "287082" {
		t.Fatalf("got %q %v", c, err)
	}
	if c, _ := TOTPCode(secret, 1111111109); c != "081804" {
		t.Fatalf("got %q", c)
	}
}

func TestTOTPWindowAndReplay(t *testing.T) {
	secret, _ := NewTOTPSecret()
	now := int64(1_700_000_000)
	code, _ := TOTPCode(secret, now)
	step, ok := CheckTOTP(secret, code, now, 0)
	if !ok || step != now/30 {
		t.Fatalf("%d %v", step, ok)
	}
	if _, ok := CheckTOTP(secret, code, now, step); ok {
		t.Fatal("a code was accepted twice")
	}
	if _, ok := CheckTOTP(secret, code, now+30, step-5); !ok {
		t.Fatal("one step of drift was refused")
	}
	if _, ok := CheckTOTP(secret, code, now+90, 0); ok {
		t.Fatal("a code three steps old was accepted")
	}
	if _, ok := CheckTOTP(secret, "12345", now, 0); ok {
		t.Fatal("a short code was accepted")
	}
	if u := TOTPURI("Launchway", "alice", secret); !strings.HasPrefix(u, "otpauth://totp/Launchway:alice?") || !strings.Contains(u, "secret="+secret) {
		t.Fatal(u)
	}
}

func TestCodes(t *testing.T) {
	c := NewSetupCode()
	if len(c) != 29 || strings.Count(c, "-") != 4 {
		t.Fatalf("shape %q", c)
	}
	h := HashCode(c)
	if !SameCode(strings.ToLower(strings.ReplaceAll(c, "-", " ")), h) || SameCode("AAAAA-AAAAA-AAAAA-AAAAA-AAAAA", h) {
		t.Fatal("compare")
	}
	seen := map[string]bool{}
	for _, r := range NewRecoveryCodes(10) {
		if len(r) != 11 || seen[r] {
			t.Fatalf("recovery code %q", r)
		}
		seen[r] = true
	}
	if len(NewInviteToken()) != 32 {
		t.Fatal("invite token")
	}
}

func TestBackoffAndLimiter(t *testing.T) {
	want := map[int]time.Duration{0: 0, 4: 0, 5: 30 * time.Second, 6: time.Minute, 7: 2 * time.Minute, 20: 15 * time.Minute}
	for n, d := range want {
		if got := Backoff(n); got != d {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, d)
		}
	}
	l := &Limiter{Max: 3, Window: time.Minute}
	now := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a", now); !ok {
			t.Fatal("refused too early")
		}
	}
	if ok, wait := l.Allow("a", now.Add(time.Second)); ok || wait <= 0 {
		t.Fatalf("fourth attempt allowed: %v %v", ok, wait)
	}
	if ok, _ := l.Allow("b", now); !ok {
		t.Fatal("another key is limited")
	}
	if ok, _ := l.Allow("a", now.Add(2*time.Minute)); !ok {
		t.Fatal("not released after the window")
	}
}
