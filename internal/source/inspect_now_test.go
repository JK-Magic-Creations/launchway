package source

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// fakeInspector adds one check and a gate, the shape of the real Inspection, or nothing.
type fakeInspector struct{ none bool }

func (f fakeInspector) Inspect(_ context.Context, _ InspectRequest, p *pipeline.Pipeline) (*pipeline.Pipeline, string, error) {
	if f.none {
		return p, "Standing orders are switched off for this installation.", nil
	}
	q := p.Clone()
	err := q.AppendSteps([]*pipeline.Step{
		{ID: "inspect-secrets", Image: "scanner:1", Run: "scan", Added: &pipeline.Added{By: pipeline.AddedByStandingOrders, Check: "secrets", Why: "every repository"}},
		{ID: "inspection", Kind: pipeline.KindGate, Needs: []string{"inspect-secrets"}, Gate: &pipeline.Gate{Policy: []byte("{}")}, Added: &pipeline.Added{By: pipeline.AddedByStandingOrders}},
	}, pipeline.SchemaStandings)
	return q, "added", err
}

// An Inspection by hand is a run of only what Standing orders add: the pipeline has none of its own steps, and a
// repository with no pipeline file at all can be inspected.
func TestInspectNowRunsOnlyTheInspection(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	m.SetInspector(fakeInspector{})
	pollOnce(t, m)
	run, err := m.InspectNow(context.Background(), StartRequest{Project: "app", Actor: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := eng.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range d.Graph.Steps {
		ids = append(ids, s.ID)
		if !s.IsGate() && s.Image == "" {
			t.Errorf("the step %s has no image: it would be a container without one", s.ID)
		}
	}
	if strings.Join(ids, ",") != "inspect-secrets,inspection" || d.Graph.Name != InspectionPipelineName {
		t.Errorf("the run holds %v of %q", ids, d.Graph.Name)
	}
	if d.Event != pipeline.EventManual || d.Actor != "ada" {
		t.Errorf("event %s by %s", d.Event, d.Actor)
	}
}

// Nothing to run is said in words, and no empty run is made.
func TestInspectNowWithNoChecksSaysWhy(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	m.SetInspector(fakeInspector{none: true})
	pollOnce(t, m)
	before, _ := eng.ListRuns(engine.RunFilter{Project: "app"})
	_, err := m.InspectNow(context.Background(), StartRequest{Project: "app"})
	if !errors.Is(err, engine.ErrConflict) || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("%v", err)
	}
	after, _ := eng.ListRuns(engine.RunFilter{Project: "app"})
	if len(after) != len(before) {
		t.Error("a run was made with no check in it")
	}
	if _, err := m.InspectNow(context.Background(), StartRequest{Project: "nope"}); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("%v", err)
	}
}
