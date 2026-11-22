package engine

import "github.com/jk-magic-creations/launchway/internal/store"

// A person's own sessions (the browsers they are signed in on).

// SessionRow is a session as its owner sees it.
type SessionRow = store.SessionRow

// SessionsOf lists a person's sessions that have not ended.
func (e *Engine) SessionsOf(user string, now int64) ([]SessionRow, error) {
	return e.ident().SessionsOf(e.scope0(), user, now)
}

// EndSessionByID ends one of a person's sessions by its short name; false when there is none.
func (e *Engine) EndSessionByID(user, id string) (bool, error) {
	return e.ident().EndSessionByID(e.scope0(), user, id)
}

// EndOtherSessions ends every session of a person except the one with this hash ("" keeps none).
func (e *Engine) EndOtherSessions(user, keepHash string) error {
	return e.ident().EndSessionsExcept(e.scope0(), user, keepHash)
}
