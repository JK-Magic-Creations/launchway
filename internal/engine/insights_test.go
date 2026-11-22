package engine

import (
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/store"
)

func TestComputeInsights(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	out := &Insights{From: from.UnixMilli(), To: from.Add(3 * day).UnixMilli(), Series: []InsightPoint{}, SlowestSteps: []InsightStepRow{}, FlakiestSteps: []InsightStepRow{}}
	at := func(d int, h int) int64 {
		return from.Add(time.Duration(d)*day + time.Duration(h)*time.Hour).UnixMilli()
	}
	runs := []store.InsightRun{
		{Project: "a", Status: "succeeded", CreatedAt: at(0, 1), StartedAt: at(0, 1), FinishedAt: at(0, 1) + 10_000},
		{Project: "a", Status: "failed", CreatedAt: at(0, 2), StartedAt: at(0, 2), FinishedAt: at(0, 2) + 30_000},
		{Project: "a", Status: "succeeded", CreatedAt: at(1, 1), StartedAt: at(1, 1), FinishedAt: at(1, 1) + 20_000},
		{Project: "secret-flight", Status: "failed", CreatedAt: at(1, 2), StartedAt: at(1, 2), FinishedAt: at(1, 2) + 99_000},
		{Project: "a", Status: "canceled", CreatedAt: at(2, 1)},
	}
	steps := []store.InsightStep{
		{Project: "a", Step: "build", Status: "succeeded", QueuedAt: at(0, 1), StartedAt: at(0, 1) + 5_000, FinishedAt: at(0, 1) + 15_000, RunCreatedAt: at(0, 1)},
		{Project: "a", Step: "test", Status: "failed", QueuedAt: at(0, 2), StartedAt: at(0, 2) + 1_000, FinishedAt: at(0, 2) + 31_000, RunCreatedAt: at(0, 2)},
		{Project: "a", Step: "test", Status: "succeeded", QueuedAt: at(1, 1), StartedAt: at(1, 1) + 1_000, FinishedAt: at(1, 1) + 11_000, RunCreatedAt: at(1, 1)},
		{Project: "secret-flight", Step: "hidden-step", Status: "failed", QueuedAt: at(1, 2), StartedAt: at(1, 2), FinishedAt: at(1, 2) + 50_000, RunCreatedAt: at(1, 2)},
	}
	tests := []store.InsightTests{{Project: "a", At: at(0, 3), Total: 100, Failed: 4, Flaky: 1}, {Project: "secret-flight", At: at(1, 3), Total: 500, Failed: 50}}
	keep := func(p string) bool { return p == "a" }
	computeInsights(out, runs, steps, tests, keep, from, day)
	if out.Runs != 4 || out.Succeeded != 2 || out.Failed != 1 || out.Canceled != 1 {
		t.Fatalf("counts: %+v", out)
	}
	if got := out.SuccessRate; got < 0.66 || got > 0.67 {
		t.Errorf("success rate %v, want 2 of 3 finished", got)
	}
	if out.DurationP50 != 20 || out.DurationP95 != 30 {
		t.Errorf("duration p50/p95 = %v/%v, want 20/30", out.DurationP50, out.DurationP95)
	}
	if out.TestsTotal != 100 || out.TestsFailed != 4 || out.TestsFlaky != 1 {
		t.Errorf("tests: %d/%d/%d: a Flight that is not kept leaked in", out.TestsTotal, out.TestsFailed, out.TestsFlaky)
	}
	if len(out.Series) != 4 || out.Series[0].Runs != 2 || out.Series[1].Runs != 1 || out.Series[2].Runs != 1 {
		t.Errorf("series: %+v", out.Series)
	}
	for _, r := range append(out.SlowestSteps, out.FlakiestSteps...) {
		if r.Step == "hidden-step" {
			t.Errorf("a step of a Flight that is not kept leaked into the rows: %+v", r)
		}
	}
	if len(out.FlakiestSteps) != 1 || out.FlakiestSteps[0].Step != "test" || out.FlakiestSteps[0].FailureRate != 0.5 {
		t.Errorf("flakiest: %+v", out.FlakiestSteps)
	}
	if len(out.SlowestSteps) == 0 || out.SlowestSteps[0].Step != "test" {
		t.Errorf("slowest: %+v", out.SlowestSteps)
	}
	if out.QueueWaitP50 != 1 {
		t.Errorf("queue wait p50 = %v, want 1", out.QueueWaitP50)
	}
}

func TestPercentile(t *testing.T) {
	if percentile(nil, 0.5) != 0 {
		t.Error("no values should give 0")
	}
	if got := percentile([]float64{4, 1, 3, 2}, 0.95); got != 4 {
		t.Errorf("p95 = %v", got)
	}
}
