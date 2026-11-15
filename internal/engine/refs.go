package engine

import (
	"database/sql"
	"maps"
	"slices"
)

// Every server polls every project's repository, so each change to a ref is
// seen by all of them. A change is acted on by the one server that claims
// it: claiming is a compare-and-set on refs_seen, done in the same
// transaction as whatever is recorded about the change.

// RefChange is a ref moving to a new commit, as one server sees it.
type RefChange struct {
	Ref    string // refs/heads/main
	Old    string // the commit recorded for the ref before ("" if none was)
	New    string // the commit the ref points at now
	SeenAt int64  // when this server started the fetch it saw New in, Unix milliseconds
}

// PushDecision records what was done about one change to a ref.
type PushDecision struct {
	At      int64  `json:"at"`
	Ref     string `json:"ref"`
	SHA     string `json:"sha"`
	Outcome string `json:"outcome"` // started, ignored, error
	Detail  string `json:"detail"`
	RunID   string `json:"run_id,omitempty"`
}

// keepDecisions is how many push decisions are kept per project.
const keepDecisions = 50

// SeenRefs returns the commit recorded for each of a project's refs.
func (e *Engine) SeenRefs(project string) (map[string]string, error) {
	rows, err := e.db.Query(`SELECT ref, sha FROM refs_seen WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ref, sha string
		if err := rows.Scan(&ref, &sha); err != nil {
			return nil, err
		}
		out[ref] = sha
	}
	return out, rows.Err()
}

// SeedRefs records where a project's refs point the first time the project
// is seen, without deciding anything about them: history does not start
// runs. Refs already recorded, perhaps by another server, are left alone.
func (e *Engine) SeedRefs(project string, refs map[string]string, seenAt int64) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// In order, so that servers seeding the same project at once take the
	// rows' locks in the same order and cannot deadlock.
	for _, ref := range slices.Sorted(maps.Keys(refs)) {
		if _, err := tx.Exec(`INSERT INTO refs_seen(project, ref, sha, seen_at) VALUES(?,?,?,?)
			ON CONFLICT(project, ref) DO NOTHING`, project, ref, refs[ref], seenAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PullRequestsSeeded is a key kept in refs_seen beside a project's refs
// (no Git ref can contain a colon). It records that the project's
// pull-request refs have been seeded, so that the pull requests that
// already exist when a server first looks for them, open or long closed,
// do not start runs. Older servers ignore it.
const PullRequestsSeeded = ":pull-requests-seeded"

// PullRequests reports whether this server knows pull-request runs.
func (e *Engine) PullRequests() bool { return e.knows(schemaPullRequests) }

// MoveRef claims a change without recording a decision: for changes that
// nobody asked to hear about, such as the pull requests of a project that
// does not run them. It reports whether this server claimed it.
func (e *Engine) MoveRef(project string, ch RefChange) (bool, error) {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	claimed, err := claimRef(tx, project, ch)
	if err != nil || !claimed {
		return false, err
	}
	return true, tx.Commit()
}

// RecordPush claims a change and records the decision made about it, in one
// transaction. It returns false, recording nothing, if another server has
// already claimed the change or has seen a newer state of the ref.
func (e *Engine) RecordPush(project string, ch RefChange, d PushDecision) (bool, error) {
	claimed, err := e.recordPush(project, ch, d)
	if claimed {
		e.pruneDecisions(project)
	}
	return claimed, err
}

func (e *Engine) recordPush(project string, ch RefChange, d PushDecision) (bool, error) {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	claimed, err := claimRef(tx, project, ch)
	if err != nil || !claimed {
		return false, err
	}
	d.At = e.now().UnixMilli()
	if err := insertDecision(tx, project, d); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// claimRef moves a ref from ch.Old to ch.New if nobody else has, and if the
// commit on record came from a fetch no newer than ch's: a server whose
// mirror is behind must not move a ref back. Under concurrent claims the
// database lets exactly one succeed.
func claimRef(tx *txx, project string, ch RefChange) (bool, error) {
	var res sql.Result
	var err error
	if ch.Old == "" {
		res, err = tx.Exec(`INSERT INTO refs_seen(project, ref, sha, seen_at) VALUES(?,?,?,?)
			ON CONFLICT(project, ref) DO NOTHING`, project, ch.Ref, ch.New, ch.SeenAt)
	} else {
		res, err = tx.Exec(`UPDATE refs_seen SET sha = ?, seen_at = ?
			WHERE project = ? AND ref = ? AND sha = ? AND seen_at <= ?`,
			ch.New, ch.SeenAt, project, ch.Ref, ch.Old, ch.SeenAt)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// insertDecision records a decision.
func insertDecision(tx *txx, project string, d PushDecision) error {
	_, err := tx.Exec(`INSERT INTO push_decisions(project, at, ref, sha, outcome, detail, run_id) VALUES(?,?,?,?,?,?,?)`,
		project, d.At, d.Ref, d.SHA, d.Outcome, d.Detail, d.RunID)
	return err
}

// pruneDecisions forgets a project's decisions beyond the newest
// keepDecisions. It is tidying only, done outside the transaction that
// recorded the decision so that servers pruning at once cannot make each
// other's work fail; an error is just logged.
func (e *Engine) pruneDecisions(project string) {
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`DELETE FROM push_decisions WHERE project = ? AND id NOT IN
		(SELECT id FROM push_decisions WHERE project = ? ORDER BY at DESC, id DESC LIMIT ?)`,
		project, project, keepDecisions); err != nil {
		e.log.Warn("forgetting old push decisions", "project", project, "err", err)
	}
}

// PushDecisions returns a project's most recent push decisions, newest first.
func (e *Engine) PushDecisions(project string) ([]PushDecision, error) {
	rows, err := e.db.Query(`SELECT at, ref, sha, outcome, detail, run_id FROM push_decisions
		WHERE project = ? ORDER BY at DESC, id DESC LIMIT ?`, project, keepDecisions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushDecision{}
	for rows.Next() {
		var d PushDecision
		if err := rows.Scan(&d.At, &d.Ref, &d.SHA, &d.Outcome, &d.Detail, &d.RunID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SharedValue returns the value stored under key, storing proposed first if
// there is none. Servers starting together all get the same value.
func (e *Engine) SharedValue(key, proposed string) (string, error) {
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, "shared:"+key, proposed); err != nil {
		return "", err
	}
	var v string
	err := e.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, "shared:"+key).Scan(&v)
	return v, err
}
