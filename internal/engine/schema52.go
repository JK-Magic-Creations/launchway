package engine

// Schema 52, the first step of the wall between Bases (internal/store,
// docs/development/the-wall.md). It is the last number (52), after organizing (51).

// schemaWall is the schema that brought Bases, accounts and memberships.
const schemaWall = 52

func init() {
	if len(migrations) != schemaWall-1 {
		panic("the wall migration (52) must follow migrations 1 to 51")
	}
	migrations = append(migrations, wallMigration)
}

// 52: Bases. A Base is a customer's top level; every installation has the
//
//	default Base, and the people, sessions and tokens that exist belong to it.
//	New: bases (the list, global), accounts (opaque ids, global) and
//	memberships (an account's handle and role in one Base). Added to the six
//	identity tables: base_id, with a default naming the default Base, and a
//	unique index that begins with it, so that older code which knows no
//	Bases keeps working unchanged against the same tables. Nothing about a
//	single-Base installation changes. The narrow keys (username alone) are
//	replaced by composite ones only when several Bases are turned on, by
//	store.ContractKeys: a deliberate one-way step that is not part of this
//	migration, so that upgrading never takes away the ability to roll back.
//	Compatibility: additive: three new tables, a new column with a default
//	and new indexes on six tables.
var wallMigration = migration{additive, `
CREATE TABLE bases (
  id         TEXT PRIMARY KEY,
  handle     TEXT    NOT NULL UNIQUE,
  name       TEXT    NOT NULL DEFAULT '',
  host       TEXT    NOT NULL DEFAULT '',
  status     TEXT    NOT NULL DEFAULT 'active',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  created_by TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX bases_host ON bases(host) WHERE host <> '';
INSERT INTO bases(id, handle, name, status) VALUES('bs_default', 'default', 'default', 'active');

CREATE TABLE accounts (
  id         TEXT PRIMARY KEY,
  created_at {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE memberships (
  base_id    TEXT    NOT NULL,
  account_id TEXT    NOT NULL,
  handle     TEXT    NOT NULL,
  role       TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT 'active',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (base_id, account_id),
  UNIQUE (base_id, handle)
);

ALTER TABLE users ADD COLUMN base_id TEXT NOT NULL DEFAULT 'bs_default';
ALTER TABLE sessions ADD COLUMN base_id TEXT NOT NULL DEFAULT 'bs_default';
ALTER TABLE api_tokens ADD COLUMN base_id TEXT NOT NULL DEFAULT 'bs_default';
ALTER TABLE local_accounts ADD COLUMN base_id TEXT NOT NULL DEFAULT 'bs_default';
ALTER TABLE local_links ADD COLUMN base_id TEXT NOT NULL DEFAULT 'bs_default';
ALTER TABLE local_recovery_codes ADD COLUMN base_id TEXT NOT NULL DEFAULT 'bs_default';

CREATE UNIQUE INDEX users_base_username ON users(base_id, username);
CREATE UNIQUE INDEX sessions_base_hash ON sessions(base_id, id_hash);
CREATE INDEX sessions_base_user ON sessions(base_id, username);
CREATE UNIQUE INDEX api_tokens_base_hash ON api_tokens(base_id, hash);
CREATE UNIQUE INDEX api_tokens_base_id ON api_tokens(base_id, id);
CREATE UNIQUE INDEX api_tokens_base_user_name ON api_tokens(base_id, username, name);
CREATE UNIQUE INDEX local_accounts_base_username ON local_accounts(base_id, username);
CREATE UNIQUE INDEX local_links_base_hash ON local_links(base_id, token_hash);
CREATE UNIQUE INDEX local_recovery_base_code ON local_recovery_codes(base_id, username, code_hash);

INSERT INTO memberships(base_id, account_id, handle, role, state, created_at)
  SELECT 'bs_default', 'ac_' || {{RAND}}, u.username, COALESCE(l.role, ''),
         CASE WHEN l.disabled = 1 THEN 'disabled' ELSE 'active' END, 0
  FROM (SELECT username FROM users UNION SELECT username FROM local_accounts) u
  LEFT JOIN local_accounts l ON l.username = u.username;
INSERT INTO accounts(id, created_at) SELECT account_id, 0 FROM memberships;
`}
