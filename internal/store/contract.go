package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// ContractKeys is the one-way step that turns on several Bases for the
// identity tables: their keys, which began with the name alone (a username,
// a token's hash), begin with base_id instead, and base_id loses its
// default, so that a statement that forgets it fails instead of landing in the
// default Base.
//
// Migration 70 did not do this, on purpose: it only added base_id beside
// the old keys, so that an installation with one Base keeps every ability it
// had, including going back to the previous build. This step cannot be
// undone except from a backup; it records itself (meta wall_keys) and raises
// the lowest schema a build may run against to minSchema. It is safe to call
// again and from several servers at once.
func (e *Env) ContractKeys(_ context.Context, minSchema int) error {
	return e.doGlobal(true, func(q Querier) error {
		if e.PG {
			if _, err := q.Exec(`SELECT pg_advisory_xact_lock(7123402)`); err != nil {
				return err
			}
		}
		var v string
		err := q.QueryRow(`SELECT value FROM meta WHERE key = 'wall_keys'`).Scan(&v)
		if err == nil && v == "composite" {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, stmt := range contractSQL {
			if _, err := q.Exec(Render(e.PG, stmt)); err != nil {
				return fmt.Errorf("making the keys of the identity tables begin with base_id: %w\n%s", err, stmt)
			}
		}
		var cur string
		_ = q.QueryRow(`SELECT value FROM meta WHERE key = 'schema_min'`).Scan(&cur)
		have, _ := strconv.Atoi(cur)
		if _, err := q.Exec(`INSERT INTO meta(key, value) VALUES('schema_min', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			strconv.Itoa(max(have, minSchema))); err != nil {
			return err
		}
		_, err = q.Exec(`INSERT INTO meta(key, value) VALUES('wall_keys', 'composite') ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
		return err
	})
}

// KeysAreComposite says whether ContractKeys has run.
func (e *Env) KeysAreComposite() (bool, error) {
	var v string
	err := e.DB.QueryRow(`SELECT value FROM meta WHERE key = 'wall_keys'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "composite", err
}

// contractSQL rebuilds each identity table: copy aside, drop, create with the
// new keys and no default for base_id, copy back, drop the copy.
var contractSQL = []string{
	`CREATE TABLE users_old AS SELECT * FROM users`,
	`DROP TABLE users`,
	`CREATE TABLE users (
  base_id      TEXT    NOT NULL,
  username     TEXT    NOT NULL,
  group_list   TEXT    NOT NULL,
  last_login   {{BIG}} NOT NULL,
  display_name TEXT    NOT NULL DEFAULT '',
  email        TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (base_id, username)
)`,
	`INSERT INTO users(base_id, username, group_list, last_login, display_name, email)
  SELECT base_id, username, group_list, last_login, display_name, email FROM users_old`,
	`DROP TABLE users_old`,

	`CREATE TABLE sessions_old AS SELECT * FROM sessions`,
	`DROP TABLE sessions`,
	`CREATE TABLE sessions (
  base_id    TEXT    NOT NULL,
  id_hash    TEXT    NOT NULL,
  username   TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  last_seen  {{BIG}} NOT NULL,
  expires_at {{BIG}} NOT NULL,
  PRIMARY KEY (base_id, id_hash)
)`,
	`INSERT INTO sessions(base_id, id_hash, username, created_at, last_seen, expires_at)
  SELECT base_id, id_hash, username, created_at, last_seen, expires_at FROM sessions_old`,
	`DROP TABLE sessions_old`,
	`CREATE INDEX sessions_expires ON sessions(expires_at)`,
	`CREATE INDEX sessions_base_user ON sessions(base_id, username)`,

	`CREATE TABLE api_tokens_old AS SELECT * FROM api_tokens`,
	`DROP TABLE api_tokens`,
	`CREATE TABLE api_tokens (
  base_id        TEXT    NOT NULL,
  id             TEXT    NOT NULL,
  hash           TEXT    NOT NULL,
  username       TEXT    NOT NULL,
  name           TEXT    NOT NULL,
  created_at     {{BIG}} NOT NULL,
  expires_at     {{BIG}} NOT NULL,
  last_used      {{BIG}} NOT NULL DEFAULT 0,
  role           TEXT    NOT NULL DEFAULT '',
  projects       TEXT    NOT NULL DEFAULT '',
  last_used_from TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (base_id, id),
  UNIQUE (base_id, hash),
  UNIQUE (base_id, username, name)
)`,
	`INSERT INTO api_tokens(base_id, id, hash, username, name, created_at, expires_at, last_used, role, projects, last_used_from)
  SELECT base_id, id, hash, username, name, created_at, expires_at, last_used, role, projects, last_used_from FROM api_tokens_old`,
	`DROP TABLE api_tokens_old`,

	`CREATE TABLE local_accounts_old AS SELECT * FROM local_accounts`,
	`DROP TABLE local_accounts`,
	`CREATE TABLE local_accounts (
  base_id         TEXT    NOT NULL,
  username        TEXT    NOT NULL,
  display_name    TEXT    NOT NULL DEFAULT '',
  email           TEXT    NOT NULL DEFAULT '',
  role            TEXT    NOT NULL,
  password_hash   TEXT    NOT NULL DEFAULT '',
  password_set_at {{BIG}} NOT NULL DEFAULT 0,
  totp_secret     TEXT    NOT NULL DEFAULT '',
  totp_confirmed  INTEGER NOT NULL DEFAULT 0,
  totp_last_step  {{BIG}} NOT NULL DEFAULT 0,
  failed_count    INTEGER NOT NULL DEFAULT 0,
  locked_until    {{BIG}} NOT NULL DEFAULT 0,
  disabled        INTEGER NOT NULL DEFAULT 0,
  break_glass     INTEGER NOT NULL DEFAULT 0,
  created_at      {{BIG}} NOT NULL,
  created_by      TEXT    NOT NULL DEFAULT '',
  last_login_at   {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (base_id, username)
)`,
	// No column list: this copies the (already sealed) second-factor secrets as they are,
	// in the table's own column order, and is not a writer of them in the sense of internal/sealing.
	`INSERT INTO local_accounts SELECT base_id, username, display_name, email, role, password_hash, password_set_at, totp_secret, totp_confirmed,
  totp_last_step, failed_count, locked_until, disabled, break_glass, created_at, created_by, last_login_at FROM local_accounts_old`,
	`DROP TABLE local_accounts_old`,

	`CREATE TABLE local_links_old AS SELECT * FROM local_links`,
	`DROP TABLE local_links`,
	`CREATE TABLE local_links (
  base_id    TEXT    NOT NULL,
  token_hash TEXT    NOT NULL,
  username   TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  role       TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  created_by TEXT    NOT NULL DEFAULT '',
  expires_at {{BIG}} NOT NULL,
  used_at    {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (base_id, token_hash)
)`,
	`INSERT INTO local_links(base_id, token_hash, username, kind, role, created_at, created_by, expires_at, used_at)
  SELECT base_id, token_hash, username, kind, role, created_at, created_by, expires_at, used_at FROM local_links_old`,
	`DROP TABLE local_links_old`,
	`CREATE INDEX local_links_user ON local_links(base_id, username)`,

	`CREATE TABLE local_recovery_codes_old AS SELECT * FROM local_recovery_codes`,
	`DROP TABLE local_recovery_codes`,
	`CREATE TABLE local_recovery_codes (
  base_id   TEXT    NOT NULL,
  username  TEXT    NOT NULL,
  code_hash TEXT    NOT NULL,
  used_at   {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (base_id, username, code_hash)
)`,
	`INSERT INTO local_recovery_codes(base_id, username, code_hash, used_at)
  SELECT base_id, username, code_hash, used_at FROM local_recovery_codes_old`,
	`DROP TABLE local_recovery_codes_old`,
}
