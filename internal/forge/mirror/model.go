package mirror

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Direction of a mirror.
const (
	Pull = "pull"
	Push = "push"
)

// States of a mirror's last attempt.
const (
	StatePending = "pending" // never run
	StateOK      = "ok"
	StateError   = "error"
)

// Mirror is one mirror of a hosted repository.
type Mirror struct {
	ID         string
	RepoID     string
	Direction  string
	URL        string
	Username   string
	Credential forge.SecretRef
	Interval   time.Duration
	Enabled    bool
	// Prune says refs the other side lost are deleted here (pull) or on the
	// other side (push).
	Prune bool
	State string
	// LastTry and LastOK are when a run last started and last succeeded.
	LastTry, LastOK time.Time
	LastError       string
	Failures        int
	NextAt          time.Time
	// SyncedGen is the generation of the repository a push mirror last
	// pushed; DirtySince is when it first fell behind (zero when it has not).
	SyncedGen  int64
	DirtySince time.Time
	Refs       int
	LeaseUntil time.Time
	CreatedAt  time.Time
	CreatedBy  string
	// PublishReleases (push mirrors only) says a Launchway release, once
	// published, is also created on the outside Git host; OutsideAPI is the
	// address of that host's API when it cannot be derived from URL.
	PublishReleases bool
	OutsideAPI      string
}

// Spec is what a person gives to set a mirror up.
type Spec struct {
	URL string `json:"url"`
	// Username goes with the credential (a token is sent as the password).
	// It is not a secret.
	Username   string          `json:"username,omitempty"`
	Credential forge.SecretRef `json:"credential,omitzero"`
	// IntervalSeconds is how often the mirror runs; default 1 hour for a
	// pull mirror, 6 hours for a push mirror (which also runs after every
	// push). At least 60.
	IntervalSeconds int64 `json:"interval_seconds,omitempty"`
	// Prune defaults to true.
	Prune *bool `json:"prune,omitempty"`
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
	// PublishReleases (push mirrors only, default false) makes the release
	// of a published Launchway release on the outside Git host too, with
	// this mirror's own credential. OutsideAPI is the address of that
	// host's API for a self-hosted host (empty: derived from the address).
	PublishReleases bool   `json:"publish_releases,omitempty"`
	OutsideAPI      string `json:"outside_api,omitempty"`
}

// OutsideChange changes a mirror's outside-release settings; a nil field is
// left as it is.
type OutsideChange struct {
	PublishReleases *bool
	OutsideAPI      *string
}

// OutsideState is how one release stands on a mirror's outside host.
type OutsideState struct {
	Release   string    `json:"release"`
	Version   string    `json:"version,omitempty"`
	State     string    `json:"state"`
	URL       string    `json:"url,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Attempts  int       `json:"attempts"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// View is a mirror as the API shows it. It never holds a credential, only
// where one is read from.
type View struct {
	ID              string    `json:"id"`
	Repository      string    `json:"repository"`
	Direction       string    `json:"direction"`
	URL             string    `json:"url"`
	Username        string    `json:"username,omitempty"`
	Credential      string    `json:"credential,omitempty"`
	IntervalSeconds int64     `json:"interval_seconds"`
	Enabled         bool      `json:"enabled"`
	Prune           bool      `json:"prune"`
	State           string    `json:"state"`
	Running         bool      `json:"running,omitempty"`
	LastAttempt     time.Time `json:"last_attempt,omitzero"`
	LastSuccess     time.Time `json:"last_success,omitzero"`
	LastError       string    `json:"last_error,omitempty"`
	Failures        int       `json:"failures"`
	NextRun         time.Time `json:"next_run,omitzero"`
	Refs            int       `json:"refs"`
	// LagSeconds is how far behind the mirror is: for a pull mirror the
	// time since it last matched its source; for a push mirror how long
	// accepted updates have waited to be pushed (0 when it is current).
	LagSeconds int64     `json:"lag_seconds"`
	Behind     bool      `json:"behind"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by,omitempty"`
	// PublishReleases and OutsideAPI are the outside-release settings of a
	// push mirror; Outside is how its latest releases stand there.
	PublishReleases bool           `json:"publish_releases"`
	OutsideAPI      string         `json:"outside_api,omitempty"`
	Outside         []OutsideState `json:"outside,omitempty"`
}

const (
	minInterval         = time.Minute
	maxInterval         = 30 * 24 * time.Hour
	defaultPullInterval = time.Hour
	defaultPushInterval = 6 * time.Hour
	maxPushMirrors      = 10
)

// ErrBusy is returned when a mirror is running on a server already.
var ErrBusy = errors.New("this mirror is running right now")

// validate checks a spec and returns the mirror it describes.
func (s Spec) mirror(direction string, allowFiles bool) (Mirror, error) {
	bad := func(f string, a ...any) error {
		return fmt.Errorf("%w: mirror: %s", forge.ErrInvalid, fmt.Sprintf(f, a...))
	}
	m := Mirror{Direction: direction, URL: strings.TrimSpace(s.URL), Username: s.Username, Credential: s.Credential, Enabled: true, Prune: true}
	u, err := url.Parse(m.URL)
	local := allowFiles && strings.HasPrefix(m.URL, "file://")
	if !local && (err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http")) {
		return m, bad("the address must be an https or http address of a Git repository")
	}
	if !local && u.User != nil {
		return m, bad("the address must not contain a user name or password; give the credential as a reference (credential.env or credential.file)")
	}
	if err := s.Credential.Validate(); err != nil {
		return m, bad("%v", err)
	}
	if strings.ContainsAny(s.Username, "\r\n:\x00") || len(s.Username) > 200 {
		return m, bad("the user name is not valid")
	}
	m.Interval = defaultPullInterval
	if direction == Push {
		m.Interval = defaultPushInterval
	}
	if s.IntervalSeconds != 0 {
		m.Interval = time.Duration(s.IntervalSeconds) * time.Second
		if m.Interval < minInterval || m.Interval > maxInterval {
			return m, bad("interval_seconds must be between 60 and %d", int64(maxInterval/time.Second))
		}
	}
	if s.Prune != nil {
		m.Prune = *s.Prune
	}
	if s.Enabled != nil {
		m.Enabled = *s.Enabled
	}
	if s.PublishReleases || s.OutsideAPI != "" {
		if direction != Push {
			return m, bad("publish_releases and outside_api belong to a push mirror: a release is published on the host a mirror copies to")
		}
		if err := checkOutsideAPI(s.OutsideAPI); err != nil {
			return m, bad("%v", err)
		}
		m.PublishReleases, m.OutsideAPI = s.PublishReleases, strings.TrimSpace(s.OutsideAPI)
	}
	return m, nil
}

// checkOutsideAPI validates the address of an outside host's API.
func checkOutsideAPI(a string) error {
	a = strings.TrimSpace(a)
	if a == "" {
		return nil
	}
	u, err := url.Parse(a)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("outside_api must be an https or http address with no user name, password, query or fragment")
	}
	return nil
}

// ---------------------------------------------------------------- tables

const cols = `id, repo_id, direction, url, username, cred_env, cred_file, interval_secs, enabled, prune, state, last_try_at, last_ok_at,
	last_error, failures, next_at, synced_gen, dirty_since, ref_count, lease_until, created_at, created_by, publish_releases, outside_api`

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func scan(sc interface{ Scan(...any) error }) (Mirror, error) {
	var m Mirror
	var interval, tried, ok, next, dirty, lease, created int64
	var enabled, prune, publish int
	err := sc.Scan(&m.ID, &m.RepoID, &m.Direction, &m.URL, &m.Username, &m.Credential.Env, &m.Credential.File, &interval, &enabled, &prune, &m.State,
		&tried, &ok, &m.LastError, &m.Failures, &next, &m.SyncedGen, &dirty, &m.Refs, &lease, &created, &m.CreatedBy, &publish, &m.OutsideAPI)
	m.Interval = time.Duration(interval) * time.Second
	m.Enabled, m.Prune, m.PublishReleases = enabled != 0, prune != 0, publish != 0
	m.LastTry, m.LastOK, m.NextAt, m.DirtySince = fromMS(tried), fromMS(ok), fromMS(next), fromMS(dirty)
	m.LeaseUntil, m.CreatedAt = fromMS(lease), fromMS(created)
	return m, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) insert(ctx context.Context, m Mirror) (Mirror, error) {
	m.ID = ids.New("mir")
	m.State = StatePending
	m.CreatedAt = s.now().UTC()
	m.NextAt = m.CreatedAt
	_, err := s.db.Exec(ctx, `INSERT INTO repo_mirrors(id, repo_id, direction, url, username, cred_env, cred_file, interval_secs, enabled, prune, state,
		next_at, created_at, created_by, publish_releases, outside_api) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RepoID, m.Direction, m.URL, m.Username, m.Credential.Env, m.Credential.File, int64(m.Interval/time.Second), b2i(m.Enabled), b2i(m.Prune),
		m.State, ms(m.NextAt), ms(m.CreatedAt), m.CreatedBy, b2i(m.PublishReleases), m.OutsideAPI)
	if err != nil {
		if store.IsUnique(err) {
			return m, fmt.Errorf("%w: this repository already has that mirror", forge.ErrExists)
		}
		return m, err
	}
	return m, nil
}

// Get returns a mirror by its ID.
func (s *Service) Get(ctx context.Context, id string) (Mirror, error) {
	m, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM repo_mirrors WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, fmt.Errorf("%w: mirror %s", forge.ErrNotFound, id)
	}
	return m, err
}

func (s *Service) query(ctx context.Context, where string, args ...any) ([]Mirror, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM repo_mirrors `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mirror
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Of lists the mirrors of a repository, pull first.
func (s *Service) Of(ctx context.Context, repoID string) ([]Mirror, error) {
	return s.query(ctx, `WHERE repo_id = ? ORDER BY direction DESC, created_at, id`, repoID)
}

// All lists every mirror.
func (s *Service) All(ctx context.Context) ([]Mirror, error) {
	return s.query(ctx, `ORDER BY repo_id, direction DESC, created_at, id`)
}

// PullOf returns a repository's pull mirror, or forge.ErrNotFound.
func (s *Service) PullOf(ctx context.Context, repoID string) (Mirror, error) {
	ms, err := s.query(ctx, `WHERE repo_id = ? AND direction = 'pull'`, repoID)
	if err != nil {
		return Mirror{}, err
	}
	if len(ms) == 0 {
		return Mirror{}, fmt.Errorf("%w: this repository is not a pull mirror", forge.ErrNotFound)
	}
	return ms[0], nil
}

func (s *Service) due(ctx context.Context, limit int) ([]Mirror, error) {
	now := ms(s.now())
	return s.query(ctx, fmt.Sprintf(`WHERE enabled = 1 AND next_at <= ? AND lease_until < ? ORDER BY next_at LIMIT %d`, limit), now, now)
}

// claim takes a mirror's lease; false means another run holds it.
func (s *Service) claim(ctx context.Context, id string, d time.Duration) (bool, error) {
	now := s.now()
	res, err := s.db.Exec(ctx, `UPDATE repo_mirrors SET lease_holder = ?, lease_until = ? WHERE id = ? AND lease_until < ?`,
		s.o.Server, ms(now.Add(d)), id, ms(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Service) release(ctx context.Context, id string) {
	_, _ = s.db.Exec(context.WithoutCancel(ctx), `UPDATE repo_mirrors SET lease_holder = '', lease_until = 0 WHERE id = ?`, id)
}

// View describes a mirror for people.
func (s *Service) view(m Mirror, path string) View {
	now := s.now()
	v := View{ID: m.ID, Repository: path, Direction: m.Direction, URL: m.URL, Username: m.Username, Credential: m.Credential.String(),
		IntervalSeconds: int64(m.Interval / time.Second), Enabled: m.Enabled, Prune: m.Prune, State: m.State, Running: m.LeaseUntil.After(now),
		LastAttempt: m.LastTry, LastSuccess: m.LastOK, LastError: m.LastError, Failures: m.Failures, Refs: m.Refs,
		CreatedAt: m.CreatedAt, CreatedBy: m.CreatedBy, PublishReleases: m.PublishReleases, OutsideAPI: m.OutsideAPI}
	if m.Enabled {
		v.NextRun = m.NextAt
	}
	switch m.Direction {
	case Push:
		if !m.DirtySince.IsZero() {
			v.LagSeconds = int64(now.Sub(m.DirtySince) / time.Second)
		}
		v.Behind = m.Enabled && v.LagSeconds > int64(s.o.BehindAfter/time.Second)
	default:
		since := m.LastOK
		if since.IsZero() {
			since = m.CreatedAt
		}
		v.LagSeconds = int64(now.Sub(since) / time.Second)
		v.Behind = m.Enabled && v.LagSeconds > int64((2*m.Interval+s.o.BehindAfter)/time.Second)
	}
	if m.Failures > 0 && m.Direction == Pull {
		v.Behind = v.Behind || m.Failures >= 3
	}
	return v
}
