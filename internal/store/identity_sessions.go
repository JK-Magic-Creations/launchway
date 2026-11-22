package store

import "strings"

// A person's own signed-in browsers: listing them, and ending one or all the others. A session is named to its owner
// by the first characters of the hash of its cookie, which is not enough to use it.

// SessionRow is one session as its owner sees it.
type SessionRow struct {
	ID        string // the first characters of the stored hash
	CreatedAt int64
	LastSeen  int64
	ExpiresAt int64
	Hash      string // the whole stored hash, for the caller to tell which is its own; never sent to a client
}

// SessionIDLen is how many characters of the hash name a session.
const SessionIDLen = 12

// SessionsOf lists a person's sessions that have not ended, newest use first.
func (i *Identity) SessionsOf(sc Scope, user string, now int64) ([]SessionRow, error) {
	out := []SessionRow{}
	err := i.env.Do(sc, false, func(q *Q) error {
		rows, err := q.Query(`SELECT id_hash, created_at, last_seen, expires_at FROM sessions WHERE base_id = ? AND username = ? AND expires_at > ? ORDER BY last_seen DESC, id_hash`, q.Base(), user, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r SessionRow
			if err := rows.Scan(&r.Hash, &r.CreatedAt, &r.LastSeen, &r.ExpiresAt); err != nil {
				return err
			}
			r.ID = r.Hash[:min(SessionIDLen, len(r.Hash))]
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// EndSessionByID ends one of a person's sessions by the name SessionsOf gave it; it says whether there was one. The
// name must be the whole of what SessionsOf gave, so a short prefix ends nothing.
func (i *Identity) EndSessionByID(sc Scope, user, id string) (bool, error) {
	if len(id) != SessionIDLen || strings.ContainsAny(id, "%_\\") {
		return false, nil
	}
	var n int64
	err := i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`DELETE FROM sessions WHERE base_id = ? AND username = ? AND id_hash LIKE ? ESCAPE '\'`, q.Base(), user, id+"%")
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n > 0, err
}
