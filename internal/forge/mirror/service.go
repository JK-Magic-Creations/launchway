package mirror

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Managed are the ref namespaces a mirror keeps equal: branches, tags and
// notes. Other refs (a forge's pull request refs, Launchway's own) are never
// copied and never touched.
var Managed = []string{"refs/heads/", "refs/tags/", "refs/notes/"}

// Storage is what the service needs of the repository storage.
type Storage interface {
	Converge(ctx context.Context, repo string, o storage.ConvergeOptions) (storage.ConvergeResult, error)
	WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error
	Generation(ctx context.Context, repo string) (int64, error)
	Refs(ctx context.Context, repo string) (map[string]string, error)
}

// Creator makes hosted repositories (the internal provider's Repositories).
type Creator interface {
	Create(ctx context.Context, r forge.NewRepository) (forge.Repository, error)
}

// SetCreator says how an import makes repositories.
func (s *Service) SetCreator(c Creator) { s.o.Creator = c }

// Options configure a Service.
type Options struct {
	// Server names this server (the holder of leases).
	Server string
	// Guard decides which addresses may be contacted; required.
	Guard *egress.Guard
	// MaxDuration is the longest one run may take (default 6h); IdleTimeout
	// is how long a transfer may make no progress (default 5m).
	MaxDuration, IdleTimeout time.Duration
	// Concurrent is how many mirrors run at once on this server (default 2).
	Concurrent int
	// BehindAfter is how long a push mirror may wait before it is "behind"
	// and an alert is raised (default 15m).
	BehindAfter time.Duration
	// Tick is how often due mirrors are looked for (default 10s).
	Tick time.Duration
	// Creator makes the repositories an import brings in.
	Creator Creator
	// Limits is the memory, CPU and file-size ceiling of every git process.
	Limits gitexec.Limits
	Now    func() time.Time
	Log    *slog.Logger
}

// Service runs mirrors.
type Service struct {
	db   store.DB
	st   *store.Store
	sto  Storage
	conn forge.Connection
	o    Options
	wake chan struct{}

	mu      sync.Mutex
	alerted map[string]bool
	// onChange is called after a sync changed a repository.
	onChange func(ctx context.Context, repoID string)
}

// New makes a service for the repositories of one connection.
func New(db store.DB, st *store.Store, sto Storage, conn forge.Connection, o Options) *Service {
	if o.Guard == nil {
		o.Guard, _ = egress.New(nil)
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = 6 * time.Hour
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 5 * time.Minute
	}
	if o.Concurrent <= 0 {
		o.Concurrent = 2
	}
	if o.BehindAfter <= 0 {
		o.BehindAfter = 15 * time.Minute
	}
	if o.Tick <= 0 {
		o.Tick = 10 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Server == "" {
		o.Server = "server"
	}
	return &Service{db: db, st: st, sto: sto, conn: conn, o: o, wake: make(chan struct{}, 1), alerted: map[string]bool{}}
}

func (s *Service) now() time.Time { return s.o.Now() }

// Guard is the egress guard the service applies.
func (s *Service) Guard() *egress.Guard { return s.o.Guard }

func (s *Service) repoByPath(ctx context.Context, path string) (forge.Repository, error) {
	r, err := s.st.RepositoryByPath(ctx, s.conn.ID, path)
	if err != nil {
		return r, err
	}
	if store.InTrash(r) {
		return r, fmt.Errorf("%w: repository %s", forge.ErrNotFound, path)
	}
	return r, nil
}

func (s *Service) pathOf(ctx context.Context, repoID string) string {
	if r, err := s.st.Repository(ctx, repoID); err == nil {
		return r.Path
	}
	return repoID
}

// View describes one mirror.
func (s *Service) View(ctx context.Context, m Mirror) View { return s.view(m, s.pathOf(ctx, m.RepoID)) }

// Views lists the mirrors of a repository.
func (s *Service) Views(ctx context.Context, repoPath string) ([]View, error) {
	r, err := s.repoByPath(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	ms, err := s.Of(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(ms))
	for _, m := range ms {
		out = append(out, s.view(m, r.Path))
	}
	return out, nil
}

// AllViews lists every mirror of every live repository.
func (s *Service) AllViews(ctx context.Context) ([]View, error) {
	ms, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	var out []View
	for _, m := range ms {
		p, ok := paths[m.RepoID]
		if !ok {
			r, err := s.st.Repository(ctx, m.RepoID)
			if err != nil || store.InTrash(r) {
				paths[m.RepoID] = ""
				continue
			}
			p = r.Path
			paths[m.RepoID] = p
		}
		if p != "" {
			out = append(out, s.view(m, p))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Repository < out[j].Repository })
	return out, nil
}

// SetPull makes a repository a pull mirror of an address, or changes the
// mirror it already is. A repository that has content of its own is never
// made a mirror: its content would be overwritten by whoever controls the
// address.
func (s *Service) SetPull(ctx context.Context, repoPath string, in Spec, by string) (Mirror, error) {
	r, err := s.repoByPath(ctx, repoPath)
	if err != nil {
		return Mirror{}, err
	}
	m, err := in.mirror(Pull, s.o.Guard.AllowFiles)
	if err != nil {
		return m, err
	}
	if err := s.o.Guard.Check(ctx, m.URL); err != nil {
		return m, fmt.Errorf("%w: mirror: %v", forge.ErrInvalid, err)
	}
	m.RepoID, m.CreatedBy = r.ID, by
	if cur, err := s.PullOf(ctx, r.ID); err == nil {
		_, err := s.db.Exec(ctx, `UPDATE repo_mirrors SET url = ?, username = ?, cred_env = ?, cred_file = ?, interval_secs = ?, enabled = ?, prune = ?,
			next_at = ?, failures = 0 WHERE id = ?`, m.URL, m.Username, m.Credential.Env, m.Credential.File, int64(m.Interval/time.Second),
			b2i(m.Enabled), b2i(m.Prune), ms(s.now()), cur.ID)
		if err != nil {
			return m, err
		}
		s.kick()
		return s.Get(ctx, cur.ID)
	}
	if r.Mirror {
		return m, fmt.Errorf("%w: %s is a mirror already", forge.ErrExists, r.Path)
	}
	refs, err := s.sto.Refs(ctx, r.ID)
	if err != nil {
		return m, err
	}
	for n := range refs {
		if hasManaged(n) {
			return m, fmt.Errorf("%w: %s has branches or tags of its own. Mirroring it would replace them with whatever the address holds. Make a new repository for the mirror, or leave this one as it is", forge.ErrInvalid, r.Path)
		}
	}
	m, err = s.insert(ctx, m)
	if err != nil {
		return m, err
	}
	if err := s.st.SetMirror(ctx, r.ID, true); err != nil {
		return m, err
	}
	s.kick()
	return m, nil
}

func hasManaged(name string) bool {
	for _, p := range Managed {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Update changes whether a mirror runs, how often, and whether it prunes.
func (s *Service) Update(ctx context.Context, id string, enabled *bool, interval *int64, prune *bool) (Mirror, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return m, err
	}
	if enabled != nil {
		m.Enabled = *enabled
	}
	if prune != nil {
		m.Prune = *prune
	}
	if interval != nil {
		d := time.Duration(*interval) * time.Second
		if d < minInterval || d > maxInterval {
			return m, fmt.Errorf("%w: mirror: interval_seconds must be between 60 and %d", forge.ErrInvalid, int64(maxInterval/time.Second))
		}
		m.Interval = d
	}
	if _, err := s.db.Exec(ctx, `UPDATE repo_mirrors SET enabled = ?, prune = ?, interval_secs = ?, next_at = ? WHERE id = ?`,
		b2i(m.Enabled), b2i(m.Prune), int64(m.Interval/time.Second), ms(s.now()), id); err != nil {
		return m, err
	}
	s.kick()
	return s.Get(ctx, id)
}

// Remove deletes a push mirror. A pull mirror is ended with Promote, which is
// explicit about what it does to the repository.
func (s *Service) Remove(ctx context.Context, id string) (Mirror, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return m, err
	}
	if m.Direction == Pull {
		return m, fmt.Errorf("%w: a pull mirror is ended by making the repository the primary (stop mirroring), which lets it take pushes", forge.ErrInvalid)
	}
	_, err = s.db.Exec(ctx, `DELETE FROM repo_mirrors WHERE id = ?`, id)
	return m, err
}

// Promote stops a repository being a mirror and makes it the primary: it
// takes pushes from now on, and the pull mirror is deleted. With syncFirst
// the mirror runs once more first, so the primary starts from everything the
// old primary had; if that run fails, nothing changes and the error is
// returned.
func (s *Service) Promote(ctx context.Context, repoPath string, syncFirst bool, by string) (Mirror, error) {
	r, err := s.repoByPath(ctx, repoPath)
	if err != nil {
		return Mirror{}, err
	}
	m, err := s.PullOf(ctx, r.ID)
	if err != nil {
		return m, err
	}
	if syncFirst {
		if _, err := s.SyncNow(ctx, m.ID); err != nil {
			return m, fmt.Errorf("the final sync failed, so the repository is still a mirror: %w", err)
		}
	}
	ok, err := s.claim(ctx, m.ID, 5*time.Minute)
	if err != nil {
		return m, err
	}
	if !ok {
		return m, ErrBusy
	}
	defer s.release(ctx, m.ID)
	if err := s.st.SetMirror(ctx, r.ID, false); err != nil {
		return m, err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM repo_mirrors WHERE id = ?`, m.ID); err != nil {
		_ = s.st.SetMirror(ctx, r.ID, true)
		return m, err
	}
	s.event(ctx, r.ID, "mirror.promoted", "stopped mirroring "+m.URL+"; the repository is the primary", by)
	s.changed(ctx, r.ID) // its push mirrors now carry what the primary has
	return m, nil
}

// SyncNow runs one mirror now and returns it as it is afterwards. A failure
// of the run is the returned error as well as the mirror's last error.
func (s *Service) SyncNow(ctx context.Context, id string) (Mirror, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return m, err
	}
	ok, err := s.claim(ctx, id, s.o.MaxDuration+time.Minute)
	if err != nil {
		return m, err
	}
	if !ok {
		return m, ErrBusy
	}
	defer s.release(ctx, id)
	runErr := s.run(ctx, m)
	m, err = s.Get(ctx, id)
	if err != nil {
		return m, err
	}
	return m, runErr
}

// event records something a mirror did in the connection's event log.
func (s *Service) event(ctx context.Context, repoID, kind, detail, outcome string) {
	_ = s.st.AppendEvent(context.WithoutCancel(ctx), store.Event{Connection: s.conn.ID, Repository: repoID, Kind: forge.EventKind(kind), Outcome: outcome, Detail: detail})
}

func (s *Service) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Start runs due mirrors until ctx ends, and raises the alert for a push
// mirror that has fallen behind.
// The returned channel is closed once it has stopped and no run is left.
func (s *Service) Start(ctx context.Context) <-chan struct{} {
	sem := make(chan struct{}, s.o.Concurrent)
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer wg.Wait()
		for {
			s.tick(ctx, sem, &wg)
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			case <-time.After(s.o.Tick):
			}
		}
	}()
	return done
}

func (s *Service) tick(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	if ctx.Err() != nil {
		return
	}
	s.checkBehind(ctx)
	due, err := s.due(ctx, 50)
	if err != nil {
		s.o.Log.Warn("looking for mirrors that are due", "err", err)
		return
	}
	for _, m := range due {
		select {
		case sem <- struct{}{}:
		default:
			return // all slots busy; the rest are picked up on the next tick
		}
		ok, err := s.claim(ctx, m.ID, s.o.MaxDuration+time.Minute)
		if err != nil || !ok {
			<-sem
			continue
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done(); s.release(ctx, m.ID) }()
			_ = s.run(ctx, m)
		}()
	}
}

// checkBehind raises an alert (a log line at warning level and an event in
// the connection's log, "mirror.behind") once when a mirror falls behind, and
// "mirror.recovered" when it catches up.
func (s *Service) checkBehind(ctx context.Context) {
	all, err := s.All(ctx)
	if err != nil {
		return
	}
	for _, m := range all {
		v := s.view(m, "")
		s.mu.Lock()
		was := s.alerted[m.ID]
		s.alerted[m.ID] = v.Behind
		s.mu.Unlock()
		switch {
		case v.Behind && !was:
			path := s.pathOf(ctx, m.RepoID)
			s.o.Log.Warn("a mirror is behind", "repository", path, "direction", m.Direction, "lag_seconds", v.LagSeconds, "error", m.LastError)
			s.event(ctx, m.RepoID, "mirror.behind", fmt.Sprintf("%s mirror of %s is %ds behind: %s", m.Direction, path, v.LagSeconds, m.LastError), "behind")
		case !v.Behind && was:
			path := s.pathOf(ctx, m.RepoID)
			s.o.Log.Info("a mirror caught up", "repository", path, "direction", m.Direction)
			s.event(ctx, m.RepoID, "mirror.recovered", fmt.Sprintf("%s mirror of %s caught up", m.Direction, path), "ok")
		}
	}
}

// backoff is how long to wait after the n-th failure in a row: a minute,
// doubling, at most an hour.
func backoff(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// run runs one mirror, whose lease the caller holds, and records the result.
func (s *Service) run(ctx context.Context, m Mirror) error {
	ctx, cancel := context.WithTimeout(ctx, s.o.MaxDuration)
	defer cancel()
	started := s.now()
	refs, err := s.runPull(ctx, m)
	s.finishPull(ctx, m, started, refs, err)
	return err
}

func (s *Service) finishPull(ctx context.Context, m Mirror, started time.Time, refs int, err error) {
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	if err == nil {
		_, uerr := s.db.Exec(ctx, `UPDATE repo_mirrors SET state = ?, last_try_at = ?, last_ok_at = ?, last_error = '', failures = 0, next_at = ?, ref_count = ? WHERE id = ?`,
			StateOK, ms(started), ms(now), ms(now.Add(m.Interval)), refs, m.ID)
		if uerr != nil {
			s.o.Log.Warn("recording a mirror's success", "mirror", m.ID, "err", uerr)
		}
		return
	}
	msg := s.clean(m, err.Error())
	_, uerr := s.db.Exec(ctx, `UPDATE repo_mirrors SET state = ?, last_try_at = ?, last_error = ?, failures = failures + 1, next_at = ? WHERE id = ?`,
		StateError, ms(started), msg, ms(now.Add(backoff(m.Failures+1))), m.ID)
	if uerr != nil {
		s.o.Log.Warn("recording a mirror's failure", "mirror", m.ID, "err", uerr)
	}
	s.o.Log.Warn("a pull mirror failed", "repository", s.pathOf(ctx, m.RepoID), "error", msg)
}

// clean removes credentials from an error before it is kept or shown, and
// bounds its length.
func (s *Service) clean(m Mirror, msg string) string {
	msg = redactText(msg)
	if tok, err := m.Credential.Resolve(); err == nil && len(tok) > 3 {
		msg = strings.ReplaceAll(msg, tok, "***")
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > 800 {
		msg = msg[:800] + "…"
	}
	return msg
}

var errNotMirror = errors.New("this repository is not a mirror, so a sync will not write to it")
