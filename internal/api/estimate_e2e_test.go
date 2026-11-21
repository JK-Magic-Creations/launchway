package api

import (
	"testing"
	"time"
)

const estimatePipeline = `
pipeline: demo
steps:
  build:
    image: none
    run: sleep 0.4
  test:
    image: none
    needs: build
    run: sleep 0.8
  lint:
    image: none
    needs: build
    run: sleep 0.2
`

// After a pipeline has run a few times, the next run carries estimates
// in the API: per step, for the whole run, and in the run list.
func TestEstimatesFromHistory(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	// Too little history first.
	id := e.start(t, estimatePipeline)
	d := e.wait(t, id, 30*time.Second)
	if d.Estimate == nil || d.Estimate.Known {
		t.Fatalf("a pipeline that never ran has an estimate: %+v", d.Estimate)
	}
	for i := 0; i < 3; i++ {
		e.wait(t, e.start(t, estimatePipeline), 30*time.Second)
	}
	id = e.start(t, estimatePipeline)
	d = e.wait(t, id, 30*time.Second)
	if d.Estimate == nil || !d.Estimate.Known || d.Estimate.UsualMs <= 0 || d.Estimate.TookMs <= 0 {
		t.Fatalf("no took-versus-usual after a run with history: %+v", d.Estimate)
	}
	for _, s := range d.StepList {
		if s.Estimate == nil || !s.Estimate.Enough || s.Estimate.TypicalMs <= 0 || s.Estimate.Samples < 3 {
			t.Fatalf("step %s: %+v", s.ID, s.Estimate)
		}
	}
	var test, lint int64
	for _, s := range d.StepList {
		switch s.ID {
		case "test":
			test = s.Estimate.TypicalMs
		case "lint":
			lint = s.Estimate.TypicalMs
		}
	}
	if test <= lint {
		t.Fatalf("the longer step should have the longer typical time: test %d lint %d", test, lint)
	}
}
