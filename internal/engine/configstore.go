package engine

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Configuration in the database (schema 23). This file is only storage:
// objects, their history and a revision counter. What the objects mean,
// how they are checked and how they are applied is internal/settings.
//
// config_changes is the log, appended to in the same transaction as each
// change and never rewritten; config_objects is the state the log folds to.
// Every change raises the organisation's revision in config_revisions; a
// writer says which revision it checked its change against, and the change
// is refused (ErrConfigMoved) if another change came first. That makes the
// whole configuration the unit of validation: two changes that are each
// valid but not together (deleting an environment while a pool is pointed
// at it) cannot both commit.

// schemaConfig is the schema that brought configuration in the database.
const schemaConfig = 23

// ErrConfigMoved: the configuration changed since the revision a write was
// checked against. The writer checks again and retries.
var ErrConfigMoved = errors.New("the configuration changed meanwhile")

// Config managers: who owns an object (see ConfigObject.ManagedBy).
const (
	ManagedByAPI       = "api"
	ManagedByImport    = "import"
	ManagedByTerraform = "terraform"
)

// Config actions, in the history.
const (
	ConfigCreate  = "create"
	ConfigUpdate  = "update"
	ConfigDelete  = "delete"
	ConfigRestore = "restore"
)

// ConfigObject is one stored configuration object.
type ConfigObject struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Unit is the unit the object belongs to ("" is the organisation): for
	// a project, the unit it is in; for anything else, the level it is
	// defined at, which it applies to.
	Unit      string          `json:"unit"`
	Spec      json.RawMessage `json:"spec"`
	Version   int64           `json:"version"`
	ManagedBy string          `json:"managed_by"`
	CreatedAt int64           `json:"created_at"`
	CreatedBy string          `json:"created_by"`
	UpdatedAt int64           `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
	// DeletedAt is set on a deleted object, which can be restored.
	DeletedAt int64  `json:"deleted_at,omitempty"`
	DeletedBy string `json:"deleted_by,omitempty"`
}

// Deleted reports whether the object is deleted.
func (o ConfigObject) Deleted() bool { return o.DeletedAt != 0 }

// ConfigChange is one entry of an object's history.
type ConfigChange struct {
	ID         int64           `json:"id"`
	Revision   int64           `json:"revision"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Version    int64           `json:"version"`
	Action     string          `json:"action"`
	Actor      string          `json:"actor"`
	At         int64           `json:"at"`
	ManagedBy  string          `json:"managed_by"`
	UnitBefore string          `json:"unit_before"`
	UnitAfter  string          `json:"unit_after"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	ImportID   string          `json:"import_id,omitempty"`
	Comment    string          `json:"comment,omitempty"`
}

// ConfigWrite is one change to make.
type ConfigWrite struct {
	Kind, Name string
	Action     string // ConfigCreate or ConfigUpdate (both: put), ConfigDelete, ConfigRestore
	Unit       string
	Spec       json.RawMessage
	// IfVersion: -1 writes whatever the version; 0 only creates (the
	// object must not exist, or be deleted); otherwise the object's version
	// must be this.
	IfVersion int64
	ManagedBy string
	Comment   string
}

// ConfigImport is an import recorded with the changes it made.
type ConfigImport struct {
	ID       string          `json:"id"`
	At       int64           `json:"at"`
	Actor    string          `json:"actor"`
	Origin   string          `json:"origin"` // the file, or what sent it
	Digest   string          `json:"digest"`
	Summary  json.RawMessage `json:"summary"`
	Revision int64           `json:"revision"`
}

// ConfigKnown reports whether this build and its database keep
// configuration in the database.
func (e *Engine) ConfigKnown() bool { return e.knows(schemaConfig) }

func (e *Engine) configCheck() error {
	if !e.knows(schemaConfig) {
		return fmt.Errorf("%w: this server does not keep configuration in the database (schema %d)", ErrConflict, schemaConfig)
	}
	return nil
}

// ConfigRevision returns the organisation's configuration revision: 0 when
// nothing was ever stored.
func (e *Engine) ConfigRevision() (int64, error) {
	if err := e.configCheck(); err != nil {
		return 0, err
	}
	var rev int64
	err := e.db.QueryRow(`SELECT revision FROM config_revisions WHERE org = ?`, "").Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

const configCols = `kind, name, unit, spec, version, managed_by, created_at, created_by, updated_at, updated_by, deleted_at, deleted_by`

func scanConfig(sc interface{ Scan(...any) error }) (ConfigObject, error) {
	var o ConfigObject
	var spec string
	err := sc.Scan(&o.Kind, &o.Name, &o.Unit, &spec, &o.Version, &o.ManagedBy, &o.CreatedAt, &o.CreatedBy, &o.UpdatedAt, &o.UpdatedBy,
		&o.DeletedAt, &o.DeletedBy)
	o.Spec = json.RawMessage(spec)
	return o, err
}

// ConfigSnapshot returns every object, deleted ones too, and the revision
// they are at, read together.
func (e *Engine) ConfigSnapshot() (int64, []ConfigObject, error) {
	if err := e.configCheck(); err != nil {
		return 0, nil, err
	}
	tx, err := e.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var rev int64
	if err := tx.QueryRow(`SELECT revision FROM config_revisions WHERE org = ?`, "").Scan(&rev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	rows, err := tx.Query(`SELECT `+configCols+` FROM config_objects WHERE org = ? ORDER BY kind, name`, "")
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var out []ConfigObject
	for rows.Next() {
		o, err := scanConfig(rows)
		if err != nil {
			return 0, nil, err
		}
		out = append(out, o)
	}
	return rev, out, rows.Err()
}

// WriteConfig makes changes, all or none, if the configuration is still at
// revision base. It returns the new revision and the changes recorded; a
// put that changes nothing records nothing (so applying the same thing
// twice is quiet). imp, if given, is recorded as the import that made them.
func (e *Engine) WriteConfig(base int64, actor string, writes []ConfigWrite, imp *ConfigImport) (int64, []ConfigChange, error) {
	if err := e.configCheck(); err != nil {
		return 0, nil, err
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	now := e.now().UnixMilli()
	var rev int64
	err = tx.QueryRow(`SELECT revision FROM config_revisions WHERE org = ?`, "").Scan(&rev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// The first change: create the row, or find that another server
		// just did (its change came first).
		res, err := tx.Exec(`INSERT INTO config_revisions(org, revision, updated_at) VALUES(?, 0, ?) ON CONFLICT(org) DO NOTHING`, "", now)
		if err != nil {
			return 0, nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, nil, ErrConfigMoved
		}
	case err != nil:
		return 0, nil, err
	}
	if rev != base {
		return 0, nil, ErrConfigMoved
	}
	next := rev + 1
	// Taking the row first serialises writers on PostgreSQL; the check
	// that it is still at rev catches one that committed in between.
	res, err := tx.Exec(`UPDATE config_revisions SET revision = ?, updated_at = ? WHERE org = ? AND revision = ?`, next, now, "", rev)
	if err != nil {
		if isConflict(err) {
			return 0, nil, ErrConfigMoved
		}
		return 0, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, nil, ErrConfigMoved
	}
	impID := ""
	if imp != nil {
		if imp.ID == "" {
			imp.ID = ids.New("imp")
		}
		impID = imp.ID
	}
	var changes []ConfigChange
	for _, w := range writes {
		c, err := e.writeOne(tx, w, actor, now, next, impID)
		if err != nil {
			return 0, nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	if imp != nil {
		imp.At, imp.Actor, imp.Revision = now, actor, next
		if len(imp.Summary) == 0 {
			imp.Summary = json.RawMessage("{}")
		}
		if _, err := tx.Exec(`INSERT INTO config_imports(id, org, at, actor, origin, digest, summary, revision) VALUES(?,?,?,?,?,?,?,?)`,
			imp.ID, "", now, actor, imp.Origin, imp.Digest, string(imp.Summary), next); err != nil {
			return 0, nil, err
		}
	}
	if len(changes) == 0 && imp == nil {
		// Nothing changed: leave the revision where it was.
		return rev, nil, nil
	}
	if err := e.notifyOthers(tx, note{Kind: noteConfig}); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		if isConflict(err) {
			return 0, nil, ErrConfigMoved
		}
		return 0, nil, err
	}
	e.configChanged()
	return next, changes, nil
}

func (e *Engine) writeOne(tx *txx, w ConfigWrite, actor string, now, rev int64, impID string) (*ConfigChange, error) {
	cur, err := scanConfig(tx.QueryRow(`SELECT `+configCols+` FROM config_objects WHERE org = ? AND kind = ? AND name = ?`, "", w.Kind, w.Name))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	what := w.Kind + " " + w.Name
	switch {
	case w.IfVersion > 0 && (!exists || cur.Version != w.IfVersion):
		have := int64(0)
		if exists {
			have = cur.Version
		}
		return nil, fmt.Errorf("%w: %s is at version %d, not %d: someone changed it meanwhile; read it again", ErrConflict, what, have, w.IfVersion)
	case w.IfVersion == 0 && exists && !cur.Deleted() && (w.Action == ConfigCreate || w.Action == ConfigUpdate):
		return nil, fmt.Errorf("%w: %s already exists", ErrConflict, what)
	}
	if w.ManagedBy == "" {
		w.ManagedBy = ManagedByAPI
	}
	c := &ConfigChange{Revision: rev, Kind: w.Kind, Name: w.Name, Actor: actor, At: now, ManagedBy: w.ManagedBy, ImportID: impID, Comment: w.Comment}
	if exists {
		c.UnitBefore, c.Before = cur.Unit, cur.Spec
		if cur.Deleted() {
			c.Before = nil
		}
	}
	switch w.Action {
	case ConfigCreate, ConfigUpdate:
		spec := compactJSON(w.Spec)
		if exists && !cur.Deleted() && cur.Unit == w.Unit && bytes.Equal(compactJSON(cur.Spec), spec) && cur.ManagedBy == w.ManagedBy {
			return nil, nil // nothing changes
		}
		c.UnitAfter, c.After = w.Unit, spec
		if !exists {
			c.Action, c.Version = ConfigCreate, 1
			_, err = tx.Exec(`INSERT INTO config_objects(org, `+configCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,0,'')`,
				"", w.Kind, w.Name, w.Unit, string(spec), 1, w.ManagedBy, now, actor, now, actor)
		} else {
			c.Action, c.Version = ConfigUpdate, cur.Version+1
			if cur.Deleted() {
				c.Action = ConfigCreate // made again, keeping its history
			}
			_, err = tx.Exec(`UPDATE config_objects SET unit = ?, spec = ?, version = ?, managed_by = ?, updated_at = ?, updated_by = ?,
				deleted_at = 0, deleted_by = '' WHERE org = ? AND kind = ? AND name = ?`,
				w.Unit, string(spec), c.Version, w.ManagedBy, now, actor, "", w.Kind, w.Name)
		}
	case ConfigDelete:
		if !exists || cur.Deleted() {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, what)
		}
		c.Action, c.Version, c.UnitAfter = ConfigDelete, cur.Version+1, cur.Unit
		_, err = tx.Exec(`UPDATE config_objects SET version = ?, updated_at = ?, updated_by = ?, deleted_at = ?, deleted_by = ?
			WHERE org = ? AND kind = ? AND name = ?`, c.Version, now, actor, now, actor, "", w.Kind, w.Name)
	case ConfigRestore:
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, what)
		}
		if !cur.Deleted() {
			return nil, fmt.Errorf("%w: %s is not deleted", ErrConflict, what)
		}
		c.Action, c.Version, c.UnitAfter, c.After = ConfigRestore, cur.Version+1, cur.Unit, cur.Spec
		c.Before = nil
		_, err = tx.Exec(`UPDATE config_objects SET version = ?, updated_at = ?, updated_by = ?, deleted_at = 0, deleted_by = ''
			WHERE org = ? AND kind = ? AND name = ?`, c.Version, now, actor, "", w.Kind, w.Name)
	default:
		return nil, fmt.Errorf("%w: unknown action %q", ErrInvalid, w.Action)
	}
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(`INSERT INTO config_changes(org, revision, kind, name, version, action, actor, at, managed_by, unit_before, unit_after,
		spec_before, spec_after, import_id, comment) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		"", rev, c.Kind, c.Name, c.Version, c.Action, actor, now, c.ManagedBy, c.UnitBefore, c.UnitAfter,
		string(c.Before), string(c.After), impID, c.Comment).Scan(&c.ID)
	return c, err
}

func compactJSON(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return b
	}
	return buf.Bytes()
}

// ConfigHistoryFilter narrows the history. Empty fields match everything.
type ConfigHistoryFilter struct {
	Kind, Name, ImportID string
	Before               int64 // only changes with a smaller ID: the next page
	Limit                int
}

// ConfigHistory returns changes, newest first.
func (e *Engine) ConfigHistory(f ConfigHistoryFilter) ([]ConfigChange, error) {
	if err := e.configCheck(); err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := []string{"org = ?"}
	args := []any{""}
	for _, c := range []struct {
		cond string
		val  string
	}{{"kind = ?", f.Kind}, {"name = ?", f.Name}, {"import_id = ?", f.ImportID}} {
		if c.val != "" {
			where = append(where, c.cond)
			args = append(args, c.val)
		}
	}
	if f.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	args = append(args, f.Limit)
	rows, err := e.db.Query(`SELECT id, revision, kind, name, version, action, actor, at, managed_by, unit_before, unit_after,
		spec_before, spec_after, import_id, comment FROM config_changes WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigChange{}
	for rows.Next() {
		var c ConfigChange
		var before, after string
		if err := rows.Scan(&c.ID, &c.Revision, &c.Kind, &c.Name, &c.Version, &c.Action, &c.Actor, &c.At, &c.ManagedBy, &c.UnitBefore,
			&c.UnitAfter, &before, &after, &c.ImportID, &c.Comment); err != nil {
			return nil, err
		}
		if before != "" {
			c.Before = json.RawMessage(before)
		}
		if after != "" {
			c.After = json.RawMessage(after)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConfigImports lists the imports, newest first.
func (e *Engine) ConfigImports() ([]ConfigImport, error) {
	if err := e.configCheck(); err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT id, at, actor, origin, digest, summary, revision FROM config_imports WHERE org = ? ORDER BY at DESC, id DESC LIMIT 200`, "")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigImport{}
	for rows.Next() {
		var i ConfigImport
		var sum string
		if err := rows.Scan(&i.ID, &i.At, &i.Actor, &i.Origin, &i.Digest, &sum, &i.Revision); err != nil {
			return nil, err
		}
		i.Summary = json.RawMessage(sum)
		out = append(out, i)
	}
	return out, rows.Err()
}

// ConfigImportByOrigin returns the first import from this origin, if
// there was one.
func (e *Engine) ConfigImportByOrigin(origin string) (*ConfigImport, error) {
	if err := e.configCheck(); err != nil {
		return nil, err
	}
	var i ConfigImport
	var sum string
	err := e.db.QueryRow(`SELECT id, at, actor, origin, digest, summary, revision FROM config_imports WHERE org = ? AND origin = ?
		ORDER BY at, id LIMIT 1`, "", origin).Scan(&i.ID, &i.At, &i.Actor, &i.Origin, &i.Digest, &sum, &i.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	i.Summary = json.RawMessage(sum)
	return &i, err
}

// OnConfigChange registers fn to be called (on its own goroutine, never
// twice at once) whenever the configuration may have changed: on this
// server at once, on others when the notification arrives. Watchers should
// also poll ConfigRevision, as notifications can be lost.
func (e *Engine) OnConfigChange(fn func()) {
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	e.cfgWatchers = append(e.cfgWatchers, fn)
}

func (e *Engine) configChanged() {
	e.cfgMu.Lock()
	ws := append([]func(){}, e.cfgWatchers...)
	e.cfgMu.Unlock()
	for _, fn := range ws {
		go fn()
	}
}
