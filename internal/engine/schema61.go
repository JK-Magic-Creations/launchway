package engine

// Schema 61: verified commits and tags (see docs/guides/verified-commits.md):
// the signing keys people register, the cache of signature checks, and the
// installation's own signing key.

// schemaSigning is the schema that brought verified commits and tags.
const schemaSigning = 61

func init() {
	if len(migrations) != schemaSigning-1 {
		panic("migration 61 must follow migrations 1 to 60")
	}
	migrations = append(migrations, signingMigration)
}

// 61: signing_keys, the public keys people sign commits and tags with (SSH
//
//	signing keys; an OpenPGP key has its own kind for later), each owned by one
//	person, with an optional expiry and the time it was revoked from (0:
//	never); signature_cache, the cryptographic verdict on an object (an
//	object never changes, so it is made once; who owns the key is judged
//	afresh); signing_installation, the installation's own key, its private
//	half sealed under the root key (internal/sealing).
//	Compatibility: additive: new tables only, ignored by an older server.
//	Downgrade: nothing to undo.
var signingMigration = migration{additive, `
CREATE TABLE signing_keys (
  id          TEXT PRIMARY KEY,
  owner       TEXT    NOT NULL,
  kind        TEXT    NOT NULL DEFAULT 'ssh',
  name        TEXT    NOT NULL,
  fingerprint TEXT    NOT NULL,
  public_text TEXT    NOT NULL,
  created_at  {{BIG}} NOT NULL,
  expires_at  {{BIG}} NOT NULL DEFAULT 0,
  revoked_at  {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX signing_keys_by_fingerprint ON signing_keys(fingerprint);
CREATE INDEX signing_keys_by_owner ON signing_keys(owner);
CREATE TABLE signature_cache (
  object_sha      TEXT PRIMARY KEY,
  sig_type        TEXT    NOT NULL DEFAULT '',
  key_fingerprint TEXT    NOT NULL DEFAULT '',
  crypto_ok       INTEGER NOT NULL DEFAULT 0,
  reason          TEXT    NOT NULL DEFAULT '',
  checked_at      {{BIG}} NOT NULL DEFAULT 0
);
CREATE TABLE signing_installation (
  id          TEXT PRIMARY KEY,
  algo        TEXT    NOT NULL,
  public_text TEXT    NOT NULL,
  fingerprint TEXT    NOT NULL,
  sealed      TEXT    NOT NULL,
  created_at  {{BIG}} NOT NULL DEFAULT 0
);
`}
