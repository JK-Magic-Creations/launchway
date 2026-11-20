package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCOptions configure sign-in with an OpenID Connect provider.
type OIDCOptions struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string   // this server's /auth/callback, as the browser reaches it
	Scopes        []string // must include openid
	UsernameClaim string   // tried first; then email, then sub
	GroupsClaim   string
	HTTPClient    *http.Client     // for calls to the provider; default has a 10s timeout
	Now           func() time.Time // for tests
}

// OIDC signs people in with the authorization code flow, PKCE, state and
// nonce. The provider is found through discovery the first time it is
// needed, and found again on later calls until that succeeds, so a server
// can start while the provider is unreachable.
type OIDC struct {
	o OIDCOptions

	mu       sync.Mutex
	provider *oidc.Provider
}

// Claims is who the provider says signed in.
type Claims struct {
	Subject  string
	Username string
	Groups   []string
	Name     string // the name claim, if any: for showing and for authoring commits
	Email    string // the email claim, unless the provider says it is unverified
}

// NewOIDC fills in defaults. It does not contact the provider.
func NewOIDC(o OIDCOptions) *OIDC {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{oidc.ScopeOpenID, "profile", "email", "groups"}
	}
	if !slices.Contains(o.Scopes, oidc.ScopeOpenID) {
		o.Scopes = append([]string{oidc.ScopeOpenID}, o.Scopes...)
	}
	if o.UsernameClaim == "" {
		o.UsernameClaim = "preferred_username"
	}
	if o.GroupsClaim == "" {
		o.GroupsClaim = "groups"
	}
	return &OIDC{o: o}
}

func (c *OIDC) ctx(ctx context.Context) context.Context {
	ctx = oidc.ClientContext(ctx, c.o.HTTPClient)
	return context.WithValue(ctx, oauth2.HTTPClient, c.o.HTTPClient)
}

// Discover finds the provider's endpoints and keys, if that has not been
// done yet.
func (c *OIDC) Discover(ctx context.Context) (*oidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil {
		return c.provider, nil
	}
	// The provider keeps only the HTTP client from this context (its key
	// set fetches keys without the context's deadline).
	dctx, cancel := context.WithTimeout(c.ctx(ctx), 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, c.o.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering the sign-in provider at %s: %w", c.o.Issuer, err)
	}
	c.provider = p
	return p, nil
}

func (c *OIDC) oauth(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{ClientID: c.o.ClientID, ClientSecret: c.o.ClientSecret, RedirectURL: c.o.RedirectURL,
		Endpoint: p.Endpoint(), Scopes: c.o.Scopes}
}

// AuthURL is where to send the browser to sign in.
func (c *OIDC) AuthURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	p, err := c.Discover(ctx)
	if err != nil {
		return "", err
	}
	return c.oauth(p).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// Exchange trades the code the browser brought back for an ID token,
// checks the token (signature, issuer, audience, expiry and nonce) and
// returns who it names.
func (c *OIDC) Exchange(ctx context.Context, code, verifier, nonce string) (*Claims, error) {
	p, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	if code == "" {
		return nil, errors.New("the provider sent no code")
	}
	ctx = c.ctx(ctx)
	conf := c.oauth(p)
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchanging the code: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, errors.New("the provider returned no ID token")
	}
	idt, err := p.VerifierContext(ctx, &oidc.Config{ClientID: c.o.ClientID, Now: c.o.Now}).Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("checking the ID token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("the ID token's nonce does not match this sign-in")
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	// Some providers put groups (or the username) only in the userinfo
	// response. It is asked only when the ID token lacks them.
	if _, ok := claims[c.o.GroupsClaim]; !ok && p.UserInfoEndpoint() != "" {
		if ui, err := p.UserInfo(ctx, conf.TokenSource(ctx, tok)); err == nil && ui.Subject == idt.Subject {
			extra := map[string]any{}
			if ui.Claims(&extra) == nil {
				for k, v := range extra {
					if _, ok := claims[k]; !ok {
						claims[k] = v
					}
				}
			}
		}
	}
	return c.claims(idt.Subject, claims)
}

// RedirectURL is this server's sign-in callback, as browsers reach it.
func (c *OIDC) RedirectURL() string { return c.o.RedirectURL }

// Issuer is the provider's issuer.
func (c *OIDC) Issuer() string { return c.o.Issuer }

// ClientID is the client ID Launchway signs in with.
func (c *OIDC) ClientID() string { return c.o.ClientID }

// VerifyAccessToken checks an access token the provider issued as a JWT
// (RFC 9068) for a resource: its signature with the provider's keys, its
// issuer, that audience is among its audiences, that it has not expired,
// and that it carries every scope in scopes. It returns who it is for, from
// the same claims sign-in uses, and the client it was issued to.
func (c *OIDC) VerifyAccessToken(ctx context.Context, raw, audience string, scopes []string) (*Claims, string, error) {
	p, err := c.Discover(ctx)
	if err != nil {
		return nil, "", err
	}
	if audience == "" || audience == c.o.ClientID {
		// An ID token for Launchway's own sign-in has that audience: it must
		// never pass for an access token.
		return nil, "", errors.New("no audience to check access tokens against")
	}
	tok, err := p.Verifier(&oidc.Config{ClientID: audience, Now: c.o.Now}).Verify(c.ctx(ctx), raw)
	if err != nil {
		return nil, "", err
	}
	claims := map[string]any{}
	if err := tok.Claims(&claims); err != nil {
		return nil, "", err
	}
	if _, isID := claims["nonce"]; isID {
		return nil, "", errors.New("an ID token is not an access token")
	}
	granted := map[string]bool{}
	switch sc := claims["scope"].(type) {
	case string:
		for _, s := range strings.Fields(sc) {
			granted[s] = true
		}
	case []any: // some providers send scp as a list
		for _, v := range sc {
			if s, ok := v.(string); ok {
				granted[s] = true
			}
		}
	}
	if scp, ok := claims["scp"].([]any); ok {
		for _, v := range scp {
			if s, ok := v.(string); ok {
				granted[s] = true
			}
		}
	}
	for _, want := range scopes {
		if !granted[want] {
			return nil, "", fmt.Errorf("the access token lacks the scope %q", want)
		}
	}
	client, _ := claims["client_id"].(string)
	if client == "" {
		client, _ = claims["azp"].(string)
	}
	out, err := c.claims(tok.Subject, claims)
	if err != nil {
		return nil, "", err
	}
	if _, ok := claims[c.o.GroupsClaim]; !ok {
		out.Groups = nil // not said, as opposed to none
	}
	return out, client, nil
}

// claims picks out the username and groups.
func (c *OIDC) claims(subject string, m map[string]any) (*Claims, error) {
	out := &Claims{Subject: subject}
	for _, name := range []string{c.o.UsernameClaim, "email", "sub"} {
		if name == "email" && m["email_verified"] == false {
			continue // an address the provider has not checked names nobody
		}
		if s, ok := m[name].(string); ok && strings.TrimSpace(s) != "" {
			out.Username = strings.TrimSpace(s)
			break
		}
	}
	if !ValidName(out.Username) {
		return nil, fmt.Errorf("the ID token has no usable username (claims %s, email, sub)", c.o.UsernameClaim)
	}
	switch g := m[c.o.GroupsClaim].(type) {
	case []any:
		for _, v := range g {
			if s, ok := v.(string); ok && ValidName(s) && len(out.Groups) < 1000 {
				out.Groups = append(out.Groups, s)
			}
		}
	case string: // some providers send a single group as a string
		if ValidName(g) {
			out.Groups = []string{g}
		}
	}
	if s, ok := m["name"].(string); ok && ValidName(strings.TrimSpace(s)) {
		out.Name = strings.TrimSpace(s)
	}
	if s, ok := m["email"].(string); ok && m["email_verified"] != false && validEmail(s) {
		out.Email = s
	}
	slices.Sort(out.Groups)
	out.Groups = slices.Compact(out.Groups)
	if out.Groups == nil {
		out.Groups = []string{}
	}
	return out, nil
}

// validEmail accepts a plain address: no spaces, angle brackets or control
// characters, one @ with something on both sides, at most 254 bytes. It is
// only ever written into a commit's author line, never sent mail.
func validEmail(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, " <>\"\\,;") || !ValidName(s) {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	return at > 0 && at < len(s)-1 && strings.Count(s, "@") == 1
}

// ValidName reports whether s is acceptable as a username, group or token
// name: 1 to 256 bytes of valid UTF-8 without control characters.
func ValidName(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
