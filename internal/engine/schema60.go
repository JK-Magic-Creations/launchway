package engine

// Schema 60: notifications people receive (see docs/guides/notifications-for-people.md):
// the inbox, each person's preferences and watches, and the outgoing mail queue.

// schemaNotices is the schema that brought the person-to-person notifications.
const schemaNotices = 60

func init() {
	if len(migrations) != schemaNotices-1 {
		panic("migration 60 must follow migrations 1 to 59")
	}
	migrations = append(migrations, noticesMigration)
}

// 60: notices, one row per notification in a person's inbox (kind says what
//
//	happened; repo and link say where; restricted says the repository is private,
//	so that mail carries less; mail_state says what became of the e-mail:
//	empty (none wanted), queued, digest (waiting for the person's daily digest),
//	sent, or skipped; dedupe stops a time-based notice from repeating);
//	notice_prefs, a person's choice of in-app, email or off per kind;
//	notice_watch, a person's watch level (all, participating, none) per
//	repository or Flight; notice_people, quiet hours and the digest;
//	mail_queue, the messages waiting to go out, with their tries (the body is
//	emptied once a message is sent or has failed for good); mail_meta, the
//	installation's counters.
//	Compatibility: additive: new tables only, ignored by an older server.
//	Downgrade: nothing to undo.
var noticesMigration = migration{additive, `
CREATE TABLE notices (
  id         TEXT PRIMARY KEY,
  recipient  TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  repo       TEXT    NOT NULL DEFAULT '',
  subject    TEXT    NOT NULL DEFAULT '',
  title      TEXT    NOT NULL,
  detail     TEXT    NOT NULL DEFAULT '',
  link       TEXT    NOT NULL DEFAULT '',
  actor      TEXT    NOT NULL DEFAULT '',
  restricted INTEGER NOT NULL DEFAULT 0,
  mail_state TEXT    NOT NULL DEFAULT '',
  dedupe     TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  read_at    {{BIG}} NOT NULL DEFAULT 0,
  done_at    {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX notices_by_recipient ON notices(recipient, done_at, created_at);
CREATE INDEX notices_by_dedupe ON notices(recipient, dedupe);
CREATE INDEX notices_by_mail ON notices(mail_state, recipient);
CREATE TABLE notice_prefs (
  person  TEXT NOT NULL,
  kind    TEXT NOT NULL,
  channel TEXT NOT NULL,
  PRIMARY KEY (person, kind)
);
CREATE TABLE notice_watch (
  person TEXT NOT NULL,
  scope  TEXT NOT NULL,
  level  TEXT NOT NULL,
  PRIMARY KEY (person, scope)
);
CREATE TABLE notice_people (
  person         TEXT PRIMARY KEY,
  quiet_start    INTEGER NOT NULL DEFAULT -1,
  quiet_end      INTEGER NOT NULL DEFAULT -1,
  zone           TEXT    NOT NULL DEFAULT 'UTC',
  digest         INTEGER NOT NULL DEFAULT 0,
  digest_hour    INTEGER NOT NULL DEFAULT 8,
  last_digest_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE TABLE mail_queue (
  id         TEXT PRIMARY KEY,
  recipient  TEXT    NOT NULL,
  to_addr    TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  subject    TEXT    NOT NULL,
  text_body  TEXT    NOT NULL DEFAULT '',
  html_body  TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT 'pending',
  attempts   INTEGER NOT NULL DEFAULT 0,
  next_at    {{BIG}} NOT NULL DEFAULT 0,
  last_error TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  sent_at    {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX mail_queue_due ON mail_queue(state, next_at);
`}
