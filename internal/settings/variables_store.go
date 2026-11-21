package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// VariableStore keeps variables and the audit of their changes (schema 29).
// It checks what a variable is (names, types, values) and what the store
// alone can know (a name has one type everywhere); who may change what, and
// what a lock forbids, are decided above it, by the Service.
type VariableStore struct {
	db  *engine.Database
	now func() time.Time
}

// NewVariableStore returns the store of an engine's database.
func NewVariableStore(eng *engine.Engine) *VariableStore {
	return &VariableStore{db: eng.Database(), now: time.Now}
}

const varCols = `level, scope, name, environment, type, of_type, value, description, locked, lock_reason, locked_by, locked_at,
	version, created_at, created_by, updated_at, updated_by`

func scanVariable(sc interface{ Scan(...any) error }) (Variable, error) {
	var v Variable
	var level, typ, value, reason, by string
	var locked int
	var at int64
	if err := sc.Scan(&level, &v.Scope, &v.Name, &v.Environment, &typ, &v.Of, &value, &v.Description, &locked, &reason, &by, &at,
		&v.Version, &v.CreatedAt, &v.CreatedBy, &v.UpdatedAt, &v.UpdatedBy); err != nil {
		return v, err
	}
	v.Level, v.Type, v.Value = Level(level), VarType(typ), json.RawMessage(value)
	if locked != 0 {
		v.Lock = &Lock{Reason: reason, By: by, At: at}
	}
	return v, nil
}

// All returns every variable of the organization, widest level first, for
// resolving and for the views.
func (s *VariableStore) All(ctx context.Context) ([]Variable, error) {
	rows, err := s.db.Query(ctx, `SELECT `+varCols+` FROM variables WHERE org = ? ORDER BY level, scope, name, environment`, DefaultOrg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Variable{}
	for rows.Next() {
		v, err := scanVariable(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// At returns the variables set at one level and scope.
func (s *VariableStore) At(ctx context.Context, level Level, scope string) ([]Variable, error) {
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	out := []Variable{}
	for _, v := range all {
		if v.Level == level && v.Scope == scope {
			out = append(out, v)
		}
	}
	return out, nil
}

// Get returns one variable, or engine.ErrNotFound.
func (s *VariableStore) Get(ctx context.Context, k VarKey) (*Variable, error) {
	return getVariable(ctx, s.db.QueryRow(ctx, `SELECT `+varCols+` FROM variables
		WHERE org = ? AND level = ? AND scope = ? AND name = ? AND environment = ?`, DefaultOrg, string(k.Level), k.Scope, k.Name, k.Environment), k)
}

func getVariable(_ context.Context, row *sql.Row, k VarKey) (*Variable, error) {
	v, err := scanVariable(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: variable %s", engine.ErrNotFound, k)
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// SetInput is what a caller gives to set a variable.
type SetInput struct {
	VarKey
	Type        VarType
	Of          string
	Value       json.RawMessage
	Description string
}

// Set creates the variable or changes its value, type of connection and
// description. A lock it already has stays. The change is audited in the
// same transaction. It reports whether anything changed.
func (s *VariableStore) Set(ctx context.Context, actor string, in SetInput) (*Variable, bool, error) {
	if err := CheckKey(in.VarKey); err != nil {
		return nil, false, err
	}
	if len(in.Description) > MaxVariableDescLen || containsLine(in.Description) {
		return nil, false, variableProblem("a description is one line of at most %d characters", MaxVariableDescLen)
	}
	val, err := CheckValue(in.Type, in.Of, in.Value)
	if err != nil {
		return nil, false, err
	}
	in.Value = val
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if err := checkTypeAgrees(ctx, tx, in); err != nil {
		return nil, false, err
	}
	now := s.now().UnixMilli()
	old, err := getVariable(ctx, tx.QueryRow(ctx, `SELECT `+varCols+` FROM variables
		WHERE org = ? AND level = ? AND scope = ? AND name = ? AND environment = ?`, DefaultOrg, string(in.Level), in.Scope, in.Name, in.Environment), in.VarKey)
	switch {
	case errors.Is(err, engine.ErrNotFound):
		if _, err := tx.Exec(ctx, `INSERT INTO variables(org, level, scope, name, environment, type, of_type, value, description,
			version, created_at, created_by, updated_at, updated_by) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
			DefaultOrg, string(in.Level), in.Scope, in.Name, in.Environment, string(in.Type), in.Of, string(in.Value), in.Description,
			now, actor, now, actor); err != nil {
			return nil, false, err
		}
		if err := s.audit(ctx, tx, actor, in.VarKey, "set", "", DisplayValue(in.Type, in.Value), in.Description, now); err != nil {
			return nil, false, err
		}
	case err != nil:
		return nil, false, err
	default:
		if old.Type == in.Type && old.Of == in.Of && string(old.Value) == string(in.Value) && old.Description == in.Description {
			return old, false, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE variables SET type = ?, of_type = ?, value = ?, description = ?, version = version + 1,
			updated_at = ?, updated_by = ? WHERE org = ? AND level = ? AND scope = ? AND name = ? AND environment = ?`,
			string(in.Type), in.Of, string(in.Value), in.Description, now, actor, DefaultOrg, string(in.Level), in.Scope, in.Name, in.Environment); err != nil {
			return nil, false, err
		}
		if err := s.audit(ctx, tx, actor, in.VarKey, "set", DisplayValue(old.Type, old.Value), DisplayValue(in.Type, in.Value), in.Description, now); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	v, err := s.Get(ctx, in.VarKey)
	return v, true, err
}

func containsLine(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}

// checkTypeAgrees refuses a type or connection kind that differs from what
// the same name has anywhere else in the organization: the first level to
// set a name fixes its type.
func checkTypeAgrees(ctx context.Context, tx *engine.Tx, in SetInput) error {
	rows, err := tx.Query(ctx, `SELECT level, scope, environment, type, of_type FROM variables WHERE org = ? AND name = ?
		ORDER BY CASE level WHEN 'installation' THEN 0 WHEN 'organization' THEN 1 WHEN 'unit' THEN 2 ELSE 3 END, scope`, DefaultOrg, in.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var level, scope, env, typ, of string
		if err := rows.Scan(&level, &scope, &env, &typ, &of); err != nil {
			return err
		}
		if Level(level) == in.Level && scope == in.Scope && env == in.Environment {
			continue // the variable itself may change its own type
		}
		if VarType(typ) != in.Type || of != in.Of {
			where := level
			if scope != "" && Level(level) != LevelOrganization {
				where += " " + scope
			}
			was := typ
			if of != "" {
				was += " of " + of
			}
			return variableProblem("%s is %s (set at %s); a variable keeps one type everywhere", in.Name, was, where)
		}
	}
	return rows.Err()
}

// Delete removes a variable, audited. It does not check who uses it: the
// Service lists the projects that would be affected first.
func (s *VariableStore) Delete(ctx context.Context, actor string, k VarKey) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := getVariable(ctx, tx.QueryRow(ctx, `SELECT `+varCols+` FROM variables
		WHERE org = ? AND level = ? AND scope = ? AND name = ? AND environment = ?`, DefaultOrg, string(k.Level), k.Scope, k.Name, k.Environment), k)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM variables WHERE org = ? AND level = ? AND scope = ? AND name = ? AND environment = ?`,
		DefaultOrg, string(k.Level), k.Scope, k.Name, k.Environment); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, actor, k, "delete", DisplayValue(old.Type, old.Value), "", "", s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// SetLock locks the variable: nobody below its level may change what it
// stands for. A reason is required, because it is shown wherever the lock
// bites.
func (s *VariableStore) SetLock(ctx context.Context, actor string, k VarKey, reason string) (*Variable, error) {
	if reason == "" || len(reason) > MaxLockReasonLen || containsLine(reason) {
		return nil, variableProblem("a lock needs a reason of one line, at most %d characters: it is shown to everyone it stops", MaxLockReasonLen)
	}
	return s.lock(ctx, actor, k, true, reason)
}

// ClearLock removes the lock.
func (s *VariableStore) ClearLock(ctx context.Context, actor string, k VarKey) (*Variable, error) {
	return s.lock(ctx, actor, k, false, "")
}

func (s *VariableStore) lock(ctx context.Context, actor string, k VarKey, on bool, reason string) (*Variable, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	old, err := getVariable(ctx, tx.QueryRow(ctx, `SELECT `+varCols+` FROM variables
		WHERE org = ? AND level = ? AND scope = ? AND name = ? AND environment = ?`, DefaultOrg, string(k.Level), k.Scope, k.Name, k.Environment), k)
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	action, locked, who, at := "unlock", 0, "", int64(0)
	if on {
		action, locked, who, at = "lock", 1, actor, now
	}
	if (old.Lock != nil) == on && (!on || old.Lock.Reason == reason) {
		return old, tx.Commit() // nothing to change
	}
	if _, err := tx.Exec(ctx, `UPDATE variables SET locked = ?, lock_reason = ?, locked_by = ?, locked_at = ?, version = version + 1,
		updated_at = ?, updated_by = ? WHERE org = ? AND level = ? AND scope = ? AND name = ? AND environment = ?`,
		locked, reason, who, at, now, actor, DefaultOrg, string(k.Level), k.Scope, k.Name, k.Environment); err != nil {
		return nil, err
	}
	if err := s.audit(ctx, tx, actor, k, action, "", "", reason, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, k)
}

// audit records a change. Values are recorded as people read them: a secret
// variable as its reference. The secret it points at is never read.
func (s *VariableStore) audit(ctx context.Context, tx *engine.Tx, actor string, k VarKey, action, old, nu, detail string, at int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO variable_changes(org, at, actor, level, scope, name, environment, action, old_value, new_value, detail)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, DefaultOrg, at, actor, string(k.Level), k.Scope, k.Name, k.Environment, action,
		clip(old, 500), clip(nu, 500), clip(detail, 300))
	return err
}

// Changes returns the audit of variables, newest first; a name narrows it.
func (s *VariableStore) Changes(ctx context.Context, name string, limit int) ([]VariableChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q, args := `SELECT id, at, actor, level, scope, name, environment, action, old_value, new_value, detail FROM variable_changes WHERE org = ?`, []any{DefaultOrg}
	if name != "" {
		q += ` AND name = ?`
		args = append(args, name)
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY id DESC LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VariableChange{}
	for rows.Next() {
		var c VariableChange
		var level string
		if err := rows.Scan(&c.ID, &c.At, &c.Actor, &level, &c.Scope, &c.Name, &c.Environment, &c.Action, &c.Old, &c.New, &c.Detail); err != nil {
			return nil, err
		}
		c.Level = Level(level)
		out = append(out, c)
	}
	return out, rows.Err()
}
