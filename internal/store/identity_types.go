package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Profile is what the provider said about a person at their latest sign-in,
// beyond their username: used to author the commits they save. Either may be
// empty.
type Profile struct {
	Name  string // display name
	Email string // only an address the provider has not said is unverified
}

// Session is a signed-in browser.
type Session struct {
	User      string
	Groups    []string // from the person's latest sign-in
	Profile   Profile  // likewise
	CreatedAt int64
	LastSeen  int64
	ExpiresAt int64
}

// APIToken describes a token. The token itself is never stored.
type APIToken struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	User      string  `json:"-"`
	Owner     Profile `json:"-"` // the owner's profile as of their latest sign-in
	CreatedAt int64   `json:"created_at"`
	ExpiresAt int64   `json:"expires_at"`
	LastUsed  int64   `json:"last_used"`
	// The role the token is limited to ("" for its owner's), the project
	// patterns it is limited to (none for every project), and the address it
	// was last used from.
	Role         string   `json:"role,omitempty"`
	Projects     []string `json:"projects,omitempty"`
	LastUsedFrom string   `json:"last_used_from,omitempty"`
	// Repositories limits the token to hosted repositories (see TokenRepo);
	// RepoAccess ("read" or "write") is non-empty exactly when it does.
	// SourceRanges are the CIDR ranges the token may be used from (none for
	// anywhere).
	Repositories []TokenRepo `json:"repositories,omitempty"`
	RepoAccess   string      `json:"repo_access,omitempty"`
	SourceRanges []string    `json:"source_ranges,omitempty"`
	// OwnerName names whose token it is, in lists only admins see.
	OwnerName string `json:"user,omitempty"`
}

// TokenRepo is one hosted repository a token is limited to: an exact
// "owner/name" (with the repository's ID, so a repository deleted and made
// again under the name is not the one the token was made for) or "owner/*".
type TokenRepo struct {
	Pattern string `json:"pattern"`
	ID      string `json:"id,omitempty"`
}

// ServiceAccount is a non-person account that owns tokens: no password and
// no sign-in.
type ServiceAccount struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role"` // the most a token of the account can have
	Disabled    bool   `json:"disabled"`
	CreatedAt   int64  `json:"created_at"`
	CreatedBy   string `json:"created_by,omitempty"`
}

// LocalAccount is one built-in account.
type LocalAccount struct {
	Username      string
	DisplayName   string
	Email         string
	Role          string // viewer, operator or admin
	PasswordHash  string // Argon2id; empty for an invited person who has not chosen one yet
	PasswordSetAt int64
	TOTPSecret    string // base32; sealed under the root key in the database, in the clear only above this package
	TOTPConfirmed bool
	TOTPLastStep  int64
	FailedCount   int64
	LockedUntil   int64
	Disabled      bool
	BreakGlass    bool
	CreatedAt     int64
	CreatedBy     string
	LastLoginAt   int64
}

// Link is a one-time link, as stored (the secret part is kept only as a hash).
type Link struct {
	Hash      string
	Username  string
	Kind      string
	Role      string
	CreatedAt int64
	CreatedBy string
	ExpiresAt int64
	UsedAt    int64
}

// SetupState is the one-time code that starts an installation.
type SetupState struct {
	CodeHash  string
	IssuedAt  int64
	ExpiresAt int64
	UsedAt    int64
	Source    string // "file", "generated" or "console"
}

// Member is an account's place in one Base.
type Member struct {
	AccountID string
	Handle    string
	Role      string // "" for a person whose role comes from their provider's groups
	State     string // active or disabled
	CreatedAt int64
}

func decodeGroups(s sql.NullString) []string {
	out := []string{}
	if s.Valid {
		_ = json.Unmarshal([]byte(s.String), &out)
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Sealer seals one value bound to its row; internal/sealing's Box satisfies
// it (through the engine, which holds the box). The store seals what it
// writes to a secret column, so that the registry of sealed columns can name
// it as the writer.
type Sealer interface {
	Enabled() bool
	SealRow(row string, plain []byte) (string, error)
}

// ErrSealUnavailable means a secret was to be stored and no root key is in use.
var ErrSealUnavailable = errors.New("no root key to seal the secret with")
