package config

import (
	"fmt"
)

// The values of auth.local.require_second_factor.
const (
	SecondFactorAdmins = "admins" // administrators must have one (the default)
	SecondFactorAll    = "all"    // everyone with a built-in account must
	SecondFactorOff    = "off"    // nobody must (people may still turn it on)
)

// LocalAuthConfig is the built-in accounts: people who sign in with a
// password held by this installation.
type LocalAuthConfig struct {
	// Enabled turns built-in sign-in on next to an identity provider (mode
	// oidc), so that switching providers can never lock everyone out. With
	// mode local it is always on. Default: off with oidc.
	Enabled bool `yaml:"enabled"`
	// RequireSecondFactor says who must set up a second factor (TOTP or a
	// passkey) before they can finish creating an account: admins (default),
	// all or off.
	RequireSecondFactor string `yaml:"require_second_factor"`
	// SetupCodeFile is a file holding the one-time code that lets the first
	// administrator be created (the chart mounts a Secret there). Without
	// it the server makes a code and writes it to its log.
	SetupCodeFile string `yaml:"setup_code_file"`
	// SetupCodeTTL is how long a setup code is good for; default 24h.
	SetupCodeTTL string `yaml:"setup_code_ttl"`
}

func (l *LocalAuthConfig) check() error {
	switch l.RequireSecondFactor {
	case "", SecondFactorAdmins, SecondFactorAll, SecondFactorOff:
	default:
		return fmt.Errorf("auth.local.require_second_factor %q is not one of admins, all, off", l.RequireSecondFactor)
	}
	return nil
}

// SecondFactorRule returns require_second_factor with its default.
func (l *LocalAuthConfig) SecondFactorRule() string {
	if l.RequireSecondFactor == "" {
		return SecondFactorAdmins
	}
	return l.RequireSecondFactor
}

// LocalAccounts says whether built-in sign-in is on.
func (a *AuthConfig) LocalAccounts() bool {
	return a.Mode == AuthLocal || (a.Mode == AuthOIDC && a.Local.Enabled)
}

// ApplyAuthDefault resolves a mode that was not written: built-in accounts
// when the server listens beyond loopback (so a fresh installation never
// starts open), no sign-in on loopback (local development).
func (c *Config) ApplyAuthDefault(listen string) {
	if c.Auth.modeDefaulted && c.Auth.Mode == AuthNone && !IsLoopback(listen) {
		c.Auth.Mode = AuthLocal
	}
}
