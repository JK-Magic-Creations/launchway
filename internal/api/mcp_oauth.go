package api

// OAuth for the MCP endpoint, as the MCP authorization rules describe it
// for a resource server: /mcp publishes OAuth 2.0 Protected Resource
// Metadata (RFC 9728) naming the sign-in provider as its authorization
// server, answers 401 with a challenge that points at it, and accepts the
// provider's JWT access tokens (RFC 9068) whose audience is this endpoint
// (RFC 8707). Launchway is not an authorization server: clients register
// with, and get tokens from, the provider. This is off unless configured
// (mcp.oauth.resource); Launchway API tokens work either way.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// viaOAuth marks an identity that comes from the provider's access token.
const viaOAuth = "oauth"

// MCPOAuth says which of the provider's access tokens /mcp accepts.
type MCPOAuth struct {
	// Resource is the endpoint's canonical URI, such as
	// https://launchway.example.com/mcp: what clients ask the provider for
	// (the resource parameter) and what tokens must carry as an audience.
	Resource string
	// Scopes, if any, must all be granted to a token.
	Scopes []string
}

// metadataURL is where the resource's metadata is: RFC 9728 inserts
// /.well-known/oauth-protected-resource between the host and the path.
func (o *MCPOAuth) metadataURL() string {
	u, err := url.Parse(o.Resource)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path
}

// CheckMCPOAuth checks the OAuth settings against the sign-in settings.
func CheckMCPOAuth(o *MCPOAuth, a *Auth) error {
	if o == nil {
		return nil
	}
	if a == nil || a.OIDC == nil {
		return errors.New("mcp.oauth needs sign-in with OpenID Connect (auth.mode: oidc)")
	}
	u, err := url.Parse(o.Resource)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("mcp.oauth.resource must be this server's MCP address, such as https://launchway.example.com/mcp")
	}
	if !strings.HasSuffix(u.Path, mcpPath) {
		return errors.New("mcp.oauth.resource must end in " + mcpPath)
	}
	if o.Resource == a.OIDC.ClientID() {
		return errors.New("mcp.oauth.resource must not be the sign-in client ID: ID tokens carry that audience")
	}
	return nil
}

// protectedResourceMetadata serves RFC 9728 metadata for /mcp: 404 unless
// OAuth is configured.
func (s *Server) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if s.mcpNow().OAuth == nil || s.Auth == nil {
		writeErr(w, http.StatusNotFound, "this server does not accept OAuth access tokens at /mcp; use a Launchway API token")
		return
	}
	o := s.mcpNow().OAuth
	meta := map[string]any{
		"resource":                 o.Resource,
		"authorization_servers":    []string{s.Auth.OIDC.Issuer()},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Launchway",
	}
	if len(o.Scopes) > 0 {
		meta["scopes_supported"] = o.Scopes
	}
	// Public configuration, meant to be read by clients on other sites.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, meta)
}

// oauthIdentity checks the provider's access token and returns whom it is
// for, with their groups from the token or, if it has none, from their
// latest sign-in. A token that does not check out is nil, recorded in the
// audit trail and counted as a failure, like an unknown API token.
func (s *Server) oauthIdentity(r *http.Request, raw string) (*identity, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	claims, client, err := s.Auth.OIDC.VerifyAccessToken(ctx, raw, s.mcpNow().OAuth.Resource, s.mcpNow().OAuth.Scopes)
	if err != nil {
		s.tokenDenied(r, true, "", "", "an OAuth access token that does not check out: "+clip(err.Error(), 200))
		return nil, nil
	}
	groups := claims.Groups
	profile := engine.Profile{Name: claims.Name, Email: claims.Email}
	if groups == nil {
		g, p, err := s.Engine.LastSignIn(claims.Username)
		switch {
		case errors.Is(err, engine.ErrNotFound):
			groups = []string{}
		case err != nil:
			return nil, err
		default:
			groups, profile = g, p
		}
	}
	tag := "OAuth client"
	if client != "" {
		tag += " " + clip(client, 100)
	}
	return &identity{User: claims.Username, Groups: groups, Profile: profile, Via: viaOAuth, tokenTag: tag}, nil
}
