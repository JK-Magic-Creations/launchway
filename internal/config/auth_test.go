package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

func load(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "launchway.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestAuthConfig(t *testing.T) {
	c, err := load(t, `
listen: 0.0.0.0:8080
server: { trust_proxy: true }
auth:
  mode: oidc
  idle_timeout: 2h
  oidc:
    issuer: https://sso.example.com/application/o/launchway/
    client_id: launchway
    client_secret_file: secret.txt
    redirect_url: https://ci.example.com/auth/callback
  access:
    - { group: platform-admins, role: admin }
    - { group: payments,        role: operator, projects: ["payments-*"] }
    - { user: dana,             role: operator, projects: [web] }
`)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Server.TrustProxy || c.Auth.Mode != AuthOIDC {
		t.Fatalf("%+v", c)
	}
	if !filepath.IsAbs(c.Auth.OIDC.ClientSecretFile) {
		t.Errorf("the secret file is not made absolute: %s", c.Auth.OIDC.ClientSecretFile)
	}
	idle, life, _ := c.Auth.Sessions()
	if idle != 2*time.Hour || life != DefaultSessionLifetime {
		t.Errorf("sessions %s %s", idle, life)
	}
	p, err := c.Auth.Policy()
	if err != nil {
		t.Fatal(err)
	}
	if p.RoleOn("x", []string{"payments"}, "payments-api") != auth.Operator || p.RoleOn("dana", nil, "web") != auth.Operator {
		t.Error("the bindings were not read")
	}
	if c.AllowSelfApproval() {
		t.Error("with sign-in, self-approval must default to off")
	}
	if err := c.CheckExposure("0.0.0.0:8080"); err != nil {
		t.Errorf("sign-in is on, so any address is fine: %v", err)
	}

	os.WriteFile(c.Auth.OIDC.ClientSecretFile, []byte("s3cret\n"), 0o600)
	if s, err := c.Auth.OIDC.ClientSecret(); err != nil || s != "s3cret" {
		t.Errorf("secret %q %v", s, err)
	}
	c.Auth.OIDC.ClientSecretFile, c.Auth.OIDC.ClientSecretEnv = "", "LW_TEST_OIDC_SECRET"
	t.Setenv("LW_TEST_OIDC_SECRET", "")
	if _, err := c.Auth.OIDC.ClientSecret(); err == nil {
		t.Error("an empty secret was accepted")
	}

	// Defaults: no sign-in, self-approval allowed.
	c, err = load(t, "")
	if err != nil || c.Auth.Mode != AuthNone || !c.AllowSelfApproval() {
		t.Fatalf("defaults %+v %v", c.Auth, err)
	}
}

func TestAuthConfigMistakes(t *testing.T) {
	for yaml, want := range map[string]string{
		"auth: {mode: ldap}":                                             `mode "ldap" is not one of none, oidc`,
		"auth: {access: [{group: a, role: admin}]}":                      `access is set, but mode is "none"`,
		"auth: {mode: oidc, access: [{group: a, role: admin}]}":          "issuer, client_id and redirect_url are required",
		"auth: {mode: oidc, oidc: {client_secret: x}}":                   "field client_secret not found", // never inline
		"auth: {mode: oidc, idle_timeout: soon}":                         `idle_timeout "soon" must be a duration`,
		"auth: {mode: oidc, access: [{group: a, role: owner}]}":          `access[0]: role "owner" is not one of`,
		"auth: {mode: oidc, access: [{role: admin}]}":                    "access[0]: give exactly one of group or user",
		"auth: {mode: oidc}":                                             "access is empty",
		"auth: {mode: oidc, oidc: {redirect_url: 'https://x/callback'}}": "must be this server's address followed by /auth/callback",
		"auth: {mode: oidc, oidc: {issuer: x, client_id: y, redirect_url: 'https://x/auth/callback', client_secret_env: A, client_secret_file: B}}": "exactly one of client_secret_env or client_secret_file",
	} {
		_, err := load(t, yaml)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error containing %q", yaml, err, want)
		}
	}
}

func TestCheckExposure(t *testing.T) {
	none := &Config{Auth: AuthConfig{Mode: AuthNone}}
	for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080", "127.0.0.2:9000"} {
		if err := none.CheckExposure(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080", "192.168.1.5:8080", "ci.example.com:8080", "8080"} {
		err := none.CheckExposure(addr)
		if err == nil || !strings.Contains(err.Error(), "refusing to listen on "+addr+" without sign-in") ||
			!strings.Contains(err.Error(), "auth.allow_anonymous: true") {
			t.Errorf("%s: %v", addr, err)
		}
	}
	allowed := &Config{Auth: AuthConfig{Mode: AuthNone, AllowAnonymous: true}}
	if err := allowed.CheckExposure("0.0.0.0:8080"); err != nil {
		t.Errorf("explicitly allowed: %v", err)
	}
}
