package verify

import (
	"encoding/json"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// The recorded series in testdata/ are made by this generator with fixed
// seeds. They are committed so that the tests do not depend on how the
// random number generator draws normal deviates in a given Go release.
// To regenerate them: go test ./internal/verify/ -run TestGenerateFixtures -update
var update = flag.Bool("update", false, "rewrite the fixtures in testdata/")

// scenario is one recorded verification: the spec, the intervals of the
// verification being judged, and the intervals of earlier passed
// verifications (newest first) to build its baseline from.
type scenario struct {
	Spec      Spec         `json:"spec"`
	Intervals []Interval   `json:"intervals"`
	Earlier   [][]Interval `json:"earlier,omitempty"`
}

const (
	minute = int64(60_000)
	day    = 24 * 60 * minute
	// now is when the generated verifications start: 2026-11-19 14:00 UTC.
	now = int64(1_795_960_800_000)
)

// series describes how one metric behaves over time.
type series struct {
	name   string
	level  func(t int64) float64 // the true value at time t
	noise  float64               // sd of the reading noise
	nonNeg bool                  // clip readings at zero (rates)
}

// genIntervals records a window: every interval has `per` readings of each
// series for the canary and, when stable is set, for the stable version.
// canaryLevel may override the canary's true value by interval index.
func genIntervals(r *rand.Rand, start int64, n, per int, ss []series, stable bool, canaryLevel func(name string, idx int, base float64) float64) []Interval {
	var out []Interval
	step := minute / int64(per)
	for i := range n {
		iv := Interval{Index: i, Start: start + int64(i)*minute, End: start + int64(i+1)*minute}
		for _, s := range ss {
			md := MetricData{Name: s.name}
			for p := range per {
				t := iv.Start + int64(p)*step
				lvl := s.level(t)
				c := lvl
				if canaryLevel != nil {
					c = canaryLevel(s.name, i, lvl)
				}
				md.Canary = append(md.Canary, Point{T: t, V: reading(r, c, s)})
				if stable {
					md.Stable = append(md.Stable, Point{T: t, V: reading(r, lvl, s)})
				}
			}
			iv.Metrics = append(iv.Metrics, md)
		}
		out = append(out, iv)
	}
	return out
}

func reading(r *rand.Rand, level float64, s series) float64 {
	v := level + r.NormFloat64()*s.noise
	if s.nonNeg && v < 0 {
		v = 0
	}
	// Six significant digits, as a metrics source would return them.
	return roundSig(v, 6)
}

func roundSig(v float64, digits int) float64 {
	if v == 0 {
		return 0
	}
	p := math.Pow(10, float64(digits-1)-math.Floor(math.Log10(math.Abs(v))))
	return math.Round(v*p) / p
}

func constant(v float64) func(int64) float64 { return func(int64) float64 { return v } }

// diurnal is a daily cycle peaking at 14:00 UTC (the generated "now").
func diurnal(mean, amp float64) func(int64) float64 {
	return func(t int64) float64 {
		phase := float64((t-now)%day+day) / float64(day)
		return mean + amp*math.Cos(2*math.Pi*phase)
	}
}

func fixtureScenarios() map[string]scenario {
	out := map[string]scenario{}

	// (a) A healthy service with a noisy error rate around 0.3%, judged
	// against the stable version and a baseline of three earlier runs.
	{
		r := rand.New(rand.NewPCG(1, 2))
		ss := []series{
			{name: "error-rate", level: constant(0.003), noise: 0.0006, nonNeg: true},
			{name: "latency-p95", level: constant(220), noise: 15},
		}
		spec := Spec{Sensitivity: Medium, Intervals: 10, IntervalMS: minute, Metrics: []MetricSpec{
			{Name: "error-rate", Direction: HigherIsWorse, Stable: true, Baseline: true},
			{Name: "latency-p95", Direction: HigherIsWorse, Baseline: true, Max: ptr(800.0)},
		}}
		sc := scenario{Spec: spec, Intervals: genIntervals(r, now, 10, 6, ss, true, nil)}
		for d := 1; d <= 3; d++ {
			sc.Earlier = append(sc.Earlier, genIntervals(r, now-int64(d)*day, 10, 6, ss, false, nil))
		}
		out["healthy-error-rate"] = sc
	}

	// (b) Latency with a daily cycle (200 ms ± 60), baseline recorded at
	// the same time of day on the three days before; and the same
	// verification against a baseline recorded twelve hours off.
	{
		ss := []series{{name: "latency-p50", level: diurnal(200, 60), noise: 10}}
		spec := Spec{Sensitivity: Medium, Intervals: 10, IntervalMS: minute, Metrics: []MetricSpec{
			{Name: "latency-p50", Direction: HigherIsWorse, Baseline: true},
		}}
		r := rand.New(rand.NewPCG(3, 4))
		same := scenario{Spec: spec, Intervals: genIntervals(r, now, 10, 6, ss, false, nil)}
		opposite := scenario{Spec: spec, Intervals: same.Intervals}
		for d := 1; d <= 3; d++ {
			same.Earlier = append(same.Earlier, genIntervals(r, now-int64(d)*day, 10, 6, ss, false, nil))
			opposite.Earlier = append(opposite.Earlier, genIntervals(r, now-int64(d)*day-day/2, 10, 6, ss, false, nil))
		}
		out["diurnal-latency"] = same
		out["diurnal-latency-opposite-phase"] = opposite
	}

	// (e) A real regression: the error rate goes from 0.3% to 4.2% from
	// the third interval on.
	{
		r := rand.New(rand.NewPCG(5, 6))
		ss := []series{{name: "error-rate", level: constant(0.003), noise: 0.0006, nonNeg: true}}
		spec := Spec{Sensitivity: Medium, Intervals: 10, IntervalMS: minute, Metrics: []MetricSpec{
			{Name: "error-rate", Direction: HigherIsWorse, Stable: true, Baseline: true},
		}}
		regress := func(_ string, idx int, base float64) float64 {
			if idx >= 2 {
				return 0.042
			}
			return base
		}
		sc := scenario{Spec: spec, Intervals: genIntervals(r, now, 10, 6, ss, true, regress)}
		for d := 1; d <= 3; d++ {
			sc.Earlier = append(sc.Earlier, genIntervals(r, now-int64(d)*day, 10, 6, ss, false, nil))
		}
		out["regression"] = sc
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// TestGenerateFixtures rewrites testdata/ when run with -update.
func TestGenerateFixtures(t *testing.T) {
	if !*update {
		t.Skip("run with -update to regenerate the fixtures")
	}
	for name, sc := range fixtureScenarios() {
		b, err := json.MarshalIndent(sc, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("testdata", name+".json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// loadScenario reads a fixture and builds its baseline the way the engine
// does: BaselineData of each earlier run, merged newest first.
func loadScenario(t *testing.T, name string) (scenario, Baseline) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("reading fixture %s: %v (regenerate with -update)", name, err)
	}
	var sc scenario
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var recs []BaselineRecord
	for i, ivs := range sc.Earlier {
		m, l := BaselineData(sc.Spec, ivs)
		recs = append(recs, BaselineRecord{Run: BaselineRun{Run: name, Number: 100 - i}, Metrics: m, Logs: l})
	}
	return sc, MergeBaselines(recs)
}
