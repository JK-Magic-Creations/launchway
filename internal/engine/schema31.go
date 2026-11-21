package engine

// Schema 31, SSH keys, host keys, webhooks and branch rules of hosted
// repositories (internal/forge/sshkeys, internal/forge/webhooks and
// internal/api/forge_*.go), kept in a file of its own: db.go is over the
// size ratchet. It is named so that its init runs after schema30.go's.

// schemaSSH is the schema that brought Git over SSH and webhooks.
const schemaSSH = 31

func init() {
	if len(migrations) != schemaSSH-1 {
		panic("the SSH and webhooks migration (31) must follow migrations 1 to 30")
	}
	migrations = append(migrations, sshMigration)
}

// 31: Git over SSH, webhooks and branch rules.
//
//	ssh_host_keys holds the server's host keys, one per algorithm, sealed
//	(AES-256-GCM) so that every server sharing the database presents the
//	same keys. ssh_keys holds the public keys people add and the deploy
//	keys of repositories (kind says which; a deploy key names its
//	repository and says whether it may push); the fingerprint is unique
//	over both kinds, so a key belongs to one owner. repo_webhooks are
//	hooks of one repository (repo_id) or of every repository of an owner
//	(owner); the secret is sealed and never shown; repo_hook_deliveries is
//	the log of attempts per hook. branch_rules are the protected-branch
//	patterns of a repository.
//	Compatibility: additive: new tables and nothing else. Nothing older
//	code reads changes.
//	Downgrade: nothing to undo for an older server. The tables stay and are
//	ignored; drop them only if the feature is abandoned.
var sshMigration = migration{additive, `
CREATE TABLE ssh_host_keys (
  algo        TEXT PRIMARY KEY,
  public      TEXT    NOT NULL,
  sealed      TEXT    NOT NULL,
  fingerprint TEXT    NOT NULL,
  created_at  {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE ssh_keys (
  id             TEXT PRIMARY KEY,
  kind           TEXT    NOT NULL,
  owner          TEXT    NOT NULL DEFAULT '',
  repo_id        TEXT    NOT NULL DEFAULT '',
  name           TEXT    NOT NULL,
  key_type       TEXT    NOT NULL,
  public         TEXT    NOT NULL,
  fingerprint    TEXT    NOT NULL UNIQUE,
  can_write      INTEGER NOT NULL DEFAULT 0,
  groups         TEXT    NOT NULL DEFAULT '[]',
  created_at     {{BIG}} NOT NULL DEFAULT 0,
  created_by     TEXT    NOT NULL DEFAULT '',
  expires_at     {{BIG}} NOT NULL DEFAULT 0,
  last_used_at   {{BIG}} NOT NULL DEFAULT 0,
  last_used_from TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX ssh_keys_by_owner ON ssh_keys (kind, owner);
CREATE INDEX ssh_keys_by_repo ON ssh_keys (repo_id);

CREATE TABLE repo_webhooks (
  id              TEXT PRIMARY KEY,
  repo_id         TEXT    NOT NULL DEFAULT '',
  owner           TEXT    NOT NULL DEFAULT '',
  name            TEXT    NOT NULL,
  url             TEXT    NOT NULL,
  events          TEXT    NOT NULL DEFAULT '[]',
  secret_sealed   TEXT    NOT NULL DEFAULT '',
  active          INTEGER NOT NULL DEFAULT 1,
  failures        INTEGER NOT NULL DEFAULT 0,
  disabled_reason TEXT    NOT NULL DEFAULT '',
  disabled_at     {{BIG}} NOT NULL DEFAULT 0,
  created_at      {{BIG}} NOT NULL DEFAULT 0,
  created_by      TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX repo_webhooks_by_repo ON repo_webhooks (repo_id);
CREATE INDEX repo_webhooks_by_owner ON repo_webhooks (owner);

CREATE TABLE repo_hook_deliveries (
  id              TEXT PRIMARY KEY,
  hook_id         TEXT    NOT NULL,
  event           TEXT    NOT NULL,
  payload         TEXT    NOT NULL,
  created_at      {{BIG}} NOT NULL,
  test            INTEGER NOT NULL DEFAULT 0,
  state           TEXT    NOT NULL DEFAULT 'pending',
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at {{BIG}} NOT NULL DEFAULT 0,
  claimed_until   {{BIG}} NOT NULL DEFAULT 0,
  status          INTEGER NOT NULL DEFAULT 0,
  duration_ms     {{BIG}} NOT NULL DEFAULT 0,
  error           TEXT    NOT NULL DEFAULT '',
  response        TEXT    NOT NULL DEFAULT '',
  redelivery_of   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX repo_hook_deliveries_by_hook ON repo_hook_deliveries (hook_id, created_at);
CREATE INDEX repo_hook_deliveries_due ON repo_hook_deliveries (state, next_attempt_at);

CREATE TABLE branch_rules (
  id         TEXT PRIMARY KEY,
  repo_id    TEXT    NOT NULL,
  pattern    TEXT    NOT NULL,
  push_role  TEXT    NOT NULL DEFAULT 'admin',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  created_by TEXT    NOT NULL DEFAULT ''
)
`}
