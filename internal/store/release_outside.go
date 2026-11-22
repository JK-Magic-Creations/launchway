package store

// The release published to an outside Git host (schema 56). release_outside
// is a release-engine table, so until the engine moves into the store (stage
// S2) its statements run on the connection the engine hands in, like
// step_usage's. One row per release and push mirror; the rows never hold a
// credential, and a reason is stored already redacted by the caller.

// Outside states.
const (
	OutsidePending   = "pending"
	OutsidePublished = "published"
	OutsideFailed    = "failed"
)

// OutsideRow is one row of release_outside.
type OutsideRow struct {
	ID        string
	ReleaseID string
	MirrorID  string
	State     string
	URL       string
	Reason    string
	Attempts  int
	NextAt    int64
	CreatedAt int64
	UpdatedAt int64
}

const outsideCols = `id, release_id, mirror_id, state, outside_url, reason, attempts, next_at, created_at, updated_at`

func scanOutside(rows interface{ Scan(...any) error }) (OutsideRow, error) {
	var r OutsideRow
	err := rows.Scan(&r.ID, &r.ReleaseID, &r.MirrorID, &r.State, &r.URL, &r.Reason, &r.Attempts, &r.NextAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func queryOutside(q Queryer, where string, args ...any) ([]OutsideRow, error) {
	rows, err := q.Query(`SELECT `+outsideCols+` FROM release_outside `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutsideRow
	for rows.Next() {
		r, err := scanOutside(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueueOutside records that a release is to be published on a mirror's
// outside host. It does nothing when the pair has a row already.
func QueueOutside(x Execer, id, releaseID, mirrorID string, now int64) error {
	_, err := x.Exec(`INSERT INTO release_outside(id, release_id, mirror_id, state, next_at, created_at, updated_at)
		VALUES(?, ?, ?, 'pending', ?, ?, ?) ON CONFLICT(release_id, mirror_id) DO NOTHING`, id, releaseID, mirrorID, now, now, now)
	return err
}

// OutsideDue returns the pending rows whose time has come, oldest first.
func OutsideDue(q Queryer, now int64, limit int) ([]OutsideRow, error) {
	return queryOutside(q, `WHERE state = 'pending' AND next_at <= ? ORDER BY next_at, id LIMIT `+itoa(limit), now)
}

// OutsideOfRelease returns a release's rows, by mirror.
func OutsideOfRelease(q Queryer, releaseID string) ([]OutsideRow, error) {
	return queryOutside(q, `WHERE release_id = ? ORDER BY created_at, id`, releaseID)
}

// OutsideOfMirror returns the newest rows of a mirror.
func OutsideOfMirror(q Queryer, mirrorID string, limit int) ([]OutsideRow, error) {
	return queryOutside(q, `WHERE mirror_id = ? ORDER BY created_at DESC, id DESC LIMIT `+itoa(limit), mirrorID)
}

// OutsideOf returns the row of one release and mirror, and whether there is one.
func OutsideOf(q Queryer, releaseID, mirrorID string) (OutsideRow, bool, error) {
	rows, err := queryOutside(q, `WHERE release_id = ? AND mirror_id = ?`, releaseID, mirrorID)
	if err != nil || len(rows) == 0 {
		return OutsideRow{}, false, err
	}
	return rows[0], true, nil
}

// ClaimOutside takes a pending row for one worker by moving its time ahead by
// lease; false means another worker has it, or it is no longer pending.
func ClaimOutside(x Execer, id string, now, lease int64) (bool, error) {
	res, err := x.Exec(`UPDATE release_outside SET next_at = ?, updated_at = ? WHERE id = ? AND state = 'pending' AND next_at <= ?`, now+lease, now, id, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetOutside records the result of an attempt.
func SetOutside(x Execer, id, state, url, reason string, attempts int, nextAt, now int64) error {
	_, err := x.Exec(`UPDATE release_outside SET state = ?, outside_url = ?, reason = ?, attempts = ?, next_at = ?, updated_at = ? WHERE id = ?`,
		state, url, reason, attempts, nextAt, now, id)
	return err
}

// RetryOutside makes a failed row pending again, with its attempts counted
// from the start; it reports whether a failed row was found.
func RetryOutside(x Execer, releaseID, mirrorID string, now int64) (bool, error) {
	res, err := x.Exec(`UPDATE release_outside SET state = 'pending', reason = '', attempts = 0, next_at = ?, updated_at = ?
		WHERE release_id = ? AND mirror_id = ? AND state = 'failed'`, now, now, releaseID, mirrorID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func itoa(n int) string {
	if n <= 0 {
		n = 1
	}
	b := []byte{}
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
