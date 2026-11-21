package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"
)

// Every write a runner makes is safe to repeat. A runner that lost the
// answer (the server restarted, the database failed over, the network
// dropped) sends the same call again; the server applies it once and
// repeats the first answer. Three mechanisms, none of which needs a
// runner that predates them to change:
//
//   - log batches carry a position (see AppendLogsAt); a repeat adds nothing;
//   - the end of a step attempt is recorded, in the transaction that ends
//     its lease, under finish:<lease> (the server derives the key, so even
//     an older runner is covered);
//   - a call that carries an Idempotency-Key header has its first successful
//     answer kept (RememberWrite) and repeated for the same key.
//
// The records are rows of the meta table with the prefix writePrefix, so no
// schema change is needed; old ones are removed (pruneWrites).
const (
	writePrefix = "idem:"
	// writeKeep is how long a recorded answer is kept: longer than any
	// outage the runner spool rides through.
	writeKeep = 7 * 24 * time.Hour
)

var writeCount atomic.Int64

type writeRecord struct {
	At     int64  `json:"at"`
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"`
}

// RecallWrite returns the first answer recorded under key.
func (e *Engine) RecallWrite(key string) (status int, body []byte, ok bool) {
	var v string
	if err := e.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, writePrefix+key).Scan(&v); err != nil {
		return 0, nil, false
	}
	var r writeRecord
	if json.Unmarshal([]byte(v), &r) != nil {
		return 0, nil, false
	}
	return r.Status, r.Body, true
}

func putWrite(q interface {
	Exec(string, ...any) (sql.Result, error)
}, key string, r writeRecord) error {
	data, _ := json.Marshal(r)
	_, err := q.Exec(`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, writePrefix+key, string(data))
	return err
}

// RememberWrite records the first answer to a call, so that a repeat gets
// the same one. An answer already recorded is kept.
func (e *Engine) RememberWrite(key string, status int, body []byte) error {
	err := putWrite(e.db, key, writeRecord{At: e.now().UnixMilli(), Status: status, Body: body})
	if err == nil && writeCount.Add(1)%200 == 0 {
		e.pruneWrites()
	}
	return err
}

// pruneWrites removes answers older than writeKeep.
func (e *Engine) pruneWrites() {
	rows, err := e.db.Query(`SELECT key, value FROM meta WHERE key LIKE ?`, writePrefix+"%")
	if err != nil {
		return
	}
	var old []string
	cut := e.now().Add(-writeKeep).UnixMilli()
	for rows.Next() {
		var k, v string
		var r writeRecord
		if rows.Scan(&k, &v) == nil && json.Unmarshal([]byte(v), &r) == nil && r.At < cut {
			old = append(old, k)
		}
	}
	rows.Close()
	for _, k := range old {
		_, _ = e.db.Exec(`DELETE FROM meta WHERE key = ?`, k)
	}
}

// FinishRecorded reports whether the attempt that held the lease already
// ended through Finish, so that a repeated Finish is a success.
func (e *Engine) FinishRecorded(leaseID string) bool {
	_, _, ok := e.RecallWrite("finish:" + leaseID)
	return ok
}

// rememberFinish records, in the transaction that ends the lease, that
// Finish has been applied.
func (c *cmd) rememberFinish(leaseID string) error {
	return putWrite(c.tx, "finish:"+leaseID, writeRecord{At: c.e.now().UnixMilli(), Status: 204})
}

// finishRepeat is what Finish answers when it finds no lease: success if the
// attempt had already been finished with this lease, otherwise err.
func (e *Engine) finishRepeat(leaseID string, err error) error {
	if errors.Is(err, ErrLeaseLost) && e.FinishRecorded(leaseID) {
		return nil
	}
	return err
}

// commandReport says what to do with a runner's report that it did a
// command: act on it (true), ignore it because the same report was already
// recorded (false, nil), or refuse it because the command is not the
// runner's to finish (false, ErrLeaseLost).
func commandReport(cs *CommandState, res CommandResult) (bool, error) {
	switch {
	case cs == nil || cs.Take != res.Take:
		return false, ErrLeaseLost
	case cs.State == CommandTaken:
		return true, nil
	case cs.State == CommandDone && cs.Result == res.Result && !strings.HasPrefix(res.Result, CommandReleased):
		return false, nil
	}
	return false, ErrLeaseLost
}
