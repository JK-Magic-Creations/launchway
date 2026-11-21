package verify

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// pts makes points of the given values, a second apart.
func pts(vs ...float64) []Point {
	out := make([]Point, len(vs))
	for i, v := range vs {
		out[i] = Point{T: now + int64(i)*1000, V: v}
	}
	return out
}

// window makes n intervals with one metric whose canary (and stable, when
// given) readings come from the functions, by interval index.
func window(n int, name string, canary, stable func(i int) []Point) []Interval {
	var out []Interval
	for i := range n {
		md := MetricData{Name: name, Canary: canary(i)}
		if stable != nil {
			md.Stable = stable(i)
		}
		out = append(out, Interval{Index: i, Start: now + int64(i)*minute, End: now + int64(i+1)*minute, Metrics: []MetricData{md}})
	}
	return out
}

// baselineOf is a baseline with the given values for one metric.
func baselineOf(name string, vs ...float64) Baseline {
	return Baseline{Metrics: map[string][]float64{name: vs}}
}

// around makes n baseline values alternating mean-d and mean+d.
func around(n int, mean, d float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = mean - d
		if i%2 == 1 {
			out[i] = mean + d
		}
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("verdict is not valid JSON (a NaN or Inf?): %v", err)
	}
	return string(b)
}

func TestThresholds(t *testing.T) {
	cases := []struct {
		sens           string
		k, effect, sim float64
		factor         float64
	}{
		{High, 2, 0.05, 0.9, 2},
		{Medium, 3, 0.10, 0.75, 3},
		{Low, 4.5, 0.25, 0.6, 5},
		{"", 3, 0.10, 0.75, 3},
		{"extreme", 3, 0.10, 0.75, 3},
	}
	for _, c := range cases {
		k, e := Thresholds(c.sens)
		if k != c.k || e != c.effect || MatchThreshold(c.sens) != c.sim || RateFactor(c.sens) != c.factor {
			t.Errorf("%q: got k %g, effect %g, match %g, factor %g", c.sens, k, e, MatchThreshold(c.sens), RateFactor(c.sens))
		}
	}
}

// (a) A noisy healthy service passes against a baseline of three runs.
func TestHealthyNoisyServicePasses(t *testing.T) {
	sc, base := loadScenario(t, "healthy-error-rate")
	if got := len(base.Metrics["error-rate"]); got != 30 {
		t.Fatalf("baseline has %d error-rate values, want 30 (10 intervals of 3 runs)", got)
	}
	v := Evaluate(sc.Spec, base, sc.Intervals)
	if v.Outcome != Passed || !v.Final {
		t.Fatalf("outcome %s (final %v): %s\n%s", v.Outcome, v.Final, v.Summary, mustJSON(t, v.Metrics))
	}
	if v.Summary != "All 2 metrics passed over 10 intervals." {
		t.Errorf("summary %q", v.Summary)
	}
	m := v.Metrics[0]
	if m.Canary.N != 60 || m.Stable.N != 60 || m.Baseline.N != 30 {
		t.Errorf("stats counts canary %d stable %d baseline %d", m.Canary.N, m.Stable.N, m.Baseline.N)
	}
	if !strings.Contains(m.Reason, "standard deviations") || !strings.Contains(m.Reason, "medium sensitivity needs 3") {
		t.Errorf("pass reason does not give the deviations: %s", m.Reason)
	}
	for _, c := range m.Intervals {
		if c.StableZ == nil || c.BaselineZ == nil {
			t.Fatalf("interval %d lacks a z: %+v", c.Index, c)
		}
	}
}

// (b) A daily cycle passes when the baseline is from the same time of day,
// and fails (a documented limit) when it is from twelve hours earlier.
func TestDiurnalLatency(t *testing.T) {
	sc, base := loadScenario(t, "diurnal-latency")
	if v := Evaluate(sc.Spec, base, sc.Intervals); v.Outcome != Passed {
		t.Errorf("same phase: %s: %s", v.Outcome, v.Summary)
	}
	sc, base = loadScenario(t, "diurnal-latency-opposite-phase")
	v := Evaluate(sc.Spec, base, sc.Intervals)
	if v.Outcome != Failed || !strings.Contains(v.Summary, "versus baseline") {
		t.Errorf("opposite phase should fail against the baseline: %s: %s", v.Outcome, v.Summary)
	}
}

func TestMetricOutcomes(t *testing.T) {
	const name = "m"
	flat := func(v float64) func(int) []Point { return func(int) []Point { return pts(v, v, v) } }
	cases := []struct {
		name    string
		spec    MetricSpec
		sens    string
		n       int // intervals in the window
		base    Baseline
		canary  func(i int) []Point
		stable  func(i int) []Point
		outcome string
		final   bool
		reason  string // a piece the metric's reason must contain
	}{
		// (c) Sparse: three of ten intervals have a reading.
		{"sparse series", MetricSpec{Name: name, Baseline: true}, Medium, 10, baselineOf(name, around(20, 100, 5)...),
			func(i int) []Point {
				if i%4 == 0 {
					return pts(100)
				}
				return nil
			}, nil, Inconclusive, true, "only 3 of 10 intervals had data, 5 needed"},
		{"too few points per interval", MetricSpec{Name: name, Baseline: true, MinPoints: 3}, Medium, 4, baselineOf(name, around(20, 100, 5)...),
			func(int) []Point { return pts(100, 101) }, nil, Inconclusive, true, "only 0 of 4 intervals had data"},
		{"readings that are not numbers are dropped", MetricSpec{Name: name, Baseline: true, MinPoints: 2}, Medium, 4, baselineOf(name, around(20, 100, 5)...),
			func(int) []Point { return pts(100, nan(), inf()) }, nil, Inconclusive, true, "only 0 of 4 intervals had data"},

		// (d) Flat zero everywhere passes; a tiny difference passes; one
		// interval of errors is not two in a row; two are.
		{"flat zero", MetricSpec{Name: name, Stable: true, Baseline: true}, Medium, 4, baselineOf(name, make([]float64, 20)...),
			flat(0), flat(0), Passed, true, "0 standard deviations"},
		{"flat zero, tiny difference", MetricSpec{Name: name, Stable: true, Baseline: true}, Medium, 4, baselineOf(name, make([]float64, 20)...),
			flat(1e-12), flat(0), Passed, true, "0.0 standard deviations above"},
		{"beyond k but under the minimum effect", MetricSpec{Name: name, Baseline: true}, Low, 4, baselineOf(name, around(20, 100, 0)...),
			flat(123), nil, Passed, true, "4.6 standard deviations above the baseline 100 (low sensitivity needs 4.5); larger deviations were differences under 25% of the reference, too small to count"},
		{"flat zero, one interval of errors", MetricSpec{Name: name, Stable: true, Baseline: true}, Medium, 4, baselineOf(name, make([]float64, 20)...),
			func(i int) []Point {
				if i == 1 {
					return pts(0.02, 0.01, 0.03)
				}
				return pts(0, 0, 0)
			}, flat(0), Passed, true, ""},
		{"flat zero, errors in two intervals", MetricSpec{Name: name, Stable: true, Baseline: true}, Medium, 4, baselineOf(name, make([]float64, 20)...),
			func(i int) []Point {
				if i >= 1 {
					return pts(0.02, 0.01, 0.03)
				}
				return pts(0, 0, 0)
			}, flat(0), Failed, true, "0.02 versus baseline 0, more than 1000 standard deviations above, as the reference barely varies (medium sensitivity needs 3) in intervals 2 and 3"},

		// (f) Throughput dropping: lower is worse fails; higher is worse
		// does not.
		{"throughput drop, lower is worse", MetricSpec{Name: name, Direction: LowerIsWorse, Baseline: true}, Medium, 6, baselineOf(name, around(30, 1000, 20)...),
			func(i int) []Point {
				if i >= 2 {
					return pts(600, 610, 590)
				}
				return pts(1000, 990, 1010)
			}, nil, Failed, true, "600 versus baseline 1000"},
		{"throughput drop, higher is worse", MetricSpec{Name: name, Direction: HigherIsWorse, Baseline: true}, Medium, 6, baselineOf(name, around(30, 1000, 20)...),
			func(i int) []Point { return pts(600, 610, 590) }, nil, Passed, true, ""},

		// (g) Either direction fails both ways.
		{"either, up", MetricSpec{Name: name, Direction: Either, Baseline: true}, Medium, 4, baselineOf(name, around(30, 1000, 20)...),
			flat(1400), nil, Failed, true, "standard deviations above"},
		{"either, down", MetricSpec{Name: name, Direction: Either, Baseline: true}, Medium, 4, baselineOf(name, around(30, 1000, 20)...),
			flat(600), nil, Failed, true, "standard deviations below"},

		// (h) Fail-fast bounds fail at the first breach.
		{"fail fast max", MetricSpec{Name: name, FailFastMax: ptr(2000.0)}, Medium, 10, Baseline{},
			flat(2500), nil, Failed, true, "2500 is above the fail-fast maximum 2000 in interval 1; that is a fail-fast bound"},
		{"fail fast min", MetricSpec{Name: name, FailFastMin: ptr(10.0)}, Medium, 10, Baseline{},
			flat(5), nil, Failed, true, "5 is below the fail-fast minimum 10 in interval 1"},
		{"fixed max, two in a row", MetricSpec{Name: name, Max: ptr(1.0)}, Medium, 10, Baseline{},
			flat(1.5), nil, Failed, true, "1.5 is above the maximum 1 in intervals 1 and 2"},

		// (i) A moderate deviation (about 2.4 sd, 25%) fails at high
		// sensitivity and passes at medium and low.
		{"moderate deviation, high", MetricSpec{Name: name, Baseline: true}, High, 4, baselineOf(name, around(20, 100, 10)...),
			flat(125), nil, Failed, true, "2.4 standard deviations above (high sensitivity needs 2)"},
		{"moderate deviation, medium", MetricSpec{Name: name, Baseline: true}, Medium, 4, baselineOf(name, around(20, 100, 10)...),
			flat(125), nil, Passed, true, "2.4 standard deviations above the baseline 100 (medium sensitivity needs 3)"},
		{"moderate deviation, low", MetricSpec{Name: name, Baseline: true}, Low, 4, baselineOf(name, around(20, 100, 10)...),
			flat(125), nil, Passed, true, "(low sensitivity needs 4.5)"},

		// (k) A baseline-only metric without enough baseline.
		{"no baseline yet", MetricSpec{Name: name, Baseline: true}, Medium, 4, baselineOf(name, 1, 2, 3, 4),
			flat(3), nil, Inconclusive, true, "nothing to compare with: no baseline yet: 4 earlier readings, 10 needed"},
		{"no baseline, but stable", MetricSpec{Name: name, Baseline: true, Stable: true}, Medium, 4, Baseline{},
			flat(3), flat(3), Passed, true, "baseline comparison skipped: no baseline yet: 0 earlier readings, 10 needed"},
		{"stable missing in every interval", MetricSpec{Name: name, Stable: true}, Medium, 4, Baseline{},
			flat(3), nil, Inconclusive, true, "no stable readings in any interval"},
		{"no checks at all", MetricSpec{Name: name}, Medium, 4, Baseline{},
			flat(3), nil, Inconclusive, true, "no bounds and no stable or baseline comparison"},
		{"single interval window fails on one", MetricSpec{Name: name, Max: ptr(1.0)}, Medium, 1, Baseline{},
			flat(2), nil, Failed, true, "in interval 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := Spec{Sensitivity: c.sens, Intervals: c.n, IntervalMS: minute, Metrics: []MetricSpec{c.spec}}
			v := Evaluate(spec, c.base, window(c.n, name, c.canary, c.stable))
			mustJSON(t, v)
			m := v.Metrics[0]
			if m.Outcome != c.outcome || v.Outcome != c.outcome || v.Final != c.final {
				t.Fatalf("got %s (verdict %s, final %v), want %s: %s", m.Outcome, v.Outcome, v.Final, c.outcome, m.Reason)
			}
			if !strings.Contains(m.Reason, c.reason) {
				t.Errorf("reason %q lacks %q", m.Reason, c.reason)
			}
		})
	}
}

func nan() float64 { var z float64; return z / z }
func inf() float64 { var z float64; return 1 / z }

// (h) A fail-fast breach decides the whole verification after one
// interval of ten.
func TestFailFastIsFinalAtOnce(t *testing.T) {
	spec := Spec{Sensitivity: Medium, Intervals: 10, IntervalMS: minute, Metrics: []MetricSpec{
		{Name: "latency", FailFastMax: ptr(2000.0)},
		{Name: "other", Max: ptr(5.0)},
	}}
	iv := Interval{Index: 0, Metrics: []MetricData{{Name: "latency", Canary: pts(2500)}, {Name: "other", Canary: pts(1)}}}
	v := Evaluate(spec, Baseline{}, []Interval{iv})
	if v.Outcome != Failed || !v.Final || v.Intervals != 1 {
		t.Fatalf("%s final %v after %d: %s", v.Outcome, v.Final, v.Intervals, v.Summary)
	}
	if v.Metrics[1].Outcome != Running {
		t.Errorf("the other metric should still be running: %s", v.Metrics[1].Outcome)
	}
	if !strings.HasPrefix(v.Summary, "latency: 2500 is above the fail-fast maximum 2000") {
		t.Errorf("summary %q", v.Summary)
	}
}

// (e) A real regression fails at the second consecutive failing interval,
// at once, and the summary names the metric and the deviation.
func TestRegressionFailsAtSecondInterval(t *testing.T) {
	sc, base := loadScenario(t, "regression")
	for upto := 1; upto <= 3; upto++ {
		v := Evaluate(sc.Spec, base, sc.Intervals[:upto])
		if v.Outcome != Running || v.Final {
			t.Fatalf("after %d intervals: %s (final %v): %s", upto, v.Outcome, v.Final, v.Summary)
		}
	}
	v := Evaluate(sc.Spec, base, sc.Intervals[:4])
	if v.Outcome != Failed || !v.Final {
		t.Fatalf("after 4 intervals: %s (final %v): %s", v.Outcome, v.Final, v.Summary)
	}
	for _, want := range []string{"error-rate: 0.04", "versus baseline 0.003", "standard deviations above", "medium sensitivity needs 3", "in intervals 3 and 4"} {
		if !strings.Contains(v.Summary, want) {
			t.Errorf("summary %q lacks %q", v.Summary, want)
		}
	}
	m := v.Metrics[0]
	if got := m.Intervals[2]; got.Outcome != Failed || got.StableZ == nil || *got.StableZ < 10 {
		t.Errorf("interval 3 check: %+v", got)
	}
	// The whole window gives the same failure.
	if all := Evaluate(sc.Spec, base, sc.Intervals); all.Summary != v.Summary || all.Outcome != Failed {
		t.Errorf("whole window: %s: %s", all.Outcome, all.Summary)
	}
}

// (j) A source that errors in every interval is inconclusive.
func TestSourceErrorsAreInconclusive(t *testing.T) {
	spec := Spec{Sensitivity: Medium, Intervals: 5, IntervalMS: minute,
		Metrics: []MetricSpec{{Name: "error-rate", Stable: true, Baseline: true}},
		Logs:    []LogSpec{{Name: "app", Baseline: true}}}
	var ivs []Interval
	for i := range 5 {
		ivs = append(ivs, Interval{Index: i,
			Metrics: []MetricData{{Name: "error-rate", Error: "query timed out after 30s"}},
			Logs:    []LogData{{Name: "app", Error: "the log source refused the query: 403 Forbidden"}}})
	}
	v := Evaluate(spec, baselineOf("error-rate", around(20, 0.003, 0.0002)...), ivs)
	if v.Outcome != Inconclusive || !v.Final {
		t.Fatalf("%s: %s", v.Outcome, v.Summary)
	}
	if r := v.Metrics[0].Reason; !strings.Contains(r, "only 0 of 5 intervals had data") || !strings.Contains(r, "last error: query timed out") {
		t.Errorf("metric reason %q", r)
	}
	if r := v.Logs[0].Reason; !strings.Contains(r, "could not read the logs in any interval: the log source refused") {
		t.Errorf("log reason %q", r)
	}
	if c := v.Metrics[0].Intervals[0]; c.Outcome != noData || !strings.Contains(c.Reason, "could not read the metric") {
		t.Errorf("interval check %+v", c)
	}
}

func TestGapsAndConsecutiveFailures(t *testing.T) {
	spec := Spec{Sensitivity: Medium, Intervals: 6, IntervalMS: minute, Metrics: []MetricSpec{{Name: "m", Max: ptr(1.0)}}}
	bad := func(i int) Interval { return Interval{Index: i, Metrics: []MetricData{{Name: "m", Canary: pts(5)}}} }
	empty := func(i int) Interval { return Interval{Index: i, Metrics: []MetricData{{Name: "m"}}} }
	cases := []struct {
		name string
		ivs  []Interval
		want string
	}{
		{"a missing index breaks the run", []Interval{bad(0), bad(2)}, Running},
		{"an interval without data neither breaks nor extends it", []Interval{bad(0), empty(1), bad(2)}, Failed},
		{"indexes outside the window are ignored", []Interval{bad(0), bad(6), bad(-1)}, Running},
	}
	for _, c := range cases {
		if v := Evaluate(spec, Baseline{}, c.ivs); v.Outcome != c.want {
			t.Errorf("%s: %s: %s", c.name, v.Outcome, v.Summary)
		}
	}
}

func TestEmptySpecIsInconclusive(t *testing.T) {
	v := Evaluate(Spec{Intervals: 3}, Baseline{}, nil)
	if v.Outcome != Inconclusive || !v.Final || !strings.Contains(v.Summary, "no metrics and no log queries") {
		t.Errorf("%+v", v)
	}
}

// (l) Shuffled intervals and duplicates give the identical verdict.
func TestDeterministicWhateverTheOrder(t *testing.T) {
	sc, base := loadScenario(t, "regression")
	logs := logScenario(t, false)
	sc.Spec.Logs = logs.spec.Logs
	for i := range sc.Intervals {
		sc.Intervals[i].Logs = logs.intervals[i%len(logs.intervals)].Logs
	}
	base.Logs = logs.base.Logs
	for _, upto := range []int{3, 4, 10} {
		ivs := sc.Intervals[:upto]
		want := mustJSON(t, Evaluate(sc.Spec, base, ivs))
		r := rand.New(rand.NewPCG(7, uint64(upto)))
		for range 20 {
			shuffled := append([]Interval(nil), ivs...)
			shuffled = append(shuffled, ivs[r.IntN(len(ivs))], ivs[r.IntN(len(ivs))])
			r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			if got := mustJSON(t, Evaluate(sc.Spec, base, shuffled)); got != want {
				t.Fatalf("%d intervals: shuffled verdict differs:\n%s\nwant\n%s", upto, got, want)
			}
		}
	}
}

func TestFirstReportOfAnIndexWins(t *testing.T) {
	spec := Spec{Intervals: 2, Metrics: []MetricSpec{{Name: "m", Max: ptr(1.0)}}}
	first := Interval{Index: 0, Metrics: []MetricData{{Name: "m", Canary: pts(0.5)}}}
	later := Interval{Index: 0, Metrics: []MetricData{{Name: "m", Canary: pts(9)}}}
	v := Evaluate(spec, Baseline{}, []Interval{first, later})
	if v.Metrics[0].Intervals[0].Value != 0.5 || len(v.Metrics[0].Intervals) != 1 {
		t.Errorf("%+v", v.Metrics[0].Intervals)
	}
}

// A health check's plain sentence is part of why an interval failed.
func TestCheckNoteExplainsAFailedInterval(t *testing.T) {
	min := 1.0
	spec := Spec{Intervals: 2, IntervalMS: 60_000, Metrics: []MetricSpec{{Name: "healthy", Direction: LowerIsWorse, Min: &min}}}
	bad := func(i int) Interval {
		return Interval{Index: i, Start: int64(i) * 60_000, End: int64(i+1) * 60_000, Metrics: []MetricData{{
			Name: "healthy", Canary: []Point{{T: int64(i) * 60_000, V: 1}, {T: int64(i)*60_000 + 1, V: 0}},
			Note: "1 of 2 requests to /healthz returned 503"}}}
	}
	v := Evaluate(spec, Baseline{}, []Interval{bad(0), bad(1)})
	if v.Outcome != Failed || !v.Final || !strings.Contains(v.Summary, "(1 of 2 requests to /healthz returned 503)") {
		t.Fatalf("%+v", v)
	}
}
