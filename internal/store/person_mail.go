package store

import (
	"context"
	"errors"
)

// The outgoing mail queue (schema 60): mail_queue. A message's text is kept
// only while it waits; once it is sent or has failed for good the text is
// emptied, so the table holds who was written to and what became of it, never
// what was said.

// ErrMailQueueFull is returned when too many messages wait.
var ErrMailQueueFull = errors.New("too many messages are waiting to be sent")

// MailRow is a message in the queue.
type MailRow struct {
	ID, Person, To, Kind, Subject, Text, HTML string
	Attempts                                  int
}

// EnqueueMail stores a message, unless max messages already wait.
func EnqueueMail(ctx context.Context, db CtxDB, m MailRow, nextAt, now int64, max int) error {
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mail_queue WHERE state = 'pending'`).Scan(&n); err != nil {
		return err
	}
	if n >= max {
		return ErrMailQueueFull
	}
	_, err := db.Exec(ctx, `INSERT INTO mail_queue(id, recipient, to_addr, kind, subject, text_body, html_body, state, attempts, next_at, created_at) VALUES(?,?,?,?,?,?,?,'pending',0,?,?)`,
		m.ID, m.Person, m.To, m.Kind, m.Subject, m.Text, m.HTML, nextAt, now)
	return err
}

// MailCounts is what the queue holds.
type MailCounts struct{ Pending, Sent, Failed, SentSince int }

// CountMail counts messages by state, and those sent since a time.
func CountMail(ctx context.Context, db CtxDB, since int64) (MailCounts, error) {
	var c MailCounts
	rows, err := db.Query(ctx, `SELECT state, count(*) FROM mail_queue GROUP BY state`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return c, err
		}
		switch st {
		case "pending":
			c.Pending = n
		case "sent":
			c.Sent = n
		case "failed":
			c.Failed = n
		}
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	c.SentSince, err = MailSentSince(ctx, db, since)
	return c, err
}

// MailSentSince counts messages sent after a time (Unix ms).
func MailSentSince(ctx context.Context, db CtxDB, since int64) (n int, err error) {
	err = db.QueryRow(ctx, `SELECT count(*) FROM mail_queue WHERE state = 'sent' AND sent_at > ?`, since).Scan(&n)
	return
}

// DueMail lists the messages whose time has come, oldest first.
func DueMail(ctx context.Context, db CtxDB, now int64, limit int) ([]MailRow, error) {
	rs, err := db.Query(ctx, `SELECT id, recipient, to_addr, kind, subject, text_body, html_body, attempts FROM mail_queue WHERE state = 'pending' AND next_at <= ? ORDER BY created_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []MailRow
	for rs.Next() {
		var r MailRow
		if err := rs.Scan(&r.ID, &r.Person, &r.To, &r.Kind, &r.Subject, &r.Text, &r.HTML, &r.Attempts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// MailSent records a message as sent and forgets its text.
func MailSent(ctx context.Context, db CtxDB, id string, attempts int, at int64) {
	_, _ = db.Exec(ctx, `UPDATE mail_queue SET state = 'sent', attempts = ?, sent_at = ?, text_body = '', html_body = '', last_error = '' WHERE id = ?`, attempts, at, id)
}

// MailFailed records a message as given up on and forgets its text.
func MailFailed(ctx context.Context, db CtxDB, id string, attempts int, reason string) {
	_, _ = db.Exec(ctx, `UPDATE mail_queue SET state = 'failed', attempts = ?, text_body = '', html_body = '', last_error = ? WHERE id = ?`, attempts, reason, id)
}

// MailRetry records a failed try and when the next one is.
func MailRetry(ctx context.Context, db CtxDB, id string, attempts int, nextAt int64, reason string) {
	_, _ = db.Exec(ctx, `UPDATE mail_queue SET attempts = ?, next_at = ?, last_error = ? WHERE id = ?`, attempts, nextAt, reason, id)
}

// MailRow returns a queued message's state, its tries and its text, for tests and the settings page.
func MailState(ctx context.Context, db CtxDB, id string) (state string, attempts int, text, lastError string, err error) {
	err = db.QueryRow(ctx, `SELECT state, attempts, text_body, last_error FROM mail_queue WHERE id = ?`, id).Scan(&state, &attempts, &text, &lastError)
	return
}

// PruneMail deletes finished messages created before a time.
func PruneMail(ctx context.Context, db CtxDB, before int64) {
	_, _ = db.Exec(ctx, `DELETE FROM mail_queue WHERE state <> 'pending' AND created_at < ?`, before)
}
