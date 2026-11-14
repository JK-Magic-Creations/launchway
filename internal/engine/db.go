package engine

import (
	"database/sql"
	"fmt"
	"net/url"
	"strconv"

	_ "modernc.org/sqlite" // pure Go, so the binary needs no C library
)

// openDB opens (creating if needed) the SQLite database at path and brings
// its schema up to date.
func openDB(path string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"busy_timeout(10000)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrations are applied in order, once each. Never edit one that has
// shipped; add a new one.
var migrations = []string{
	// 1: the event log and the tables derived from it.
	`
CREATE TABLE events (
  seq    INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT    NOT NULL,
  type   TEXT    NOT NULL,
  at     INTEGER NOT NULL,
  data   TEXT    NOT NULL
);
CREATE INDEX events_run ON events(run_id, seq);

CREATE TABLE runs (
  id               TEXT PRIMARY KEY,
  project          TEXT    NOT NULL,
  number           INTEGER NOT NULL,
  pipeline         TEXT    NOT NULL,
  status           TEXT    NOT NULL,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  event            TEXT    NOT NULL DEFAULT '',
  ref              TEXT    NOT NULL DEFAULT '',
  branch           TEXT    NOT NULL DEFAULT '',
  tag              TEXT    NOT NULL DEFAULT '',
  sha              TEXT    NOT NULL DEFAULT '',
  actor            TEXT    NOT NULL DEFAULT '',
  message          TEXT    NOT NULL DEFAULT '',
  author           TEXT    NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  started_at       INTEGER NOT NULL DEFAULT 0,
  finished_at      INTEGER NOT NULL DEFAULT 0,
  last_seq         INTEGER NOT NULL,
  UNIQUE(project, number)
);
CREATE INDEX runs_created ON runs(created_at DESC);
CREATE INDEX runs_project ON runs(project, number DESC);

CREATE TABLE steps (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  position    INTEGER NOT NULL,
  status      TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  queued_at   INTEGER NOT NULL DEFAULT 0,
  started_at  INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  exit_code   INTEGER,
  error       TEXT    NOT NULL DEFAULT '',
  skip_kind   TEXT    NOT NULL DEFAULT '',
  runner      TEXT    NOT NULL DEFAULT '',
  lease_id    TEXT    NOT NULL DEFAULT '',
  outputs     TEXT    NOT NULL DEFAULT '{}',
  PRIMARY KEY (run_id, step_id)
);
CREATE INDEX steps_status ON steps(status, queued_at);

CREATE TABLE logs (
  run_id  TEXT    NOT NULL,
  step_id TEXT    NOT NULL,
  attempt INTEGER NOT NULL,
  seq     INTEGER NOT NULL,
  at      INTEGER NOT NULL,
  stream  TEXT    NOT NULL,
  text    TEXT    NOT NULL,
  PRIMARY KEY (run_id, step_id, attempt, seq)
);

CREATE TABLE runners (
  id        TEXT PRIMARY KEY,
  name      TEXT    NOT NULL,
  executor  TEXT    NOT NULL DEFAULT '',
  version   TEXT    NOT NULL DEFAULT '',
  last_seen INTEGER NOT NULL
);

CREATE TABLE refs_seen (
  project TEXT NOT NULL,
  ref     TEXT NOT NULL,
  sha     TEXT NOT NULL,
  PRIMARY KEY (project, ref)
);
`,
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	current, _ := strconv.Atoi(v)
	if current > len(migrations) {
		return fmt.Errorf("the database was written by a newer version of slipway (schema %d, this build knows %d)", current, len(migrations))
	}
	for i := current; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.Itoa(i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
