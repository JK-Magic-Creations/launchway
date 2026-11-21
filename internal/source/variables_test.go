package source

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const variablePipeline = `
pipeline: app
inputs:
  region: { type: string, from: region, default: us }
  replicas: { type: number, from: replicas }
steps:
  go:
    image: alpine
    run: echo go
`

type fakeVars struct {
	mu       sync.Mutex
	vars     pipeline.Variables
	envSeen  []string
	recorded map[string][]pipeline.BoundVariable
}

func (f *fakeVars) Bind(_ context.Context, project, env string) (pipeline.Variables, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envSeen = append(f.envSeen, project+"/"+env)
	return f.vars, nil
}

func (f *fakeVars) RecordRun(_ context.Context, runID, env string, used []pipeline.BoundVariable) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recorded == nil {
		f.recorded = map[string][]pipeline.BoundVariable{}
	}
	f.recorded[runID] = used
	return nil
}

func TestARunTakesVariablesRecordsThemAndRefusesAValueAgainstALock(t *testing.T) {
	dir, repo := prRepo(t, variablePipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	f := &fakeVars{vars: pipeline.Variables{
		"region":   {Name: "region", Type: "string", Text: "eu", Display: "eu", Source: "the wing"},
		"replicas": {Name: "replicas", Type: "number", Text: "3", Display: "3", Source: "project app", Locked: true, LockedBy: "the base (fleet)", Who: "an administrator of every project"},
	}}
	m.SetVariables(f)
	ctx := context.Background()
	run, err := m.StartRunWith(ctx, StartRequest{Project: "app", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := eng.RunTrigger(run.ID)
	if tr.Inputs["region"] != "eu" || tr.Inputs["replicas"] != "3" {
		t.Fatalf("inputs: %+v", tr.Inputs)
	}
	if got := f.recorded[run.ID]; len(got) != 2 || got[0].Name != "region" {
		t.Fatalf("recorded: %+v", f.recorded)
	}
	// A value given when the run starts beats an unlocked variable...
	run, err = m.StartRunWith(ctx, StartRequest{Project: "app", Actor: "ann", Inputs: map[string]string{"region": "ap"}})
	if err != nil {
		t.Fatal(err)
	}
	if tr, _ := eng.RunTrigger(run.ID); tr.Inputs["region"] != "ap" {
		t.Fatalf("inputs: %+v", tr.Inputs)
	}
	// ...but not a locked one: refused before the run exists.
	n := len(runsOf(t, eng))
	_, err = m.StartRunWith(ctx, StartRequest{Project: "app", Actor: "ann", Inputs: map[string]string{"replicas": "9"}})
	var ie pipeline.InputErrors
	if !errors.As(err, &ie) || !strings.Contains(ie["replicas"], "replicas is locked to 3 by the base (fleet); this run asked for 9. Only an administrator of every project can change it.") {
		t.Fatalf("%v", err)
	}
	if len(runsOf(t, eng)) != n {
		t.Fatal("a run was created")
	}
	// Changing the variables afterwards leaves the recorded run as it was.
	f.vars["region"] = pipeline.BoundVariable{Name: "region", Type: "string", Text: "xx", Display: "xx"}
	if tr, _ := eng.RunTrigger(run.ID); tr.Inputs["region"] != "ap" {
		t.Fatal("a run drifted")
	}
}

func TestWithoutAVariableSourceTheDefaultApplies(t *testing.T) {
	dir, repo := prRepo(t, strings.Replace(variablePipeline, "replicas: { type: number, from: replicas }", "replicas: { type: number, from: replicas, default: 1 }", 1))
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	run, err := m.StartRunWith(context.Background(), StartRequest{Project: "app", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if tr, _ := eng.RunTrigger(run.ID); tr.Inputs["region"] != "us" || tr.Inputs["replicas"] != "1" {
		t.Fatalf("%+v", tr.Inputs)
	}
}

func TestTheEnvironmentInputSelectsTheEnvironmentsVariables(t *testing.T) {
	dir, repo := prRepo(t, strings.Replace(variablePipeline, "inputs:\n", "inputs:\n  environment: { type: choice, options: [staging, prod], default: staging }\n", 1))
	m, _ := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	f := &fakeVars{vars: pipeline.Variables{"replicas": {Name: "replicas", Type: "number", Text: "2", Display: "2"}}}
	m.SetVariables(f)
	if _, err := m.StartRunWith(context.Background(), StartRequest{Project: "app", Actor: "ann", Inputs: map[string]string{"environment": "prod"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.envSeen) != 1 || f.envSeen[0] != "app/prod" {
		t.Fatalf("%v", f.envSeen)
	}
}
