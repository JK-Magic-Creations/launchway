package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// BaseID is the opaque identifier of a Base: "bs_" and 16 random bytes in
// hex. It is never derived from a name and never shown in an error.
type BaseID string

// DefaultBaseID is the Base every installation has. A single-Base
// installation has no other.
const DefaultBaseID BaseID = "bs_default"

// DefaultHandle is the default Base's handle.
const DefaultHandle = "default"

// Scope says which Base an operation is for. It has no exported fields and
// no exported constructor: the only ways to get one are the Registry's doors,
// and the architecture test lists the files that may call them.
type Scope struct {
	id BaseID
}

// Valid reports whether the scope names a Base.
func (s Scope) Valid() bool { return s.id != "" }

// ID returns the Base's identifier (for keys, prefixes and log fields; never
// for a message to the person asking).
func (s Scope) ID() BaseID { return s.id }

// Base describes a Base row.
type Base struct {
	ID        BaseID
	Handle    string
	Name      string
	Host      string
	Status    string // active, suspended
	CreatedAt int64
	CreatedBy string
}

// Registry knows the Bases and issues scopes.
type Registry struct {
	env   *Env
	multi bool
}

// NewRegistry returns the Registry; multi says whether the multi_base switch
// is on (a second Base can be created, and host and handle resolve to it).
func NewRegistry(env *Env, multi bool) *Registry { return &Registry{env: env, multi: multi} }

// Multi reports whether several Bases are turned on.
func (r *Registry) Multi() bool { return r.multi }

// DefaultScope is the scope of the default Base: how every code path that
// predates the wall acts (a single-Base installation has no other). With
// several Bases on, a request path must not use it; the architecture test
// allows it only in the files that adapt the old interfaces.
func (r *Registry) DefaultScope() Scope { return Scope{id: DefaultBaseID} }

// ScopeForHost is the router's door: the Base whose host name this is. With
// one Base it is always the default Base. An unknown host and a suspended
// Base give the same error, so a visitor learns nothing about which Bases
// exist.
func (r *Registry) ScopeForHost(ctx context.Context, host string) (Scope, error) {
	if !r.multi {
		return r.DefaultScope(), nil
	}
	return r.byColumn(ctx, true, strings.ToLower(host))
}

// ScopeForHandle is the router's door for the path form (/b/<handle>/).
func (r *Registry) ScopeForHandle(ctx context.Context, handle string) (Scope, error) {
	if !r.multi {
		return r.DefaultScope(), nil
	}
	return r.byColumn(ctx, false, strings.ToLower(handle))
}

func (r *Registry) byColumn(_ context.Context, byHost bool, val string) (Scope, error) {
	if val == "" {
		return Scope{}, fmt.Errorf("%w: base", ErrNotFound)
	}
	q := `SELECT id FROM bases WHERE handle = ? AND status = 'active'`
	if byHost {
		q = `SELECT id FROM bases WHERE host = ? AND status = 'active'`
	}
	var id string
	if err := r.env.DB.QueryRow(q, val).Scan(&id); err != nil {
		if isNoRows(err) {
			return Scope{}, fmt.Errorf("%w: base", ErrNotFound)
		}
		return Scope{}, err
	}
	return Scope{id: BaseID(id)}, nil
}

// ServiceScope is the door for a background job that must act in one named
// Base (a run's Base, found from the run). Every use is deliberate and the
// architecture test limits the callers.
func (r *Registry) ServiceScope(_ context.Context, id BaseID) (Scope, error) {
	var got string
	if err := r.env.DB.QueryRow(`SELECT id FROM bases WHERE id = ?`, string(id)).Scan(&got); err != nil {
		if isNoRows(err) {
			return Scope{}, fmt.Errorf("%w: base", ErrNotFound)
		}
		return Scope{}, err
	}
	return Scope{id: BaseID(got)}, nil
}

// ForEachBase runs fn once for each active Base, each with its own scope. One
// Base's failure does not stop the others; the errors come back joined.
func (r *Registry) ForEachBase(ctx context.Context, fn func(Scope) error) error {
	bases, err := r.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range bases {
		if b.Status != "active" {
			continue
		}
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := fn(Scope{id: b.ID}); err != nil {
			errs = append(errs, fmt.Errorf("base %s: %w", b.ID, err))
		}
	}
	return errors.Join(errs...)
}

// List returns every Base, oldest first. It is the operator's view (there is
// no scope to ask with): nothing a Base's own principal can call reaches it.
func (r *Registry) List(_ context.Context) ([]Base, error) {
	rows, err := r.env.DB.Query(`SELECT id, handle, name, host, status, created_at, created_by FROM bases ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Base
	for rows.Next() {
		var b Base
		var id string
		if err := rows.Scan(&id, &b.Handle, &b.Name, &b.Host, &b.Status, &b.CreatedAt, &b.CreatedBy); err != nil {
			return nil, err
		}
		b.ID = BaseID(id)
		out = append(out, b)
	}
	return out, rows.Err()
}

// NewBase says what a new Base is called.
type NewBase struct {
	Handle    string // lower-case label: its host name and path
	Name      string // display name
	Host      string // optional full host name
	CreatedBy string
	Now       int64
}

var handleRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)

// reserved are labels no Base may take: they name things of the service.
var reserved = map[string]bool{
	"default": true, "www": true, "api": true, "app": true, "admin": true, "vendor": true, "console": true, "status": true,
	"git": true, "ssh": true, "registry": true, "docs": true, "help": true, "support": true, "billing": true, "mail": true,
	"static": true, "assets": true, "cdn": true, "demo": true, "sandbox": true, "signup": true, "login": true, "auth": true,
	"launchway": true, "internal": true, "system": true, "root": true, "operator": true, "metrics": true, "health": true,
}

// ReservedHandles lists the labels no Base may take.
func ReservedHandles() []string {
	out := make([]string, 0, len(reserved))
	for k := range reserved {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CreateBase makes a new Base. It needs the multi_base switch. The first
// Base, the default, is made by the migration and cannot be made again.
func (r *Registry) CreateBase(_ context.Context, nb NewBase) (Base, error) {
	if !r.multi {
		return Base{}, ErrDisabled
	}
	handle := strings.ToLower(strings.TrimSpace(nb.Handle))
	if !handleRe.MatchString(handle) || reserved[handle] {
		return Base{}, fmt.Errorf("%w: that label cannot be used for a Base", ErrConflict)
	}
	host := strings.ToLower(strings.TrimSpace(nb.Host))
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Base{}, err
	}
	b := Base{ID: BaseID("bs_" + hex.EncodeToString(raw)), Handle: handle, Name: nb.Name, Host: host, Status: "active", CreatedAt: nb.Now, CreatedBy: nb.CreatedBy}
	if b.Name == "" {
		b.Name = handle
	}
	err := r.env.doGlobal(true, func(q Querier) error {
		_, err := q.Exec(`INSERT INTO bases(id, handle, name, host, status, created_at, created_by) VALUES(?,?,?,?,?,?,?)`,
			string(b.ID), b.Handle, b.Name, b.Host, b.Status, b.CreatedAt, b.CreatedBy)
		return err
	})
	if IsConflict(err) {
		return Base{}, fmt.Errorf("%w: that label cannot be used for a Base", ErrConflict)
	}
	return b, err
}

// SetStatus suspends or resumes a Base (active or suspended).
func (r *Registry) SetStatus(_ context.Context, id BaseID, status string) error {
	if status != "active" && status != "suspended" {
		return fmt.Errorf("%w: status %q", ErrConflict, status)
	}
	if id == DefaultBaseID && status != "active" {
		return fmt.Errorf("%w: the default Base cannot be suspended", ErrConflict)
	}
	return r.env.doGlobal(true, func(q Querier) error {
		res, err := q.Exec(`UPDATE bases SET status = ? WHERE id = ?`, status, string(id))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: base", ErrNotFound)
		}
		return nil
	})
}
