package engine

import (
	"testing"
)

func samples(kind string, ms ...int64) []stepSample {
	out := make([]stepSample, len(ms))
	for i, m := range ms {
		out[i] = stepSample{run: "r" + string(rune('a'+i)), number: 100 - i, ms: m, kind: kind}
	}
	return out
}

func TestEstimateStepTypicalAndRange(t *testing.T) {
	e := estimateStep(samples("branch", 100_000, 120_000, 110_000, 90_000, 105_000, 130_000, 95_000), "branch")
	if !e.Enough || e.Samples != 7 || e.Basis != "same_kind" {
		t.Fatalf("%+v", e)
	}
	if e.TypicalMs < 100_000 || e.TypicalMs > 112_000 || e.LowMs >= e.TypicalMs || e.HighMs <= e.TypicalMs || e.SlowMs < e.HighMs {
		t.Fatalf("typical %d low %d high %d slow %d", e.TypicalMs, e.LowMs, e.HighMs, e.SlowMs)
	}
	if e.LastRun != "ra" || e.LastRunNumber != 100 {
		t.Fatalf("last run %q %d", e.LastRun, e.LastRunNumber)
	}
}

func TestEstimateStepTooFewSamples(t *testing.T) {
	e := estimateStep(samples("branch", 100_000, 120_000), "branch")
	if e.Enough || e.TypicalMs != 0 || e.Samples != 2 {
		t.Fatalf("%+v", e)
	}
	if e := estimateStep(nil, "branch"); e.Enough || e.Samples != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestEstimateStepOutlierDoesNotMoveIt(t *testing.T) {
	// One run took an hour because a runner was stuck.
	e := estimateStep(samples("branch", 100_000, 3_600_000, 105_000, 95_000, 110_000, 98_000, 102_000), "branch")
	if e.TypicalMs > 110_000 || e.HighMs > 120_000 || e.SlowMs > 3_000_000 && false {
		t.Fatalf("typical %d high %d slow %d", e.TypicalMs, e.HighMs, e.SlowMs)
	}
	if e.SlowMs > 200_000 {
		t.Fatalf("an hour-long outlier set the slow mark to %d", e.SlowMs)
	}
}

func TestEstimateStepBimodalCache(t *testing.T) {
	// Newest first: mostly cache hits (2 min), sometimes misses (9 min).
	e := estimateStep(samples("branch", 120_000, 125_000, 540_000, 118_000, 122_000, 530_000, 119_000, 121_000), "branch")
	if e.Bimodal == nil {
		t.Fatalf("not seen as two groups: %+v", e)
	}
	if e.TypicalMs > 130_000 || e.Bimodal.FastMs > 130_000 || e.Bimodal.SlowMs < 500_000 || e.Bimodal.FastShare < 0.7 {
		t.Fatalf("%+v %+v", e, e.Bimodal)
	}
	// Mostly misses: the typical is the slow group.
	e = estimateStep(samples("branch", 540_000, 520_000, 120_000, 530_000, 550_000, 125_000, 535_000), "branch")
	if e.Bimodal == nil || e.TypicalMs < 500_000 {
		t.Fatalf("%+v", e)
	}
}

func TestEstimateStepNotBimodalWhenSpread(t *testing.T) {
	e := estimateStep(samples("branch", 100_000, 120_000, 140_000, 160_000, 180_000, 200_000), "branch")
	if e.Bimodal != nil {
		t.Fatalf("a spread is not two groups: %+v", e.Bimodal)
	}
}

func TestEstimateStepPrefersSameKindOfRun(t *testing.T) {
	var s []stepSample
	s = append(s, samples("pull_request", 60_000, 62_000, 58_000)...)
	s = append(s, samples("branch", 300_000, 310_000, 305_000, 295_000)...)
	if e := estimateStep(s, "pull_request"); e.Basis != "same_kind" || e.TypicalMs > 70_000 {
		t.Fatalf("%+v", e)
	}
	// Two pull request runs are too few of their own: use all.
	e := estimateStep(append(samples("pull_request", 60_000, 62_000), samples("branch", 300_000, 310_000, 305_000)...), "pull_request")
	if e.Basis != "all_runs" || e.Samples != 5 {
		t.Fatalf("%+v", e)
	}
}

func TestEstimateStepRecentCountsMore(t *testing.T) {
	// The step got slower recently (newest first).
	e := estimateStep(samples("branch", 200_000, 205_000, 195_000, 100_000, 100_000, 100_000, 100_000), "branch")
	if e.TypicalMs < 150_000 {
		t.Fatalf("recent runs should weigh more: typical %d", e.TypicalMs)
	}
}

func TestEstimateStepApprovalHasNone(t *testing.T) {
	s := samples("branch", 3_600_000, 7_200_000, 1_000_000)
	for i := range s {
		s[i].person = true
	}
	if e := estimateStep(s, "branch"); e.Enough || !e.Person {
		t.Fatalf("%+v", e)
	}
}

func fixedEsts(ids map[string]int64) map[string]*StepEstimate {
	out := map[string]*StepEstimate{}
	for id, ms := range ids {
		out[id] = &StepEstimate{Enough: true, Samples: 5, TypicalMs: ms, LowMs: ms * 8 / 10, HighMs: ms * 12 / 10}
	}
	return out
}

func TestRemainingOnParallelBranches(t *testing.T) {
	//      -> lint (1m) ------------\
	// build (2m)                      -> package (1m)
	//      -> test (5m) -> e2e (3m) -/
	steps := []estStep{
		{ID: "build", Status: StepSucceeded},
		{ID: "lint", Needs: []string{"build"}, Status: StepRunning, StartedAt: 1_000_000 - 30_000},
		{ID: "test", Needs: []string{"build"}, Status: StepRunning, StartedAt: 1_000_000 - 60_000},
		{ID: "e2e", Needs: []string{"test"}, Status: StepPending},
		{ID: "package", Needs: []string{"lint", "e2e"}, Status: StepPending},
	}
	ests := fixedEsts(map[string]int64{"build": 120_000, "lint": 60_000, "test": 300_000, "e2e": 180_000, "package": 60_000})
	typ, low, high, unknown, waiting, over := remaining(steps, ests, 1_000_000)
	// test has 4m left, then e2e 3m, then package 1m: 8m; lint's 30s is off the path.
	if typ != 480_000 || len(unknown) != 0 || len(waiting) != 0 || over {
		t.Fatalf("typ %d unknown %v waiting %v over %v", typ, unknown, waiting, over)
	}
	if low >= typ || high <= typ {
		t.Fatalf("range %d..%d around %d", low, high, typ)
	}
}

func TestRemainingSkipsPeopleAndFlagsUnknown(t *testing.T) {
	steps := []estStep{
		{ID: "test", Status: StepSucceeded},
		{ID: "approve", Needs: []string{"test"}, Status: StepWaitingS, Person: true},
		{ID: "ship", Needs: []string{"approve"}, Status: StepPending},
		{ID: "notes", Needs: []string{"test"}, Status: StepPending}, // never run before
	}
	ests := fixedEsts(map[string]int64{"ship": 90_000})
	typ, _, _, unknown, waiting, _ := remaining(steps, ests, 1)
	if typ != 90_000 || len(unknown) != 1 || unknown[0] != "notes" || len(waiting) != 1 || waiting[0] != "approve" {
		t.Fatalf("typ %d unknown %v waiting %v", typ, unknown, waiting)
	}
}

func TestRemainingPastTheUsualTime(t *testing.T) {
	steps := []estStep{{ID: "slow", Status: StepRunning, StartedAt: 1}}
	ests := fixedEsts(map[string]int64{"slow": 60_000})
	typ, _, _, _, _, over := remaining(steps, ests, 1+500_000)
	if !over || typ <= 0 || typ > 10_000 {
		t.Fatalf("typ %d over %v: a step past its range has only a little left, and says so", typ, over)
	}
}

func TestEstimateForRunStates(t *testing.T) {
	h := &history{steps: map[string][]stepSample{"a": samples("branch", 60_000, 61_000, 59_000)}, runs: []runSample{{run: "x", ms: 100_000, kind: "branch"}, {run: "y", ms: 100_000, kind: "branch"}, {run: "z", ms: 100_000, kind: "branch"}}}
	steps := []estStep{{ID: "a", Status: StepPending}, {ID: "b", Needs: []string{"a"}, Status: StepPending}}
	re, per := estimateFor(h, "now", "branch", steps, 1, false, 0)
	if !re.Known || re.TotalMs < 55_000 || !re.Partial || len(re.UnknownSteps) != 1 || per["b"].Enough {
		t.Fatalf("%+v", re)
	}
	// A pipeline that never ran.
	re, _ = estimateFor(&history{steps: map[string][]stepSample{}}, "now", "branch", steps, 1, false, 0)
	if re.Known || re.Reason != "no_history" {
		t.Fatalf("%+v", re)
	}
	// After it ended: faster than usual.
	re, _ = estimateFor(h, "now", "branch", steps, 1, true, 88_000)
	if !re.Known || re.UsualMs != 100_000 || re.DiffPercent != -12 || re.TookMs != 88_000 {
		t.Fatalf("%+v", re)
	}
}
