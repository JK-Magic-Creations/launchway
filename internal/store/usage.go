package store

import "database/sql"

// What a step attempt used (schema 54). step_usage is a run-engine table, so
// until the engine moves into the store (stage S2) its statements are run on
// the connection or transaction the engine hands in, which is why they take
// a Execer and not a Scope. They name the run, which belongs to one Base.

// UsageRow is one row of step_usage.
type UsageRow struct {
	// Sizing says where the step's memory and CPU came from (declared in the
	// file, learned from earlier runs, the installation's default, or raised
	// after a memory kill), SizingRuns how many measured runs a learned figure
	// rests on, and Killed that the attempt was killed for memory.
	Sizing     string
	SizingRuns int
	Killed     bool

	Step             string
	Attempt          int
	Measured         bool
	Method, Note     string
	MemoryPeak       int64
	CPUPeakMilli     int64
	CPUMillis        int64
	ReservedMemory   int64
	ReservedCPUMilli int64
	Runner           string
	At               int64
}

// Execer is what runs a statement written with ?.
type Execer interface {
	Exec(q string, args ...any) (sql.Result, error)
}

// Queryer reads rows.
type Queryer interface {
	Query(q string, args ...any) (*sql.Rows, error)
}

// ExecQuery is what runs a statement and reads rows.
type ExecQuery interface {
	Execer
	Queryer
}

// SaveStepSizing records, when a step attempt is handed out, where its
// numbers came from. The row is completed (measured or not) when the attempt
// ends; an attempt that is lost keeps this row and nothing else.
func SaveStepSizing(x Execer, run, step string, attempt int, sizing string, runs int, now int64) error {
	_, err := x.Exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, sizing, sizing_runs, created_at) VALUES(?,?,?,0,?,?,?)
		ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET sizing = excluded.sizing, sizing_runs = excluded.sizing_runs`,
		run, step, attempt, sizing, runs, now)
	return err
}

// handedOut reads what an attempt was handed out with (SaveStepSizing), if it was.
func handedOut(x Queryer, run, step string, attempt int) (UsageRow, error) {
	var prev UsageRow
	rows, err := x.Query(`SELECT sizing, sizing_runs FROM step_usage WHERE run_id = ? AND step_id = ? AND attempt = ?`, run, step, attempt)
	if err != nil {
		return prev, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&prev.Sizing, &prev.SizingRuns); err != nil {
			return prev, err
		}
	}
	return prev, rows.Err()
}

// SaveStepUsage records one step attempt's use, replacing an earlier record
// of the same attempt. What the attempt was handed out with (SaveStepSizing)
// stays unless r says its own.
func SaveStepUsage(x ExecQuery, run string, r UsageRow) error {
	m := 0
	if r.Measured {
		m = 1
	}
	if r.Sizing == "" {
		prev, err := handedOut(x, run, r.Step, r.Attempt)
		if err != nil {
			return err
		}
		r.Sizing, r.SizingRuns = prev.Sizing, prev.SizingRuns
	}
	killed := 0
	if r.Killed {
		killed = 1
	}
	_, err := x.Exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, method, note, memory_peak, cpu_peak_milli, cpu_ms,
		reserved_memory, reserved_cpu_milli, runner, created_at, sizing, sizing_runs, killed) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET measured = excluded.measured, method = excluded.method, note = excluded.note,
		memory_peak = excluded.memory_peak, cpu_peak_milli = excluded.cpu_peak_milli, cpu_ms = excluded.cpu_ms,
		reserved_memory = excluded.reserved_memory, reserved_cpu_milli = excluded.reserved_cpu_milli,
		sizing = excluded.sizing, sizing_runs = excluded.sizing_runs, killed = excluded.killed`,
		run, r.Step, r.Attempt, m, r.Method, r.Note, r.MemoryPeak, r.CPUPeakMilli, r.CPUMillis,
		r.ReservedMemory, r.ReservedCPUMilli, r.Runner, r.At, r.Sizing, r.SizingRuns, killed)
	return err
}

// StepUsages returns a run's records, ordered by step and attempt.
func StepUsages(q Queryer, run string) ([]UsageRow, error) {
	rows, err := q.Query(`SELECT step_id, attempt, measured, method, note, memory_peak, cpu_peak_milli, cpu_ms, reserved_memory, reserved_cpu_milli, sizing, sizing_runs, killed
		FROM step_usage WHERE run_id = ? ORDER BY step_id, attempt`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var m int
		var killed int
		if err := rows.Scan(&r.Step, &r.Attempt, &m, &r.Method, &r.Note, &r.MemoryPeak, &r.CPUPeakMilli, &r.CPUMillis, &r.ReservedMemory, &r.ReservedCPUMilli, &r.Sizing, &r.SizingRuns, &killed); err != nil {
			return nil, err
		}
		r.Killed = killed != 0
		r.Measured = m != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// Peak is one measured attempt's highest memory use, with the branch of its
// run.
type Peak struct {
	Bytes  int64
	Branch string
}

// StepPeaks returns the highest memory use of the most recent measured
// attempts of a step of a pipeline of a Flight, newest first, at most limit.
// An attempt killed for memory does not count: what it used is only known to
// be at least its limit.
func StepPeaks(q Queryer, project, pipeline, step string, limit int) ([]Peak, error) {
	rows, err := q.Query(`SELECT u.memory_peak, r.branch FROM step_usage u JOIN runs r ON r.id = u.run_id
		WHERE r.project = ? AND r.pipeline = ? AND u.step_id = ? AND u.measured = 1 AND u.memory_peak > 0
		AND u.killed = 0
		ORDER BY u.created_at DESC LIMIT ?`, project, pipeline, step, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peak
	for rows.Next() {
		var p Peak
		if err := rows.Scan(&p.Bytes, &p.Branch); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
