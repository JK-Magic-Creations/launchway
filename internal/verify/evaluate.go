package verify

import (
	"fmt"
	"sort"
	"strings"
)

// noData is an interval check's outcome when the interval has no usable
// readings for the metric.
const noData = "no_data"

// Evaluate judges the intervals reported so far. It is a pure function:
// every server folds the same spec, baseline and intervals into the same
// verdict, byte for byte, whatever order the intervals arrive in.
//
// Intervals may have gaps (an index not reported yet); those present are
// judged in index order. A second report of an index is ignored (the first
// in the slice wins), and so are indexes outside the window.
//
// A metric fails as soon as enough consecutive intervals fail (see
// MetricSpec.Failures) or a fail-fast bound is breached; a log query fails
// as soon as a failing cluster is seen. Either makes the verdict failed
// and final at once. Otherwise nothing is decided until every interval of
// the window is in; then a check with too little data is inconclusive,
// never a silent pass.
func Evaluate(spec Spec, base Baseline, intervals []Interval) Verdict {
	n := windowOf(spec)
	judged := judgedIntervals(intervals, n)
	allIn := len(judged) == n
	v := Verdict{Intervals: len(judged)}
	for _, m := range spec.Metrics {
		v.Metrics = append(v.Metrics, judgeMetric(spec, n, m, base, judged, allIn))
	}
	for _, l := range spec.Logs {
		v.Logs = append(v.Logs, judgeLog(spec, l, base, judged, allIn))
	}
	decide(&v, n, allIn)
	return v
}

// windowOf is how many intervals the window has; a spec without any is
// treated as a window of one.
func windowOf(spec Spec) int {
	return max(spec.Intervals, 1)
}

// judgedIntervals keeps the first report of each index inside the window,
// ordered by index.
func judgedIntervals(intervals []Interval, n int) []Interval {
	seen := make(map[int]bool, len(intervals))
	out := make([]Interval, 0, min(len(intervals), n))
	for _, iv := range intervals {
		if iv.Index < 0 || iv.Index >= n || seen[iv.Index] {
			continue
		}
		seen[iv.Index] = true
		out = append(out, iv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func findMetric(ms []MetricData, name string) *MetricData {
	for i := range ms {
		if ms[i].Name == name {
			return &ms[i]
		}
	}
	return nil
}

// metricJudge carries what judging one metric needs and learns.
type metricJudge struct {
	m            MetricSpec
	sens, dir    string
	k, minEffect float64
	minPoints    int

	canary, stable, base acc
	baseReady            bool

	checked       bool // some check could run in some interval
	withData      int
	stableMissing int
	lastError     string

	// worst deviation the wrong way, per comparison, and whether a
	// deviation beyond k was let through because the difference was tiny
	worstStable, worstBase *float64
	tinyEffect             bool
}

// judgeMetric judges one metric over the intervals so far.
func judgeMetric(spec Spec, n int, m MetricSpec, base Baseline, judged []Interval, allIn bool) MetricVerdict {
	j := &metricJudge{m: m, sens: sensitivityOf(spec.Sensitivity), dir: directionOf(m.Direction), minPoints: max(m.MinPoints, 1)}
	j.k, j.minEffect = Thresholds(j.sens)
	failures := m.Failures
	if failures <= 0 {
		failures = 2
		if n == 1 {
			failures = 1
		}
	}
	minIntervals := m.MinIntervals
	if minIntervals <= 0 {
		minIntervals = max(1, (n+1)/2)
	}
	if m.Baseline {
		for _, x := range base.Metrics[m.Name] {
			if finite(x) {
				j.base.add(x)
			}
		}
		j.baseReady = j.base.n >= minBaselineValues
	}

	mv := MetricVerdict{Name: m.Name}
	var (
		streak        []int // indexes of the current run of failing intervals
		prev          = -1
		failed, fast  bool
		failWhy       string
		failAt        []int
		lastFailWhy   string
		lastFailIndex []int
	)
	for _, iv := range judged {
		// A missing index breaks a run: nothing is known about it. An
		// interval without data neither extends nor breaks it.
		if prev >= 0 && iv.Index != prev+1 {
			streak = nil
		}
		prev = iv.Index
		chk, why, breach := j.interval(iv)
		mv.Intervals = append(mv.Intervals, chk)
		switch chk.Outcome {
		case Failed:
			streak = append(streak, iv.Index)
			lastFailWhy, lastFailIndex = why, []int{iv.Index}
			if failed {
				continue
			}
			if breach {
				failed, fast, failWhy, failAt = true, true, why, []int{iv.Index}
			} else if len(streak) >= failures {
				failed, failWhy = true, why
				failAt = append([]int(nil), streak[len(streak)-failures:]...)
			}
		case Passed:
			streak = nil
		}
	}
	mv.Canary, mv.Stable, mv.Baseline = j.canary.stats(), j.stable.stats(), j.base.stats()

	switch {
	case failed:
		mv.Outcome = Failed
		mv.Reason = fmt.Sprintf("%s: %s in %s", m.Name, failWhy, intervalWords(failAt))
		if fast {
			mv.Reason += "; that is a fail-fast bound, so the verification stops at once"
		}
	case !allIn:
		mv.Outcome = Running
		mv.Reason = fmt.Sprintf("%s: %d of %d intervals judged, %d with data; nothing has failed so far", m.Name, len(judged), n, j.withData)
		if lastFailWhy != "" {
			mv.Reason += fmt.Sprintf(" (%s in %s, not yet %d in a row)", lastFailWhy, intervalWords(lastFailIndex), failures)
		}
	case j.withData < minIntervals:
		mv.Outcome = Inconclusive
		mv.Reason = fmt.Sprintf("%s: only %d of %d intervals had data, %d needed; too little data is inconclusive, never a pass", m.Name, j.withData, n, minIntervals)
		if j.lastError != "" {
			mv.Reason += "; last error: " + j.lastError
		}
	case !j.checked:
		mv.Outcome = Inconclusive
		mv.Reason = m.Name + ": nothing to compare with: " + strings.Join(j.missing(), "; ")
	default:
		mv.Outcome = Passed
		mv.Reason = j.passReason(n)
	}
	return mv
}

// interval judges the metric in one interval. It returns the check, the
// first failure in words, and whether a fail-fast bound was breached.
func (j *metricJudge) interval(iv Interval) (IntervalCheck, string, bool) {
	chk := IntervalCheck{Index: iv.Index}
	md := findMetric(iv.Metrics, j.m.Name)
	var pts []float64
	switch {
	case md == nil:
		chk.Outcome, chk.Reason = noData, "the report has no readings for this metric"
	case md.Error != "":
		j.lastError = clip(md.Error, MaxErrorBytes)
		chk.Outcome, chk.Reason = noData, "could not read the metric: "+j.lastError
	default:
		pts = finitePoints(md.Canary)
		if len(pts) < j.minPoints {
			chk.Outcome = noData
			chk.Reason = fmt.Sprintf("%s, %d needed", plural(len(pts), "usable reading", "usable readings"), j.minPoints)
		}
	}
	if chk.Outcome == noData {
		return chk, "", false
	}
	j.withData++
	for _, x := range pts {
		j.canary.add(x)
	}
	value := meanOf(pts)
	chk.Value = value
	m := j.m
	var fails, notes []string
	breach := false

	// Fail-fast bounds, then fixed bounds.
	if m.FailFastMax != nil || m.FailFastMin != nil {
		j.checked = true
	}
	if m.FailFastMax != nil && value > *m.FailFastMax {
		fails, breach = append(fails, fmt.Sprintf("%s is above the fail-fast maximum %s", fnum(value), fnum(*m.FailFastMax))), true
	}
	if m.FailFastMin != nil && value < *m.FailFastMin {
		fails, breach = append(fails, fmt.Sprintf("%s is below the fail-fast minimum %s", fnum(value), fnum(*m.FailFastMin))), true
	}
	if m.Max != nil || m.Min != nil {
		j.checked = true
	}
	if m.Max != nil && value > *m.Max {
		fails = append(fails, fmt.Sprintf("%s is above the maximum %s", fnum(value), fnum(*m.Max)))
	}
	if m.Min != nil && value < *m.Min {
		fails = append(fails, fmt.Sprintf("%s is below the minimum %s", fnum(value), fnum(*m.Min)))
	}

	// The baseline: earlier passed verifications' interval means.
	if m.Baseline && j.baseReady {
		j.checked = true
		z, large := deviation(value, j.base.mean, j.base.sd(), j.minEffect)
		chk.BaselineZ = &z
		j.worstBase = j.worse(j.worstBase, z, large)
		if large && bad(j.dir, z, j.k) {
			fails = append(fails, fmt.Sprintf("%s versus baseline %s, %s (%s sensitivity needs %g)", fnum(value), fnum(j.base.mean), zWords(z), j.sens, j.k))
		}
	}

	// The stable version in the same interval, against the spread of all
	// stable readings so far.
	if m.Stable {
		sp := finitePoints(md.Stable)
		if len(sp) >= j.minPoints {
			j.checked = true
			for _, x := range sp {
				j.stable.add(x)
			}
			sm := meanOf(sp)
			z, large := deviation(value, sm, j.stable.sd(), j.minEffect)
			chk.StableZ = &z
			j.worstStable = j.worse(j.worstStable, z, large)
			if large && bad(j.dir, z, j.k) {
				fails = append(fails, fmt.Sprintf("%s versus stable %s, %s (%s sensitivity needs %g)", fnum(value), fnum(sm), zWords(z), j.sens, j.k))
			}
		} else {
			j.stableMissing++
			notes = append(notes, "no stable readings in this interval, so the stable comparison was skipped")
		}
	}

	if md.Note != "" {
		if len(fails) > 0 {
			fails[0] += " (" + clip(md.Note, MaxErrorBytes) + ")"
		} else {
			notes = append(notes, clip(md.Note, MaxErrorBytes))
		}
	}
	if len(fails) > 0 {
		chk.Outcome = Failed
		chk.Reason = strings.Join(append(fails, notes...), "; ")
		return chk, fails[0], breach
	}
	chk.Outcome = Passed
	chk.Reason = strings.Join(notes, "; ")
	return chk, "", false
}

// worse keeps the deviation that goes furthest the wrong way, and notes a
// deviation beyond k that did not count because the difference was tiny.
func (j *metricJudge) worse(cur *float64, z float64, large bool) *float64 {
	if !large && bad(j.dir, z, j.k) {
		j.tinyEffect = true
	}
	if cur == nil || badness(j.dir, z) > badness(j.dir, *cur) {
		return &z
	}
	return cur
}

// missing says why no check could run.
func (j *metricJudge) missing() []string {
	m := j.m
	var why []string
	if m.Baseline && !j.baseReady {
		why = append(why, fmt.Sprintf("no baseline yet: %s, %d needed", plural(j.base.n, "earlier reading", "earlier readings"), minBaselineValues))
	}
	if m.Stable {
		why = append(why, "no stable readings in any interval")
	}
	if len(why) == 0 {
		why = append(why, "the metric has no bounds and no stable or baseline comparison")
	}
	return why
}

// passReason says why a metric passed, with the worst deviations seen.
func (j *metricJudge) passReason(n int) string {
	m := j.m
	parts := []string{fmt.Sprintf("mean %s over %d of %d intervals", fnum(j.canary.mean), j.withData, n)}
	var dev []string
	if j.worstBase != nil {
		dev = append(dev, "at worst "+zWords(*j.worstBase)+" the baseline "+fnum(j.base.mean))
	}
	if j.worstStable != nil {
		dev = append(dev, "at worst "+zWords(*j.worstStable)+" the stable version")
	}
	if len(dev) > 0 {
		parts = append(parts, fmt.Sprintf("%s (%s sensitivity needs %g)", strings.Join(dev, ", "), j.sens, j.k))
	}
	if j.tinyEffect {
		parts = append(parts, fmt.Sprintf("larger deviations were differences under %g%% of the reference, too small to count", j.minEffect*100))
	}
	if m.Max != nil || m.Min != nil || m.FailFastMax != nil || m.FailFastMin != nil {
		parts = append(parts, "within its bounds")
	}
	if m.Baseline && !j.baseReady {
		parts = append(parts, fmt.Sprintf("baseline comparison skipped: no baseline yet: %s, %d needed", plural(j.base.n, "earlier reading", "earlier readings"), minBaselineValues))
	}
	if j.stableMissing > 0 {
		parts = append(parts, fmt.Sprintf("stable comparison skipped in %s without stable readings", plural(j.stableMissing, "interval", "intervals")))
	}
	return m.Name + ": " + strings.Join(parts, "; ")
}

// decide sets the overall outcome and summary from the metric and log
// verdicts.
func decide(v *Verdict, n int, allIn bool) {
	if len(v.Metrics) == 0 && len(v.Logs) == 0 {
		v.Outcome, v.Final = Inconclusive, true
		v.Summary = "Nothing to verify: the step has no metrics and no log queries."
		return
	}
	var failed, inconclusive []string
	for _, m := range v.Metrics {
		switch m.Outcome {
		case Failed:
			failed = append(failed, m.Reason)
		case Inconclusive:
			inconclusive = append(inconclusive, m.Reason)
		}
	}
	for _, l := range v.Logs {
		switch l.Outcome {
		case Failed:
			failed = append(failed, l.Reason)
		case Inconclusive:
			inconclusive = append(inconclusive, l.Reason)
		}
	}
	switch {
	case len(failed) > 0:
		v.Outcome, v.Final = Failed, true
		v.Summary = sentence(failed[0])
		if len(failed) > 1 {
			v.Summary += fmt.Sprintf(" %s also failed.", plural(len(failed)-1, "other check", "other checks"))
		}
	case !allIn:
		v.Outcome = Running
		v.Summary = fmt.Sprintf("%d of %d intervals judged; nothing has failed so far.", v.Intervals, n)
	case len(inconclusive) > 0:
		v.Outcome, v.Final = Inconclusive, true
		v.Summary = "Nothing failed, but the result is inconclusive: " + sentence(inconclusive[0])
		if len(inconclusive) > 1 {
			v.Summary += fmt.Sprintf(" %s also inconclusive.", plural(len(inconclusive)-1, "other check is", "other checks are"))
		}
	default:
		v.Outcome, v.Final = Passed, true
		v.Summary = passSummary(v, n)
	}
}

func passSummary(v *Verdict, n int) string {
	over := " over " + plural(n, "interval", "intervals") + "."
	switch {
	case len(v.Metrics)+len(v.Logs) == 1 && len(v.Metrics) == 1:
		return "Metric " + v.Metrics[0].Name + " passed" + over
	case len(v.Metrics)+len(v.Logs) == 1:
		return "Log query " + v.Logs[0].Name + " passed" + over
	}
	var what []string
	if len(v.Metrics) > 0 {
		what = append(what, plural(len(v.Metrics), "metric", "metrics"))
	}
	if len(v.Logs) > 0 {
		what = append(what, plural(len(v.Logs), "log query", "log queries"))
	}
	return "All " + strings.Join(what, " and ") + " passed" + over
}

// sentence ends s with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}
