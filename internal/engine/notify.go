package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jk-magic-creations/launchway/internal/hub"
)

// Servers sharing a PostgreSQL database tell each other about changes with
// LISTEN/NOTIFY, so that a step queued on one server wakes runners waiting on
// another, and a browser connected to any server sees live updates.
//
// A notification only says what changed. NOTIFY payloads are limited to
// about 8000 bytes, so the receiving server reads the details back from the
// database and publishes them to its own hub.

const (
	noteRun  = "run"  // a run and some of its steps changed
	noteLog  = "log"  // log lines were added
	noteWake = "wake" // work may have become available: waiting runners should look
)

// note is the payload of one notification.
type note struct {
	From   string   `json:"from"` // the sending server's instance ID
	Kind   string   `json:"kind"`
	Run    string   `json:"run"`
	Steps  []string `json:"steps,omitempty"`  // noteRun: the steps that changed
	All    bool     `json:"all,omitempty"`    // noteRun: too many to list; every step may have changed
	Queued bool     `json:"queued,omitempty"` // noteRun: a step was queued, so waiting runners should look
	Step   string   `json:"step,omitempty"`   // noteLog
	// noteLog: the lines are FromSeq..ToSeq (inclusive) of this attempt.
	Attempt int `json:"attempt,omitempty"`
	FromSeq int `json:"from_seq,omitempty"`
	ToSeq   int `json:"to_seq,omitempty"`
}

// maxNoteBytes keeps payloads well inside PostgreSQL's limit.
const maxNoteBytes = 4000

// notifyOthers queues a notification inside tx, so that it is delivered if
// and only if tx commits. It does nothing on SQLite, where there is only
// ever one server.
func (e *Engine) notifyOthers(tx *txx, n note) error {
	if !e.db.pg {
		return nil
	}
	n.From = e.instance
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if len(data) > maxNoteBytes && len(n.Steps) > 0 {
		n.Steps, n.All = nil, true
		if data, err = json.Marshal(n); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`SELECT pg_notify(?, ?)`, e.channel, string(data))
	return err
}

// channelFor names the notification channel for a schema. Notifications
// are database-wide, so installations in different schemas of the same
// database must use different channels.
func channelFor(schema string) string {
	ch := "launchway:" + schema
	if len(ch) > 63 { // the longest identifier PostgreSQL keeps
		sum := sha256.Sum256([]byte(schema))
		ch = "launchway:" + hex.EncodeToString(sum[:16])
	}
	return ch
}

// startListening opens the dedicated connection that receives notifications.
func (e *Engine) startListening(ctx context.Context) (*pgx.Conn, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(cctx, e.db.dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to listen for other servers: %w", err)
	}
	if _, err := conn.Exec(cctx, "LISTEN "+pgx.Identifier{e.channel}.Sanitize()); err != nil {
		conn.Close(context.Background())
		return nil, fmt.Errorf("listening for other servers: %w", err)
	}
	return conn, nil
}

// listen receives notifications until ctx ends, reconnecting with backoff
// whenever the connection fails.
func (e *Engine) listen(ctx context.Context, conn *pgx.Conn) {
	defer e.bg.Done()
	const first, most = 100 * time.Millisecond, 5 * time.Second
	backoff := first
	for {
		if conn != nil {
			err := e.receive(ctx, conn)
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			conn.Close(closeCtx)
			cancel()
			conn = nil
			if ctx.Err() != nil {
				return
			}
			e.log.Warn("lost the connection that hears from other servers; reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		c, err := e.startListening(ctx)
		if err != nil {
			if ctx.Err() == nil {
				e.log.Warn("reconnecting to hear from other servers", "err", err, "retry_in", backoff)
			}
			backoff = min(backoff*2, most)
			continue
		}
		conn, backoff = c, first
		// Work may have been queued while nothing was listening.
		e.notify()
	}
}

func (e *Engine) receive(ctx context.Context, conn *pgx.Conn) error {
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var msg note
		if err := json.Unmarshal([]byte(n.Payload), &msg); err != nil {
			e.log.Warn("ignoring a notification that is not understood", "err", err)
			continue
		}
		if msg.From == e.instance {
			continue // this server already acted on it
		}
		e.handleNote(msg)
	}
}

// handleNote reacts on this server to a change made by another.
func (e *Engine) handleNote(n note) {
	switch n.Kind {
	case noteRun:
		if n.Queued {
			e.notify()
		}
		steps := map[string]bool{}
		for _, id := range n.Steps {
			steps[id] = true
		}
		if n.All {
			ids, err := e.stepIDs(n.Run)
			if err != nil {
				e.log.Warn("reading a run another server changed", "run", n.Run, "err", err)
			}
			for _, id := range ids {
				steps[id] = true
			}
		}
		e.publishChanges(n.Run, steps)
	case noteWake:
		e.notify()
	case noteLog:
		lines, err := e.logRange(n.Run, n.Step, n.Attempt, n.FromSeq, n.ToSeq)
		if err != nil {
			e.log.Warn("reading log lines another server stored", "run", n.Run, "err", err)
			return
		}
		if len(lines) > 0 {
			e.hub.Publish(hub.Message{Type: "log", RunID: n.Run, Data: LogBatch{Step: n.Step, Attempt: n.Attempt, Lines: lines}})
		}
	}
}

// stepIDs lists a run's steps.
func (e *Engine) stepIDs(runID string) ([]string, error) {
	rows, err := e.db.Query(`SELECT step_id FROM steps WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// logRange reads log lines from..to (inclusive) of one step attempt.
func (e *Engine) logRange(runID, stepID string, attempt, from, to int) ([]LogLine, error) {
	rows, err := e.db.Query(`SELECT seq, at, stream, text FROM logs
		WHERE run_id = ? AND step_id = ? AND attempt = ? AND seq >= ? AND seq <= ? ORDER BY seq`,
		runID, stepID, attempt, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Seq, &l.At, &l.Stream, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
