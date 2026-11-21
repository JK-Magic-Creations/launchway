package togglestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// Env is what the service needs to know about an environment of the
// engine: whether it is protected (where approval is on by default), the
// floor of approvals its deployments need, and who may approve.
type Env struct {
	Known        bool
	Protected    bool
	MinApprovals int
	// MayApprove, when set, limits who may approve a toggle change there
	// (the environment's reviewers); nil: anyone the API lets in.
	MayApprove func(user string, groups []string) bool
}

// Deps are what a Service is built from.
type Deps struct {
	DB *engine.Database
	// UnitOf returns the unit (squadron) that holds a project, or "".
	UnitOf func(project string) string
	// Environment describes an environment of the engine by name.
	Environment func(name string) Env
	// SelfApproval says whether the person who asked for a change may
	// approve it (approvals.allow_self).
	SelfApproval func() bool
	Now          func() time.Time
	// CheckEvery is how often a server looks at the revision counter to learn
	// that another server changed something (default 1s).
	CheckEvery time.Duration
}

// Actor is who does something, and how.
type Actor struct {
	User   string
	Groups []string
	// Via says how: api, cli, mcp, ui, pipeline, config, schedule, rollback.
	Via string
}

// Service is the toggles of an installation.
type Service struct {
	d Deps

	snapMu sync.Mutex
	snaps  map[string]*Snapshot
	revMu  sync.Mutex
	rev    int64
	revAt  time.Time
	change chan struct{} // closed and replaced when the revision moves

	counts   counts
	keyCache keyCache
	limits   Limits
}

// Limits is where the SDK routes keep their rate limits.
func (s *Service) Limits() *Limits { return &s.limits }

// New returns the service of a database.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.CheckEvery <= 0 {
		d.CheckEvery = time.Second
	}
	if d.UnitOf == nil {
		d.UnitOf = func(string) string { return "" }
	}
	if d.Environment == nil {
		d.Environment = func(string) Env { return Env{} }
	}
	if d.SelfApproval == nil {
		d.SelfApproval = func() bool { return false }
	}
	s := &Service{d: d, snaps: map[string]*Snapshot{}, change: make(chan struct{})}
	s.counts.m = map[countKey]int64{}
	s.keyCache.m = map[string]cachedKey{}
	return s
}

func (s *Service) now() int64 { return s.d.Now().UnixMilli() }

// chain returns the levels that apply to something set at w, widest first: the
// installation, the organization, the unit that holds the project, the project.
func (s *Service) chain(w Where, project string) []Where {
	out := []Where{{Level: LevelInstallation}, {Level: LevelOrganization}}
	switch w.Level {
	case LevelUnit:
		out = append(out, w)
	case LevelProject:
		if u := s.d.UnitOf(w.Scope); u != "" {
			out = append(out, Where{Level: LevelUnit, Scope: u})
		}
		out = append(out, w)
	}
	if w.Level == LevelInstallation {
		return out[:1]
	}
	if w.Level == LevelOrganization {
		return out[:2]
	}
	_ = project
	return out
}

// ProjectWhere is the project level of a project.
func ProjectWhere(project string) Where { return Where{Level: LevelProject, Scope: project} }

// resolve returns the definition of key that a project sees: the nearest
// level that defines it. A lock at a wider level hides what is set nearer.
func (s *Service) resolve(ctx context.Context, q queryer, project, key string) (*Definition, error) {
	defs, err := listDefs(ctx, q, s.d.UnitOf(project), project)
	if err != nil {
		return nil, err
	}
	var best *Definition
	for i := range defs {
		d := defs[i]
		if d.Key.Key != key {
			continue
		}
		if best != nil && best.Lock != nil {
			continue
		}
		if best == nil || levelRank(d.Level) > levelRank(best.Level) {
			best = &d
		}
	}
	if best == nil {
		return nil, missing("no toggle %s in project %s", key, project)
	}
	return best, nil
}

func newSalt(key string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return key + "." + hex.EncodeToString(b[:])
}

// DefinitionInput is what makes a toggle.
type DefinitionInput struct {
	Key
	Type          toggles.ValueType
	Kind          toggles.Kind
	Description   string
	Default       any
	SafeValue     any
	ClientVisible bool
	Owner         string
	Tags          []string
	ExpiresAt     int64
	// Origin says what made it: "api", "config:<commit>", "run:<id>".
	Origin string
}

func checkTags(tags []string) error {
	if len(tags) > 10 {
		return invalid("at most 10 tags")
	}
	for _, t := range tags {
		if t == "" || len(t) > 32 || strings.ContainsAny(t, ", \t\n") {
			return invalid("a tag is at most 32 characters with no commas or spaces")
		}
	}
	return nil
}

func (in *DefinitionInput) check() error {
	if err := in.Check(); err != nil {
		return err
	}
	if err := toggles.CheckKey(in.Key.Key); err != nil {
		return invalid("%v", err)
	}
	if !in.Type.Valid() {
		return invalid("type is boolean, string, number or json")
	}
	if in.Kind == "" {
		in.Kind = toggles.KindRelease
	}
	if !in.Kind.Valid() {
		return invalid("kind is release, kill, experiment, operational or permission")
	}
	if len(in.Description) > 1000 || len(in.Owner) > 100 {
		return invalid("a description is at most 1000 characters and an owner at most 100")
	}
	if err := checkTags(in.Tags); err != nil {
		return err
	}
	if err := toggles.CheckValue(in.Type, in.Default); err != nil {
		return invalid("default: %v", err)
	}
	if in.Kind == toggles.KindKill {
		if in.SafeValue == nil {
			in.SafeValue = in.Default
		}
	}
	if in.SafeValue != nil {
		if err := toggles.CheckValue(in.Type, in.SafeValue); err != nil {
			return invalid("safe_value: %v", err)
		}
	}
	return nil
}

// Create defines a toggle. Every environment starts off, serving the default.
func (s *Service) Create(ctx context.Context, who Actor, in DefinitionInput, reason string) (*Definition, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// A toggle of this key at a wider level that is locked cannot be redefined nearer.
	if err := lockedAbove(ctx, tx, in); err != nil {
		return nil, err
	}
	now := s.now()
	salt := newSalt(in.Key.Key)
	tags := strings.Join(in.Tags, ",")
	if in.Origin == "" {
		in.Origin = who.Via
	}
	res, err := tx.Exec(ctx, `INSERT INTO toggles(org, level, scope, key, type, kind, description, salt, default_value, safe_value, client_visible, owner, tags,
			expires_at, origin, version, created_at, created_by, updated_at, updated_by)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?) ON CONFLICT DO NOTHING`,
		Org, string(in.Level), in.Scope, in.Key.Key, string(in.Type), string(in.Kind), in.Description, salt, js(in.Default), js(in.SafeValue), b2i(in.ClientVisible),
		in.Owner, tags, in.ExpiresAt, in.Origin, now, who.User, now, who.User)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, conflict("the toggle %s already exists", in.Key)
	}
	d, err := getDef(ctx, tx, in.Key)
	if err != nil {
		return nil, err
	}
	ch := &Change{At: now, Project: projectOf(in.Where), Level: in.Level, Scope: in.Scope, Toggle: in.Key.Key, Action: ActionCreate, Actor: who.User, Reason: reason,
		Via: who.Via, Status: StatusApplied, New: raw(d)}
	if err := insertChange(ctx, tx, ch); err != nil {
		return nil, err
	}
	d.OriginChange = ch.ID
	if _, err := tx.Exec(ctx, `UPDATE toggles SET origin_change = ? WHERE org = ? AND level = ? AND scope = ? AND key = ?`, ch.ID, Org, string(in.Level), in.Scope, in.Key.Key); err != nil {
		return nil, err
	}
	if err := bump(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.noticeLocal(ctx)
	return d, nil
}

// lockedAbove refuses a definition of a key that a wider level has locked.
func lockedAbove(ctx context.Context, tx *engine.Tx, in DefinitionInput) error {
	rows, err := tx.Query(ctx, `SELECT level, scope, lock_reason, locked_by FROM toggles WHERE org = ? AND key = ? AND locked = 1`, Org, in.Key.Key)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var lv, sc, why, by string
		if err := rows.Scan(&lv, &sc, &why, &by); err != nil {
			return err
		}
		if levelRank(Level(lv)) < levelRank(in.Level) {
			return conflict("%s is locked at the %s level by %s (%s): a nearer level may not define it", in.Key.Key, lv, by, why)
		}
	}
	return rows.Err()
}

func projectOf(w Where) string {
	if w.Level == LevelProject {
		return w.Scope
	}
	return ""
}

func raw(v any) json.RawMessage { return json.RawMessage(js(v)) }

// Patch changes a definition: what is nil stays.
type Patch struct {
	Description   *string
	Default       *any
	SafeValue     *any
	Kind          *toggles.Kind
	ClientVisible *bool
	Owner         *string
	Tags          *[]string
	ExpiresAt     *int64
}

// Update changes a toggle's definition (never its type or its key).
func (s *Service) Update(ctx context.Context, who Actor, k Key, p Patch, reason string) (*Definition, error) {
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := getDef(ctx, tx, k)
	if err != nil {
		return nil, err
	}
	old := *d
	if p.Description != nil {
		d.Description = *p.Description
	}
	if p.Default != nil {
		d.Default = *p.Default
	}
	if p.SafeValue != nil {
		d.SafeValue = *p.SafeValue
	}
	if p.Kind != nil {
		d.Kind = *p.Kind
	}
	if p.ClientVisible != nil {
		d.ClientVisible = *p.ClientVisible
	}
	if p.Owner != nil {
		d.Owner = *p.Owner
	}
	if p.Tags != nil {
		d.Tags = *p.Tags
	}
	if p.ExpiresAt != nil {
		d.ExpiresAt = *p.ExpiresAt
	}
	in := DefinitionInput{Key: d.Key, Type: d.Type, Kind: d.Kind, Description: d.Description, Default: d.Default, SafeValue: d.SafeValue,
		ClientVisible: d.ClientVisible, Owner: d.Owner, Tags: d.Tags}
	if err := in.check(); err != nil {
		return nil, err
	}
	d.Kind, d.SafeValue = in.Kind, in.SafeValue
	if d.ClientVisible {
		if err := s.checkVisiblePrereqs(ctx, tx, d.Key, d.Scope); err != nil {
			return nil, err
		}
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE toggles SET kind = ?, description = ?, default_value = ?, safe_value = ?, client_visible = ?, owner = ?, tags = ?, expires_at = ?,
			version = version + 1, updated_at = ?, updated_by = ? WHERE org = ? AND level = ? AND scope = ? AND key = ?`,
		string(d.Kind), d.Description, js(d.Default), js(d.SafeValue), b2i(d.ClientVisible), d.Owner, strings.Join(d.Tags, ","), d.ExpiresAt, now, who.User,
		Org, string(k.Level), k.Scope, k.Key); err != nil {
		return nil, err
	}
	ch := &Change{At: now, Project: projectOf(k.Where), Level: k.Level, Scope: k.Scope, Toggle: k.Key, Action: ActionUpdate, Actor: who.User, Reason: reason,
		Via: who.Via, Status: StatusApplied, Old: raw(old), New: raw(d)}
	if err := insertChange(ctx, tx, ch); err != nil {
		return nil, err
	}
	if err := bump(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.noticeLocal(ctx)
	return getDef(ctx, s.d.DB, k)
}

// checkVisiblePrereqs refuses a client-visible toggle that depends on one that
// is not: a client could otherwise be made to depend on something it may not see.
func (s *Service) checkVisiblePrereqs(ctx context.Context, q queryer, k Key, _ string) error {
	envs, err := listEnvs(ctx, q, k)
	if err != nil {
		return err
	}
	for _, e := range envs {
		for _, p := range e.State.Prerequisites {
			var visible int
			err := q.QueryRow(ctx, `SELECT client_visible FROM toggles WHERE org = ? AND key = ? ORDER BY CASE level WHEN 'project' THEN 3 WHEN 'unit' THEN 2 WHEN 'organization' THEN 1 ELSE 0 END DESC LIMIT 1`, Org, p.Toggle).Scan(&visible)
			if err == nil && visible == 0 {
				return invalid("%s is visible to client applications, and in %s it needs %s, which is not: make %s visible too, or drop the prerequisite", k.Key, e.Environment, p.Toggle, p.Toggle)
			}
		}
	}
	return nil
}

// SetLock locks or unlocks a toggle at its level: nearer levels may not define
// one of the same key.
func (s *Service) SetLock(ctx context.Context, who Actor, k Key, lock bool, reason string) error {
	if lock && strings.TrimSpace(reason) == "" {
		return invalid("say why it is locked")
	}
	res, err := s.d.DB.Exec(ctx, `UPDATE toggles SET locked = ?, lock_reason = ?, locked_by = ?, version = version + 1 WHERE org = ? AND level = ? AND scope = ? AND key = ?`,
		b2i(lock), reason, who.User, Org, string(k.Level), k.Scope, k.Key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return missing("no toggle %s", k)
	}
	act := "unlock"
	if lock {
		act = "lock"
	}
	ch := &Change{At: s.now(), Project: projectOf(k.Where), Level: k.Level, Scope: k.Scope, Toggle: k.Key, Action: ActionUpdate, Actor: who.User, Reason: act + ": " + reason, Via: who.Via, Status: StatusApplied}
	if err := insertChange(ctx, s.d.DB, ch); err != nil {
		return err
	}
	if err := bump(ctx, s.d.DB); err != nil {
		return err
	}
	s.noticeLocal(ctx)
	return nil
}

// Delete removes a toggle, with every environment's state and its history
// kept in the change (so that the removal can be reverted). It needs a reason.
func (s *Service) Delete(ctx context.Context, who Actor, k Key, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return invalid("say why the toggle is removed")
	}
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := getDef(ctx, tx, k)
	if err != nil {
		return err
	}
	envs, err := listEnvs(ctx, tx, k)
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM toggles WHERE org = ? AND level = ? AND scope = ? AND key = ?`, `DELETE FROM toggle_envs WHERE org = ? AND level = ? AND scope = ? AND key = ?`} {
		if _, err := tx.Exec(ctx, q, Org, string(k.Level), k.Scope, k.Key); err != nil {
			return err
		}
	}
	ch := &Change{At: s.now(), Project: projectOf(k.Where), Level: k.Level, Scope: k.Scope, Toggle: k.Key, Action: ActionDelete, Actor: who.User, Reason: reason, Via: who.Via,
		Status: StatusApplied, Old: raw(deleted{Definition: *d, Envs: envs})}
	if err := insertChange(ctx, tx, ch); err != nil {
		return err
	}
	if err := bump(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.noticeLocal(ctx)
	return nil
}

type deleted struct {
	Definition Definition `json:"definition"`
	Envs       []EnvState `json:"environments"`
}

// SaveSegment creates or replaces a segment at a level.
func (s *Service) SaveSegment(ctx context.Context, who Actor, w Where, name string, seg toggles.Segment, reason string) error {
	if err := w.Check(); err != nil {
		return err
	}
	if err := toggles.CheckSegment(name, seg); err != nil {
		return invalid("%v", err)
	}
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var old string
	_ = tx.QueryRow(ctx, `SELECT definition FROM toggle_segments WHERE org = ? AND level = ? AND scope = ? AND name = ?`, Org, string(w.Level), w.Scope, name).Scan(&old)
	now := s.now()
	desc := seg.Description
	seg.Description = ""
	if _, err := tx.Exec(ctx, `INSERT INTO toggle_segments(org, level, scope, name, description, definition, version, updated_at, updated_by) VALUES(?,?,?,?,?,?,1,?,?)
		ON CONFLICT(org, level, scope, name) DO UPDATE SET description = excluded.description, definition = excluded.definition, version = toggle_segments.version + 1,
		updated_at = excluded.updated_at, updated_by = excluded.updated_by`, Org, string(w.Level), w.Scope, name, desc, js(seg), now, who.User); err != nil {
		return err
	}
	seg.Description = desc
	ch := &Change{At: now, Project: projectOf(w), Level: w.Level, Scope: w.Scope, Toggle: name, Action: ActionSegment, Actor: who.User, Reason: reason, Via: who.Via, Status: StatusApplied,
		Old: json.RawMessage(old), New: raw(seg)}
	if err := insertChange(ctx, tx, ch); err != nil {
		return err
	}
	if err := bump(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.noticeLocal(ctx)
	return nil
}

// DeleteSegment removes a segment unless a toggle's rule still names it.
func (s *Service) DeleteSegment(ctx context.Context, who Actor, w Where, name, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return invalid("say why the segment is removed")
	}
	users, err := s.segmentUsers(ctx, name)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		sort.Strings(users)
		return conflict("the segment %s is used by %s: change those rules first", name, strings.Join(slices.Compact(users), ", "))
	}
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var old string
	if err := tx.QueryRow(ctx, `SELECT definition FROM toggle_segments WHERE org = ? AND level = ? AND scope = ? AND name = ?`, Org, string(w.Level), w.Scope, name).Scan(&old); err != nil {
		return missing("no segment %s at %s", name, w)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM toggle_segments WHERE org = ? AND level = ? AND scope = ? AND name = ?`, Org, string(w.Level), w.Scope, name); err != nil {
		return err
	}
	ch := &Change{At: s.now(), Project: projectOf(w), Level: w.Level, Scope: w.Scope, Toggle: name, Action: ActionSegment, Actor: who.User, Reason: reason, Via: who.Via, Status: StatusApplied, Old: json.RawMessage(old)}
	if err := insertChange(ctx, tx, ch); err != nil {
		return err
	}
	if err := bump(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.noticeLocal(ctx)
	return nil
}

// segmentUsers lists where a rule names a segment.
func (s *Service) segmentUsers(ctx context.Context, name string) ([]string, error) {
	rows, err := s.d.DB.Query(ctx, `SELECT key, environment, state FROM toggle_envs WHERE org = ?`, Org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var key, env, state string
		if err := rows.Scan(&key, &env, &state); err != nil {
			return nil, err
		}
		var st toggles.State
		if !strings.Contains(state, `"`+name+`"`) || json.Unmarshal([]byte(state), &st) != nil {
			continue
		}
		for _, r := range st.Rules {
			for _, c := range r.Clauses {
				if c.Op == toggles.OpInSegment && slices.Contains(anyStrings(c.Values), name) {
					users = append(users, key+" in "+env)
				}
			}
		}
	}
	return users, rows.Err()
}

func anyStrings(vs []any) []string {
	var out []string
	for _, v := range vs {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Segments lists the segments a project sees, nearest level winning by name.
func (s *Service) Segments(ctx context.Context, project string) ([]SegmentRow, error) {
	rows, err := listSegments(ctx, s.d.DB, s.d.UnitOf(project), project)
	if err != nil {
		return nil, err
	}
	best := map[string]SegmentRow{}
	for _, r := range rows {
		if b, ok := best[r.Name]; !ok || levelRank(r.Level) > levelRank(b.Level) {
			best[r.Name] = r
		}
	}
	out := make([]SegmentRow, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

var _ = fmt.Sprintf
