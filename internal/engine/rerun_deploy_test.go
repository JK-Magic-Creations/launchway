package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func twoDeploys(withBadRollback bool) *RunState {
	steps := []*pipeline.Step{
		{ID: "build"},
		{ID: "deploy-sandbox", Kind: pipeline.KindDeploy, Needs: []string{"build"}, Environment: "sandbox"},
		{ID: "approve-dev", Kind: pipeline.KindApproval, Needs: []string{"deploy-sandbox"}},
		{ID: "deploy-dev", Kind: pipeline.KindDeploy, Needs: []string{"approve-dev"}, Environment: "dev"},
	}
	if withBadRollback {
		// A rollback of the sandbox deployment that comes after the dev one.
		steps = append(steps, &pipeline.Step{ID: "rollback-sandbox", Kind: pipeline.KindRollback, Deployment: "deploy-sandbox", Needs: []string{"deploy-dev"}})
	}
	st := &RunState{ID: "run_1", Number: 4, Pipeline: &pipeline.Pipeline{Steps: steps}, Status: RunFailed, Steps: map[string]*StepState{}}
	status := map[string]StepStatus{"build": StepSucceeded, "deploy-sandbox": StepSucceeded, "approve-dev": StepSucceeded, "deploy-dev": StepFailed, "rollback-sandbox": StepSkippedS}
	for i, d := range steps {
		st.Order = append(st.Order, d.ID)
		st.Steps[d.ID] = &StepState{ID: d.ID, Position: i, Status: status[d.ID], Outputs: map[string]string{}}
	}
	st.Steps["build"].Outputs["digest"] = "sha256:abc"
	st.Steps["approve-dev"].DecidedBy, st.Steps["approve-dev"].DecidedAt = "ann", 1000
	st.Steps["deploy-sandbox"].Dep = &DeployState{ID: "dep_1", Closed: true}
	return st
}

// A run whose dev deploy failed after the sandbox deploy succeeded can be
// re-run from the dev deploy: the sandbox deployment (closed, and not part
// of anything that runs again) is reused, the earlier approval stands and
// the image digest is the same.
func TestRerunFromFailedDeployReusesEarlierDeployment(t *testing.T) {
	st := twoDeploys(false)
	plan, err := planRerun(st, []string{"deploy-dev"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Run, ","); got != "deploy-dev" || !plan.Deployment || plan.RunNumber != 4 {
		t.Fatalf("%+v", plan)
	}
	var approval, build *ReusedInfo
	for i := range plan.Reused {
		switch plan.Reused[i].ID {
		case "approve-dev":
			approval = &plan.Reused[i]
		case "build":
			build = &plan.Reused[i]
		}
	}
	if approval == nil || approval.DecidedBy != "ann" || approval.DecidedAt != 1000 || build == nil || build.Outputs["digest"] != "sha256:abc" {
		t.Fatalf("what is reused must be visible: %+v", plan.Reused)
	}
	// Planning from the failed step with --failed finds the same thing.
	if p2, err := planRerun(st, nil, true); err != nil || strings.Join(p2.From, ",") != "deploy-dev" {
		t.Fatalf("%+v %v", p2, err)
	}
}

// A deployment that would be reused but whose rollback runs again is never
// reused half way: the refusal names the step that works.
func TestRerunRefusesToSplitADeployment(t *testing.T) {
	_, err := planRerun(twoDeploys(true), []string{"deploy-dev"}, false)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "re-run from deploy-sandbox") {
		t.Fatalf("%v", err)
	}
	// A deploy step that failed is never reused.
	st := twoDeploys(false)
	st.Steps["deploy-sandbox"].Status = StepFailed
	if _, err := planRerun(st, []string{"deploy-dev"}, false); err == nil || !strings.Contains(err.Error(), "re-run from deploy-sandbox") {
		t.Fatalf("%v", err)
	}
}
