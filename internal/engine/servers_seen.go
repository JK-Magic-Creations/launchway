package engine

import (
	"context"
	"errors"
	"net/url"
)

// ErrCannotSeeServers means this build's own connection does not show its
// name to PostgreSQL (a connection pooler that drops it), so unnamed
// connections cannot be told from this build's: the caller must not wait for
// them to stop.
var ErrCannotSeeServers = errors.New("this server's own database connection does not show its application name, so older servers cannot be told from this one")

// ApplicationName is what this build's connections call themselves in
// PostgreSQL (pg_stat_activity.application_name). Builds before encryption at
// rest did not name theirs, so a connection with an empty name that belongs to
// this database and role is a server of an older release: the one signal an
// older server cannot help giving, because it holds connections while it runs.
const ApplicationName = "launchway"

// withApplicationName names the connections unless the address already does.
func withApplicationName(addr string) string {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "" {
		return addr // a key=value string: the operator chose; leave it
	}
	q := u.Query()
	if q.Get("application_name") != "" {
		return addr
	}
	q.Set("application_name", ApplicationName)
	u.RawQuery = q.Encode()
	return u.String()
}

// OlderServers counts the connections to this database, by this database
// role, that do not name themselves: servers of a release that did not, still
// running. SQLite has one process, so none. It is what the sealing of
// existing secrets waits on, so that no older server meets a sealed value.
func (e *Engine) OlderServers(ctx context.Context) (int, error) {
	if !e.db.pg {
		return 0, nil
	}
	var own string
	if err := e.db.QueryRowContext(ctx, `SELECT current_setting('application_name')`).Scan(&own); err != nil {
		return 0, err
	}
	if own == "" {
		return 0, ErrCannotSeeServers
	}
	var n int
	err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND usename = current_user AND pid <> pg_backend_pid()
		  AND backend_type = 'client backend' AND application_name = ''`).Scan(&n)
	return n, err
}
