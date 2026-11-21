package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func smokeAnalysis() deploy.AnalysisRun {
	return deploy.AnalysisRun{Name: "shop-7-smoke", Phase: "Failed", PodHash: "abc", Namespace: "shop", Metrics: []deploy.Metric{{
		Name: "smoke", Phase: "Failed", Count: 20, Successful: 19, Failed: 1, FailureLimit: 0, Provider: "job",
		Measurements: []deploy.Measurement{{Phase: "Failed", Message: "could not resolve host new.shop"}},
		Job:          &deploy.JobRef{Name: "smoke-7", Namespace: "shop"},
		JobLog:       &deploy.JobLog{Note: "the job's log is in shop/smoke-7; this runner may not read it"}}}}
}

// A deploy failed by an analysis says which metric stopped it and how, keeps
// the analysis as evidence, and says what happened next.
func TestDeployCauseAnalysisAndNext(t *testing.T) {
	def := &pipeline.Step{ID: "deploy-dev", Kind: pipeline.KindDeploy}
	snap := deploy.Snapshot{Rollout: &deploy.Rollout{Found: true, CurrentPodHash: "abc", Aborted: true}, Analysis: []deploy.AnalysisRun{smokeAnalysis()}}
	c := deployCause(def, "dev", deploy.Verdict{Outcome: deploy.Failed, Phase: deploy.PhaseAborted, Detail: "the rollout was aborted"}, snap)
	if c.Kind != CauseAnalysisFailed || c.Analysis == nil {
		t.Fatalf("%+v", c)
	}
	for _, want := range []string{"dev deploy was stopped", `"smoke" analysis`, "failed 1 of 20", "could not resolve host new.shop"} {
		if !strings.Contains(c.Summary, want) {
			t.Fatalf("summary lacks %q: %s", want, c.Summary)
		}
	}
	ev := strings.Join(c.Evidence(), "\n")
	if !strings.Contains(ev, "job shop/smoke-7") || !strings.Contains(ev, "may not read it") {
		t.Fatalf("evidence: %s", ev)
	}

	p := &pipeline.Pipeline{Steps: []*pipeline.Step{def, {ID: "rollback-dev", Kind: pipeline.KindRollback, Deployment: "deploy-dev"}}}
	st := &RunState{Pipeline: p, Order: []string{"deploy-dev", "rollback-dev"}, Steps: map[string]*StepState{
		"deploy-dev":   {ID: "deploy-dev", Status: StepFailed, Why: c, Error: "x", Dep: &DeployState{Environment: "dev", Previous: &DeployRef{Version: "v1.2.3"}}},
		"rollback-dev": {ID: "rollback-dev", Status: StepSucceeded},
	}}
	got := st.causeOf("deploy-dev")
	if got.Next == nil || got.Next.Kind != "rolled_back" || got.Next.Summary != "Rolled back to v1.2.3 by rollback-dev." {
		t.Fatalf("%+v", got.Next)
	}
	st.Steps["rollback-dev"].Status = StepFailed
	st.Steps["rollback-dev"].Error = "boom"
	if n := st.causeOf("deploy-dev").Next; n.Kind != "rollback_failed" {
		t.Fatalf("%+v", n)
	}
}

// Steps that never had a structured cause still say why in a plain sentence.
func TestDerivedCauseKinds(t *testing.T) {
	exit := 2
	p := &pipeline.Pipeline{Steps: []*pipeline.Step{{ID: "build"}, {ID: "deploy-dev", Kind: pipeline.KindDeploy}}}
	for name, tc := range map[string]struct {
		s    StepState
		kind string
		has  string
	}{
		"exit":     {StepState{ID: "build", Status: StepFailed, ExitCode: &exit}, CauseScriptExit, "exit code 2"},
		"timeout":  {StepState{ID: "build", Status: StepFailed, FailCause: pipeline.CauseTimeout, Error: "timed out after 10m"}, CauseTimeout, "ran out of time"},
		"lost":     {StepState{ID: "build", Status: StepFailed, FailCause: pipeline.CauseInfrastructure, Error: "runner stopped responding"}, CauseRunnerLost, "stopped responding"},
		"refused":  {StepState{ID: "deploy-dev", Status: StepFailed, Error: "deploy steps do not run in pull-request runs"}, CauseRefused, "Deploy steps do not run"},
		"deadline": {StepState{ID: "deploy-dev", Status: StepFailed, Error: "not healthy within 10m; no deploy runner observed it"}, CauseTimeout, "Not healthy within"},
	} {
		s := tc.s
		st := &RunState{Pipeline: p, Order: []string{s.ID}, Steps: map[string]*StepState{s.ID: &s}}
		c := st.causeOf(s.ID)
		if c == nil || c.Kind != tc.kind || !strings.Contains(c.Summary, tc.has) {
			t.Errorf("%s: %+v", name, c)
		}
	}
	st := &RunState{Pipeline: p, Order: []string{"build"}, Steps: map[string]*StepState{"build": {ID: "build", Status: StepSucceeded}}}
	if st.causeOf("build") != nil {
		t.Error("a step that did not fail has no cause")
	}
}
