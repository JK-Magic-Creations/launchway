package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The notices of people (schema 60): notices, notice_prefs, notice_watch and
// notice_people. Pending tables (stage S2); the statements run on the
// connection the caller hands in.

// NoticeRow is one notice in a person's inbox.
type NoticeRow struct {
	ID, Kind, Repo, Subject, Title, Detail, Link, Actor, MailState string
	Restricted                                                     bool
	CreatedAt, ReadAt, DoneAt                                      int64
}

const noticeCols = `id, kind, repo, subject, title, detail, link, actor, restricted, mail_state, created_at, read_at, done_at`

func scanNoticeRow(r interface{ Scan(...any) error }) (NoticeRow, error) {
	var n NoticeRow
	var restricted int
	err := r.Scan(&n.ID, &n.Kind, &n.Repo, &n.Subject, &n.Title, &n.Detail, &n.Link, &n.Actor, &restricted, &n.MailState, &n.CreatedAt, &n.ReadAt, &n.DoneAt)
	n.Restricted = restricted != 0
	return n, err
}

// AddNotice stores a notice for a person.
func AddNotice(ctx context.Context, db CtxDB, person string, n NoticeRow, dedupe string) error {
	r := 0
	if n.Restricted {
		r = 1
	}
	_, err := db.Exec(ctx, `INSERT INTO notices(id, kind, repo, subject, title, detail, link, actor, restricted, mail_state, recipient, dedupe, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Kind, n.Repo, n.Subject, n.Title, n.Detail, n.Link, n.Actor, r, n.MailState, person, dedupe, n.CreatedAt)
	return err
}

// NoticeSeen says whether a person has a notice with the dedupe key.
func NoticeSeen(ctx context.Context, db CtxDB, person, dedupe string) bool {
	var n int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE recipient = ? AND dedupe = ?`, person, dedupe).Scan(&n)
	return n > 0
}

// ListNotices lists a person's notices, newest first. state is unread, open,
// done or all; kind may be empty.
func ListNotices(ctx context.Context, db CtxDB, person, state, kind string, limit int) ([]NoticeRow, error) {
	q := `SELECT ` + noticeCols + ` FROM notices WHERE recipient = ?`
	args := []any{person}
	switch state {
	case "unread":
		q += ` AND read_at = 0 AND done_at = 0`
	case "done":
		q += ` AND done_at <> 0`
	case "all":
	default:
		q += ` AND done_at = 0`
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	rows, err := db.Query(ctx, q+fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoticeRow
	for rows.Next() {
		n, err := scanNoticeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoticeCounts counts a person's unread and open (not done) notices.
func NoticeCounts(ctx context.Context, db CtxDB, person string) (unread, open int, err error) {
	if err = db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE recipient = ? AND done_at = 0 AND read_at = 0`, person).Scan(&unread); err != nil {
		return
	}
	err = db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE recipient = ? AND done_at = 0`, person).Scan(&open)
	return
}

// MarkNoticeRead sets the read mark of a person's notice to a time, or clears
// it with 0; setting leaves an earlier mark alone. It returns how many rows
// changed.
func MarkNoticeRead(ctx context.Context, db CtxDB, person, id string, at int64) (int64, error) {
	q := `UPDATE notices SET read_at = ? WHERE id = ? AND recipient = ?`
	if at != 0 {
		q += ` AND read_at = 0`
	}
	res, err := db.Exec(ctx, q, at, id, person)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkNoticeDone is MarkNoticeRead for the done mark.
func MarkNoticeDone(ctx context.Context, db CtxDB, person, id string, at int64) (int64, error) {
	q := `UPDATE notices SET done_at = ? WHERE id = ? AND recipient = ?`
	if at != 0 {
		q += ` AND done_at = 0`
	}
	res, err := db.Exec(ctx, q, at, id, person)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetNoticeMailState records what became of a notice's mail.
func SetNoticeMailState(ctx context.Context, db CtxDB, id, state string) {
	_, _ = db.Exec(ctx, `UPDATE notices SET mail_state = ? WHERE id = ?`, state, id)
}

// DigestNotices lists a person's notices that wait for the digest.
func DigestNotices(ctx context.Context, db CtxDB, person string) ([]NoticeRow, error) {
	rows, err := db.Query(ctx, `SELECT `+noticeCols+` FROM notices WHERE recipient = ? AND mail_state = 'digest' ORDER BY created_at LIMIT 100`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoticeRow
	for rows.Next() {
		n, err := scanNoticeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoticePrefs returns the channels a person chose, by kind.
func NoticePrefs(ctx context.Context, db CtxDB, person string) (map[string]string, error) {
	rows, err := db.Query(ctx, `SELECT kind, channel FROM notice_prefs WHERE person = ?`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, c string
		if err := rows.Scan(&k, &c); err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, rows.Err()
}

// SetNoticePref replaces a person's channel for a kind.
func SetNoticePref(ctx context.Context, db CtxDB, person, kind, channel string) error {
	if _, err := db.Exec(ctx, `DELETE FROM notice_prefs WHERE person = ? AND kind = ?`, person, kind); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `INSERT INTO notice_prefs(person, kind, channel) VALUES(?,?,?)`, person, kind, channel)
	return err
}

// WatchRow is a person's level on a scope.
type WatchRow struct{ Scope, Level string }

// NoticeWatches lists a person's watches.
func NoticeWatches(ctx context.Context, db CtxDB, person string) ([]WatchRow, error) {
	rows, err := db.Query(ctx, `SELECT scope, level FROM notice_watch WHERE person = ? ORDER BY scope`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchRow
	for rows.Next() {
		var w WatchRow
		if err := rows.Scan(&w.Scope, &w.Level); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetNoticeWatch replaces a person's level on a scope; an empty level
// removes the watch. At most limit watches are kept for a person.
func SetNoticeWatch(ctx context.Context, db CtxDB, person, scope, level string, limit int) error {
	if _, err := db.Exec(ctx, `DELETE FROM notice_watch WHERE person = ? AND scope = ?`, person, scope); err != nil || level == "" {
		return err
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM notice_watch WHERE person = ?`, person).Scan(&n); err != nil {
		return err
	}
	if n >= limit {
		return ErrTooManyWatches
	}
	_, err := db.Exec(ctx, `INSERT INTO notice_watch(person, scope, level) VALUES(?,?,?)`, person, scope, level)
	return err
}

// ErrTooManyWatches is returned when a person has the most watches allowed.
var ErrTooManyWatches = errors.New("too many watches")

// NoticeWatchLevel returns a person's level on a scope, if they chose one.
func NoticeWatchLevel(ctx context.Context, db CtxDB, person, scope string) (string, bool, error) {
	var l string
	err := db.QueryRow(ctx, `SELECT level FROM notice_watch WHERE person = ? AND scope = ?`, person, scope).Scan(&l)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return l, err == nil, err
}

// NoticeWatchers lists the people who watch a scope at level all.
func NoticeWatchers(ctx context.Context, db CtxDB, scope string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT person FROM notice_watch WHERE scope = ? AND level = 'all' ORDER BY person LIMIT 1000`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PersonRow is a person's quiet hours and digest.
type PersonRow struct {
	QuietStart, QuietEnd, DigestHour int
	Zone                             string
	Digest                           bool
	LastDigestAt                     int64
}

// NoticePerson returns a person's row, if they chose anything.
func NoticePerson(ctx context.Context, db CtxDB, person string) (PersonRow, bool, error) {
	var r PersonRow
	var d int
	err := db.QueryRow(ctx, `SELECT quiet_start, quiet_end, zone, digest, digest_hour, last_digest_at FROM notice_people WHERE person = ?`, person).Scan(&r.QuietStart, &r.QuietEnd, &r.Zone, &d, &r.DigestHour, &r.LastDigestAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	r.Digest = d != 0
	return r, err == nil, err
}

// SetNoticePerson replaces a person's row, keeping when the last digest went.
func SetNoticePerson(ctx context.Context, db CtxDB, person string, r PersonRow) error {
	if old, ok, err := NoticePerson(ctx, db, person); err != nil {
		return err
	} else if ok {
		r.LastDigestAt = old.LastDigestAt
	}
	if _, err := db.Exec(ctx, `DELETE FROM notice_people WHERE person = ?`, person); err != nil {
		return err
	}
	d := 0
	if r.Digest {
		d = 1
	}
	_, err := db.Exec(ctx, `INSERT INTO notice_people(person, quiet_start, quiet_end, zone, digest, digest_hour, last_digest_at) VALUES(?,?,?,?,?,?,?)`,
		person, r.QuietStart, r.QuietEnd, r.Zone, d, r.DigestHour, r.LastDigestAt)
	return err
}

// DigestPeople lists the people who chose a daily digest.
func DigestPeople(ctx context.Context, db CtxDB) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT person FROM notice_people WHERE digest = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetLastDigest records when a person's digest last went.
func SetLastDigest(ctx context.Context, db CtxDB, person string, at int64) {
	_, _ = db.Exec(ctx, `UPDATE notice_people SET last_digest_at = ? WHERE person = ?`, at, person)
}

// A credential that ends soon, for the warning.
type ExpiringCredential struct {
	Owner, What, Name string
	At                int64
}

// ExpiringCredentials lists the SSH keys, signing keys and API tokens of people that end between two times (Unix ms).
func ExpiringCredentials(ctx context.Context, db CtxDB, from, to int64) []ExpiringCredential {
	out := expiring(ctx, db, "SSH key", `SELECT owner, name, expires_at FROM ssh_keys WHERE kind = 'user' AND expires_at > ? AND expires_at < ?`, from, to)
	out = append(out, expiring(ctx, db, "signing key", `SELECT owner, name, expires_at FROM signing_keys WHERE expires_at > ? AND expires_at < ? AND revoked_at = 0`, from, to)...)
	// A token that never ends has expires_at 0 and is not listed. Only tokens of people are warned about: a service
	// account's tokens have no one to tell here (they are listed with the account).
	return append(out, expiring(ctx, db, "API token", `SELECT t.username, t.name, t.expires_at FROM api_tokens t WHERE t.expires_at > ? AND t.expires_at < ?
		AND EXISTS (SELECT 1 FROM users u WHERE u.base_id = t.base_id AND u.username = t.username)`, from, to)...)
}

func expiring(ctx context.Context, db CtxDB, what, q string, from, to int64) []ExpiringCredential {
	rows, err := db.Query(ctx, q, from, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ExpiringCredential
	for rows.Next() {
		c := ExpiringCredential{What: what}
		if rows.Scan(&c.Owner, &c.Name, &c.At) == nil {
			out = append(out, c)
		}
	}
	_ = rows.Err()
	return out
}
