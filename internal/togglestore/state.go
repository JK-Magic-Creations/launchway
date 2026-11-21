package togglestore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// StateChange asks for a toggle to behave differently in an environment.
type StateChange struct {
	// Project is the project the change is made for: its environments, its
	// safeguards. A toggle shared from a higher level is changed for the
	// environments of every project below; the safeguards are those of the level.
	Project     string
	Toggle      Key
	Environment string
	State       toggles.State
	Reason      string
	// Retreat marks a change that goes back to a state that was in effect
	// before (a revert): it needs a reason but is never held for approval.
	Retreat bool
}

// Outcome is what a change did: applied now, or waiting for approval.
type Outcome struct {
	Change *Change `json:"change"`
	// Waiting is true when the change has not happened yet: it needs approvals.
	Waiting bool `json:"waiting"`
	// Needs says what a waiting change needs, in words.
	Needs string `json:"needs,omitempty"`
	// AlsoUndone, for a revert, counts later changes that the revert goes back over.
	AlsoUndone int `json:"also_undone,omitempty"`
}

func (s *Service) envGlanceState(ctx context.Context, q queryer, k Key, env string, d *Definition) (toggles.State, *EnvState, error) {
	e, err := getEnv(ctx, q, k, env)
	if err != nil {
		return toggles.State{}, nil, err
	}
	if e == nil {
		return offState(d), nil, nil
	}
	return e.State, e, nil
}

// offState is what a toggle does in an environment where nothing was set: off,
// serving its default.
func offState(d *Definition) toggles.State {
	return toggles.State{On: false, OffValue: d.Default, Fallthrough: toggles.Serve{Value: d.Default}}
}

// isRetreat reports whether a state of a kill switch hands out only its safe value.
func isRetreat(d *Definition, st toggles.State) bool {
	if d.Kind != toggles.KindKill || d.SafeValue == nil {
		return false
	}
	safe := toggles.Canonical(d.SafeValue)
	if !st.On {
		return toggles.Canonical(st.OffValue) == safe
	}
	v, ok := toggles.Settled(st)
	return ok && v == safe
}

// ChangeState sets what a toggle does in an environment, under the
// safeguards: a reason when one is required, and an approval when one is, in
// which case the change waits and nothing happens yet.
func (s *Service) ChangeState(ctx context.Context, who Actor, in StateChange) (*Outcome, error) {
	if in.Environment == "" || len(in.Environment) > 64 {
		return nil, invalid("say which environment")
	}
	d, err := getDef(ctx, s.d.DB, in.Toggle)
	if err != nil {
		return nil, err
	}
	st := in.State
	if err := toggles.CheckState(d.Type, &st); err != nil {
		return nil, invalid("%v", err)
	}
	if d.ClientVisible {
		for _, p := range st.Prerequisites {
			var visible int
			if err := s.d.DB.QueryRow(ctx, `SELECT client_visible FROM toggles WHERE org = ? AND key = ? ORDER BY CASE level WHEN 'project' THEN 3 WHEN 'unit' THEN 2 WHEN 'organization' THEN 1 ELSE 0 END DESC LIMIT 1`, Org, p.Toggle).Scan(&visible); err == nil && visible == 0 {
				return nil, invalid("%s is visible to client applications and %s is not, so it cannot be a prerequisite of it", p.Toggle, p.Toggle)
			}
		}
	}
	if in.Project != "" {
		if err := s.checkLoops(ctx, in.Project, in.Environment, d.Key.Key, st); err != nil {
			return nil, err
		}
	}
	guards, err := s.Safeguards(ctx, in.Toggle.Where, in.Environment)
	if err != nil {
		return nil, err
	}
	if guards.RequireReason && strings.TrimSpace(in.Reason) == "" {
		return nil, fmt.Errorf("%w: say why (the safeguard %s is on, set by %s)", ErrReasonRequired, toggles.GuardReason, guards.From[toggles.GuardReason])
	}
	if len(in.Reason) > 1000 {
		return nil, invalid("a reason is at most 1000 characters")
	}
	retreat := in.Retreat || isRetreat(d, st)
	needApproval := guards.RequireApproval && !retreat
	if !needApproval {
		return s.applyState(ctx, who, in, st, guards, nil)
	}
	// Waiting: the proposed state is kept; nothing else changes.
	ch := &Change{At: s.now(), Project: in.Project, Level: in.Toggle.Level, Scope: in.Toggle.Scope, Toggle: d.Key.Key, Environment: in.Environment, Action: ActionState,
		Actor: who.User, Reason: in.Reason, Via: who.Via, Status: StatusPending, New: raw(st), Approvals: ApprovalRecord{Need: guards.Approvals}}
	if old, _, err := s.envGlanceState(ctx, s.d.DB, in.Toggle, in.Environment, d); err == nil {
		ch.Old = raw(old)
	}
	if err := insertChange(ctx, s.d.DB, ch); err != nil {
		return nil, err
	}
	return &Outcome{Change: ch, Waiting: true, Needs: fmt.Sprintf("%d approval(s) from someone other than %s (safeguard %s, set by %s)", guards.Approvals, who.User, toggles.GuardApproval, guards.From[toggles.GuardApproval])}, nil
}

// applyState writes a state and its change in one transaction. pending, when
// set, is the waiting change this completes.
func (s *Service) applyState(ctx context.Context, who Actor, in StateChange, st toggles.State, guards toggles.Resolved, pending *Change) (*Outcome, error) {
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := getDef(ctx, tx, in.Toggle)
	if err != nil {
		return nil, err
	}
	old, _, err := s.envGlanceState(ctx, tx, in.Toggle, in.Environment, d)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if err := putEnv(ctx, tx, in.Toggle, in.Environment, st, who.User, now); err != nil {
		return nil, err
	}
	watch := int64(0)
	if guards.WatchMinutes > 0 {
		watch = now + int64(guards.WatchMinutes)*60_000
	}
	var ch *Change
	if pending != nil {
		ch = pending
		ch.Status, ch.DecidedBy, ch.DecidedAt, ch.WatchUntil = StatusApplied, who.User, now, watch
		ch.Old, ch.New = raw(old), raw(st)
		if _, err := tx.Exec(ctx, `UPDATE toggle_changes SET status = ?, decided_by = ?, decided_at = ?, watch_until = ?, old_value = ?, new_value = ?, approvals = ? WHERE org = ? AND id = ?`,
			ch.Status, ch.DecidedBy, ch.DecidedAt, watch, string(ch.Old), string(ch.New), js(ch.Approvals), Org, ch.ID); err != nil {
			return nil, err
		}
	} else {
		ch = &Change{At: now, Project: in.Project, Level: in.Toggle.Level, Scope: in.Toggle.Scope, Toggle: d.Key.Key, Environment: in.Environment, Action: ActionState,
			Actor: who.User, Reason: in.Reason, Via: who.Via, Status: StatusApplied, Old: raw(old), New: raw(st), WatchUntil: watch, DecidedBy: who.User, DecidedAt: now}
		if err := insertChange(ctx, tx, ch); err != nil {
			return nil, err
		}
	}
	if err := bump(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.noticeLocal(ctx)
	return &Outcome{Change: ch}, nil
}

// checkLoops refuses a state that makes a loop of prerequisites among the
// toggles a project sees in an environment.
func (s *Service) checkLoops(ctx context.Context, project, env, key string, st toggles.State) error {
	rs, err := s.Build(ctx, project, env)
	if err != nil {
		return err
	}
	f := rs.Toggles[key]
	f.State = st
	rs.Toggles[key] = f
	if loop := toggles.PrerequisiteLoop(rs.Toggles); loop != nil {
		return invalid("these toggles would need each other: %s", strings.Join(loop, " -> "))
	}
	return nil
}

// MayDecide says whether a person may approve or reject a waiting change, and
// if not, why.
func (s *Service) mayDecide(ch *Change, user string, groups []string) error {
	if ch.Status != StatusPending {
		return conflict("the change is %s, not waiting", ch.Status)
	}
	if user == ch.Actor && !s.d.SelfApproval() {
		return refused("you asked for this change; someone else must approve it (approvals.allow_self lets you approve your own)")
	}
	if env := s.d.Environment(ch.Environment); env.MayApprove != nil && !env.MayApprove(user, groups) {
		return refused("only the reviewers of %s may approve changes there", ch.Environment)
	}
	return nil
}

// Approve adds a person's approval to a waiting change; with enough of them
// the change is made.
func (s *Service) Approve(ctx context.Context, who Actor, id int64, comment string) (*Outcome, error) {
	ch, err := getChange(ctx, s.d.DB, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayDecide(ch, who.User, who.Groups); err != nil {
		return nil, err
	}
	if slices.ContainsFunc(ch.Approvals.By, func(a Approval) bool { return a.User == who.User }) {
		return nil, conflict("you have already approved this change")
	}
	ch.Approvals.By = append(ch.Approvals.By, Approval{User: who.User, At: s.now(), Comment: comment})
	if len(ch.Approvals.By) < max(1, ch.Approvals.Need) {
		if _, err := s.d.DB.Exec(ctx, `UPDATE toggle_changes SET approvals = ? WHERE org = ? AND id = ? AND status = 'pending'`, js(ch.Approvals), Org, id); err != nil {
			return nil, err
		}
		return &Outcome{Change: ch, Waiting: true, Needs: fmt.Sprintf("%d more approval(s)", ch.Approvals.Need-len(ch.Approvals.By))}, nil
	}
	var st toggles.State
	if err := json.Unmarshal(ch.New, &st); err != nil {
		return nil, err
	}
	k := Key{Where: Where{Level: ch.Level, Scope: ch.Scope}, Key: ch.Toggle}
	guards, err := s.Safeguards(ctx, k.Where, ch.Environment)
	if err != nil {
		return nil, err
	}
	// The change is made as a whole: by the one who asked, approved by those named.
	return s.applyState(ctx, who, StateChange{Project: ch.Project, Toggle: k, Environment: ch.Environment, State: st, Reason: ch.Reason}, st, guards, ch)
}

// Reject turns a waiting change down.
func (s *Service) Reject(ctx context.Context, who Actor, id int64, comment string) (*Change, error) {
	ch, err := getChange(ctx, s.d.DB, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayDecide(ch, who.User, who.Groups); err != nil {
		return nil, err
	}
	return s.finishWaiting(ctx, ch, StatusRejected, who.User, comment)
}

// Withdraw lets the person who asked take the request back.
func (s *Service) Withdraw(ctx context.Context, who Actor, id int64) (*Change, error) {
	ch, err := getChange(ctx, s.d.DB, id)
	if err != nil {
		return nil, err
	}
	if ch.Status != StatusPending {
		return nil, conflict("the change is %s, not waiting", ch.Status)
	}
	if ch.Actor != who.User {
		return nil, refused("only %s, who asked for it, may withdraw it", ch.Actor)
	}
	return s.finishWaiting(ctx, ch, StatusWithdrawn, who.User, "")
}

func (s *Service) finishWaiting(ctx context.Context, ch *Change, status, by, comment string) (*Change, error) {
	res, err := s.d.DB.Exec(ctx, `UPDATE toggle_changes SET status = ?, decided_by = ?, decided_at = ?, reason = CASE WHEN ? = '' THEN reason ELSE reason || ' | ' || ? END
		WHERE org = ? AND id = ? AND status = 'pending'`, status, by, s.now(), comment, comment, Org, ch.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, conflict("the change was decided meanwhile")
	}
	return getChange(ctx, s.d.DB, ch.ID)
}

// Revert goes back over a change: the environment's state, or the definition,
// returns to what it was before the change. It is a retreat (a reason, no
// approval) and is a change of its own, which can itself be reverted.
func (s *Service) Revert(ctx context.Context, who Actor, id int64, reason string) (*Outcome, error) {
	ch, err := getChange(ctx, s.d.DB, id)
	if err != nil {
		return nil, err
	}
	if ch.Status != StatusApplied {
		return nil, conflict("the change was %s, not made: there is nothing to revert", ch.Status)
	}
	if ch.RevertedBy != 0 {
		return nil, conflict("change %d was already reverted by change %d", id, ch.RevertedBy)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: say why it is reverted", ErrReasonRequired)
	}
	k := Key{Where: Where{Level: ch.Level, Scope: ch.Scope}, Key: ch.Toggle}
	var out *Outcome
	switch ch.Action {
	case ActionState:
		var st toggles.State
		if err := json.Unmarshal(ch.Old, &st); err != nil {
			return nil, conflict("the change does not say what it replaced")
		}
		later, err := queryChanges(ctx, s.d.DB, `AND level = ? AND scope = ? AND key = ? AND environment = ? AND action = 'state' AND status = 'applied' AND id > ? AND reverts = 0`,
			string(k.Level), k.Scope, k.Key, ch.Environment, id)
		if err != nil {
			return nil, err
		}
		out, err = s.ChangeState(ctx, who, StateChange{Project: ch.Project, Toggle: k, Environment: ch.Environment, State: st,
			Reason: fmt.Sprintf("revert of change %d: %s", id, reason), Retreat: true})
		if err != nil {
			return nil, err
		}
		out.AlsoUndone = len(later)
	case ActionUpdate:
		var old Definition
		if len(ch.Old) == 0 || json.Unmarshal(ch.Old, &old) != nil || old.Key.Key == "" {
			return nil, conflict("this change cannot be reverted: it did not record a definition")
		}
		def, err := s.Update(ctx, who, k, Patch{Description: &old.Description, Default: &old.Default, SafeValue: &old.SafeValue, Kind: &old.Kind,
			ClientVisible: &old.ClientVisible, Owner: &old.Owner, Tags: &old.Tags, ExpiresAt: &old.ExpiresAt}, fmt.Sprintf("revert of change %d: %s", id, reason))
		if err != nil {
			return nil, err
		}
		_ = def
		last, err := queryChanges(ctx, s.d.DB, `AND level = ? AND scope = ? AND key = ? AND action = 'update' ORDER BY id DESC LIMIT 1`, string(k.Level), k.Scope, k.Key)
		if err != nil || len(last) == 0 {
			return nil, err
		}
		out = &Outcome{Change: &last[0]}
	case ActionDelete:
		var gone deleted
		if json.Unmarshal(ch.Old, &gone) != nil || gone.Definition.Key.Key == "" {
			return nil, conflict("this change cannot be reverted: it did not record the toggle")
		}
		out, err = s.restore(ctx, who, gone, fmt.Sprintf("revert of change %d: %s", id, reason))
		if err != nil {
			return nil, err
		}
	default:
		return nil, conflict("a change of kind %s cannot be reverted here: make the opposite change", ch.Action)
	}
	if _, err := s.d.DB.Exec(ctx, `UPDATE toggle_changes SET reverted_by = ? WHERE org = ? AND id = ?`, out.Change.ID, Org, id); err != nil {
		return nil, err
	}
	if _, err := s.d.DB.Exec(ctx, `UPDATE toggle_changes SET reverts = ? WHERE org = ? AND id = ?`, id, Org, out.Change.ID); err != nil {
		return nil, err
	}
	out.Change.Reverts = id
	return out, nil
}

// restore brings back a deleted toggle with its states.
func (s *Service) restore(ctx context.Context, who Actor, gone deleted, reason string) (*Outcome, error) {
	d := gone.Definition
	in := DefinitionInput{Key: d.Key, Type: d.Type, Kind: d.Kind, Description: d.Description, Default: d.Default, SafeValue: d.SafeValue,
		ClientVisible: d.ClientVisible, Owner: d.Owner, Tags: d.Tags, ExpiresAt: d.ExpiresAt, Origin: "restore"}
	if _, err := s.Create(ctx, who, in, reason); err != nil {
		return nil, err
	}
	// The salt is kept, so that nobody moves between buckets.
	if _, err := s.d.DB.Exec(ctx, `UPDATE toggles SET salt = ? WHERE org = ? AND level = ? AND scope = ? AND key = ?`, d.Salt, Org, string(d.Level), d.Scope, d.Key.Key); err != nil {
		return nil, err
	}
	for _, e := range gone.Envs {
		if err := putEnv(ctx, s.d.DB, d.Key, e.Environment, e.State, who.User, s.now()); err != nil {
			return nil, err
		}
	}
	if err := bump(ctx, s.d.DB); err != nil {
		return nil, err
	}
	s.noticeLocal(ctx)
	last, err := queryChanges(ctx, s.d.DB, `AND level = ? AND scope = ? AND key = ? AND action = 'create' ORDER BY id DESC LIMIT 1`, string(d.Level), d.Scope, d.Key.Key)
	if err != nil || len(last) == 0 {
		return nil, err
	}
	return &Outcome{Change: &last[0]}, nil
}

// Pending lists the changes waiting for approval in a project.
func (s *Service) Pending(ctx context.Context, project string) ([]Change, error) {
	return queryChanges(ctx, s.d.DB, `AND status = 'pending' AND (project = ? OR project = '') ORDER BY id DESC`, project)
}

// Change returns one change.
func (s *Service) Change(ctx context.Context, id int64) (*Change, error) {
	return getChange(ctx, s.d.DB, id)
}

// History returns a toggle's changes, newest first.
func (s *Service) History(ctx context.Context, k Key, env string, limit int) ([]Change, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where, args := `AND level = ? AND scope = ? AND key = ?`, []any{string(k.Level), k.Scope, k.Key}
	if env != "" {
		where += ` AND environment = ?`
		args = append(args, env)
	}
	return queryChanges(ctx, s.d.DB, where+fmt.Sprintf(` ORDER BY id DESC LIMIT %d`, limit), args...)
}
