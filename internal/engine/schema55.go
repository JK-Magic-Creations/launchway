package engine

// Schema 55: narrow credentials (docs/guide/credentials-for-machines.md). It is
// number 55, after the measured use of a step attempt (54).

// schemaNarrow is the schema that brought repository and source-address limits
// on API tokens, and service accounts.
const schemaNarrow = 55

func init() {
	if len(migrations) != schemaNarrow-1 {
		panic("migration 55 must follow migrations 1 to 54")
	}
	migrations = append(migrations, narrowMigration)
}

// 55: api_tokens gains three columns and service_accounts is new.
//
//	repos is a JSON list of the hosted repositories a token is limited to (each
//	{"pattern":"owner/name" or "owner/*","id":"<repository id>" for an exact
//	name}); repo_access is "read" or "write" and is non-empty exactly when the
//	token is limited to repositories (a limited token whose list has since
//	become empty can do nothing); source_ranges is a JSON list of CIDR ranges
//	the token may be used from, empty for anywhere. service_accounts are
//	non-person accounts: no password, no sign-in; role is the most a token of
//	the account can have.
//	audit gains token_id and token_name: the token a request was authenticated
//	with ("" when it was not a token), so that an alert can tell one automation
//	from another; never the secret.
//	Compatibility: additive. A token that carries any of the new limits is
//	stored under a hash prefix older servers do not look up, and a token made before this migration has empty values, which mean
//	"no such limit": it behaves exactly as before.
//	Downgrade: nothing to undo. A token with a repository or source-address
//	limit is stored under the "v3:" hash prefix (see narrowPrefix), which a server of
//	an older schema never looks up, so during a rollback it refuses such a token
//	instead of accepting it without the limit it does not know.
var narrowMigration = migration{additive, `
ALTER TABLE api_tokens ADD COLUMN repos TEXT NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN repo_access TEXT NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN source_ranges TEXT NOT NULL DEFAULT '';

ALTER TABLE audit ADD COLUMN token_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit ADD COLUMN token_name TEXT NOT NULL DEFAULT '';

CREATE TABLE service_accounts (
  base_id     TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  role        TEXT    NOT NULL DEFAULT 'viewer',
  disabled    INTEGER NOT NULL DEFAULT 0,
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  created_by  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (base_id, name)
);
`}
