package engine

// API tokens limited to a role and to projects (schema 13).
//
// A limited token's hash is stored as "v2:" and the SHA-256, which servers
// older than schema 13 never look up: during a rollback such a token is
// refused, rather than accepted with its owner's full rights. A token with
// no limits keeps the plain hash, so it works on older servers too.

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// limitedPrefix marks the hash of a limited token.
const limitedPrefix = "v2:"

// narrowPrefix marks the hash of a token limited to repositories or to source
// addresses (schema 55). A server of an older schema looks up neither the
// plain hash nor "v2:", so during a rollback such a token is refused instead of
// being accepted without the limit that server does not know.
const narrowPrefix = "v3:"

// TokensLimited reports whether this server knows limited tokens.
func (e *Engine) TokensLimited() bool { return e.knows(schemaTriggers) }

// CreateLimitedToken stores a token that may be limited to a role and to
// projects.
func (e *Engine) CreateLimitedToken(t APIToken, hash string) error {
	if t.RepoAccess != "" || len(t.SourceRanges) > 0 {
		return e.ident().CreateToken(e.scope0(), t, narrowPrefix+hash)
	}
	if t.Role == "" && len(t.Projects) == 0 {
		return e.CreateToken(t, hash)
	}
	return e.ident().CreateToken(e.scope0(), t, limitedPrefix+hash)
}

// LookupToken finds a token by the hash of its value, limited or not, with
// its owner's groups as of their latest sign-in.
func (e *Engine) LookupToken(hash string) (*APIToken, []string, error) {
	return e.ident().TokenByHash(e.scope0(), hash, limitedPrefix+hash, narrowPrefix+hash)
}

// TouchTokenFrom records that a token was used at now from an address,
// unless that was already recorded since notBefore from the same address.
func (e *Engine) TouchTokenFrom(id string, now, notBefore int64, addr string) error {
	return e.ident().TouchToken(e.scope0(), id, now, notBefore, addr)
}

// TokenList lists a person's tokens, or with user "" everyone's, newest
// first.
func (e *Engine) TokenList(user string) ([]APIToken, error) {
	return e.ident().Tokens(e.scope0(), user)
}

// RevokeAnyToken deletes anyone's token, for an admin.
func (e *Engine) RevokeAnyToken(id string) (*APIToken, error) {
	return e.ident().RevokeAnyToken(e.scope0(), id)
}

// IsLimitedHash reports whether a stored hash is that of a limited token.
func IsLimitedHash(h string) bool {
	return strings.HasPrefix(h, limitedPrefix) || strings.HasPrefix(h, narrowPrefix)
}

// TokenRepo is one hosted repository a token is limited to.
type TokenRepo = store.TokenRepo

// ServiceAccount is a non-person account that owns tokens.
type ServiceAccount = store.ServiceAccount

// ServicePrincipalPrefix starts the user name a service account's tokens act
// as. Sign-in through a provider refuses names that start with it, and a
// built-in account's name cannot hold the colon, so no person can be one.
const ServicePrincipalPrefix = "service:"

// ServicePrincipal is the user name a service account acts as.
func ServicePrincipal(name string) string { return ServicePrincipalPrefix + name }

// RewriteTokenRepos lets fn change the repository list of every token that
// has one (a repository was renamed or deleted).
func (e *Engine) RewriteTokenRepos(fn func([]TokenRepo) ([]TokenRepo, bool)) error {
	return e.ident().RewriteTokenRepos(e.scope0(), fn)
}

// CreateServiceAccount adds a service account.
func (e *Engine) CreateServiceAccount(a ServiceAccount) error {
	return e.ident().CreateServiceAccount(e.scope0(), a)
}

// ServiceAccounts lists the service accounts.
func (e *Engine) ServiceAccounts() ([]ServiceAccount, error) {
	return e.ident().ServiceAccounts(e.scope0())
}

// ServiceAccount finds a service account by name.
func (e *Engine) ServiceAccount(name string) (*ServiceAccount, error) {
	return e.ident().ServiceAccount(e.scope0(), name)
}

// SetServiceAccount changes a service account's role or switches it off or on.
func (e *Engine) SetServiceAccount(name string, role *string, disabled *bool) error {
	return e.ident().SetServiceAccount(e.scope0(), name, role, disabled)
}

// DeleteServiceAccount removes a service account and its tokens.
func (e *Engine) DeleteServiceAccount(name string) error {
	return e.ident().DeleteServiceAccount(e.scope0(), name, ServicePrincipal(name))
}
