package engine

import (
	"database/sql"
	"errors"
	"strconv"
)

// An installation that runs with no sign-in (auth.mode none, allow_anonymous)
// is never switched by an upgrade. Someone can ask for sign-in from the page
// ("set up sign-in now"); the request is kept here, and the servers' next
// start runs with built-in accounts (auth.mode local) until the first
// administrator has used the one-time setup code. Nothing else changes the
// mode.
const metaSignInRequested = "auth_local_requested"

// RequestLocalSignIn records that built-in sign-in was asked for, at now (ms).
func (e *Engine) RequestLocalSignIn(now int64) error {
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, metaSignInRequested, strconv.FormatInt(now, 10))
	return err
}

// LocalSignInRequested says whether built-in sign-in was asked for, and when.
func (e *Engine) LocalSignInRequested() (at int64, requested bool, err error) {
	var v string
	err = e.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaSignInRequested).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	at, _ = strconv.ParseInt(v, 10, 64)
	return at, true, nil
}
