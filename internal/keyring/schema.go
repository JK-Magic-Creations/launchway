package keyring

// SchemaSQL creates the keyring's tables (the engine's migration 34 runs it,
// after replacing {{BIG}} with the database's 64-bit integer type; the tests
// run it on SQLite).
//
//	encryption_state  small facts: installation_id, the migration's progress
//	encryption_keks   key-encryption keys, each wrapped by the root key; the
//	                  holder column says which kind of holder wrapped it
//	encryption_deks   data keys per purpose and organization, each wrapped
//	                  by a key-encryption key
//
// Nothing here holds a usable key: every value is wrapped, and the root key
// is not in the database.
const SchemaSQL = `
CREATE TABLE encryption_state (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE encryption_keks (
  version    {{BIG}} PRIMARY KEY,
  holder     TEXT    NOT NULL,
  wrapped    TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL DEFAULT 0,
  retired_at {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE encryption_deks (
  purpose     TEXT    NOT NULL,
  org         TEXT    NOT NULL DEFAULT '',
  version     {{BIG}} NOT NULL,
  kek_version {{BIG}} NOT NULL,
  wrapped     TEXT    NOT NULL,
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (purpose, org, version)
);
`
