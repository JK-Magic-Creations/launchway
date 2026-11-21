package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/hub"
)

// AppendLogs stores output lines for the step held by leaseID. Sending logs
// also renews the lease, and records that its runner was seen. The lines go
// after those already stored: see AppendLogsAt for a call that may be repeated.
func (e *Engine) AppendLogs(leaseID string, lines []LogLine) error {
	return e.AppendLogsAt(leaseID, -1, lines)
}

// LogGapError says a batch starts after lines the server does not have: the
// runner must send from Have (its own count of lines of the attempt).
type LogGapError struct{ Have int }

func (g *LogGapError) Error() string {
	return fmt.Sprintf("conflict: the server has %d lines of this step attempt; send from there", g.Have)
}
func (g *LogGapError) Is(t error) bool { return t == ErrConflict }

// AppendLogsAt is AppendLogs for a runner that counts the lines it has sent
// of the step attempt: offset is how many lines came before the first of
// lines. Lines the server already has (a batch repeated after a lost answer)
// are skipped, so a repeat adds nothing and a partly repeated batch adds
// only its new tail. An offset beyond what the server has is a LogGapError.
// A negative offset means the runner does not count: lines are appended.
func (e *Engine) AppendLogsAt(leaseID string, offset int, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > MaxLogBatch {
		return fmt.Errorf("%w: at most %d lines per call", ErrConflict, MaxLogBatch)
	}
	return e.db.retryTx(0, false, func() error { return e.appendLogs(leaseID, offset, lines) })
}

func (e *Engine) appendLogs(leaseID string, offset int, lines []LogLine) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var l leaseRef
	var last int
	var runnerID string
	now := e.now()
	if offset >= 0 {
		// The lease row is locked first, so that the count read is the
		// count the new lines follow, on any server.
		var have int
		err := tx.QueryRow(`UPDATE leases SET expires_at = ? WHERE id = ? RETURNING log_seq, runner`,
			now.Add(e.ttl).UnixMilli(), leaseID).Scan(&have, &runnerID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
		switch skip := have - offset; {
		case skip < 0:
			return &LogGapError{Have: have}
		case skip >= len(lines):
			return e.commitSeen(tx, runnerID, now)
		default:
			lines = lines[skip:]
		}
	}
	// Taking the sequence numbers locks the lease row, so concurrent calls
	// for the same lease (on any server) get separate ranges.
	err = tx.QueryRow(`UPDATE leases SET log_seq = log_seq + ?, expires_at = ? WHERE id = ?
		RETURNING run_id, step_id, attempt, log_seq, runner`, len(lines), now.Add(e.ttl).UnixMilli(), leaseID).
		Scan(&l.runID, &l.stepID, &l.attempt, &last, &runnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if err := sawRunner(tx, runnerID, now); err != nil {
		return err
	}
	first := last - len(lines) + 1
	out := make([]LogLine, len(lines))
	for i, ln := range lines {
		if len(ln.Text) > MaxLogLineBytes {
			ln.Text = ln.Text[:MaxLogLineBytes] + " …[line truncated]"
		}
		if ln.At == 0 {
			ln.At = e.now().UnixMilli()
		}
		if ln.Stream == "" {
			ln.Stream = "stdout"
		}
		ln.Seq = first + i
		out[i] = ln
		if _, err := tx.Exec(`INSERT INTO logs(run_id, step_id, attempt, seq, at, stream, text) VALUES(?,?,?,?,?,?,?)`,
			l.runID, l.stepID, l.attempt, ln.Seq, ln.At, ln.Stream, ln.Text); err != nil {
			return err
		}
	}
	if err := e.notifyOthers(tx, note{Kind: noteLog, Run: l.runID, Step: l.stepID, Attempt: l.attempt, FromSeq: first, ToSeq: last}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.hub.Publish(hub.Message{Type: "log", RunID: l.runID, Data: LogBatch{Step: l.stepID, Attempt: l.attempt, Lines: out}})
	return nil
}

// LogBatch is the live update sent when log lines arrive.
type LogBatch struct {
	Step    string    `json:"step"`
	Attempt int       `json:"attempt"`
	Lines   []LogLine `json:"lines"`
}

// commitSeen records that a runner was seen and commits: the whole of a
// repeated log batch was already stored.
func (e *Engine) commitSeen(tx *txx, runnerID string, now time.Time) error {
	if err := sawRunner(tx, runnerID, now); err != nil {
		return err
	}
	return tx.Commit()
}
