package engine

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// Insights are the charts of the Insights pages, worked out from what the
// product keeps in its own database. Who may see which Flight is decided by
// the caller: Projects lists exactly the Flights to count, and nothing outside
// the list is read into the result.

// InsightsQuery is what an Insights page asks.
type InsightsQuery struct {
	// Projects are the Flights to count; nil means every Flight. An empty,
	// non-nil list counts none (a person who may see nothing).
	Projects []string
	From, To time.Time
	Branch   string
	// Bucket is the length of a point of the series; 0 chooses from the range.
	Bucket time.Duration
}

// InsightPoint is one point of the run series.
type InsightPoint struct {
	At           int64   `json:"at"`
	Runs         int     `json:"runs"`
	Succeeded    int     `json:"succeeded"`
	Failed       int     `json:"failed"`
	SuccessRate  float64 `json:"success_rate"`
	DurationP50  float64 `json:"duration_p50_s"`
	DurationP95  float64 `json:"duration_p95_s"`
	QueueWaitP50 float64 `json:"queue_wait_p50_s"`
	QueueWaitP95 float64 `json:"queue_wait_p95_s"`
	TestsTotal   int64   `json:"tests_total"`
	TestsFailed  int64   `json:"tests_failed"`
	TestFailRate float64 `json:"test_failure_rate"`
}

// InsightStepRow is a step's typical duration, or how often it fails.
type InsightStepRow struct {
	Step        string  `json:"step"`
	Runs        int     `json:"runs"`
	Failures    int     `json:"failures"`
	FailureRate float64 `json:"failure_rate"`
	P50         float64 `json:"p50_s"`
	P95         float64 `json:"p95_s"`
}

// InsightsDelivery are the four delivery measures of the range.
type InsightsDelivery struct {
	Launches          int     `json:"launches"`
	Failures          int     `json:"failures"`
	FrequencyPerWeek  float64 `json:"frequency_per_week"`
	LeadTimeMedianSec float64 `json:"lead_time_median_s"`
	ChangeFailureRate float64 `json:"change_failure_rate"`
	RestoreMedianSec  float64 `json:"time_to_restore_median_s"`
	Unrestored        int     `json:"unrestored"`
	// Series are the same measures over time (Branch does not apply: deliveries are not of a branch).
	Series []InsightDeliveryPoint `json:"series"`
}

// InsightDeliveryPoint is one point of the delivery series.
type InsightDeliveryPoint struct {
	At                int64   `json:"at"`
	Launches          int     `json:"launches"`
	Failures          int     `json:"failures"`
	LeadTimeMedianSec float64 `json:"lead_time_median_s"`
	RestoreMedianSec  float64 `json:"time_to_restore_median_s"`
}

// Insights is the answer.
type Insights struct {
	From          int64             `json:"from"`
	To            int64             `json:"to"`
	BucketSeconds int64             `json:"bucket_seconds"`
	Branch        string            `json:"branch,omitempty"`
	Runs          int               `json:"runs"`
	Succeeded     int               `json:"succeeded"`
	Failed        int               `json:"failed"`
	Canceled      int               `json:"canceled"`
	SuccessRate   float64           `json:"success_rate"`
	DurationP50   float64           `json:"duration_p50_s"`
	DurationP95   float64           `json:"duration_p95_s"`
	QueueWaitP50  float64           `json:"queue_wait_p50_s"`
	QueueWaitP95  float64           `json:"queue_wait_p95_s"`
	Series        []InsightPoint    `json:"series"`
	SlowestSteps  []InsightStepRow  `json:"slowest_steps"`
	FlakiestSteps []InsightStepRow  `json:"flakiest_steps"`
	TestsTotal    int64             `json:"tests_total"`
	TestsFailed   int64             `json:"tests_failed"`
	TestsFlaky    int64             `json:"tests_flaky"`
	Delivery      *InsightsDelivery `json:"delivery,omitempty"`
	// Truncated says a bound was reached: the oldest rows of the range are counted, the rest are not.
	Truncated bool `json:"truncated,omitempty"`
}

const (
	insightRowLimit  = 50_000
	insightTopSteps  = 10
	insightMaxPoints = 400
)

// Insights works out the charts of a range.
func (e *Engine) Insights(q InsightsQuery) (*Insights, error) {
	if !q.To.After(q.From) || q.To.Sub(q.From) > 3*366*24*time.Hour {
		return nil, fmt.Errorf("%w: the period must end after it starts and be at most three years", ErrInvalid)
	}
	bucket := q.Bucket
	if bucket <= 0 {
		bucket = chooseBucket(q.To.Sub(q.From))
	}
	if int64(q.To.Sub(q.From)/bucket) > insightMaxPoints {
		return nil, fmt.Errorf("%w: that range and bucket make more than %d points", ErrInvalid, insightMaxPoints)
	}
	out := &Insights{From: q.From.UnixMilli(), To: q.To.UnixMilli(), BucketSeconds: int64(bucket / time.Second), Branch: q.Branch,
		Series: []InsightPoint{}, SlowestSteps: []InsightStepRow{}, FlakiestSteps: []InsightStepRow{}}
	if q.Projects != nil && len(q.Projects) == 0 {
		out.Series = emptySeries(q.From, q.To, bucket)
		return out, nil
	}
	only := ""
	set := map[string]bool(nil)
	if q.Projects != nil {
		set = map[string]bool{}
		for _, p := range q.Projects {
			set[p] = true
		}
		if len(q.Projects) == 1 {
			only = q.Projects[0]
		}
	}
	keep := func(p string) bool { return set == nil || set[p] }
	from, to := q.From.UnixMilli(), q.To.UnixMilli()
	runs, err := store.InsightRuns(e.db, only, q.Branch, from, to, insightRowLimit+1)
	if err != nil {
		return nil, err
	}
	steps, err := store.InsightSteps(e.db, only, q.Branch, from, to, insightRowLimit+1)
	if err != nil {
		return nil, err
	}
	var tests []store.InsightTests
	if e.knows(schemaDebrief) {
		if tests, err = store.InsightTestSummaries(e.db, only, q.Branch, from, to, insightRowLimit+1); err != nil {
			return nil, err
		}
	}
	if len(runs) > insightRowLimit || len(steps) > insightRowLimit || len(tests) > insightRowLimit {
		out.Truncated = true
	}
	computeInsights(out, runs, steps, tests, keep, q.From, bucket)
	if d, err := e.insightsDelivery(q, set); err == nil {
		out.Delivery = d
	}
	return out, nil
}

func (e *Engine) insightsDelivery(q InsightsQuery, set map[string]bool) (*InsightsDelivery, error) {
	project := ""
	if len(set) == 1 {
		for p := range set {
			project = p
		}
	}
	visible := func(p string) bool { return set == nil || set[p] }
	rep, err := e.BuildReleaseReport(project, q.From, q.To, visible)
	if err != nil {
		return nil, err
	}
	m := rep.Measures
	d := &InsightsDelivery{Launches: m.Launches, Failures: m.Failures, FrequencyPerWeek: m.FrequencyPerWeek, LeadTimeMedianSec: m.LeadTimeMedianSec,
		ChangeFailureRate: m.ChangeFailureRate, RestoreMedianSec: m.RestoreMedianSec, Unrestored: m.Unrestored, Series: []InsightDeliveryPoint{}}
	for _, p := range rep.Series {
		d.Series = append(d.Series, InsightDeliveryPoint{At: p.From.UnixMilli(), Launches: p.Launches, Failures: p.Failures,
			LeadTimeMedianSec: p.LeadTimeMedianSec, RestoreMedianSec: p.RestoreMedianSec})
	}
	return d, nil
}

func chooseBucket(span time.Duration) time.Duration {
	switch {
	case span <= 3*24*time.Hour:
		return time.Hour
	case span <= 60*24*time.Hour:
		return 24 * time.Hour
	case span <= 400*24*time.Hour:
		return 7 * 24 * time.Hour
	}
	return 30 * 24 * time.Hour
}

func emptySeries(from, to time.Time, bucket time.Duration) []InsightPoint {
	var s []InsightPoint
	for t := from; t.Before(to); t = t.Add(bucket) {
		s = append(s, InsightPoint{At: t.UnixMilli()})
	}
	return s
}

// computeInsights is the arithmetic, apart from the database, so it can be tested with plain rows.
func computeInsights(out *Insights, runs []store.InsightRun, steps []store.InsightStep, tests []store.InsightTests,
	keep func(string) bool, start time.Time, bucket time.Duration) {
	n := int(out.To-out.From)/int(bucket.Milliseconds()) + 1
	type acc struct {
		runs, ok, bad int
		dur, wait     []float64
		tTotal, tBad  int64
	}
	pts := make([]acc, n)
	idx := func(at int64) int {
		i := int((at - out.From) / bucket.Milliseconds())
		return min(max(i, 0), n-1)
	}
	var allDur, allWait []float64
	for _, r := range runs {
		if !keep(r.Project) {
			continue
		}
		p := &pts[idx(r.CreatedAt)]
		out.Runs++
		p.runs++
		switch RunStatus(r.Status) {
		case RunSucceeded:
			out.Succeeded++
			p.ok++
		case RunFailed:
			out.Failed++
			p.bad++
		case RunCanceled:
			out.Canceled++
		}
		if r.StartedAt > 0 && r.FinishedAt >= r.StartedAt && (RunStatus(r.Status) == RunSucceeded || RunStatus(r.Status) == RunFailed) {
			d := float64(r.FinishedAt-r.StartedAt) / 1000
			p.dur = append(p.dur, d)
			allDur = append(allDur, d)
		}
	}
	type stat struct {
		runs, fails int
		dur         []float64
	}
	byStep := map[string]*stat{}
	for _, s := range steps {
		if !keep(s.Project) {
			continue
		}
		if s.StartedAt > 0 && s.QueuedAt > 0 && s.StartedAt >= s.QueuedAt {
			w := float64(s.StartedAt-s.QueuedAt) / 1000
			pts[idx(s.RunCreatedAt)].wait = append(pts[idx(s.RunCreatedAt)].wait, w)
			allWait = append(allWait, w)
		}
		st := byStep[s.Step]
		if st == nil {
			st = &stat{}
			byStep[s.Step] = st
		}
		st.runs++
		if s.Status == string(StepFailed) {
			st.fails++
		}
		if s.StartedAt > 0 && s.FinishedAt >= s.StartedAt {
			st.dur = append(st.dur, float64(s.FinishedAt-s.StartedAt)/1000)
		}
	}
	for _, t := range tests {
		if !keep(t.Project) {
			continue
		}
		p := &pts[idx(t.At)]
		p.tTotal += t.Total
		p.tBad += t.Failed
		out.TestsTotal += t.Total
		out.TestsFailed += t.Failed
		out.TestsFlaky += t.Flaky
	}
	if finished := out.Succeeded + out.Failed; finished > 0 {
		out.SuccessRate = float64(out.Succeeded) / float64(finished)
	}
	out.DurationP50, out.DurationP95 = percentile(allDur, 0.5), percentile(allDur, 0.95)
	out.QueueWaitP50, out.QueueWaitP95 = percentile(allWait, 0.5), percentile(allWait, 0.95)
	for i, p := range pts {
		pt := InsightPoint{At: out.From + int64(i)*bucket.Milliseconds(), Runs: p.runs, Succeeded: p.ok, Failed: p.bad,
			DurationP50: percentile(p.dur, 0.5), DurationP95: percentile(p.dur, 0.95),
			QueueWaitP50: percentile(p.wait, 0.5), QueueWaitP95: percentile(p.wait, 0.95), TestsTotal: p.tTotal, TestsFailed: p.tBad}
		if p.ok+p.bad > 0 {
			pt.SuccessRate = float64(p.ok) / float64(p.ok+p.bad)
		}
		if p.tTotal > 0 {
			pt.TestFailRate = float64(p.tBad) / float64(p.tTotal)
		}
		out.Series = append(out.Series, pt)
	}
	rows := make([]InsightStepRow, 0, len(byStep))
	for name, s := range byStep {
		r := InsightStepRow{Step: name, Runs: s.runs, Failures: s.fails, P50: percentile(s.dur, 0.5), P95: percentile(s.dur, 0.95)}
		if s.runs > 0 {
			r.FailureRate = float64(s.fails) / float64(s.runs)
		}
		rows = append(rows, r)
	}
	slow := append([]InsightStepRow(nil), rows...)
	sort.Slice(slow, func(i, j int) bool {
		if slow[i].P95 != slow[j].P95 {
			return slow[i].P95 > slow[j].P95
		}
		return slow[i].Step < slow[j].Step
	})
	out.SlowestSteps = top(slow, insightTopSteps, func(r InsightStepRow) bool { return r.P95 > 0 })
	flaky := append([]InsightStepRow(nil), rows...)
	sort.Slice(flaky, func(i, j int) bool {
		if flaky[i].FailureRate != flaky[j].FailureRate {
			return flaky[i].FailureRate > flaky[j].FailureRate
		}
		if flaky[i].Runs != flaky[j].Runs {
			return flaky[i].Runs > flaky[j].Runs
		}
		return flaky[i].Step < flaky[j].Step
	})
	out.FlakiestSteps = top(flaky, insightTopSteps, func(r InsightStepRow) bool { return r.Failures > 0 })
	_ = start
}

func top(rows []InsightStepRow, n int, ok func(InsightStepRow) bool) []InsightStepRow {
	out := []InsightStepRow{}
	for _, r := range rows {
		if ok(r) && len(out) < n {
			out = append(out, r)
		}
	}
	return out
}

// percentile is the nearest-rank percentile of values (0 when there are none).
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}
