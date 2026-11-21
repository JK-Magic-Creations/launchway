package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/notify"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// The notification service: it notices what needs a person or went wrong,
// applies the notification rules (internal/notify), and delivers messages
// with a log and retries. Every server runs it; a message to one channel
// about one event is made once (a unique row), whichever server notices
// first, and delivered by whichever server claims it.

// NotifySecrets reads the references of a channel (its address, its key)
// from the server's own secret stores.
type NotifySecrets interface {
	Lookup(ctx context.Context, project string, ref pipeline.SecretRef) (string, error)
}

// Notifier is what RunNotifications needs besides the engine.
type Notifier struct {
	Secrets NotifySecrets
	// BaseURL is the address people open Launchway at, for the links.
	BaseURL string
	// Client makes the calls; it should refuse addresses inside the server's
	// own network (egress.Guard) unless an administrator allowed them.
	Client *http.Client
	Log    *slog.Logger
	// Every is how often events are looked at; default 5s.
	Every time.Duration
}

const (
	notifyEvents = `'run.finished','deploy.closed'`
	notifyCursor = "events"
)

// RunNotifications notices events and delivers messages until ctx ends.
func (e *Engine) RunNotifications(ctx context.Context, n Notifier) {
	if !e.knows(schemaDelivery) {
		return
	}
	notifyBases.Store(e, n.BaseURL)
	if n.Every <= 0 {
		n.Every = 5 * time.Second
	}
	if n.Log == nil {
		n.Log = slog.Default()
	}
	t := time.NewTicker(n.Every)
	defer t.Stop()
	for {
		e.notifyOnce(ctx, &n)
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// notifyOnce notices what is new and delivers what is due.
func (e *Engine) notifyOnce(ctx context.Context, n *Notifier) {
	if n.Log == nil {
		n.Log = slog.Default()
	}
	cfg := e.live().notify
	if len(cfg.Rules) > 0 {
		for i, f := range []func(context.Context, *Notifier) ([]notify.Event, error){e.eventsFromLog, e.eventsWaiting, e.eventsRollbackReviews} {
			evs, err := f(ctx, n)
			if err != nil {
				n.Log.Warn("could not read what to notify", "err", err, "step", i)
				continue
			}
			for _, ev := range evs {
				e.enqueueNotifications(ctx, cfg, ev)
			}
		}
	}
	e.deliverNotifications(ctx, n)
}

func (e *Engine) enqueueNotifications(ctx context.Context, cfg notify.Config, ev notify.Event) {
	for _, ch := range cfg.Targets(ev) {
		body, err := ev.Body(ch.Kind)
		if err != nil {
			continue
		}
		_, _ = e.db.Exec(`INSERT INTO notifications(id, event_key, channel, kind, event, project, title, body, status, next_at, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(event_key, channel) DO NOTHING`,
			ids.New("ntf"), ev.Key, ch.Name, ch.Kind, ev.Kind, ev.Project, clipText(ev.Title, 300), string(body), "pending", 0, time.Now().UnixMilli())
	}
}

func (n *Notifier) link(path string) string {
	return strings.TrimRight(n.BaseURL, "/") + path
}

// eventsFromLog reads new run and deployment endings from the event log.
func (e *Engine) eventsFromLog(ctx context.Context, n *Notifier) ([]notify.Event, error) {
	var cursor int64
	err := e.db.QueryRow(`SELECT value FROM notify_state WHERE name = ?`, notifyCursor).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		// The first time: from now on, not from the whole history.
		_ = e.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&cursor)
		_, err = e.db.Exec(`INSERT INTO notify_state(name, value) VALUES(?, ?) ON CONFLICT(name) DO NOTHING`, notifyCursor, cursor)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	got, err := e.readNotifyRows(cursor)
	if err != nil {
		return nil, err
	}
	var out []notify.Event
	last := cursor
	for _, r := range got {
		last = r.seq
		var p Payload
		if json.Unmarshal([]byte(r.data), &p) != nil {
			continue
		}
		if ev, ok := e.logEvent(ctx, n, r.run, r.kind, p, r.seq); ok {
			out = append(out, ev)
		}
	}
	if last > cursor {
		_, err = e.db.Exec(`UPDATE notify_state SET value = ? WHERE name = ? AND value < ?`, last, notifyCursor, last)
	}
	return out, err
}

// readNotifyRows reads the run and deployment endings after the cursor.
func (e *Engine) readNotifyRows(cursor int64) ([]struct {
	seq       int64
	run, kind string
	data      string
}, error) {
	rows, err := e.db.Query(`SELECT seq, run_id, type, data FROM events WHERE seq > ? AND type IN (`+notifyEvents+`) ORDER BY seq LIMIT 200`, cursor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var got []struct {
		seq       int64
		run, kind string
		data      string
	}
	for rows.Next() {
		var r struct {
			seq       int64
			run, kind string
			data      string
		}
		if err := rows.Scan(&r.seq, &r.run, &r.kind, &r.data); err != nil {
			return nil, err
		}
		got = append(got, r)
	}
	return got, rows.Err()
}

func (e *Engine) runFacts(ctx context.Context, runID string) (project string, number int, branch, message string) {
	_ = e.db.QueryRow(`SELECT project, number, branch, message FROM runs WHERE id = ?`, runID).Scan(&project, &number, &branch, &message)
	return
}

func (e *Engine) logEvent(ctx context.Context, n *Notifier, runID, kind string, p Payload, seq int64) (notify.Event, bool) {
	project, number, branch, message := e.runFacts(ctx, runID)
	if project == "" {
		return notify.Event{}, false
	}
	ev := notify.Event{Project: project, Branch: branch, RunID: runID, RunNumber: number, At: time.Now().UnixMilli(),
		Key: fmt.Sprintf("log:%d", seq), Link: n.link("/runs/" + runID)}
	switch {
	case kind == string(RunFinished) && p.Status == string(RunFailed):
		ev.Kind, ev.Title = notify.RunFailed, fmt.Sprintf("Run #%d of %s failed", number, project)
		ev.Detail = firstLine(message) + " Open the run to see which step failed and why."
	case kind == string(DeployClosed) && p.Deploy != nil:
		var env, service string
		_ = e.db.QueryRow(`SELECT environment, service FROM deployments WHERE id = ?`, p.Deploy.ID).Scan(&env, &service)
		ev.Environment = env
		where := service + " in " + env
		switch p.Deploy.Verdict {
		case VerdictDeployed:
			ev.Kind, ev.Title = notify.DeploymentSucceed, fmt.Sprintf("%s was deployed (run #%d of %s)", where, number, project)
		case VerdictFailed, VerdictRollbackFailed:
			ev.Kind, ev.Title = notify.DeploymentFailed, fmt.Sprintf("The deployment of %s failed (run #%d of %s)", where, number, project)
			ev.Detail = "Open the run to see why."
			if p.Deploy.Verdict == VerdictRollbackFailed {
				ev.Detail = "The rollback failed too: the environment needs a person. Open the run."
			}
		case VerdictRolledBack:
			ev.Kind, ev.Title = notify.DeploymentRolledBk, fmt.Sprintf("%s was rolled back (run #%d of %s)", where, number, project)
			ev.Detail = "The new version failed its checks and the previous one was put back. Open the run to see why."
		default:
			return notify.Event{}, false
		}
	default:
		return notify.Event{}, false
	}
	return ev, true
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if s != "" {
		s = clipText(s, 120) + "."
	}
	return s
}

// eventsWaiting reads what waits for a person: approvals, and steps that
// have waited too long.
func (e *Engine) eventsWaiting(ctx context.Context, n *Notifier) ([]notify.Event, error) {
	ap, err := e.Approvals()
	if err != nil {
		return nil, err
	}
	cfg := e.live().notify
	var out []notify.Event
	for _, a := range ap {
		env := ""
		base := notify.Event{Project: a.Project, Branch: a.Branch, Environment: env, RunID: a.RunID, RunNumber: a.RunNumber, Step: a.StepID, At: time.Now().UnixMilli(),
			Link: n.link("/runs/" + a.RunID + "?step=" + url.QueryEscape(a.StepID))}
		since := fmt.Sprintf("%s:%s:%d", a.RunID, a.StepID, a.WaitingSince)
		ev := base
		ev.Kind, ev.Key = notify.ApprovalRequested, "approval:"+since
		ev.Title = fmt.Sprintf("Run #%d of %s waits for you at %s", a.RunNumber, a.Project, a.StepID)
		ev.Detail = strings.TrimSpace(firstLine(a.CommitSubject) + " Open the run to approve or reject it.")
		out = append(out, ev)
		for i, r := range cfg.Rules {
			if !hasString(r.On, notify.StepWaitingLong) || e.now().Sub(time.UnixMilli(a.WaitingSince)) < r.Wait() {
				continue
			}
			long := base
			long.Kind, long.Key = notify.StepWaitingLong, fmt.Sprintf("long:%d:%s", i, since)
			long.Title = fmt.Sprintf("Run #%d of %s has waited for you at %s since %s", a.RunNumber, a.Project, a.StepID, clockText(a.WaitingSince))
			long.Detail = "Nobody has decided yet. Open the run to approve or reject it."
			out = append(out, long)
		}
	}
	return out, nil
}

func clockText(ms int64) string { return time.UnixMilli(ms).UTC().Format("Jan 2 15:04 UTC") }

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// eventsRollbackReviews reads emergency rollbacks that no approver has
// acknowledged yet.
func (e *Engine) eventsRollbackReviews(ctx context.Context, n *Notifier) ([]notify.Event, error) {
	if !e.knows(schemaRollback) {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT run_id, project, run_number, environment, service, actor, reason FROM rollbacks WHERE review_required = 1 AND reviewed_at = 0 LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notify.Event
	for rows.Next() {
		var runID, project, env, service, actor, reason string
		var number int
		if err := rows.Scan(&runID, &project, &number, &env, &service, &actor, &reason); err != nil {
			return nil, err
		}
		_, _, branch, _ := e.runFacts(ctx, runID)
		out = append(out, notify.Event{Kind: notify.RollbackNeedsView, Project: project, Environment: env, Branch: branch, RunID: runID, RunNumber: number, At: time.Now().UnixMilli(),
			Key:   "rbreview:" + runID,
			Title: fmt.Sprintf("%s in %s was rolled back in an emergency by %s", service, env, actor), Detail: clipText(reason, 200) + " An approver needs to review it: open the run.",
			Link: n.link("/runs/" + runID)})
	}
	return out, rows.Err()
}

// deliverNotifications sends the messages that are due.
func (e *Engine) deliverNotifications(ctx context.Context, n *Notifier) {
	now := time.Now().UnixMilli()
	list, err := e.dueNotifications(now)
	if err != nil {
		return
	}
	cfg := e.live().notify
	for _, d := range list {
		// Claim it: another server may be about to send the same one.
		res, err := e.db.Exec(`UPDATE notifications SET next_at = ? WHERE id = ? AND status = 'pending' AND next_at <= ?`, now+int64(time.Minute/time.Millisecond), d.id, now)
		if err != nil {
			continue
		}
		if k, _ := res.RowsAffected(); k != 1 {
			continue
		}
		sendErr := e.sendNotification(ctx, n, cfg, d.channel, d.project, []byte(d.body))
		e.noteDelivery(ctx, d.id, d.attempts+1, sendErr)
	}
}

type dueNotification struct {
	id, channel, kind, project, body string
	attempts                         int
}

// dueNotifications lists the messages whose time has come.
func (e *Engine) dueNotifications(now int64) ([]dueNotification, error) {
	rows, err := e.db.Query(`SELECT id, channel, kind, project, body, attempts FROM notifications WHERE status = 'pending' AND next_at <= ? ORDER BY created_at LIMIT 50`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []dueNotification
	for rows.Next() {
		var d dueNotification
		if err := rows.Scan(&d.id, &d.channel, &d.kind, &d.project, &d.body, &d.attempts); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

func (e *Engine) noteDelivery(ctx context.Context, id string, attempts int, sendErr error) {
	now := time.Now()
	if sendErr == nil {
		_, _ = e.db.Exec(`UPDATE notifications SET status = 'sent', attempts = ?, last_error = '', sent_at = ? WHERE id = ?`, attempts, now.UnixMilli(), id)
		return
	}
	if wait, more := notify.RetryAfter(attempts); more {
		_, _ = e.db.Exec(`UPDATE notifications SET attempts = ?, last_error = ?, next_at = ? WHERE id = ?`,
			attempts, clipText(sendErr.Error(), 300), now.Add(wait).UnixMilli(), id)
		return
	}
	_, _ = e.db.Exec(`UPDATE notifications SET status = 'failed', attempts = ?, last_error = ? WHERE id = ?`, attempts, clipText(sendErr.Error(), 300), id)
}

// sendNotification sends one body to a channel. The error says what went
// wrong in words and never holds the channel's address or key.
func (e *Engine) sendNotification(ctx context.Context, n *Notifier, cfg notify.Config, channel, project string, body []byte) error {
	var ch *notify.Channel
	for i := range cfg.Channels {
		if cfg.Channels[i].Name == channel {
			ch = &cfg.Channels[i]
		}
	}
	switch {
	case ch == nil:
		return fmt.Errorf("the channel %s is not in the configuration any more", channel)
	case n.Secrets == nil:
		return errors.New("this server has no secret store to read the channel's address from (ai.secrets)")
	case n.Client == nil:
		return errors.New("this server has no way to make the call")
	}
	target, err := n.Secrets.Lookup(ctx, project, ch.URL)
	if err != nil {
		return fmt.Errorf("reading the channel's address (%s): %v", ch.URL.String(), err)
	}
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("the channel's address (%s) is not an http or https address", ch.URL.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("the request could not be made")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "launchway-notify")
	if ch.Secret != nil {
		key, err := n.Secrets.Lookup(ctx, project, *ch.Secret)
		if err != nil {
			return fmt.Errorf("reading the channel's signing key (%s): %v", ch.Secret.String(), err)
		}
		req.Header.Set("X-Launchway-Signature-256", signBody(key, body))
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := n.Client.Do(req.WithContext(cctx))
	if err != nil {
		// The address is a credential: say what failed, not where.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("the call failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the channel answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return nil
}

// NotificationView is a row of the delivery log.
type NotificationView struct {
	ID        string `json:"id"`
	Event     string `json:"event"`
	Channel   string `json:"channel"`
	Kind      string `json:"kind"`
	Project   string `json:"project,omitempty"`
	Title     string `json:"title"`
	Status    string `json:"status"` // pending, sent or failed
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
	CreatedAt int64  `json:"created_at"`
	SentAt    int64  `json:"sent_at,omitempty"`
	NextAt    int64  `json:"next_attempt_at,omitempty"`
}

// Notifications returns the newest rows of the delivery log, for the
// projects the caller may see (nil: all).
func (e *Engine) Notifications(limit int, visible func(project string) bool) ([]NotificationView, error) {
	if !e.knows(schemaDelivery) {
		return []NotificationView{}, nil
	}
	limit = min(max(limit, 1), 500)
	rows, err := e.db.Query(`SELECT id, event, channel, kind, project, title, status, attempts, last_error, created_at, sent_at, next_at FROM notifications ORDER BY created_at DESC, id LIMIT ?`, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationView{}
	for rows.Next() {
		var v NotificationView
		if err := rows.Scan(&v.ID, &v.Event, &v.Channel, &v.Kind, &v.Project, &v.Title, &v.Status, &v.Attempts, &v.LastError, &v.CreatedAt, &v.SentAt, &v.NextAt); err != nil {
			return nil, err
		}
		if v.Status != "pending" {
			v.NextAt = 0
		}
		if visible != nil && !visible(v.Project) {
			continue
		}
		if out = append(out, v); len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}

// signBody signs a body as repository webhooks are signed: HMAC-SHA256,
// sent as sha256=<hex> in X-Launchway-Signature-256.
func signBody(key string, body []byte) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// notifyBases remembers the address each engine's notifier builds links from,
// so an event made outside the engine's own loop (a pull request's) carries a
// link that opens.
var notifyBases sync.Map

// PullNotice is a pull request event for the notification rules; Kind is one of the
// notify.Pull... names (pull.review_requested, pull.approved, ...).
type PullNotice struct {
	Kind, Repository, Branch, Title, Detail, Link, Key string
}

// NotifyPull offers a pull request's event to the notification rules (see NotifyEvent).
func (e *Engine) NotifyPull(ctx context.Context, n PullNotice) {
	e.NotifyEvent(ctx, notify.Event{Kind: n.Kind, Project: n.Repository, Branch: n.Branch, Title: n.Title, Detail: n.Detail, Link: n.Link, Key: n.Key})
}

// NotifyEvent offers an event made elsewhere (a review requested, a queue that
// failed) to the notification rules, and queues a message for each channel they
// name. The link is a path; the server's address is put in front of it. Nothing
// happens when no rule names the event.
func (e *Engine) NotifyEvent(ctx context.Context, ev notify.Event) {
	cfg := e.live().notify
	if len(cfg.Rules) == 0 || !e.knows(schemaDelivery) {
		return
	}
	if base, ok := notifyBases.Load(e); ok && strings.HasPrefix(ev.Link, "/") {
		ev.Link = strings.TrimRight(base.(string), "/") + ev.Link
	}
	if ev.At == 0 {
		ev.At = time.Now().UnixMilli()
	}
	e.enqueueNotifications(ctx, cfg, ev)
}

func notifyEventsKnown() []string { return notify.Events }
