package engine

import (
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/testreport"
)

// Per-test statistics (schema 44). A row per test of a project keeps what it
// is called, when it was first and last seen, how often it ran and failed,
// and its typical duration as a running average over (about) its last 20
// runs. They outlive the pruning of individual results, so a test's first
// appearance and its timings stay known, and they feed the split of tests
// among shards.

// statWindow is how many runs the average duration weighs.
const statWindow = 20

// upsertTestStats adds one attempt's results to the statistics. A test that
// ran more than once in the attempt counts once, with its longest duration.
func upsertTestStats(tx *txx, pg bool, st *RunState, at int64, cases []storedCase) error {
	type agg struct {
		c      *storedCase
		ms     int64
		failed bool
	}
	by := map[string]*agg{}
	var order []string
	for i := range cases {
		c := &cases[i]
		if c.Status == testreport.Skipped {
			continue
		}
		a := by[c.key]
		if a == nil {
			a = &agg{c: c}
			by[c.key] = a
			order = append(order, c.key)
		}
		a.ms = max(a.ms, c.DurationMs)
		a.failed = a.failed || c.Status == testreport.Failed || c.Status == testreport.Errored
	}
	minF, maxF := "MIN", "MAX"
	if pg {
		minF, maxF = "LEAST", "GREATEST"
	}
	const per = 200
	for start := 0; start < len(order); start += per {
		batch := order[start:min(start+per, len(order))]
		var q strings.Builder
		q.WriteString(`INSERT INTO test_stats(project, test_key, suite, name, file, variant, first_run, first_at, last_run, last_at, runs, failures, avg_ms, max_ms) VALUES `)
		args := make([]any, 0, len(batch)*14)
		for i, k := range batch {
			a := by[k]
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString("(?,?,?,?,?,?,?,?,?,?,1,?,?,?)")
			failed := 0
			if a.failed {
				failed = 1
			}
			args = append(args, st.Project, k, cleanText(a.c.Suite, 300), cleanText(a.c.Name, 500), cleanText(a.c.File, 300), cleanText(a.c.Variant, 100),
				st.Number, at, st.Number, at, failed, a.ms, a.ms)
		}
		fmt.Fprintf(&q, ` ON CONFLICT(project, test_key) DO UPDATE SET
			suite = excluded.suite, name = excluded.name, file = excluded.file, variant = excluded.variant,
			last_run = excluded.last_run, last_at = excluded.last_at,
			avg_ms = (test_stats.avg_ms * %[1]s(test_stats.runs, %[3]d) + excluded.avg_ms) / (%[1]s(test_stats.runs, %[3]d) + 1),
			max_ms = %[2]s(test_stats.max_ms, excluded.max_ms),
			runs = test_stats.runs + 1, failures = test_stats.failures + excluded.failures`, minF, maxF, statWindow-1)
		if _, err := tx.Exec(q.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// pruneTestsByAge removes the individual results of runs older than the
// retention (MaxAgeDays), keeping each run's totals, and the statistics of
// tests not seen since. Every server may do it at once: it only removes what
// is old.
func (e *Engine) pruneTestsByAge() {
	days := e.live().tests.MaxAgeDays
	if days <= 0 || !e.knows(schemaTests) {
		return
	}
	cutoff := e.now().UnixMilli() - int64(days)*24*3600*1000
	unlock := e.writeLock()
	_, err := e.db.Exec(`UPDATE test_reports SET stored = 0, pruned = 1 WHERE (stored > 0 OR pruned = 0)
		AND run_id IN (SELECT id FROM runs WHERE created_at < ?)`, cutoff)
	unlock()
	if err != nil {
		e.log.Warn("marking test results past their retention; trying again later", "err", err)
		return
	}
	removed := int64(0)
	for {
		unlock := e.writeLock()
		res, err := e.db.Exec(`DELETE FROM test_cases WHERE id IN (SELECT tc.id FROM test_cases tc JOIN runs r ON r.id = tc.run_id
			WHERE r.created_at < ? ORDER BY tc.id LIMIT ?)`, cutoff, pruneBatch)
		unlock()
		if err != nil {
			e.log.Warn("removing test results past their retention; trying again later", "err", err)
			return
		}
		n, _ := res.RowsAffected()
		removed += n
		if n < pruneBatch {
			break
		}
	}
	if e.knows(schemaDebrief) {
		unlock := e.writeLock()
		_, err := e.db.Exec(`DELETE FROM test_stats WHERE last_at < ?`, cutoff)
		unlock()
		if err != nil {
			e.log.Warn("removing statistics of tests not seen within the retention", "err", err)
		}
	}
	if removed > 0 {
		e.log.Info("removed test results past their retention, keeping the runs' totals", "results", removed, "days", days)
	}
}
