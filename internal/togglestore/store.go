package togglestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// queryer is what a database and a transaction both do.
type queryer interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

const defCols = `level, scope, key, type, kind, description, salt, default_value, safe_value, client_visible, owner, tags, expires_at,
	origin, origin_change, locked, lock_reason, locked_by, version, created_at, created_by, updated_at, updated_by`

func scanDef(sc interface{ Scan(...any) error }) (Definition, error) {
	var d Definition
	var level, typ, kind, def, safe, tags, reason, by string
	var visible, locked int
	if err := sc.Scan(&level, &d.Scope, &d.Key.Key, &typ, &kind, &d.Description, &d.Salt, &def, &safe, &visible, &d.Owner, &tags, &d.ExpiresAt,
		&d.Origin, &d.OriginChange, &locked, &reason, &by, &d.Version, &d.CreatedAt, &d.CreatedBy, &d.UpdatedAt, &d.UpdatedBy); err != nil {
		return d, err
	}
	d.Level, d.Type, d.Kind, d.ClientVisible = Level(level), toggles.ValueType(typ), toggles.Kind(kind), visible != 0
	_ = json.Unmarshal([]byte(def), &d.Default)
	_ = json.Unmarshal([]byte(safe), &d.SafeValue)
	d.Tags = []string{}
	if tags != "" {
		d.Tags = strings.Split(tags, ",")
	}
	if locked != 0 {
		d.Lock = &Lock{Reason: reason, By: by}
	}
	return d, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// levelClause matches the toggles, segments and safeguards at the levels a
// project sits under: the installation, the organization, its unit and itself.
func levelClause(unit, project string) (string, []any) {
	return `(level = 'installation' OR level = 'organization' OR (level = 'unit' AND scope = ?) OR (level = 'project' AND scope = ?))`, []any{unit, project}
}

// levelRank orders levels, widest first.
func levelRank(l Level) int {
	switch l {
	case LevelInstallation:
		return 0
	case LevelOrganization:
		return 1
	case LevelUnit:
		return 2
	}
	return 3
}

func getDef(ctx context.Context, q queryer, k Key) (*Definition, error) {
	d, err := scanDef(q.QueryRow(ctx, `SELECT `+defCols+` FROM toggles WHERE org = ? AND level = ? AND scope = ? AND key = ?`, Org, string(k.Level), k.Scope, k.Key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing("no toggle %s", k)
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func listDefs(ctx context.Context, q queryer, unit, project string) ([]Definition, error) {
	clause, args := levelClause(unit, project)
	rows, err := q.Query(ctx, `SELECT `+defCols+` FROM toggles WHERE org = ? AND `+clause+` ORDER BY key`, append([]any{Org}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Definition
	for rows.Next() {
		d, err := scanDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanEnv(sc interface{ Scan(...any) error }) (EnvState, error) {
	var e EnvState
	var state string
	if err := sc.Scan(&e.Environment, &state, &e.Settled, &e.SettledSince, &e.Version, &e.UpdatedAt, &e.UpdatedBy); err != nil {
		return e, err
	}
	if err := json.Unmarshal([]byte(state), &e.State); err != nil {
		return e, err
	}
	return e, nil
}

const envCols = `environment, state, settled, settled_since, version, updated_at, updated_by`

func getEnv(ctx context.Context, q queryer, k Key, env string) (*EnvState, error) {
	e, err := scanEnv(q.QueryRow(ctx, `SELECT `+envCols+` FROM toggle_envs WHERE org = ? AND level = ? AND scope = ? AND key = ? AND environment = ?`,
		Org, string(k.Level), k.Scope, k.Key, env))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func listEnvs(ctx context.Context, q queryer, k Key) ([]EnvState, error) {
	rows, err := q.Query(ctx, `SELECT `+envCols+` FROM toggle_envs WHERE org = ? AND level = ? AND scope = ? AND key = ? ORDER BY environment`,
		Org, string(k.Level), k.Scope, k.Key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvState
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// putEnv writes a state, keeping when it began to hand out one value.
func putEnv(ctx context.Context, q queryer, k Key, env string, st toggles.State, by string, now int64) error {
	settled, ok := toggles.Settled(st)
	if !ok {
		settled = ""
	}
	since := int64(0)
	if prev, err := getEnv(ctx, q, k, env); err != nil {
		return err
	} else if settled != "" {
		since = now
		if prev != nil && prev.Settled == settled && prev.SettledSince > 0 {
			since = prev.SettledSince
		}
	}
	_, err := q.Exec(ctx, `INSERT INTO toggle_envs(org, level, scope, key, environment, state, settled, settled_since, version, updated_at, updated_by)
		VALUES(?,?,?,?,?,?,?,?,1,?,?)
		ON CONFLICT(org, level, scope, key, environment) DO UPDATE SET state = excluded.state, settled = excluded.settled,
		settled_since = excluded.settled_since, version = toggle_envs.version + 1, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		Org, string(k.Level), k.Scope, k.Key, env, js(st), settled, since, now, by)
	return err
}

func bump(ctx context.Context, q queryer) error {
	_, err := q.Exec(ctx, `INSERT INTO toggle_revs(org, rev) VALUES(?, 1) ON CONFLICT(org) DO UPDATE SET rev = toggle_revs.rev + 1`, Org)
	return err
}

func currentRev(ctx context.Context, q queryer) (int64, error) {
	var rev int64
	err := q.QueryRow(ctx, `SELECT rev FROM toggle_revs WHERE org = ?`, Org).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

func listSegments(ctx context.Context, q queryer, unit, project string) ([]SegmentRow, error) {
	clause, args := levelClause(unit, project)
	rows, err := q.Query(ctx, `SELECT level, scope, name, description, definition, version, updated_at, updated_by FROM toggle_segments WHERE org = ? AND `+clause+` ORDER BY name`,
		append([]any{Org}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SegmentRow
	for rows.Next() {
		var s SegmentRow
		var level, def string
		if err := rows.Scan(&level, &s.Scope, &s.Name, &s.Segment.Description, &def, &s.Version, &s.UpdatedAt, &s.UpdatedBy); err != nil {
			return nil, err
		}
		s.Level = Level(level)
		desc := s.Segment.Description
		if err := json.Unmarshal([]byte(def), &s.Segment); err != nil {
			return nil, err
		}
		s.Segment.Description = desc
		out = append(out, s)
	}
	return out, rows.Err()
}

// Change is one entry of a toggle's history.
type Change struct {
	ID          int64           `json:"id"`
	At          int64           `json:"at"`
	Project     string          `json:"project"`
	Level       Level           `json:"level"`
	Scope       string          `json:"scope,omitempty"`
	Toggle      string          `json:"toggle"`
	Environment string          `json:"environment,omitempty"`
	Action      string          `json:"action"`
	Actor       string          `json:"actor"`
	Reason      string          `json:"reason"`
	Via         string          `json:"via"`
	Status      string          `json:"status"`
	Old         json.RawMessage `json:"old,omitempty"`
	New         json.RawMessage `json:"new,omitempty"`
	Approvals   ApprovalRecord  `json:"approvals"`
	DecidedBy   string          `json:"decided_by,omitempty"`
	DecidedAt   int64           `json:"decided_at,omitempty"`
	Reverts     int64           `json:"reverts,omitempty"`
	RevertedBy  int64           `json:"reverted_by,omitempty"`
	WatchUntil  int64           `json:"watch_until,omitempty"`
}

// The actions and statuses of a change.
const (
	ActionCreate     = "create"
	ActionUpdate     = "update"
	ActionDelete     = "delete"
	ActionState      = "state"
	ActionSegment    = "segment"
	ActionSafeguards = "safeguards"
	ActionKey        = "key"

	StatusApplied   = "applied"
	StatusPending   = "pending"
	StatusRejected  = "rejected"
	StatusWithdrawn = "withdrawn"
)

// ApprovalRecord is who has approved a change that waits, and how many must.
type ApprovalRecord struct {
	Need int        `json:"need"`
	By   []Approval `json:"by"`
}

// Approval is one person's approval.
type Approval struct {
	User    string `json:"user"`
	At      int64  `json:"at"`
	Comment string `json:"comment,omitempty"`
}

const changeCols = `id, at, project, level, scope, key, environment, action, actor, reason, via, status, old_value, new_value, approvals,
	decided_by, decided_at, reverts, reverted_by, watch_until`

func scanChange(sc interface{ Scan(...any) error }) (Change, error) {
	var c Change
	var level, old, nw, appr string
	if err := sc.Scan(&c.ID, &c.At, &c.Project, &level, &c.Scope, &c.Toggle, &c.Environment, &c.Action, &c.Actor, &c.Reason, &c.Via, &c.Status,
		&old, &nw, &appr, &c.DecidedBy, &c.DecidedAt, &c.Reverts, &c.RevertedBy, &c.WatchUntil); err != nil {
		return c, err
	}
	c.Level = Level(level)
	if old != "" {
		c.Old = json.RawMessage(old)
	}
	if nw != "" {
		c.New = json.RawMessage(nw)
	}
	_ = json.Unmarshal([]byte(appr), &c.Approvals)
	if c.Approvals.By == nil {
		c.Approvals.By = []Approval{}
	}
	return c, nil
}

func insertChange(ctx context.Context, q queryer, c *Change) error {
	if c.Approvals.By == nil {
		c.Approvals.By = []Approval{}
	}
	return q.QueryRow(ctx, `INSERT INTO toggle_changes(org, at, project, level, scope, key, environment, action, actor, reason, via, status,
			old_value, new_value, approvals, decided_by, decided_at, reverts, watch_until)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		Org, c.At, c.Project, string(c.Level), c.Scope, c.Toggle, c.Environment, c.Action, c.Actor, c.Reason, c.Via, c.Status,
		string(c.Old), string(c.New), js(c.Approvals), c.DecidedBy, c.DecidedAt, c.Reverts, c.WatchUntil).Scan(&c.ID)
}

func getChange(ctx context.Context, q queryer, id int64) (*Change, error) {
	c, err := scanChange(q.QueryRow(ctx, `SELECT `+changeCols+` FROM toggle_changes WHERE org = ? AND id = ?`, Org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing("no change %d", id)
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func queryChanges(ctx context.Context, q queryer, where string, args ...any) ([]Change, error) {
	rows, err := q.Query(ctx, `SELECT `+changeCols+` FROM toggle_changes WHERE org = ? `+where, append([]any{Org}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
