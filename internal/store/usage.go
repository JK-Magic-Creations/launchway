package store

import "database/sql"

// What a step attempt used (schema 54). step_usage is a run-engine table, so
// until the engine moves into the store (stage S2) its statements are run on
// the connection or transaction the engine hands in, which is why they take
// a Execer and not a Scope. They name the run, which belongs to one Base.

// UsageRow is one row of step_usage.
type UsageRow struct {
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

// SaveStepUsage records one step attempt's use, replacing an earlier record
// of the same attempt.
func SaveStepUsage(x Execer, run string, r UsageRow) error {
	m := 0
	if r.Measured {
		m = 1
	}
	_, err := x.Exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, method, note, memory_peak, cpu_peak_milli, cpu_ms,
		reserved_memory, reserved_cpu_milli, runner, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET measured = excluded.measured, method = excluded.method, note = excluded.note,
		memory_peak = excluded.memory_peak, cpu_peak_milli = excluded.cpu_peak_milli, cpu_ms = excluded.cpu_ms,
		reserved_memory = excluded.reserved_memory, reserved_cpu_milli = excluded.reserved_cpu_milli`,
		run, r.Step, r.Attempt, m, r.Method, r.Note, r.MemoryPeak, r.CPUPeakMilli, r.CPUMillis,
		r.ReservedMemory, r.ReservedCPUMilli, r.Runner, r.At)
	return err
}

// StepUsages returns a run's records, ordered by step and attempt.
func StepUsages(q Queryer, run string) ([]UsageRow, error) {
	rows, err := q.Query(`SELECT step_id, attempt, measured, method, note, memory_peak, cpu_peak_milli, cpu_ms, reserved_memory, reserved_cpu_milli
		FROM step_usage WHERE run_id = ? ORDER BY step_id, attempt`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var m int
		if err := rows.Scan(&r.Step, &r.Attempt, &m, &r.Method, &r.Note, &r.MemoryPeak, &r.CPUPeakMilli, &r.CPUMillis, &r.ReservedMemory, &r.ReservedCPUMilli); err != nil {
			return nil, err
		}
		r.Measured = m != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
