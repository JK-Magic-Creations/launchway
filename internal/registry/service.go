package registry

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// DB is what the registry needs of a database: Launchway's engine database
// satisfies it through a small adapter (see internal/api/registry_wire.go).
// Queries are written with ? placeholders.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
	Begin(ctx context.Context) (Tx, error)
}

// Tx is a transaction.
type Tx interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
	Commit() error
	Rollback() error
}

// execer is what both a DB and a Tx do.
type execer interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Limits keeps the registry from being used up.
type Limits struct {
	// MaxManifestBytes is the largest manifest accepted (default 4 MiB, the
	// size the Distribution specification asks registries to accept).
	MaxManifestBytes int64
	// MaxBlobBytes is the largest blob accepted (default 10 GiB).
	MaxBlobBytes int64
	// UploadTTL is how long an upload may sit unused before it is collected
	// (default 24 hours).
	UploadTTL time.Duration
	// GCGrace is how old an unreferenced blob or link must be before garbage
	// collection removes it, so that a push in progress is never collected
	// under its own feet (default 1 hour).
	GCGrace time.Duration
	// DefaultQuotaBytes is the quota of a project with no policy of its own
	// (default 20 GiB; a negative number is no quota).
	DefaultQuotaBytes int64
	// RequestsPerMinute caps the requests one caller makes (default 3000;
	// negative: no cap).
	RequestsPerMinute int
	// MaxRepositoriesPerProject and MaxTagsPerRepository (defaults 500 and
	// 5000) keep one project from filling the lists.
	MaxRepositoriesPerProject int
	MaxTagsPerRepository      int
}

func (l Limits) withDefaults() Limits {
	if l.MaxManifestBytes <= 0 {
		l.MaxManifestBytes = 4 << 20
	}
	if l.MaxBlobBytes <= 0 {
		l.MaxBlobBytes = 10 << 30
	}
	if l.UploadTTL <= 0 {
		l.UploadTTL = 24 * time.Hour
	}
	if l.GCGrace <= 0 {
		l.GCGrace = time.Hour
	}
	if l.DefaultQuotaBytes == 0 {
		l.DefaultQuotaBytes = 20 << 30
	}
	if l.RequestsPerMinute == 0 {
		l.RequestsPerMinute = 3000
	}
	if l.MaxRepositoriesPerProject <= 0 {
		l.MaxRepositoriesPerProject = 500
	}
	if l.MaxTagsPerRepository <= 0 {
		l.MaxTagsPerRepository = 5000
	}
	return l
}

// Event is something that happened to the registry's content, for the
// audit trail and webhooks.
type Event struct {
	Type    string // push, delete, tag, untag, retention, gc, quota
	Repo    string
	Digest  string
	Tag     string
	Actor   string
	Run     string
	Detail  string
	Outcome string // "ok" (default) or "denied"
	Size    int64
	Source  string // the address the request came from
}

type sourceKey struct{}

// WithSource tells the service where a request came from, for the events it
// emits.
func WithSource(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, sourceKey{}, addr)
}

// Options make a Service.
type Options struct {
	DB    DB
	Blobs blobstore.Store
	// Org says which organization a project belongs to; blobs are shared
	// only inside one (default: "default" for every project).
	Org func(project string) string
	// Protected, when it says true, keeps a manifest from retention and from
	// deletion by retention (a digest that is deployed, for example).
	Protected func(ctx context.Context, repo, digest string) bool
	// OnEvent is told of pushes, deletes and collections; it must not block.
	OnEvent func(Event)
	Limits  Limits
	// Server names this process in leases.
	Server string
	Now    func() time.Time
	Log    *slog.Logger
}

// Service is the registry's content: repositories, blobs, manifests, tags,
// uploads, policies, collection.
type Service struct {
	db     DB
	blobs  blobstore.Store
	o      Options
	limits Limits
	log    *slog.Logger
}

// New makes a service. Blobs may be nil: the service then refuses every
// write (the registry is off without a blob store).
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Org == nil {
		o.Org = func(string) string { return "default" }
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Server == "" {
		o.Server = "server"
	}
	lim := o.Limits.withDefaults()
	// A store that holds objects up to some size (the database store) bounds a blob: refuse
	// it plainly at that size rather than fail while storing it.
	if l, ok := o.Blobs.(interface{ Limits() (int64, int64) }); ok {
		if _, biggest := l.Limits(); biggest > 0 && lim.MaxBlobBytes > biggest {
			lim.MaxBlobBytes = biggest
		}
	}
	return &Service{db: o.DB, blobs: o.Blobs, o: o, limits: lim, log: o.Log}
}

// Limits returns the limits in force.
func (s *Service) Limits() Limits { return s.limits }

func (s *Service) now() int64 { return s.o.Now().UnixMilli() }

func (s *Service) emit(ctx context.Context, e Event) {
	if e.Outcome == "" {
		e.Outcome = "ok"
	}
	e.Source, _ = ctx.Value(sourceKey{}).(string)
	if s.o.OnEvent != nil {
		s.o.OnEvent(e)
	}
}

func (s *Service) org(project string) string { return s.o.Org(project) }

// ensureStore refuses when there is no blob store.
func (s *Service) ensureStore() error {
	if s.blobs == nil {
		return newErr(503, CodeUnavailable, "the built-in registry has no blob store: artifacts are off on this server", "Turn on the artifact store (artifacts.store), then restart.")
	}
	return nil
}

// withTx runs fn in a transaction, committing when it returns nil.
func (s *Service) withTx(ctx context.Context, fn func(tx Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// column runs a query that returns one text column and returns its values.
func column(ctx context.Context, q execer, query string, args ...any) ([]string, error) {
	var out []string
	err := scanAll(ctx, q, query, args, func(scan func(...any) error) error {
		var v string
		if err := scan(&v); err != nil {
			return err
		}
		out = append(out, v)
		return nil
	})
	return out, err
}

// scanAll runs a query and calls each for every row, closing the rows and
// returning their error.
func scanAll(ctx context.Context, q execer, query string, args []any, each func(scan func(...any) error) error) error {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}
