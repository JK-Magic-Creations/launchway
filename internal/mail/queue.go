package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the queue needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Queue limits.
const (
	MaxPending         = 5000 // messages waiting at once; more are refused
	DefaultMaxAttempts = 5
	DefaultPerMinute   = 30
	DefaultPerHour     = 300
	keepFinished       = 30 * 24 * time.Hour // sent and failed rows (without bodies) are kept this long
	batch              = 20
	maxBody            = 200 << 10
)

// ErrQueueFull is returned when too many messages already wait.
var ErrQueueFull = errors.New("too many messages are waiting to be sent")

// Queue holds the messages waiting to go out, with their tries.
type Queue struct {
	DB     DB
	Sender *Sender
	Now    func() time.Time
	// PerMinute and PerHour limit the installation's sending (0: the defaults).
	PerMinute, PerHour int
	MaxAttempts        int
	// MaxPending bounds the messages waiting (0: MaxPending).
	Max int
	// Audit is told of every message sent, retried or given up on: what, to
	// whom (the person), and how it went; never the subject or the body.
	Audit func(ctx context.Context, action, person, detail, outcome string)
	// Backoff is the wait before try n+1 (default 1m, 5m, 30m, 2h).
	Backoff func(attempts int) time.Duration
}

func (q *Queue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func (q *Queue) maxAttempts() int {
	if q.MaxAttempts > 0 {
		return q.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (q *Queue) limits() (int, int) {
	pm, ph := q.PerMinute, q.PerHour
	if pm <= 0 {
		pm = DefaultPerMinute
	}
	if ph <= 0 {
		ph = DefaultPerHour
	}
	return pm, ph
}

func (q *Queue) backoff(n int) time.Duration {
	if q.Backoff != nil {
		return q.Backoff(n)
	}
	steps := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	return steps[min(n-1, len(steps)-1)]
}

// Item is a message to queue.
type Item struct {
	Person  string // who it is for (the audit names them, not the address)
	To      string
	Kind    string
	Subject string
	Text    string
	HTML    string
	Headers map[string]string
	// NotBefore delays the first try (quiet hours).
	NotBefore time.Time
}

// Enqueue stores a message to be sent.
func (q *Queue) Enqueue(ctx context.Context, it Item) (string, error) {
	if len(it.Text)+len(it.HTML) > maxBody {
		return "", errors.New("the message is too long")
	}
	var n int
	if err := q.DB.QueryRow(ctx, `SELECT count(*) FROM mail_queue WHERE state = 'pending'`).Scan(&n); err != nil {
		return "", err
	}
	limit := q.Max
	if limit <= 0 {
		limit = MaxPending
	}
	if n >= limit {
		return "", ErrQueueFull
	}
	id := ids.New("mal")
	next := int64(0)
	if !it.NotBefore.IsZero() {
		next = it.NotBefore.UnixMilli()
	}
	// Headers travel in the html column's sibling-free way: they are few and
	// fixed by kind, so they are rebuilt when sending (see headersFor).
	_, err := q.DB.Exec(ctx, `INSERT INTO mail_queue(id, recipient, to_addr, kind, subject, text_body, html_body, state, attempts, next_at, created_at) VALUES(?,?,?,?,?,?,?,'pending',0,?,?)`,
		id, it.Person, it.To, it.Kind, clip(it.Subject, 300), it.Text, it.HTML, next, q.now().UnixMilli())
	return id, err
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type row struct {
	id, person, to, kind, subject, text, html string
	attempts                                  int
}

// Counts is what the queue holds, for the settings page.
type Counts struct {
	Pending, Sent, Failed int
	SentLastHour          int
}

// Counts returns the queue's numbers.
func (q *Queue) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	rows, err := q.DB.Query(ctx, `SELECT state, count(*) FROM mail_queue GROUP BY state`)
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
	err = q.DB.QueryRow(ctx, `SELECT count(*) FROM mail_queue WHERE state = 'sent' AND sent_at > ?`, q.now().Add(-time.Hour).UnixMilli()).Scan(&c.SentLastHour)
	return c, err
}

// sentSince counts messages sent since a time.
func (q *Queue) sentSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := q.DB.QueryRow(ctx, `SELECT count(*) FROM mail_queue WHERE state = 'sent' AND sent_at > ?`, t.UnixMilli()).Scan(&n)
	return n, err
}

// Allowed says whether the installation's limits leave room for one more
// message now.
func (q *Queue) Allowed(ctx context.Context) (bool, error) {
	pm, ph := q.limits()
	for _, w := range []struct {
		d time.Duration
		n int
	}{{time.Minute, pm}, {time.Hour, ph}} {
		got, err := q.sentSince(ctx, q.now().Add(-w.d))
		if err != nil {
			return false, err
		}
		if got >= w.n {
			return false, nil
		}
	}
	return true, nil
}

// RunOnce sends what is due and within the limits, and reports how many were
// sent. Messages over the limit stay waiting for the next pass.
func (q *Queue) RunOnce(ctx context.Context) (int, error) {
	if q.Sender == nil || !q.Sender.Config.Enabled() {
		return 0, nil
	}
	now := q.now()
	rs, err := q.DB.Query(ctx, `SELECT id, recipient, to_addr, kind, subject, text_body, html_body, attempts FROM mail_queue WHERE state = 'pending' AND next_at <= ? ORDER BY created_at, id LIMIT ?`, now.UnixMilli(), batch)
	if err != nil {
		return 0, err
	}
	var due []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.person, &r.to, &r.kind, &r.subject, &r.text, &r.html, &r.attempts); err != nil {
			rs.Close()
			return 0, err
		}
		due = append(due, r)
	}
	rs.Close()
	sent := 0
	for _, r := range due {
		ok, err := q.Allowed(ctx)
		if err != nil {
			return sent, err
		}
		if !ok {
			break
		}
		err = q.Sender.Send(ctx, Message{To: r.to, Subject: r.subject, Text: r.text, HTML: r.html, ID: r.id, Headers: headersFor(r.text)})
		at := r.attempts + 1
		switch {
		case err == nil:
			sent++
			_, _ = q.DB.Exec(ctx, `UPDATE mail_queue SET state = 'sent', attempts = ?, sent_at = ?, text_body = '', html_body = '', last_error = '' WHERE id = ?`, at, q.now().UnixMilli(), r.id)
			q.audit(ctx, "mail.sent", r.person, fmt.Sprintf("%s %s, try %d", r.id, r.kind, at), "ok")
		case at >= q.maxAttempts():
			_, _ = q.DB.Exec(ctx, `UPDATE mail_queue SET state = 'failed', attempts = ?, text_body = '', html_body = '', last_error = ? WHERE id = ?`, at, clip(err.Error(), 300), r.id)
			q.audit(ctx, "mail.failed", r.person, fmt.Sprintf("%s %s gave up after %d tries", r.id, r.kind, at), "error")
		default:
			_, _ = q.DB.Exec(ctx, `UPDATE mail_queue SET attempts = ?, next_at = ?, last_error = ? WHERE id = ?`, at, q.now().Add(q.backoff(at)).UnixMilli(), clip(err.Error(), 300), r.id)
			q.audit(ctx, "mail.retry", r.person, fmt.Sprintf("%s %s try %d failed", r.id, r.kind, at), "error")
		}
	}
	_, _ = q.DB.Exec(ctx, `DELETE FROM mail_queue WHERE state <> 'pending' AND created_at < ?`, now.Add(-keepFinished).UnixMilli())
	return sent, nil
}

func (q *Queue) audit(ctx context.Context, action, person, detail, outcome string) {
	if q.Audit != nil {
		q.Audit(ctx, action, person, detail, outcome)
	}
}

// Run sends in the background until the context ends.
func (q *Queue) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 15 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_, _ = q.RunOnce(ctx)
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// SendTest sends one message to an address now, outside the queue, and says
// how it went. It counts against nothing and is audited by the caller.
func (q *Queue) SendTest(ctx context.Context, to string) error {
	if q.Sender == nil {
		return ErrNotConfigured
	}
	return q.Sender.Send(ctx, Message{To: to, Subject: "Launchway test message",
		Text: "This is a test message from your Launchway installation.\n\nIf you can read this, the mail settings work.\n",
		HTML: "<p>This is a test message from your Launchway installation.</p><p>If you can read this, the mail settings work.</p>\n"})
}

// headersFor pulls the List-Unsubscribe link out of a body's footer line, so
// mail clients can offer one-click mute (RFC 8058).
func headersFor(text string) map[string]string {
	const marker = "Mute this kind of message: "
	i := strings.Index(text, marker)
	if i < 0 {
		return nil
	}
	link := strings.Fields(text[i+len(marker):])
	if len(link) == 0 || !strings.HasPrefix(link[0], "https://") {
		return nil
	}
	return map[string]string{"List-Unsubscribe": "<" + link[0] + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}
}
