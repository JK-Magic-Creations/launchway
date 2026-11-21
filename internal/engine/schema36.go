package engine

// Schema 36, built-in accounts (internal/localauth, internal/api/local_*.go),
// in a file of its own: db.go is over the size ratchet. Named so that its
// init runs after schema35.go's.

// schemaAccounts is the schema that brought built-in accounts.
const schemaAccounts = 36

func init() {
	if len(migrations) != schemaAccounts-1 {
		panic("the built-in accounts migration (36) must follow migrations 1 to 35")
	}
	migrations = append(migrations, accountsMigration)
}

// 36: built-in accounts.
//
//	local_accounts: one row per person who signs in with a password held by
//	this installation: an Argon2id hash (never the password), the role, the
//	TOTP secret and the last step used (so a code cannot be replayed), the
//	count of failed sign-ins in a row and until when the account is locked,
//	whether the account is a break-glass administrator, and when the password
//	was set. local_recovery_codes: single-use codes, stored as hashes.
//	local_links: one-time links (an invitation to a new person, or a reset
//	for a person who is locked out), stored as hashes, with an expiry and
//	when each was used. setup_state: the one-time code that lets the first
//	administrator be created, as a hash, with its expiry; one row.
//	Compatibility: additive: new tables. Nothing older code reads changes.
//	Downgrade: nothing to undo for an older server; the tables stay and are
//	ignored (an older server has no built-in sign-in).
var accountsMigration = migration{additive, `
CREATE TABLE local_accounts (
  username        TEXT PRIMARY KEY,
  display_name    TEXT    NOT NULL DEFAULT '',
  email           TEXT    NOT NULL DEFAULT '',
  role            TEXT    NOT NULL,
  password_hash   TEXT    NOT NULL DEFAULT '',
  password_set_at {{BIG}} NOT NULL DEFAULT 0,
  totp_secret     TEXT    NOT NULL DEFAULT '',
  totp_confirmed  INTEGER NOT NULL DEFAULT 0,
  totp_last_step  {{BIG}} NOT NULL DEFAULT 0,
  failed_count    {{BIG}} NOT NULL DEFAULT 0,
  locked_until    {{BIG}} NOT NULL DEFAULT 0,
  disabled        INTEGER NOT NULL DEFAULT 0,
  break_glass     INTEGER NOT NULL DEFAULT 0,
  created_at      {{BIG}} NOT NULL,
  created_by      TEXT    NOT NULL DEFAULT '',
  last_login_at   {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE local_recovery_codes (
  username  TEXT    NOT NULL,
  code_hash TEXT    NOT NULL,
  used_at   {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (username, code_hash)
);

CREATE TABLE local_links (
  token_hash TEXT PRIMARY KEY,
  username   TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  role       TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  created_by TEXT    NOT NULL DEFAULT '',
  expires_at {{BIG}} NOT NULL,
  used_at    {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX local_links_user ON local_links(username);

CREATE TABLE setup_state (
  id         INTEGER PRIMARY KEY,
  code_hash  TEXT    NOT NULL,
  issued_at  {{BIG}} NOT NULL,
  expires_at {{BIG}} NOT NULL,
  used_at    {{BIG}} NOT NULL DEFAULT 0,
  source     TEXT    NOT NULL DEFAULT ''
);
`}
