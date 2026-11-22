package store

import (
	"database/sql"
	"strconv"
	"strings"
)

// What a step attempt used (schema 54). step_usage is a run-engine table, so
// until the engine moves into the store (stage S2) its statements are run on
// the connection or transaction the engine hands in, which is why they take
// a Execer and not a Scope. They name the run, which belongs to one Base.

// What a row says about how the step's numbers came to be, and whether the
// attempt was killed for memory, is kept at the front of its note column,
// between two unit separators, so that no schema change was needed for it:
//
//	\x1fsizing=learned;runs=7;killed=1\x1fthe rest of the note
//
// Sizing is declared, learned, default or raised (after a memory kill).
const metaMark = "\x1f"

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

func encodeMeta(r UsageRow) string {
	if r.Sizing == "" && !r.Killed {
		return r.Note
	}
	meta := "sizing=" + r.Sizing
	if r.SizingRuns > 0 {
		meta += ";runs=" + strconv.Itoa(r.SizingRuns)
	}
	if r.Killed {
		meta += ";killed=1"
	}
	return metaMark + meta + metaMark + r.Note
}

func decodeMeta(note string, r *UsageRow) {
	rest, ok := strings.CutPrefix(note, metaMark)
	if !ok {
		r.Note = note
		return
	}
	meta, text, ok := strings.Cut(rest, metaMark)
	if !ok {
		r.Note = note
		return
	}
	r.Note = text
	for _, f := range strings.Split(meta, ";") {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "sizing":
			r.Sizing = v
		case "runs":
			r.SizingRuns, _ = strconv.Atoi(v)
		case "killed":
			r.Killed = v == "1"
		}
	}
}

// SaveStepSizing records, when a step attempt is handed out, where its
// numbers came from. The row is completed (measured or not) when the attempt
// ends; an attempt that is lost keeps this row and nothing else.
func SaveStepSizing(x Execer, run, step string, attempt int, sizing string, runs int, now int64) error {
	_, err := x.Exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, note, created_at) VALUES(?,?,?,0,?,?)
		ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET note = excluded.note`,
		run, step, attempt, encodeMeta(UsageRow{Sizing: sizing, SizingRuns: runs}), now)
	return err
}

// handedOut reads what an attempt was handed out with (SaveStepSizing), if it was.
func handedOut(x Queryer, run, step string, attempt int) (UsageRow, error) {
	var prev UsageRow
	rows, err := x.Query(`SELECT note FROM step_usage WHERE run_id = ? AND step_id = ? AND attempt = ?`, run, step, attempt)
	if err != nil {
		return prev, err
	}
	defer rows.Close()
	if rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			return prev, err
		}
		decodeMeta(note, &prev)
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
	r.Note = encodeMeta(r)
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
		var note string
		if err := rows.Scan(&r.Step, &r.Attempt, &m, &r.Method, &note, &r.MemoryPeak, &r.CPUPeakMilli, &r.CPUMillis, &r.ReservedMemory, &r.ReservedCPUMilli); err != nil {
			return nil, err
		}
		decodeMeta(note, &r)
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
		AND u.note NOT LIKE ?
		ORDER BY u.created_at DESC LIMIT ?`, project, pipeline, step, "%"+metaMark+"%killed=1%", limit)
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
