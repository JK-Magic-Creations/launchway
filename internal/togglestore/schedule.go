package togglestore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// Schedule is a change to a toggle in an environment that waits for its time.
// When the time comes it is made like any other change: the safeguards apply
// then (a change that needs approval waits for it, with the schedule marked
// as having asked), and it is in the history, attributed to the person who
// scheduled it, with the way "schedule".
type Schedule struct {
	ID          int64         `json:"id"`
	Project     string        `json:"project"`
	Toggle      Key           `json:"toggle"`
	Environment string        `json:"environment"`
	RunAt       int64         `json:"run_at"`
	State       toggles.State `json:"state"`
	Reason      string        `json:"reason"`
	CreatedBy   string        `json:"created_by"`
	CreatedAt   int64         `json:"created_at"`
	Status      string        `json:"status"` // pending, done, failed, canceled
	Result      string        `json:"result,omitempty"`
	DoneAt      int64         `json:"done_at,omitempty"`
}

const schedCols = `id, project, level, scope, key, environment, run_at, patch, reason, created_by, created_at, status, result, done_at`

func scanSchedule(sc interface{ Scan(...any) error }) (Schedule, error) {
	var x Schedule
	var level, patch string
	if err := sc.Scan(&x.ID, &x.Project, &level, &x.Toggle.Scope, &x.Toggle.Key, &x.Environment, &x.RunAt, &patch, &x.Reason, &x.CreatedBy, &x.CreatedAt, &x.Status, &x.Result, &x.DoneAt); err != nil {
		return x, err
	}
	x.Toggle.Level = Level(level)
	return x, json.Unmarshal([]byte(patch), &x.State)
}

// ScheduleChange asks for a change to be made at a time.
func (s *Service) ScheduleChange(ctx context.Context, who Actor, in StateChange, runAt int64) (*Schedule, error) {
	if runAt <= s.now() {
		return nil, invalid("a scheduled change is for a time in the future")
	}
	if runAt > s.now()+366*24*3600*1000 {
		return nil, invalid("a change may be scheduled at most a year ahead")
	}
	d, err := getDef(ctx, s.d.DB, in.Toggle)
	if err != nil {
		return nil, err
	}
	st := in.State
	if err := toggles.CheckState(d.Type, &st); err != nil {
		return nil, invalid("%v", err)
	}
	guards, err := s.Safeguards(ctx, in.Toggle.Where, in.Environment)
	if err != nil {
		return nil, err
	}
	if guards.RequireReason && strings.TrimSpace(in.Reason) == "" {
		return nil, fmt.Errorf("%w: say why (the safeguard %s is on)", ErrReasonRequired, toggles.GuardReason)
	}
	var n int
	if err := s.d.DB.QueryRow(ctx, `SELECT count(*) FROM toggle_schedules WHERE org = ? AND level = ? AND scope = ? AND key = ? AND status = 'pending'`,
		Org, string(in.Toggle.Level), in.Toggle.Scope, in.Toggle.Key).Scan(&n); err != nil {
		return nil, err
	}
	if n >= 50 {
		return nil, conflict("a toggle has at most 50 changes waiting for their time")
	}
	var id int64
	if err := s.d.DB.QueryRow(ctx, `INSERT INTO toggle_schedules(org, project, level, scope, key, environment, run_at, patch, reason, created_by, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, Org, in.Project, string(in.Toggle.Level), in.Toggle.Scope, in.Toggle.Key, in.Environment, runAt, js(st), in.Reason, who.User, s.now()).Scan(&id); err != nil {
		return nil, err
	}
	return s.schedule(ctx, id)
}

func (s *Service) schedule(ctx context.Context, id int64) (*Schedule, error) {
	x, err := scanSchedule(s.d.DB.QueryRow(ctx, `SELECT `+schedCols+` FROM toggle_schedules WHERE org = ? AND id = ?`, Org, id))
	if err != nil {
		return nil, missing("no scheduled change %d", id)
	}
	return &x, nil
}

// Schedules lists a project's scheduled changes, the next first; with a toggle
// only that toggle's.
func (s *Service) Schedules(ctx context.Context, project, toggle string, all bool) ([]Schedule, error) {
	q := `SELECT ` + schedCols + ` FROM toggle_schedules WHERE org = ? AND project = ?`
	args := []any{Org, project}
	if toggle != "" {
		q += ` AND key = ?`
		args = append(args, toggle)
	}
	if !all {
		q += ` AND status = 'pending'`
	}
	rows, err := s.d.DB.Query(ctx, q+` ORDER BY run_at, id LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		x, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// CancelSchedule drops a scheduled change that has not run.
func (s *Service) CancelSchedule(ctx context.Context, project string, id int64) error {
	x, err := s.schedule(ctx, id)
	if err != nil || x.Project != project {
		return missing("no scheduled change %d in project %s", id, project)
	}
	res, err := s.d.DB.Exec(ctx, `UPDATE toggle_schedules SET status = 'canceled', done_at = ? WHERE org = ? AND id = ? AND status = 'pending'`, s.now(), Org, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return conflict("the change already %s", x.Status)
	}
	return nil
}

func (s *Service) dueSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.d.DB.Query(ctx, `SELECT `+schedCols+` FROM toggle_schedules WHERE org = ? AND status = 'pending' AND run_at <= ? ORDER BY run_at, id LIMIT 50`, Org, s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []Schedule
	for rows.Next() {
		x, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		due = append(due, x)
	}
	return due, rows.Err()
}

// RunDue makes the scheduled changes whose time has come. Servers call it on a
// timer; two servers never make the same one, because each claims a change
// with a single conditional update before making it.
func (s *Service) RunDue(ctx context.Context) (int, error) {
	due, err := s.dueSchedules(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, x := range due {
		res, err := s.d.DB.Exec(ctx, `UPDATE toggle_schedules SET status = 'running' WHERE org = ? AND id = ? AND status = 'pending'`, Org, x.ID)
		if err != nil {
			return done, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // another server has it
		}
		out, err := s.ChangeState(ctx, Actor{User: x.CreatedBy, Via: "schedule"}, StateChange{Project: x.Project, Toggle: x.Toggle, Environment: x.Environment, State: x.State,
			Reason: x.Reason + " (scheduled)"})
		status, result := "done", ""
		switch {
		case err != nil:
			status, result = "failed", err.Error()
		case out.Waiting:
			result = fmt.Sprintf("made as change %d, which waits for approval", out.Change.ID)
		default:
			result = fmt.Sprintf("made as change %d", out.Change.ID)
		}
		if _, err := s.d.DB.Exec(ctx, `UPDATE toggle_schedules SET status = ?, result = ?, done_at = ? WHERE org = ? AND id = ?`, status, result, s.now(), Org, x.ID); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}
