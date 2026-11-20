package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/config"
)

// SignInRequest is what the sign-in helper is told: the answers a person
// gives in the wizard. Sign-in stays in the configuration file, because the
// server needs it before it can read the database; the helper checks the
// answers against the identity provider and writes the snippet.
type SignInRequest struct {
	Issuer      string `json:"issuer"`
	ClientID    string `json:"client_id"`
	RedirectURL string `json:"redirect_url"`
	// ClientSecretEnv is the name of the environment variable that will
	// hold the client secret (the secret itself is never sent here).
	ClientSecretEnv string `json:"client_secret_env"`
	GroupsClaim     string `json:"groups_claim"`
	UsernameClaim   string `json:"username_claim"`
	// AdminGroups and AdminUsers are the first administrators.
	AdminGroups []string `json:"admin_groups"`
	AdminUsers  []string `json:"admin_users"`
}

// SignInEndpoints are what the identity provider's discovery document says.
type SignInEndpoints struct {
	Authorization string   `json:"authorization,omitempty"`
	Token         string   `json:"token,omitempty"`
	JWKS          string   `json:"jwks,omitempty"`
	ClaimsOffered []string `json:"claims_offered,omitempty"`
	GroupsHint    string   `json:"groups_claim_hint,omitempty"`
}

// SignInCheck is the helper's answer.
type SignInCheck struct {
	OK        bool            `json:"ok"`
	Problems  []string        `json:"problems"`
	Endpoints SignInEndpoints `json:"endpoints"`
	// Snippet is the auth: block to put in the server's configuration file.
	Snippet string `json:"snippet"`
}

// CheckSignIn checks sign-in answers and writes the configuration snippet.
// client may be nil (the default client with a short timeout is used).
func CheckSignIn(ctx context.Context, in SignInRequest, client *http.Client) SignInCheck {
	out := SignInCheck{Problems: []string{}}
	in.Issuer = strings.TrimRight(strings.TrimSpace(in.Issuer), "/")
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.RedirectURL = strings.TrimSpace(in.RedirectURL)
	in.ClientSecretEnv = strings.TrimSpace(in.ClientSecretEnv)

	a := config.AuthConfig{Mode: config.AuthOIDC, OIDC: config.OIDCConfig{
		Issuer: in.Issuer, ClientID: in.ClientID, RedirectURL: in.RedirectURL, ClientSecretEnv: in.ClientSecretEnv,
		GroupsClaim: in.GroupsClaim, UsernameClaim: in.UsernameClaim},
		Admins: config.AdminsConfig{Users: in.AdminUsers, Groups: in.AdminGroups}}
	if in.ClientSecretEnv == "" {
		out.Problems = append(out.Problems, "name the environment variable that will hold the client secret, such as LAUNCHWAY_OIDC_SECRET")
		a.OIDC.ClientSecretEnv = "LAUNCHWAY_OIDC_SECRET"
	}
	if err := a.Check(); err != nil {
		for _, l := range strings.Split(err.Error(), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out.Problems = append(out.Problems, l)
			}
		}
	}
	if in.Issuer != "" {
		ep, problem := discoverOIDC(ctx, in.Issuer, client)
		out.Endpoints = ep
		if problem != "" {
			out.Problems = append(out.Problems, problem)
		}
		if gc := in.GroupsClaim; ep.ClaimsOffered != nil && len(in.AdminGroups) > 0 {
			if gc == "" {
				gc = "groups"
			}
			found := false
			for _, c := range ep.ClaimsOffered {
				found = found || c == gc
			}
			if !found {
				out.Problems = append(out.Problems, fmt.Sprintf("the identity provider does not list a %q claim, so groups may not arrive; check the provider's scopes and claim name", gc))
			}
		}
	}
	out.Snippet = signInSnippet(a)
	out.OK = len(out.Problems) == 0
	return out
}

func signInSnippet(a config.AuthConfig) string {
	type oidc struct {
		Issuer          string `yaml:"issuer"`
		ClientID        string `yaml:"client_id"`
		RedirectURL     string `yaml:"redirect_url"`
		ClientSecretEnv string `yaml:"client_secret_env"`
		UsernameClaim   string `yaml:"username_claim,omitempty"`
		GroupsClaim     string `yaml:"groups_claim,omitempty"`
	}
	type admins struct {
		Users  []string `yaml:"users,omitempty"`
		Groups []string `yaml:"groups,omitempty"`
	}
	type block struct {
		Mode   string  `yaml:"mode"`
		OIDC   oidc    `yaml:"oidc"`
		Admins *admins `yaml:"admins,omitempty"`
	}
	b := block{Mode: config.AuthOIDC, OIDC: oidc{a.OIDC.Issuer, a.OIDC.ClientID, a.OIDC.RedirectURL, a.OIDC.ClientSecretEnv, a.OIDC.UsernameClaim, a.OIDC.GroupsClaim}}
	if !a.Admins.Empty() {
		b.Admins = &admins{a.Admins.Users, a.Admins.Groups}
	}
	text, err := yaml.Marshal(map[string]block{"auth": b})
	if err != nil {
		return ""
	}
	return string(text)
}

func discoverOIDC(ctx context.Context, issuer string, client *http.Client) (SignInEndpoints, string) {
	var ep SignInEndpoints
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ep, ""
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return ep, ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ep, fmt.Sprintf("the server could not reach the identity provider at %s (%v); check the address and that this server can connect to it", issuer, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ep, fmt.Sprintf("%s/.well-known/openid-configuration answered %d; the issuer address is usually the provider's address up to the application, with no trailing path to the login page", issuer, resp.StatusCode)
	}
	var doc struct {
		Issuer        string   `json:"issuer"`
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		JWKS          string   `json:"jwks_uri"`
		Claims        []string `json:"claims_supported"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return ep, "the identity provider's discovery document is not valid JSON; is this the issuer address?"
	}
	ep = SignInEndpoints{Authorization: doc.Authorization, Token: doc.Token, JWKS: doc.JWKS, ClaimsOffered: doc.Claims}
	for _, c := range doc.Claims {
		if c == "groups" {
			ep.GroupsHint = "groups"
		}
	}
	if doc.Issuer != "" && strings.TrimRight(doc.Issuer, "/") != issuer {
		return ep, fmt.Sprintf("the provider says its issuer is %q, not %q; use exactly the address it gives, or sign-in will be refused", doc.Issuer, issuer)
	}
	return ep, ""
}
