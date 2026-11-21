package togglestore

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Evaluation counts. What is counted: how many times a toggle was evaluated,
// per environment, per UTC day, per value served and reason. What is never
// stored: who or what was evaluated. No key, no attribute, no address; the
// counts cannot be turned back into people. They live in memory and are
// written, summed, by Flush (every 30 seconds, and when the server stops).

type countKey struct {
	project, env, toggle, day, variation, reason string
}

type counts struct {
	mu sync.Mutex
	m  map[countKey]int64
}

func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

// variationText is the value served, as short text.
func variationText(v any) string {
	b, _ := json.Marshal(v)
	t := string(b)
	if len(t) > 64 {
		t = t[:61] + "..."
	}
	return t
}

// Count adds n evaluations of a toggle that served a value.
func (s *Service) Count(project, env, toggle string, value any, reason string, n int64) {
	k := countKey{project, env, toggle, dayOf(s.d.Now()), variationText(value), reason}
	s.counts.mu.Lock()
	if len(s.counts.m) < 100000 { // a flood of distinct values cannot grow it without end
		s.counts.m[k] += n
	}
	s.counts.mu.Unlock()
}

// Flush writes the counts kept in memory to the database.
func (s *Service) Flush(ctx context.Context) error {
	_ = s.FlushKeyUse(ctx)
	s.counts.mu.Lock()
	pending := s.counts.m
	s.counts.m = map[countKey]int64{}
	s.counts.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	tx, err := s.d.DB.Begin(ctx)
	if err != nil {
		s.requeue(pending)
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for k, n := range pending {
		day, _ := time.Parse("2006-01-02", k.day)
		if _, err := tx.Exec(ctx, `INSERT INTO toggle_evals(org, project, key, environment, day, variation, reason, count) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(org, project, key, environment, day, variation, reason) DO UPDATE SET count = toggle_evals.count + excluded.count`,
			Org, k.project, k.toggle, k.env, day.Unix()/86400, k.variation, k.reason, n); err != nil {
			s.requeue(pending)
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		s.requeue(pending)
		return err
	}
	return nil
}

func (s *Service) requeue(m map[countKey]int64) {
	s.counts.mu.Lock()
	for k, n := range m {
		s.counts.m[k] += n
	}
	s.counts.mu.Unlock()
}

// Usage is what is known of where a toggle is evaluated.
type Usage struct {
	Environment   string           `json:"environment"`
	Evaluations   int64            `json:"evaluations"`
	LastEvaluated string           `json:"last_evaluated,omitempty"` // a UTC date
	Variations    map[string]int64 `json:"variations"`
}

// Usage returns, per environment, the evaluations of a toggle in the last
// days (flushed counts only; the newest 30 seconds may still be in memory).
func (s *Service) Usage(ctx context.Context, project, toggle string, days int) ([]Usage, error) {
	if days <= 0 || days > 400 {
		days = 30
	}
	since := s.d.Now().UTC().Unix()/86400 - int64(days)
	rows, err := s.d.DB.Query(ctx, `SELECT environment, day, variation, SUM(count) FROM toggle_evals WHERE org = ? AND project = ? AND key = ? AND day >= ?
		GROUP BY environment, day, variation ORDER BY environment, day`, Org, project, toggle, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*Usage{}
	var order []string
	for rows.Next() {
		var env, variation string
		var day, n int64
		if err := rows.Scan(&env, &day, &variation, &n); err != nil {
			return nil, err
		}
		u := by[env]
		if u == nil {
			u = &Usage{Environment: env, Variations: map[string]int64{}}
			by[env] = u
			order = append(order, env)
		}
		u.Evaluations += n
		u.Variations[variation] += n
		u.LastEvaluated = time.Unix(day*86400, 0).UTC().Format("2006-01-02")
	}
	out := []Usage{}
	for _, e := range order {
		out = append(out, *by[e])
	}
	return out, rows.Err()
}
