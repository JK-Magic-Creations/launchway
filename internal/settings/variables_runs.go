package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// What a run resolved when it was created. The values are recorded as the run
// used them (with the whole chain behind each), so that a re-run repeats them
// and the run page can show them however the levels change afterwards. A
// secret is recorded as its reference.

// RecordedVariable is one variable a run used.
type RecordedVariable struct {
	Resolved
	Environment string `json:"environment,omitempty"`
	// RunID is the run the record belongs to: for a re-run, the original's.
	RunID string `json:"run_id"`
}

// RecordRun keeps the variables a run used. Recording twice replaces.
func (s *VariableService) RecordRun(ctx context.Context, runID, environment string, used []pipeline.BoundVariable) error {
	if len(used) == 0 {
		return nil
	}
	tx, err := s.Store.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range used {
		var r Resolved
		if len(u.Detail) > 0 {
			_ = json.Unmarshal(u.Detail, &r)
		}
		chain, _ := json.Marshal(r)
		locked := 0
		if u.Locked {
			locked = 1
		}
		if _, err := tx.Exec(ctx, `DELETE FROM run_variables WHERE run_id = ? AND name = ?`, runID, u.Name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO run_variables(run_id, name, environment, type, value, level, scope, locked, chain)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, u.Name, environment, u.Type, u.Display, string(r.From.Level), r.From.Scope, locked, string(chain)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RunVariables returns the variables a run used, as recorded. A re-run has
// none of its own: it used the original's, and says so by RunID.
func (s *VariableService) RunVariables(ctx context.Context, runID, rerunOf string) ([]RecordedVariable, error) {
	out, err := s.runRows(ctx, runID)
	if err != nil || len(out) > 0 || rerunOf == "" {
		return out, err
	}
	return s.runRows(ctx, rerunOf)
}

func (s *VariableService) runRows(ctx context.Context, runID string) ([]RecordedVariable, error) {
	rows, err := s.Store.db.Query(ctx, `SELECT name, environment, chain FROM run_variables WHERE run_id = ? ORDER BY name`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordedVariable{}
	for rows.Next() {
		var name, env, chain string
		if err := rows.Scan(&name, &env, &chain); err != nil {
			return nil, err
		}
		rv := RecordedVariable{Environment: env, RunID: runID}
		if err := json.Unmarshal([]byte(chain), &rv.Resolved); err != nil || rv.Name == "" {
			rv.Resolved = Resolved{Name: name}
		}
		out = append(out, rv)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return out, nil
}
