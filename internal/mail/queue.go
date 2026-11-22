package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// DB is what the queue needs of a database (the SQL is in internal/store).
type DB = store.CtxDB

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
var ErrQueueFull = store.ErrMailQueueFull

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
	limit := q.Max
	if limit <= 0 {
		limit = MaxPending
	}
	next := int64(0)
	if !it.NotBefore.IsZero() {
		next = it.NotBefore.UnixMilli()
	}
	id := ids.New("mal")
	err := store.EnqueueMail(ctx, q.DB, store.MailRow{ID: id, Person: it.Person, To: it.To, Kind: it.Kind, Subject: clip(it.Subject, 300), Text: it.Text, HTML: it.HTML}, next, q.now().UnixMilli(), limit)
	return id, err
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Counts is what the queue holds, for the settings page.
type Counts struct {
	Pending, Sent, Failed int
	SentLastHour          int
}

// Counts returns the queue's numbers.
func (q *Queue) Counts(ctx context.Context) (Counts, error) {
	c, err := store.CountMail(ctx, q.DB, q.now().Add(-time.Hour).UnixMilli())
	return Counts{Pending: c.Pending, Sent: c.Sent, Failed: c.Failed, SentLastHour: c.SentSince}, err
}

// sentSince counts messages sent since a time.
func (q *Queue) sentSince(ctx context.Context, t time.Time) (int, error) {
	return store.MailSentSince(ctx, q.DB, t.UnixMilli())
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
	due, err := store.DueMail(ctx, q.DB, now.UnixMilli(), batch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, r := range due {
		ok, err := q.Allowed(ctx)
		if err != nil {
			return sent, err
		}
		if !ok {
			break
		}
		err = q.Sender.Send(ctx, Message{To: r.To, Subject: r.Subject, Text: r.Text, HTML: r.HTML, ID: r.ID, Headers: headersFor(r.Text)})
		at := r.Attempts + 1
		switch {
		case err == nil:
			sent++
			store.MailSent(ctx, q.DB, r.ID, at, q.now().UnixMilli())
			q.audit(ctx, "mail.sent", r.Person, fmt.Sprintf("%s %s, try %d", r.ID, r.Kind, at), "ok")
		case at >= q.maxAttempts():
			store.MailFailed(ctx, q.DB, r.ID, at, clip(err.Error(), 300))
			q.audit(ctx, "mail.failed", r.Person, fmt.Sprintf("%s %s gave up after %d tries", r.ID, r.Kind, at), "error")
		default:
			store.MailRetry(ctx, q.DB, r.ID, at, q.now().Add(q.backoff(at)).UnixMilli(), clip(err.Error(), 300))
			q.audit(ctx, "mail.retry", r.Person, fmt.Sprintf("%s %s try %d failed", r.ID, r.Kind, at), "error")
		}
	}
	store.PruneMail(ctx, q.DB, now.Add(-keepFinished).UnixMilli())
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
