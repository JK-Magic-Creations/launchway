package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

// The values of auth.mode.
const (
	AuthNone  = "none"
	AuthOIDC  = "oidc"
	AuthLocal = "local"
)

// Session defaults.
const (
	DefaultIdleTimeout     = 8 * time.Hour
	DefaultSessionLifetime = 24 * time.Hour
)

// check reports mistakes in the auth section.
func (a *AuthConfig) check() error {
	var errs []error
	switch a.Mode {
	case AuthNone:
		if len(a.Access) > 0 {
			errs = append(errs, errors.New(`access is set, but mode is "none", where everyone has full access; set mode: oidc`))
		}
		if !a.Admins.Empty() {
			errs = append(errs, errors.New(`admins is set, but mode is "none", where everyone has full access; set mode: oidc`))
		}
	case AuthOIDC:
		if err := a.Local.check(); err != nil {
			errs = append(errs, err)
		}
		o := a.OIDC
		if o.Issuer == "" || o.ClientID == "" || o.RedirectURL == "" {
			errs = append(errs, errors.New("oidc: issuer, client_id and redirect_url are required"))
		}
		if o.Issuer != "" {
			if u, err := url.Parse(o.Issuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				errs = append(errs, fmt.Errorf("oidc.issuer %q is not an http(s) URL", o.Issuer))
			}
		}
		if o.RedirectURL != "" {
			u, err := url.Parse(o.RedirectURL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "/auth/callback" {
				errs = append(errs, fmt.Errorf("oidc.redirect_url %q must be this server's address followed by /auth/callback", o.RedirectURL))
			}
		}
		if (o.ClientSecretEnv == "") == (o.ClientSecretFile == "") {
			errs = append(errs, errors.New("oidc: give exactly one of client_secret_env or client_secret_file"))
		}
		if len(a.Access) == 0 && a.Admins.Empty() && !a.accessInDatabase {
			errs = append(errs, errors.New("access is empty, so nobody could do anything; add at least one binding, or name the first administrators in admins"))
		}
	case AuthLocal:
		if err := a.Local.check(); err != nil {
			errs = append(errs, err)
		}
	default:
		errs = append(errs, fmt.Errorf("mode %q is not one of local, oidc, none", a.Mode))
	}
	if _, _, err := a.Sessions(); err != nil {
		errs = append(errs, err)
	}
	if _, err := a.Policy(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Sessions returns the idle timeout and the lifetime of a session.
func (a *AuthConfig) Sessions() (idle, lifetime time.Duration, err error) {
	idle, lifetime = DefaultIdleTimeout, DefaultSessionLifetime
	for _, d := range []struct {
		name, text string
		into       *time.Duration
	}{{"idle_timeout", a.IdleTimeout, &idle}, {"session_lifetime", a.SessionLifetime, &lifetime}} {
		if d.text == "" {
			continue
		}
		v, err := time.ParseDuration(d.text)
		if err != nil || v < time.Minute {
			return 0, 0, fmt.Errorf("%s %q must be a duration of at least 1m, such as 8h", d.name, d.text)
		}
		*d.into = v
	}
	if idle > lifetime {
		idle = lifetime
	}
	return idle, lifetime, nil
}

// Policy returns the access bindings, checked.
func (a *AuthConfig) Policy() (*auth.Policy, error) {
	var bs []auth.Binding
	var errs []error
	for i, b := range a.Access {
		role, err := auth.ParseRole(b.Role)
		if err != nil {
			errs = append(errs, fmt.Errorf("access[%d]: %w", i, err))
			continue
		}
		bs = append(bs, auth.Binding{Group: b.Group, User: b.User, Role: role, Projects: b.Projects, Environments: b.Environments})
	}
	for _, u := range a.Admins.Users {
		if strings.TrimSpace(u) == "" {
			errs = append(errs, errors.New("admins.users: a user name is empty"))
			continue
		}
		bs = append(bs, auth.Binding{User: u, Role: auth.Admin})
	}
	for _, g := range a.Admins.Groups {
		if strings.TrimSpace(g) == "" {
			errs = append(errs, errors.New("admins.groups: a group name is empty"))
			continue
		}
		bs = append(bs, auth.Binding{Group: g, Role: auth.Admin})
	}
	if a.LocalAccounts() {
		// Built-in accounts get their role through a group no provider may claim.
		for _, r := range []auth.Role{auth.Admin, auth.Operator, auth.Viewer} {
			bs = append(bs, auth.Binding{Group: auth.LocalGroup(r), Role: r})
		}
	}
	p, err := auth.NewPolicy(bs)
	return p, errors.Join(append(errs, err)...)
}

// ClientSecret reads the OIDC client secret from where the configuration
// says it is.
func (o *OIDCConfig) ClientSecret() (string, error) {
	var v string
	switch {
	case o.ClientSecretEnv != "":
		v = os.Getenv(o.ClientSecretEnv)
		if strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("auth.oidc.client_secret_env: the environment variable %s is empty", o.ClientSecretEnv)
		}
	case o.ClientSecretFile != "":
		b, err := os.ReadFile(o.ClientSecretFile)
		if err != nil {
			return "", fmt.Errorf("auth.oidc.client_secret_file: %w", err)
		}
		v = string(b)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("auth.oidc: the client secret is empty")
	}
	return v, nil
}

// AllowSelfApproval says whether the person who started a run may approve
// it: approvals.allow_self if set; otherwise yes without sign-in, where
// everyone is the same user, and no with it.
func (c *Config) AllowSelfApproval() bool {
	if c.Approvals.AllowSelf != nil {
		return *c.Approvals.AllowSelf
	}
	return c.Auth.Mode == AuthNone
}

// CheckExposure refuses a server without sign-in that listens where others
// can reach it, unless that was asked for explicitly.
func (c *Config) CheckExposure(listen string) error {
	if c.Auth.Mode != AuthNone || c.Auth.AllowAnonymous || IsLoopback(listen) {
		return nil
	}
	return fmt.Errorf(`refusing to listen on %s without sign-in.
With auth.mode "none" there is no sign-in: anyone who can reach this address could
start pipelines, which run code on the runners, and approve deployments. Either
  - listen on a loopback address, such as 127.0.0.1:8080; or
  - turn on sign-in with built-in accounts (auth.mode: local, the default here) or
    an identity provider (auth.mode: oidc; see "Sign-in and access" in the README); or
  - if something in front of Launchway already decides who may reach it, set
    auth.allow_anonymous: true`, listen)
}

// IsLoopback reports whether a listen address accepts connections only from
// this machine.
func IsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
