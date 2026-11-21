package engine

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Configuration as code (schema 48): the parsed, redacted reports of
// configuration steps, one per step attempt and kind. The parsing is in
// internal/cac; the engine only keeps and lists what it is given.

// CacKinds.
const (
	CacAnsible = "ansible"
	CacPlan    = "plan"
)

// CacReport is one stored report.
type CacReport struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	Step        string `json:"step"`
	Attempt     int    `json:"attempt"`
	Project     string `json:"project"`
	Environment string `json:"environment,omitempty"`
	Kind        string `json:"kind"`
	Commit      string `json:"commit"`
	Check       bool   `json:"check"`
	Status      string `json:"status"`
	Recap       string `json:"recap"`
	Summary     string `json:"summary"`            // JSON
	Document    string `json:"document,omitempty"` // JSON; empty in listings
	CreatedAt   int64  `json:"created_at"`
	RunNumber   int    `json:"run_number,omitempty"`
}

// SaveCacReport stores a report; a second one for the same attempt and kind
// is kept as it was (the first parse is the record).
func (e *Engine) SaveCacReport(ctx context.Context, r CacReport) (CacReport, error) {
	if !e.knows(schemaConfigAsCode) {
		return r, ErrConflict
	}
	r.ID = ids.New("cac")
	r.CreatedAt = e.now().UnixMilli()
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO cac_reports(id, run_id, step_id, attempt, project, environment, kind, commit_sha, check_mode, status, recap, summary, document, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (run_id, step_id, attempt, kind) DO NOTHING`,
		r.ID, r.RunID, r.Step, r.Attempt, r.Project, r.Environment, r.Kind, r.Commit, boolInt(r.Check), r.Status, r.Recap, r.Summary, r.Document, r.CreatedAt)
	return r, err
}

const cacCols = `r.id, r.run_id, r.step_id, r.attempt, r.project, r.environment, r.kind, r.commit_sha, r.check_mode, r.status, r.recap, r.summary, r.created_at`

func scanCac(sc interface{ Scan(...any) error }, withDoc bool) (CacReport, error) {
	var r CacReport
	var check int
	dest := []any{&r.ID, &r.RunID, &r.Step, &r.Attempt, &r.Project, &r.Environment, &r.Kind, &r.Commit, &check, &r.Status, &r.Recap, &r.Summary, &r.CreatedAt}
	if withDoc {
		dest = append(dest, &r.Document)
	}
	err := sc.Scan(dest...)
	r.Check = check != 0
	return r, err
}

// HasCacReport says whether a report of the kind exists for the attempt.
func (e *Engine) HasCacReport(ctx context.Context, runID, step string, attempt int, kind string) (bool, error) {
	if !e.knows(schemaConfigAsCode) {
		return true, nil // nothing can be stored, so nothing is missing
	}
	var n int
	err := e.db.QueryRow(`SELECT count(*) FROM cac_reports WHERE run_id = ? AND step_id = ? AND attempt = ? AND kind = ?`, runID, step, attempt, kind).Scan(&n)
	return n > 0, err
}

// CacReportsOfRun lists a run's reports (no documents), oldest first.
func (e *Engine) CacReportsOfRun(ctx context.Context, runID string) ([]CacReport, error) {
	if !e.knows(schemaConfigAsCode) {
		return []CacReport{}, nil
	}
	return e.cacList(ctx, `SELECT `+cacCols+` FROM cac_reports r WHERE r.run_id = ? ORDER BY r.created_at, r.step_id`, runID)
}

// CacReportOf reads one report with its document: the latest attempt of the
// step, of the kind ("" any).
func (e *Engine) CacReportOf(ctx context.Context, runID, step, kind string) (CacReport, error) {
	q := `SELECT ` + cacCols + `, r.document FROM cac_reports r WHERE r.run_id = ? AND r.step_id = ?`
	args := []any{runID, step}
	if kind != "" {
		q += ` AND r.kind = ?`
		args = append(args, kind)
	}
	r, err := scanCac(e.db.QueryRow(q+` ORDER BY r.attempt DESC LIMIT 1`, args...), true)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// LatestCacReports lists a project's newest reports of a kind, newest first,
// one per (environment, step), with the run number.
func (e *Engine) LatestCacReports(ctx context.Context, project, kind string, check *bool, limit int) ([]CacReport, error) {
	if !e.knows(schemaConfigAsCode) {
		return []CacReport{}, nil
	}
	q := `SELECT ` + cacCols + ` FROM cac_reports r WHERE r.project = ?`
	args := []any{project}
	if kind != "" {
		q += ` AND r.kind = ?`
		args = append(args, kind)
	}
	if check != nil {
		q += ` AND r.check_mode = ?`
		args = append(args, boolInt(*check))
	}
	q += ` ORDER BY r.created_at DESC LIMIT ?`
	args = append(args, limit)
	return e.cacList(ctx, q, args...)
}

// CacRecaps maps run IDs to their reports' recaps (for the launch line).
func (e *Engine) CacRecaps(ctx context.Context, runIDs []string) (map[string][]CacReport, error) {
	out := map[string][]CacReport{}
	if !e.knows(schemaConfigAsCode) {
		return out, nil
	}
	for _, id := range runIDs {
		l, err := e.CacReportsOfRun(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(l) > 0 {
			out[id] = l
		}
	}
	return out, nil
}

func (e *Engine) cacList(ctx context.Context, q string, args ...any) ([]CacReport, error) {
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CacReport{}
	for rows.Next() {
		r, err := scanCac(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LeaseStep says which run and step a lease is for, without renewing it.
func (e *Engine) LeaseStep(ctx context.Context, leaseID string) (runID, stepID string, attempt int, err error) {
	err = e.db.QueryRow(`SELECT run_id, step_id, attempt FROM leases WHERE id = ?`, leaseID).Scan(&runID, &stepID, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, ErrLeaseLost
	}
	return
}
