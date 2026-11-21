package togglestore

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// Defaults are the safeguards where nothing sets them: a reason for every
// change, and an approval in a protected environment.
func (s *Service) defaults(env string) toggles.Safeguards {
	return toggles.Safeguards{RequireReason: true, RequireApproval: s.d.Environment(env).Protected, Approvals: max(1, s.d.Environment(env).MinApprovals)}
}

func (w Where) label(env string) string {
	t := w.String()
	if env != "" {
		t += ", " + env
	}
	return t
}

// layersFor reads the safeguard settings that apply to an environment at w,
// widest first; at each level the setting for all environments comes before
// the one for this environment. With self false the level w itself is left out:
// what is set above it.
func (s *Service) layersFor(ctx context.Context, q queryer, w Where, env string, self bool) ([]toggles.Layer, error) {
	var out []toggles.Layer
	for _, lv := range s.chain(w, "") {
		if !self && lv == w {
			continue
		}
		envs := []string{""}
		if env != "" {
			envs = append(envs, env)
		}
		for _, e := range envs {
			var settings, locked string
			if err := q.QueryRow(ctx, `SELECT settings, locked FROM toggle_safeguards WHERE org = ? AND level = ? AND scope = ? AND environment = ?`,
				Org, string(lv.Level), lv.Scope, e).Scan(&settings, &locked); err != nil {
				continue // none set here
			}
			l := toggles.Layer{Where: lv.label(e)}
			_ = json.Unmarshal([]byte(settings), &l.Setting)
			if locked != "" {
				l.Locked = strings.Split(locked, ",")
			}
			out = append(out, l)
		}
	}
	return out, nil
}

// Safeguards returns what applies to an environment for toggles set at w, and
// where each safeguard comes from.
func (s *Service) Safeguards(ctx context.Context, w Where, env string) (toggles.Resolved, error) {
	layers, err := s.layersFor(ctx, s.d.DB, w, env, true)
	if err != nil {
		return toggles.Resolved{}, err
	}
	return toggles.Resolve(s.defaults(env), layers), nil
}

// SetSafeguards sets the safeguards at a level, for every environment (env
// empty) or one. Turning one down needs a reason and is recorded as such in
// the history; a safeguard that a level above locked cannot be changed.
func (s *Service) SetSafeguards(ctx context.Context, who Actor, w Where, env string, set toggles.Setting, reason string) (toggles.Resolved, error) {
	if err := w.Check(); err != nil {
		return toggles.Resolved{}, err
	}
	for _, g := range set.Locked {
		if !slices.Contains(toggles.GuardNames, g) {
			return toggles.Resolved{}, invalid("a safeguard is one of %s", strings.Join(toggles.GuardNames, ", "))
		}
	}
	if set.Approvals != nil && (*set.Approvals < 1 || *set.Approvals > 10) {
		return toggles.Resolved{}, invalid("approvals is 1 to 10")
	}
	if set.WatchMinutes != nil && (*set.WatchMinutes < 0 || *set.WatchMinutes > 24*60) {
		return toggles.Resolved{}, invalid("watch_minutes is 0 to 1440")
	}
	// What a level above has locked.
	parents, err := s.layersFor(ctx, s.d.DB, w, env, false)
	if err != nil {
		return toggles.Resolved{}, err
	}
	base := toggles.Resolve(s.defaults(env), parents)
	changing := map[string]bool{GuardReasonKey: set.RequireReason != nil, toggles.GuardApproval: set.RequireApproval != nil, toggles.GuardApprovals: set.Approvals != nil, toggles.GuardWatch: set.WatchMinutes != nil}
	for g, by := range base.LockedBy {
		if changing[g] {
			return toggles.Resolved{}, conflict("%s is locked by %s: a level below may not change it", g, by)
		}
	}
	relaxed := set.Relaxes(base.Safeguards)
	if len(relaxed) > 0 && strings.TrimSpace(reason) == "" {
		return toggles.Resolved{}, invalid("turning down %s needs a reason: say why", strings.Join(relaxed, ", "))
	}
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		return toggles.Resolved{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var old string
	_ = tx.QueryRow(ctx, `SELECT settings FROM toggle_safeguards WHERE org = ? AND level = ? AND scope = ? AND environment = ?`, Org, string(w.Level), w.Scope, env).Scan(&old)
	now := s.now()
	locked := strings.Join(set.Locked, ",")
	store := set
	store.Locked = nil
	if _, err := tx.Exec(ctx, `INSERT INTO toggle_safeguards(org, level, scope, environment, settings, locked, version, updated_at, updated_by) VALUES(?,?,?,?,?,?,1,?,?)
		ON CONFLICT(org, level, scope, environment) DO UPDATE SET settings = excluded.settings, locked = excluded.locked, version = toggle_safeguards.version + 1,
		updated_at = excluded.updated_at, updated_by = excluded.updated_by`, Org, string(w.Level), w.Scope, env, js(store), locked, now, who.User); err != nil {
		return toggles.Resolved{}, err
	}
	detail := reason
	if len(relaxed) > 0 {
		detail = "relaxed " + strings.Join(relaxed, ", ") + ": " + reason
	}
	ch := &Change{At: now, Project: projectOf(w), Level: w.Level, Scope: w.Scope, Environment: env, Toggle: "", Action: ActionSafeguards, Actor: who.User, Reason: detail,
		Via: who.Via, Status: StatusApplied, Old: json.RawMessage(old), New: raw(set)}
	if err := insertChange(ctx, tx, ch); err != nil {
		return toggles.Resolved{}, err
	}
	if err := bump(ctx, tx); err != nil {
		return toggles.Resolved{}, err
	}
	if err := tx.Commit(); err != nil {
		return toggles.Resolved{}, err
	}
	s.noticeLocal(ctx)
	return s.Safeguards(ctx, w, env)
}

// GuardReasonKey is the name of the reason safeguard (see toggles.GuardReason).
const GuardReasonKey = toggles.GuardReason
