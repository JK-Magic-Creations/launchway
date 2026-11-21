package config

import "testing"

func TestAuthLocalDefaultsAndChecks(t *testing.T) {
	c, err := load(t, "listen: 0.0.0.0:8080\n")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing written: no sign-in on loopback, built-in accounts anywhere else.
	c.ApplyAuthDefault("0.0.0.0:8080")
	if c.Auth.Mode != AuthLocal || !c.Auth.LocalAccounts() {
		t.Fatalf("mode %q", c.Auth.Mode)
	}
	if err := c.CheckExposure("0.0.0.0:8080"); err != nil {
		t.Fatalf("a default install refuses to start: %v", err)
	}
	c2, _ := load(t, "")
	c2.ApplyAuthDefault("127.0.0.1:8080")
	if c2.Auth.Mode != AuthNone {
		t.Fatalf("loopback mode %q", c2.Auth.Mode)
	}
	// An explicit none is never changed, and is still refused beyond loopback.
	c3, _ := load(t, "auth: {mode: none}\n")
	c3.ApplyAuthDefault("0.0.0.0:8080")
	if c3.Auth.Mode != AuthNone || c3.CheckExposure("0.0.0.0:8080") == nil {
		t.Fatal("an explicit none was changed or allowed")
	}
	if _, err := load(t, "auth: {mode: local, local: {require_second_factor: sometimes}}\n"); err == nil {
		t.Fatal("a bad second-factor rule was accepted")
	}
	// Local accounts get their roles through reserved groups, which are never a provider's.
	p, err := c.Auth.Policy()
	if err != nil || p == nil {
		t.Fatal(err)
	}
}
