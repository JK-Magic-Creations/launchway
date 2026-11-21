package engine

import (
	"reflect"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// A failed combination of a matrix is re-run alone: the combinations that
// passed keep what they did, and what comes after the matrix runs again.
func TestRerunOneCombinationOfAMatrix(t *testing.T) {
	e, _ := newEngine(t)
	p := parse(t, `
pipeline: demo
steps:
  test:
    for_each: { go: ["1.26", "1.27"] }
    fail_fast: false
    image: x
    run: go test ./...
  report: { image: x, run: r, needs: test }
`)
	bound, err := pipeline.Bind(p, pipeline.PreviewValues(p), pipeline.BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: bound, Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	j1, j2 := take(t, e), take(t, e)
	for _, j := range []*Job{j1, j2} {
		if j.StepID == "test[go=1.26]" {
			fail(t, e, j)
		} else {
			ok(t, e, j, nil)
		}
	}
	plan, err := e.PlanRerunFrom(r.ID, []string{"test[go=1.26]"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Run, []string{"report", "test[go=1.26]"}) && !reflect.DeepEqual(plan.Run, []string{"test[go=1.26]", "report"}) {
		t.Fatalf("a re-run from one combination runs %v", plan.Run)
	}
	if !reflect.DeepEqual(plan.Reuse, []string{"test[go=1.27]"}) {
		t.Fatalf("the passing combination is reused: %v", plan.Reuse)
	}
}
