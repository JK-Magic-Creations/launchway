package engine

// Schema 38, delivery: pipeline identities that may write to a protected
// branch of a hosted repository (the GitOps write-back), and notifications.

// schemaDelivery is the schema that brought them.
const schemaDelivery = 38

func init() {
	if len(migrations) != schemaDelivery-1 {
		panic("the delivery migration (38) must follow migrations 1 to 37")
	}
	migrations = append(migrations, deliveryMigration)
}

// 38: delivery.
//
//	branch_writers admits a pipeline identity (a deploy step's short-lived
//	credential) to write to a branch that is protected: for the branches
//	matching branch, the projects matching projects may change exactly the
//	files matching files, and nothing else. One row per rule.
//	notifications is the log of what notifications were sent where
//	(internal/notify): one row per event and channel, with its attempts
//	and what went wrong, never the address or key of a channel. The pair
//	(event_key, channel) is unique, so several servers never send one
//	message twice. notify_state keeps how far the events were read.
//	Compatibility: additive: new tables. Nothing older code reads changes.
//	Downgrade: nothing to undo for an older server; the tables stay and are
//	ignored (an older server then admits no pipeline identity, which is
//	the safe way to fail).
var deliveryMigration = migration{additive, `
CREATE TABLE branch_writers (
  id         TEXT PRIMARY KEY,
  repo_id    TEXT    NOT NULL,
  branch     TEXT    NOT NULL,
  projects   TEXT    NOT NULL,
  files      TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  created_by TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX branch_writers_repo ON branch_writers(repo_id);

CREATE TABLE notifications (
  id         TEXT PRIMARY KEY,
  event_key  TEXT    NOT NULL,
  channel    TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  event      TEXT    NOT NULL,
  project    TEXT    NOT NULL DEFAULT '',
  title      TEXT    NOT NULL,
  body       TEXT    NOT NULL,
  status     TEXT    NOT NULL DEFAULT 'pending',
  attempts   {{BIG}} NOT NULL DEFAULT 0,
  next_at    {{BIG}} NOT NULL DEFAULT 0,
  last_error TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  sent_at    {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX notifications_once ON notifications(event_key, channel);
CREATE INDEX notifications_due ON notifications(status, next_at);

CREATE TABLE notify_state (
  name  TEXT PRIMARY KEY,
  value {{BIG}} NOT NULL
);
`}
