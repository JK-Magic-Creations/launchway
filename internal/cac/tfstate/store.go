// Package tfstate is Launchway's built-in Terraform and OpenTofu state: the
// HTTP backend protocol (see handler.go), kept in the database, so a team
// needs no outside bucket.
//
// State holds secrets in plain text by design of the format (generated
// passwords, provider credentials, private keys). So:
//
//   - every version is sealed under the installation's keyring, bound to its
//     own row, before it is stored; a copy of the database holds none of it;
//   - the interface and the API show resources and output names, never
//     values; the raw document leaves only through Pull, which the caller
//     guards with a permission and an audit record;
//   - every accepted state is a version (who, when, which run, which serial),
//     kept by a retention rule, and rolling back creates a new version.
package tfstate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// Limits.
const (
	MaxStateBytes = 64 << 20
	KeepVersions  = 50
	Purpose       = keyring.PurposeSecrets
	Table         = "tf_state_versions"
	Column        = "sealed"
)

// Errors a caller maps to a status.
var (
	ErrNotFound = errors.New("there is no such state")
	ErrLocked   = errors.New("the state is locked")
	ErrConflict = errors.New("the state conflicts with the current one")
	ErrInvalid  = errors.New("the state is not valid")
	ErrNoKey    = errors.New("state cannot be kept without a root key: it holds secrets, and Launchway seals them at rest (see launchway encryption)")
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidWorkspace reports whether name can name a workspace.
func ValidWorkspace(name string) bool { return nameRe.MatchString(name) }

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// TxDB is a database that can begin a transaction.
type TxDB interface {
	DB
	BeginTx(ctx context.Context) (Tx, error)
}

// Tx is a transaction.
type Tx interface {
	DB
	Commit() error
	Rollback() error
}

// Store keeps state.
type Store struct {
	DB     TxDB
	Sealer keyring.Sealer
	Now    func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Actor is who changes state: a person, or a run's step.
type Actor struct {
	Name  string
	RunID string
}

// Version is one stored state, without its content.
type Version struct {
	ID           string `json:"id"`
	Project      string `json:"project"`
	Workspace    string `json:"workspace"`
	Version      int    `json:"version"`
	Serial       int64  `json:"serial"`
	Lineage      string `json:"lineage"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Resources    int    `json:"resources"`
	Source       string `json:"source"` // write, push or rollback
	RestoredFrom int    `json:"restored_from,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	Actor        string `json:"actor"`
	CreatedAt    int64  `json:"created_at"`
}

// Workspace is one state address.
type Workspace struct {
	Project     string `json:"project"`
	Name        string `json:"name"`
	Lineage     string `json:"lineage"`
	Serial      int64  `json:"serial"`
	Version     int    `json:"version"`
	Environment string `json:"environment,omitempty"`
	Locked      *Lock  `json:"lock,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

// Lock is what a lock records; Info is the client's own lock document.
type Lock struct {
	ID        string          `json:"id"`
	RunID     string          `json:"run_id,omitempty"`
	Actor     string          `json:"actor"`
	CreatedAt int64           `json:"created_at"`
	Info      json.RawMessage `json:"info"`
}

// LockedError carries the holder, which the protocol returns to the client.
type LockedError struct{ Held Lock }

func (e *LockedError) Error() string { return ErrLocked.Error() }
func (e *LockedError) Unwrap() error { return ErrLocked }

type stateDoc struct {
	Version   int    `json:"version"`
	Serial    int64  `json:"serial"`
	Lineage   string `json:"lineage"`
	Resources []struct {
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Module    string `json:"module"`
		Provider  string `json:"provider"`
		Instances []struct {
			IndexKey any `json:"index_key"`
		} `json:"instances"`
	} `json:"resources"`
	Outputs map[string]struct {
		Sensitive bool `json:"sensitive"`
	} `json:"outputs"`
}

func parseState(body []byte) (*stateDoc, error) {
	if len(body) == 0 || len(body) > MaxStateBytes {
		return nil, fmt.Errorf("%w: the state must be between 1 byte and %d bytes", ErrInvalid, MaxStateBytes)
	}
	var d stateDoc
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("%w: not JSON: %v", ErrInvalid, err)
	}
	if d.Version == 0 || d.Lineage == "" {
		return nil, fmt.Errorf("%w: it has no version or lineage, so it is not a Terraform or OpenTofu state", ErrInvalid)
	}
	return &d, nil
}

func (s *Store) binding(id string) keyring.Binding {
	return keyring.Binding{Purpose: Purpose, Table: Table, Column: Column, Row: id}
}

func (s *Store) requireKey() error {
	if s.Sealer == nil || !s.Sealer.Enabled() {
		return ErrNoKey
	}
	return nil
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// ---- reading ----

const verCols = `id, project, workspace, version, serial, lineage, sha256, size, resources, source, restored_from, run_id, actor, created_at`

func scanVersion(sc interface{ Scan(...any) error }) (Version, error) {
	var v Version
	err := sc.Scan(&v.ID, &v.Project, &v.Workspace, &v.Version, &v.Serial, &v.Lineage, &v.SHA256, &v.Size, &v.Resources, &v.Source, &v.RestoredFrom, &v.RunID, &v.Actor, &v.CreatedAt)
	return v, err
}

// Workspaces lists a project's workspaces.
func (s *Store) Workspaces(ctx context.Context, project string) ([]Workspace, error) {
	rows, err := s.DB.Query(ctx, `SELECT project, name, lineage, serial, version, environment, created_at FROM tf_workspaces WHERE project = ? ORDER BY name`, project)
	if err != nil {
		return nil, err
	}
	out, err := scanWorkspaces(rows)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if l, err := s.lockOf(ctx, s.DB, project, out[i].Name); err == nil {
			out[i].Locked = l
		}
	}
	return out, nil
}

func scanWorkspaces(rows *sql.Rows) ([]Workspace, error) {
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.Project, &w.Name, &w.Lineage, &w.Serial, &w.Version, &w.Environment, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Workspace reads one.
func (s *Store) Workspace(ctx context.Context, project, name string) (*Workspace, error) {
	var w Workspace
	err := s.DB.QueryRow(ctx, `SELECT project, name, lineage, serial, version, environment, created_at FROM tf_workspaces WHERE project = ? AND name = ?`, project, name).
		Scan(&w.Project, &w.Name, &w.Lineage, &w.Serial, &w.Version, &w.Environment, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if l, err := s.lockOf(ctx, s.DB, project, name); err == nil {
		w.Locked = l
	}
	return &w, nil
}

// Versions lists a workspace's versions, newest first.
func (s *Store) Versions(ctx context.Context, project, workspace string) ([]Version, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+verCols+` FROM tf_state_versions WHERE project = ? AND workspace = ? ORDER BY version DESC`, project, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) versionRow(ctx context.Context, q DB, project, workspace string, n int) (Version, error) {
	var row *sql.Row
	if n == 0 {
		row = q.QueryRow(ctx, `SELECT `+verCols+` FROM tf_state_versions WHERE project = ? AND workspace = ? ORDER BY version DESC LIMIT 1`, project, workspace)
	} else {
		row = q.QueryRow(ctx, `SELECT `+verCols+` FROM tf_state_versions WHERE project = ? AND workspace = ? AND version = ?`, project, workspace, n)
	}
	v, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// Pull returns a version's raw state (version 0: the current one). It is the
// one way state leaves in clear; callers guard it with a permission, an
// audit record and, for people, a reason.
func (s *Store) Pull(ctx context.Context, project, workspace string, n int) (Version, []byte, error) {
	v, err := s.versionRow(ctx, s.DB, project, workspace, n)
	if err != nil {
		return v, nil, err
	}
	var sealed string
	if err := s.DB.QueryRow(ctx, `SELECT sealed FROM tf_state_versions WHERE id = ?`, v.ID).Scan(&sealed); err != nil {
		return v, nil, err
	}
	plain, err := s.Sealer.Open(ctx, s.binding(v.ID), sealed)
	if err != nil {
		return v, nil, fmt.Errorf("opening the stored state: %w", err)
	}
	return v, plain, nil
}

// ---- writing ----

// Put stores body as the next version of the workspace. lockID is the id the
// client holds a lock under ("" if it holds none). The first write fixes the
// lineage; a later write of another lineage is refused (adopting another
// state is Push with adopt). A serial lower than the current one is
// refused: it would take state back in time without saying so.
func (s *Store) Put(ctx context.Context, project, workspace, lockID string, body []byte, who Actor, source string, adopt bool) (Version, error) {
	if !ValidWorkspace(workspace) {
		return Version{}, fmt.Errorf("%w: a workspace name is lower case letters, digits, - and _", ErrInvalid)
	}
	if err := s.requireKey(); err != nil {
		return Version{}, err
	}
	doc, err := parseState(body)
	if err != nil {
		return Version{}, err
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	// Seal first: sealing reads the keyring from the database, which must
	// not happen while a transaction holds SQLite's write lock.
	id := ids.New("tfv")
	sealed, err := s.Sealer.Seal(ctx, s.binding(id), body)
	if err != nil {
		return Version{}, err
	}
	tx, err := s.DB.BeginTx(ctx)
	if err != nil {
		return Version{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	if held, err := s.lockOf(ctx, tx, project, workspace); err == nil && held.ID != lockID {
		return Version{}, &LockedError{Held: *held}
	}
	var curSerial int64
	var curLineage string
	var curVersion int
	err = tx.QueryRow(ctx, `SELECT serial, lineage, version FROM tf_workspaces WHERE project = ? AND name = ?`, project, workspace).Scan(&curSerial, &curLineage, &curVersion)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Version{}, err
	}
	if exists {
		if curLineage != doc.Lineage && !adopt {
			return Version{}, fmt.Errorf("%w: the lineage differs from the stored state's; to replace the state with another one, push it with adopt", ErrConflict)
		}
		if doc.Lineage == curLineage && doc.Serial < curSerial {
			return Version{}, fmt.Errorf("%w: serial %d is older than the stored serial %d", ErrConflict, doc.Serial, curSerial)
		}
		if doc.Lineage == curLineage && doc.Serial == curSerial {
			cur, err := s.versionRow(ctx, tx, project, workspace, 0)
			if err == nil && cur.SHA256 == hash {
				return cur, nil // the same state again: nothing to do
			}
			return Version{}, fmt.Errorf("%w: serial %d is already stored with different content", ErrConflict, doc.Serial)
		}
	}
	next := curVersion + 1
	now := ms(s.now())
	if !exists {
		if _, err := tx.Exec(ctx, `INSERT INTO tf_workspaces(project, name, lineage, serial, version, created_at) VALUES(?,?,?,?,?,?)`, project, workspace, doc.Lineage, doc.Serial, next, now); err != nil {
			return Version{}, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE tf_workspaces SET lineage = ?, serial = ?, version = ? WHERE project = ? AND name = ?`, doc.Lineage, doc.Serial, next, project, workspace); err != nil {
		return Version{}, err
	}
	count := 0
	for _, r := range doc.Resources {
		count += max(1, len(r.Instances))
	}
	v := Version{ID: id, Project: project, Workspace: workspace, Version: next, Serial: doc.Serial, Lineage: doc.Lineage, SHA256: hash,
		Size: int64(len(body)), Resources: count, Source: source, RunID: who.RunID, Actor: who.Name, CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO tf_state_versions(id, project, workspace, version, serial, lineage, sha256, size, sealed, resources, source, restored_from, run_id, actor, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, project, workspace, next, doc.Serial, doc.Lineage, hash, v.Size, sealed, count, source, 0, who.RunID, who.Name, now); err != nil {
		return Version{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tf_state_versions WHERE project = ? AND workspace = ? AND version <= ?`, project, workspace, next-KeepVersions); err != nil {
		return Version{}, err
	}
	return v, tx.Commit()
}

// Rollback makes an older version the current one by storing it again as a
// new version with a higher serial (state's own rule: the serial only grows).
// It changes no real resource: the next plan shows the difference.
func (s *Store) Rollback(ctx context.Context, project, workspace string, to int, who Actor) (Version, error) {
	if err := s.requireKey(); err != nil {
		return Version{}, err
	}
	if held, err := s.lockOf(ctx, s.DB, project, workspace); err == nil {
		return Version{}, &LockedError{Held: *held}
	}
	_, plain, err := s.Pull(ctx, project, workspace, to)
	if err != nil {
		return Version{}, err
	}
	cur, err := s.Workspace(ctx, project, workspace)
	if err != nil {
		return Version{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(plain, &doc); err != nil {
		return Version{}, err
	}
	doc["serial"] = cur.Serial + 1
	body, err := json.Marshal(doc)
	if err != nil {
		return Version{}, err
	}
	v, err := s.Put(ctx, project, workspace, "", body, who, "rollback", false)
	if err != nil {
		return v, err
	}
	_, err = s.DB.Exec(ctx, `UPDATE tf_state_versions SET restored_from = ? WHERE id = ?`, to, v.ID)
	v.RestoredFrom = to
	return v, err
}

// Delete removes a workspace and all its versions. Callers guard it with a
// permission; a locked workspace is refused.
func (s *Store) Delete(ctx context.Context, project, workspace string) error {
	if held, err := s.lockOf(ctx, s.DB, project, workspace); err == nil {
		return &LockedError{Held: *held}
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM tf_state_versions WHERE project = ? AND workspace = ?`, project, workspace); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM tf_workspaces WHERE project = ? AND name = ?`, project, workspace)
	return err
}

// ---- locks ----

func (s *Store) lockOf(ctx context.Context, q DB, project, workspace string) (*Lock, error) {
	var l Lock
	var info string
	err := q.QueryRow(ctx, `SELECT lock_id, info, run_id, actor, created_at FROM tf_locks WHERE project = ? AND workspace = ?`, project, workspace).Scan(&l.ID, &info, &l.RunID, &l.Actor, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	l.Info = json.RawMessage(info)
	return &l, nil
}

// Lock takes the lock. info is the client's lock document (it names an ID).
// A held lock comes back as a *LockedError with its details.
func (s *Store) Lock(ctx context.Context, project, workspace string, info []byte, who Actor) (Lock, error) {
	var in struct {
		ID string `json:"ID"`
	}
	if err := json.Unmarshal(info, &in); err != nil || in.ID == "" {
		return Lock{}, fmt.Errorf("%w: the lock document must be JSON with an ID", ErrInvalid)
	}
	if !ValidWorkspace(workspace) {
		return Lock{}, fmt.Errorf("%w: a workspace name is lower case letters, digits, - and _", ErrInvalid)
	}
	now := ms(s.now())
	res, err := s.DB.Exec(ctx, `INSERT INTO tf_locks(project, workspace, lock_id, info, run_id, actor, created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT (project, workspace) DO NOTHING`,
		project, workspace, in.ID, string(info), who.RunID, who.Name, now)
	if err != nil {
		return Lock{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		held, err := s.lockOf(ctx, s.DB, project, workspace)
		if err != nil {
			return Lock{}, err
		}
		return Lock{}, &LockedError{Held: *held}
	}
	return Lock{ID: in.ID, RunID: who.RunID, Actor: who.Name, CreatedAt: now, Info: info}, nil
}

// Unlock releases the lock the client holds under id.
func (s *Store) Unlock(ctx context.Context, project, workspace, id string) error {
	held, err := s.lockOf(ctx, s.DB, project, workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if held.ID != id {
		return &LockedError{Held: *held}
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM tf_locks WHERE project = ? AND workspace = ? AND lock_id = ?`, project, workspace, id)
	return err
}

// ForceUnlock releases whatever lock is held; the caller holds the
// permission, gives a reason and audits it.
func (s *Store) ForceUnlock(ctx context.Context, project, workspace string) (*Lock, error) {
	held, err := s.lockOf(ctx, s.DB, project, workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM tf_locks WHERE project = ? AND workspace = ?`, project, workspace)
	return held, err
}

// ReleaseRun releases every lock held by a run that has ended: Terraform has
// no lock heartbeat, so a crashed runner would otherwise leave one forever.
func (s *Store) ReleaseRun(ctx context.Context, runID string) (int, error) {
	if runID == "" {
		return 0, nil
	}
	res, err := s.DB.Exec(ctx, `DELETE FROM tf_locks WHERE run_id = ?`, runID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ---- what people see ----

// Summary is a state shown without a single value.
type Summary struct {
	Version   Version        `json:"version"`
	Resources []ResourceInfo `json:"resources"`
	Outputs   []OutputInfo   `json:"outputs"`
}

// ResourceInfo names a resource; its attributes are never listed.
type ResourceInfo struct {
	Address   string `json:"address"`
	Provider  string `json:"provider"`
	Instances int    `json:"instances"`
}

// OutputInfo names an output; values are never listed.
type OutputInfo struct {
	Name      string `json:"name"`
	Sensitive bool   `json:"sensitive"`
}

// Show describes a version without its values.
func (s *Store) Show(ctx context.Context, project, workspace string, n int) (*Summary, error) {
	v, plain, err := s.Pull(ctx, project, workspace, n)
	if err != nil {
		return nil, err
	}
	doc, err := parseState(plain)
	if err != nil {
		return nil, err
	}
	out := &Summary{Version: v, Resources: []ResourceInfo{}, Outputs: []OutputInfo{}}
	for _, r := range doc.Resources {
		addr := r.Type + "." + r.Name
		if r.Mode == "data" {
			addr = "data." + addr
		}
		if r.Module != "" {
			addr = r.Module + "." + addr
		}
		out.Resources = append(out.Resources, ResourceInfo{Address: addr, Provider: r.Provider, Instances: len(r.Instances)})
	}
	for name, o := range doc.Outputs {
		out.Outputs = append(out.Outputs, OutputInfo{Name: name, Sensitive: o.Sensitive})
	}
	sort.Slice(out.Outputs, func(i, j int) bool { return out.Outputs[i].Name < out.Outputs[j].Name })
	return out, nil
}
