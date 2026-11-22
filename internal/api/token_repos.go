package api

// Tokens limited to hosted repositories and to source addresses, and the
// real client address behind a proxy.

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

const (
	maxTokenRepos  = 50
	maxTokenRanges = 20
	accessRead     = "read"
	accessWrite    = "write"
)

// ProxySettings are what the configuration says about proxies in front of the server and about
// who may make tokens for service accounts.
type ProxySettings struct {
	// TrustedProxies, when not empty, are the only peers whose forwarding headers are believed
	// (server.trusted_proxies); ClientAddrHeader is the single-address header such a proxy sets
	// (server.client_address_header), X-Forwarded-For when empty.
	TrustedProxies   []netip.Prefix
	ClientAddrHeader string
	// AdminTokenMakesServiceTokens lets an administrator's API token, not only an administrator's
	// signed-in session, make a service account's tokens (auth.tokens.service_accounts_from_tokens).
	AdminTokenMakesServiceTokens bool
}

// repoNameRE is what an owner or a repository name looks like (the forge's own rule).
var repoNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// repoLimited reports whether the identity is a token limited to hosted
// repositories. Such a token can do nothing else.
func (id *identity) repoLimited() bool { return id != nil && id.Via == viaToken && id.repoAccess != "" }

// coversRepo reports whether one of the token's entries names the repository
// "owner/name": an exact name, or owner/*. Both halves are compared whole, so
// acme/* never reaches acme-other/x, and nothing matches a path that is not
// exactly two non-empty parts.
func coversRepo(entries []engine.TokenRepo, path string) bool {
	owner, name, ok := strings.Cut(path, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return false
	}
	for _, e := range entries {
		eo, en, _ := strings.Cut(e.Pattern, "/")
		if eo == owner && (en == "*" || en == name) {
			return true
		}
	}
	return false
}

// repoRoleCap is the most the token's access level lets it be on a repository.
func (id *identity) repoRoleCap() auth.Role {
	if id.repoAccess == accessWrite {
		return auth.Operator
	}
	return auth.Viewer
}

// repoIDMatches reports, for a repository found by its path, whether the
// token's entry for it still names this repository (an exact entry carries the
// repository's ID, so one deleted and made again under the name is not it).
func (id *identity) repoIDMatches(repo forge.Repository) bool {
	for _, e := range id.repos {
		if e.Pattern == repo.Path && e.ID != "" && e.ID != repo.ID {
			return false
		}
	}
	return true
}

// signedInForRepoTokens are the calls outside the repository routes that a
// token limited to repositories may make.
var signedInForRepoTokens = map[string]bool{
	"GET /api/v1/repositories": true, // lists only the repositories the token covers
	"GET /api/v1/me":           true,
}

// repoTokenMayCall says whether a token limited to repositories may make a
// call of this access class.
func repoTokenMayCall(a access, r *http.Request) bool {
	switch a {
	case repoRead, repoWrite, repoAdmin:
		return true
	case signedIn:
		return signedInForRepoTokens[r.Pattern]
	}
	return false
}

// ---- making tokens: repositories and source ranges ----

// parseTokenRepos checks the repositories a new token is limited to.
func (s *Server) parseTokenRepos(r *http.Request, id *identity, in []string, access string) ([]engine.TokenRepo, string) {
	if len(in) == 0 && access == "" {
		return nil, ""
	}
	if len(in) == 0 {
		return nil, "name the repositories this token is for, or leave the access level out"
	}
	if access != accessRead && access != accessWrite {
		return nil, `the access to repositories is "read" or "write"`
	}
	if s.Forge == nil {
		return nil, "this server hosts no repositories (forge.hosting is off)"
	}
	if len(in) > maxTokenRepos {
		return nil, fmt.Sprintf("at most %d repositories", maxTokenRepos)
	}
	var out []engine.TokenRepo
	seen := map[string]bool{}
	for _, p := range in {
		owner, name, ok := strings.Cut(p, "/")
		if !ok || !repoNameRE.MatchString(owner) || (name != "*" && !repoNameRE.MatchString(name)) {
			return nil, fmt.Sprintf("%q is not a repository: write owner/name, or owner/* for every repository of an owner", clip(p, 40))
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		e := engine.TokenRepo{Pattern: p}
		if name != "*" {
			repo, err := s.Forge.Provider.Repositories().Get(r.Context(), p)
			if err != nil || !s.authorize(id, auth.Read, p) {
				return nil, fmt.Sprintf("no repository %s that you can see", p)
			}
			e.ID = repo.ID
		}
		out = append(out, e)
	}
	return out, ""
}

// parseSourceRanges checks and normalises a token's source ranges: CIDR
// ranges, or single addresses.
func parseSourceRanges(in []string) ([]string, string) {
	if len(in) > maxTokenRanges {
		return nil, fmt.Sprintf("at most %d source ranges", maxTokenRanges)
	}
	var out []string
	for _, c := range in {
		c = strings.TrimSpace(c)
		p, err := netip.ParsePrefix(c)
		if err != nil {
			a, aerr := netip.ParseAddr(c)
			if aerr != nil {
				return nil, fmt.Sprintf("%q is not an address range: write it like 203.0.113.0/24 or 2001:db8::/32, or one address", clip(c, 40))
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Bits() == 0 {
			return nil, "a range of 0 bits allows every address; leave the list empty for that"
		}
		out = append(out, p.Masked().String())
	}
	return out, ""
}

// inSourceRanges reports whether an address is in one of the ranges.
func inSourceRanges(ranges []netip.Prefix, addr string) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range ranges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parsePrefixes(in []string) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range in {
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// tokenRefusal is the error of a token used from outside its source ranges;
// fail answers it with 403 and its sentence.
func tokenRefusal(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{engine.ErrForbidden}, a...)...)
}

// ---- the real client address ----

// peerAddr is the address of the TCP peer, without a port.
func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// proxyTrusted says whether the request came from a proxy whose forwarding
// headers are believed. With server.trusted_proxies the peer must be in one of
// the ranges; with only the older server.trust_proxy: true every peer is
// believed (the setting says clients cannot reach the server directly).
func (s *Server) proxyTrusted(r *http.Request) bool {
	if len(s.TrustedProxies) > 0 {
		return inSourceRanges(s.TrustedProxies, peerAddr(r))
	}
	return s.TrustProxy
}

// clientAddr is the address of whoever made the request. From a trusted proxy
// it is what the proxy says: the single address in server.client_address_header
// if one is configured, otherwise the last address of X-Forwarded-For that is
// not itself a trusted proxy (the one the proxy nearest the client saw).
// Addresses before it were sent by the client and could be anything. From any
// other peer the headers are ignored.
func (s *Server) clientAddr(r *http.Request) string {
	peer := peerAddr(r)
	if !s.proxyTrusted(r) {
		return peer
	}
	if h := s.ClientAddrHeader; h != "" && !strings.EqualFold(h, "X-Forwarded-For") {
		vals := r.Header.Values(h)
		if len(vals) == 1 {
			if ip, err := netip.ParseAddr(strings.TrimSpace(vals[0])); err == nil {
				return ip.Unmap().String()
			}
		}
		return peer
	}
	var items []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, it := range strings.Split(v, ",") {
			items = append(items, strings.TrimSpace(it))
		}
	}
	for i := len(items) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(items[i])
		if err != nil {
			return peer
		}
		ip = ip.Unmap()
		if len(s.TrustedProxies) > 0 && inSourceRanges(s.TrustedProxies, ip.String()) {
			continue // another of our own proxies: look further left
		}
		return ip.String()
	}
	return peer
}

// tokensForgetRepo takes a deleted repository out of the tokens that name it
// exactly, so that a repository made again under the name is not reachable
// with them. A token left with no repositories can do nothing.
func (s *Server) tokensForgetRepo(gone forge.Repository) {
	err := s.Engine.RewriteTokenRepos(func(list []engine.TokenRepo) ([]engine.TokenRepo, bool) {
		var out []engine.TokenRepo
		for _, e := range list {
			if e.Pattern == gone.Path && (e.ID == "" || e.ID == gone.ID) {
				continue
			}
			out = append(out, e)
		}
		return out, len(out) != len(list)
	})
	if err != nil {
		s.Log.Error("taking a deleted repository out of the tokens that name it", "repository", gone.Path, "err", err)
	}
}

// tokensFollowRename makes the tokens that name a repository exactly name it
// by its new path: a token is for the repository, not for the name.
func (s *Server) tokensFollowRename(oldPath string, now forge.Repository) {
	err := s.Engine.RewriteTokenRepos(func(list []engine.TokenRepo) ([]engine.TokenRepo, bool) {
		changed := false
		out := make([]engine.TokenRepo, len(list))
		for i, e := range list {
			if e.Pattern == oldPath && (e.ID == "" || e.ID == now.ID) {
				e.Pattern, e.ID, changed = now.Path, now.ID, true
			}
			out[i] = e
		}
		return out, changed
	})
	if err != nil {
		s.Log.Error("following a renamed repository in the tokens that name it", "repository", now.Path, "err", err)
	}
}
