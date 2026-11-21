package engine

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/debrief"
)

// eachRow runs a query and calls scan for every row, closing the rows and
// returning the first error, including one from the iteration.
func (e *Engine) eachRow(q string, args []any, scan func(*sql.Rows) error) error {
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// historyWindow is how many of a project's latest run numbers a test's
// history is read from (every branch's runs count toward the number).
const historyWindow = 200

// projectLatest is the project's highest run number.
func (e *Engine) projectLatest(project string) (int, error) {
	var n int
	err := e.db.QueryRow(`SELECT COALESCE(MAX(number), 0) FROM runs WHERE project = ?`, project).Scan(&n)
	return n, err
}

// slot is one test's results in one step of one run, as its attempts come.
type slot struct {
	key, run, step string
	number         int
	final          string
	failedEarlier  bool
	toolFlaky      bool
	sha, ref       string
	at             int64
	lastAttempt    int
}

// add takes the next attempt's result (attempts arrive in order).
func (s *slot) add(status string, attempt int, toolFlaky bool) {
	bad := status == "failed" || status == "errored"
	if s.final == "failed" && status == "passed" {
		s.failedEarlier = true
	}
	switch {
	case bad:
		s.final = "failed"
	case status == "passed":
		s.final = "passed"
	case status == "skipped" && s.final == "":
		s.final = "skipped"
	}
	s.toolFlaky = s.toolFlaky || toolFlaky
	s.lastAttempt = attempt
}

// observations reads the history of some tests in a project, oldest first,
// as one observation per run: the result of the last attempt of its step,
// "failed" if any step failed it, and Retried when it passed only after
// failing in an earlier attempt of the same step or the test tool ran it
// again. refs, when not nil, limits the history to those refs (what a
// reader of a restricted run may see).
func (e *Engine) observations(project string, keys []string, refs []string, since int) (map[string][]debrief.Observation, error) {
	out := map[string][]debrief.Observation{}
	if !e.knows(schemaTests) || len(keys) == 0 {
		return out, nil
	}
	slots := map[string]*slot{}
	var order []string
	for start := 0; start < len(keys); start += 400 {
		chunk := keys[start:min(start+400, len(keys))]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		q := `SELECT tc.test_key, tc.run_id, tc.run_number, tc.step_id, tc.attempt, tc.status, tc.flaky, r.sha, r.ref, r.created_at
			FROM test_cases tc JOIN runs r ON r.id = tc.run_id
			WHERE tc.project = ? AND tc.run_number > ? AND tc.test_key IN (` + marks + `)`
		args := []any{project, since}
		for _, k := range chunk {
			args = append(args, k)
		}
		if refs != nil {
			if len(refs) == 0 {
				return out, nil
			}
			q += ` AND r.ref IN (` + strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",") + `)`
			for _, r := range refs {
				args = append(args, r)
			}
		}
		q += ` ORDER BY tc.test_key, tc.run_number, tc.step_id, tc.attempt`
		err := e.eachRow(q, args, func(rows *sql.Rows) error {
			var key, run, step, status, sha, ref string
			var number, attempt, flaky int
			var at int64
			if err := rows.Scan(&key, &run, &number, &step, &attempt, &status, &flaky, &sha, &ref, &at); err != nil {
				return err
			}
			id := key + "\x00" + run + "\x00" + step
			s := slots[id]
			if s == nil {
				s = &slot{key: key, run: run, step: step, number: number, sha: sha, ref: ref, at: at}
				slots[id] = s
				order = append(order, id)
			}
			s.add(status, attempt, flaky != 0)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// One observation per run: a failure in any step is a failure.
	type runKey struct{ key, run string }
	merged := map[runKey]*debrief.Observation{}
	var runOrder []runKey
	for _, id := range order {
		s := slots[id]
		rk := runKey{s.key, s.run}
		o := merged[rk]
		if o == nil {
			o = &debrief.Observation{Run: s.run, Number: s.number, SHA: s.sha, Ref: s.ref, At: s.at, Status: s.final}
			merged[rk] = o
			runOrder = append(runOrder, rk)
		} else if s.final == "failed" || (o.Status == "skipped" && s.final != "") {
			o.Status = s.final
		}
		if s.final == "passed" && (s.failedEarlier || s.toolFlaky) {
			o.Retried = true
		}
	}
	for _, rk := range runOrder {
		o := merged[rk]
		out[rk.key] = append(out[rk.key], *o)
	}
	for k := range out {
		sort.SliceStable(out[k], func(i, j int) bool { return out[k][i].Number < out[k][j].Number })
	}
	return out, nil
}

// FlakyTest is a test the history shows as flaky.
type FlakyTest struct {
	Key        string   `json:"key"`
	Suite      string   `json:"suite"`
	Name       string   `json:"name"`
	File       string   `json:"file,omitempty"`
	Variant    string   `json:"variant,omitempty"`
	Score      int      `json:"score"`
	Reason     string   `json:"reason"`
	Commits    int      `json:"commits"`
	SameCommit int      `json:"same_commit"`
	Blips      int      `json:"blips"`
	Branches   []string `json:"branches"`
	FirstSeen  int64    `json:"first_seen"`
	LastRun    int      `json:"last_run"`
	LastStatus string   `json:"last_status"`
	// History is the last results, oldest first, one word each (passed,
	// failed, retried), for a strip.
	History []string `json:"history"`
	// Suggest is a quarantine entry the owner may copy into the
	// repository's quarantine file; nothing writes it.
	Suggest string `json:"suggest"`
}

// FlakyList is a project's flaky tests.
type FlakyList struct {
	Project string      `json:"project"`
	Window  int         `json:"window"` // run numbers looked at
	Tests   []FlakyTest `json:"tests"`
	Rules   string      `json:"rules"`
	// Considered counts the tests that both passed and failed in the
	// window, of which Tests are those the rules call flaky.
	Considered int `json:"considered"`
}

// FlakyRules says in words how flakiness is decided, shown beside the list.
const FlakyRules = "A test is flaky when the same commit both passed and failed it (two runs, or a pass on retry), or when it failed on single commits between passing ones at least twice. A break that lasts, or a failure that was fixed, is not flaky. The score is the share of the commits it ran on that showed this."

// ProjectFlaky lists a project's flaky tests: those that both passed and
// failed within the last historyWindow run numbers and that the rules in
// debrief.Score call flaky, most flaky first. refs limits the history (nil:
// all).
func (e *Engine) ProjectFlaky(project string, refs []string, limit int) (*FlakyList, error) {
	out := &FlakyList{Project: project, Tests: []FlakyTest{}, Rules: FlakyRules}
	if !e.knows(schemaTests) {
		return out, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	latest, err := e.projectLatest(project)
	if err != nil {
		return nil, err
	}
	since := max(latest-historyWindow, 0)
	out.Window = latest - since
	q := `SELECT tc.test_key FROM test_cases tc JOIN runs r ON r.id = tc.run_id WHERE tc.project = ? AND tc.run_number > ?`
	args := []any{project, since}
	if refs != nil {
		if len(refs) == 0 {
			return out, nil
		}
		q += ` AND r.ref IN (` + strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",") + `)`
		for _, r := range refs {
			args = append(args, r)
		}
	}
	q += ` GROUP BY tc.test_key HAVING SUM(CASE WHEN tc.status IN ('failed','errored') THEN 1 ELSE 0 END) > 0
		AND SUM(CASE WHEN tc.status = 'passed' THEN 1 ELSE 0 END) > 0 LIMIT 3000`
	var keys []string
	err = e.eachRow(q, args, func(rows *sql.Rows) error {
		var k string
		if err := rows.Scan(&k); err != nil {
			return err
		}
		keys = append(keys, k)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.Considered = len(keys)
	obs, err := e.observations(project, keys, refs, since)
	if err != nil {
		return nil, err
	}
	var flaky []string
	verdicts := map[string]debrief.FlakeVerdict{}
	for _, k := range keys {
		if v := debrief.Score(obs[k]); v.Flaky {
			verdicts[k] = v
			flaky = append(flaky, k)
		}
	}
	sort.Slice(flaky, func(i, j int) bool {
		a, b := verdicts[flaky[i]], verdicts[flaky[j]]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return flaky[i] < flaky[j]
	})
	if len(flaky) > limit {
		flaky = flaky[:limit]
	}
	names, err := e.testNames(project, flaky)
	if err != nil {
		return nil, err
	}
	for _, k := range flaky {
		v, n, h := verdicts[k], names[k], obs[k]
		ft := FlakyTest{Key: k, Suite: n.Suite, Name: n.Name, File: n.File, Variant: n.Variant, Score: v.Score, Reason: v.Reason,
			Commits: v.Commits, SameCommit: v.SameCommit, Blips: v.Blips, Branches: shortRefs(v.Branches), FirstSeen: v.FirstSeen}
		if len(h) > 0 {
			ft.LastRun, ft.LastStatus = h[len(h)-1].Number, h[len(h)-1].Status
		}
		for _, o := range h[max(len(h)-30, 0):] {
			w := o.Status
			if o.Retried {
				w = "retried"
			}
			ft.History = append(ft.History, w)
		}
		ft.Suggest = suggestQuarantine(n.Suite, n.Name)
		out.Tests = append(out.Tests, ft)
	}
	return out, nil
}

func shortRefs(refs []string) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = strings.TrimPrefix(r, "refs/heads/")
	}
	return out
}

// suggestQuarantine is the entry a person may add to the quarantine file.
func suggestQuarantine(suite, name string) string {
	test := name
	if suite != "" {
		test = suite + "::" + name
	}
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(test)
	return fmt.Sprintf("- test: \"%s\"\n  owner: your-name\n  expires: YYYY-MM-DD   # at most a year ahead\n  reason: flaky; see the Debrief", q)
}

type testName struct{ Suite, Name, File, Variant string }

// testNames finds what tests are called, from their statistics or, failing
// that, from a stored result.
func (e *Engine) testNames(project string, keys []string) (map[string]testName, error) {
	out := map[string]testName{}
	for start := 0; start < len(keys); start += 400 {
		chunk := keys[start:min(start+400, len(keys))]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := []any{project}
		for _, k := range chunk {
			args = append(args, k)
		}
		q := `SELECT test_key, MAX(suite), MAX(name), MAX(file), MAX(variant) FROM test_stats WHERE project = ? AND test_key IN (` + marks + `) GROUP BY test_key`
		if !e.knows(schemaDebrief) {
			q = `SELECT test_key, MAX(suite), MAX(name), MAX(file), '' FROM test_cases WHERE project = ? AND test_key IN (` + marks + `) GROUP BY test_key`
		}
		err := e.eachRow(q, args, func(rows *sql.Rows) error {
			var k string
			var n testName
			if err := rows.Scan(&k, &n.Suite, &n.Name, &n.File, &n.Variant); err != nil {
				return err
			}
			out[k] = n
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	var missing []string
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			missing = append(missing, k)
		}
	}
	for _, k := range missing {
		var n testName
		if err := e.db.QueryRow(`SELECT suite, name, file FROM test_cases WHERE project = ? AND test_key = ? ORDER BY id DESC LIMIT 1`, project, k).Scan(&n.Suite, &n.Name, &n.File); err == nil {
			out[k] = n
		}
	}
	return out, nil
}

// Timing is a test's typical duration (see debrief.Timing).
type TimingList struct {
	Project string           `json:"project"`
	Tests   []debrief.Timing `json:"tests"`
	// Truncated says there are more tests than were returned.
	Truncated bool `json:"truncated,omitempty"`
}

// TestTimings returns the typical duration of a project's tests, slowest
// first, from the statistics that outlive pruned results; tests not seen in
// the last 30 days are left out. limit is at most 100000.
func (e *Engine) TestTimings(project string, limit int) (*TimingList, error) {
	out := &TimingList{Project: project, Tests: []debrief.Timing{}}
	if !e.knows(schemaDebrief) {
		return out, nil
	}
	if limit <= 0 || limit > 100_000 {
		limit = 100_000
	}
	cutoff := e.now().UnixMilli() - 30*24*3600*1000
	rows, err := e.db.Query(`SELECT test_key, suite, name, file, avg_ms, runs, max_ms FROM test_stats
		WHERE project = ? AND last_at >= ? ORDER BY avg_ms DESC, test_key LIMIT ?`, project, cutoff, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t debrief.Timing
		if err := rows.Scan(&t.Key, &t.Suite, &t.Name, &t.File, &t.AvgMs, &t.Samples, &t.MaxMs); err != nil {
			return nil, err
		}
		out.Tests = append(out.Tests, t)
	}
	if len(out.Tests) > limit {
		out.Tests, out.Truncated = out.Tests[:limit], true
	}
	return out, rows.Err()
}

// TestShards is a balanced split of a project's tests for n shards.
type TestShards struct {
	Project string          `json:"project"`
	Count   int             `json:"count"`
	Shards  []debrief.Shard `json:"shards"`
	Tests   int             `json:"tests"`
	// Unknown counts the tests with no history, given the average.
	Unknown   int  `json:"unknown"`
	Truncated bool `json:"truncated,omitempty"`
	// MaxMs and MinMs are the longest and shortest shard, to judge balance.
	MaxMs int64 `json:"max_ms"`
	MinMs int64 `json:"min_ms"`
}

// SplitTests divides a project's timed tests among n shards (1 to 256) by
// duration, longest first, each to the lightest shard so far; the same
// history gives the same split. A shard's tests are named suite::name when
// the test has a suite, so a pipeline can pass them to its test tool.
func (e *Engine) SplitTests(project string, n int) (*TestShards, error) {
	if n < 1 || n > 256 {
		return nil, fmt.Errorf("%w: shards must be from 1 to 256", ErrInvalid)
	}
	tl, err := e.TestTimings(project, 0)
	if err != nil {
		return nil, err
	}
	nameOf := func(t debrief.Timing) string {
		if t.Suite != "" {
			return t.Suite + "::" + t.Name
		}
		return t.Name
	}
	shards := debrief.Split(tl.Tests, n, nameOf)
	out := &TestShards{Project: project, Count: n, Shards: shards, Tests: len(tl.Tests), Truncated: tl.Truncated}
	for _, t := range tl.Tests {
		if t.Samples == 0 {
			out.Unknown++
		}
	}
	out.MinMs = shards[0].TotalMs
	for _, s := range shards {
		out.MaxMs, out.MinMs = max(out.MaxMs, s.TotalMs), min(out.MinMs, s.TotalMs)
	}
	return out, nil
}
