package engine

// API tokens limited to a role and to projects (schema 13).
//
// A limited token's hash is stored as "v2:" and the SHA-256, which servers
// older than schema 13 never look up: during a rollback such a token is
// refused, rather than accepted with its owner's full rights. A token with
// no limits keeps the plain hash, so it works on older servers too.

import (
	"strings"
)

// limitedPrefix marks the hash of a limited token.
const limitedPrefix = "v2:"

// TokensLimited reports whether this server knows limited tokens.
func (e *Engine) TokensLimited() bool { return e.knows(schemaTriggers) }

// CreateLimitedToken stores a token that may be limited to a role and to
// projects.
func (e *Engine) CreateLimitedToken(t APIToken, hash string) error {
	if t.Role == "" && len(t.Projects) == 0 {
		return e.CreateToken(t, hash)
	}
	return e.ident().CreateToken(e.scope0(), t, limitedPrefix+hash)
}

// LookupToken finds a token by the hash of its value, limited or not, with
// its owner's groups as of their latest sign-in.
func (e *Engine) LookupToken(hash string) (*APIToken, []string, error) {
	return e.ident().TokenByHash(e.scope0(), hash, limitedPrefix+hash)
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
func IsLimitedHash(h string) bool { return strings.HasPrefix(h, limitedPrefix) }
