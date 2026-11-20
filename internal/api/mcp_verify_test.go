package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

const mcpVerifyPipeline = `pipeline: pay
steps:
  watch:
    kind: verify
    environment: prod
    duration: 3m
    interval: 1m
    metrics:
      - name: latency
        source: prometheus
        query: 'avg(latency_seconds{service="pay"})'
`

// get_verification reads a run's verifications and mark_verification marks
// one, through the same routes and checks as the API: a viewer reads but is
// not offered the change, someone who cannot see the project gets nothing.
func TestMCPVerification(t *testing.T) {
	e := mcpEnv(t, envOpts{
		environments:  []engine.Environment{{Name: "prod", Target: "homelab", Projects: []string{"pay-api"}}},
		healthSources: []engine.HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"pay-api"}}},
	})
	p, err := pipeline.Parse([]byte(mcpVerifyPipeline))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.eng.CreateRun(engine.NewRun{Project: "pay-api", Pipeline: p, Trigger: engine.Trigger{Event: "manual", Actor: "ada"},
		Source: engine.Source{Kind: "git", URL: filepath.Join(e.dir, "repos", "pay")}})
	if err != nil {
		t.Fatal(err)
	}
	deployer := engine.RunnerInfo{ID: "deployer", Name: "deployer", Targets: []string{"homelab"}, Pool: "deploy", Features: "verify"}
	j, err := e.eng.LeaseVerify(context.Background(), deployer, deployer.Targets, 0)
	if err != nil || j == nil {
		d, _ := e.eng.GetRun(r.ID)
		t.Fatalf("lease: %v %v; %s %+v", j, err, d.Status, d.StepList)
	}
	if _, err := e.eng.Start(j.LeaseID); err != nil {
		t.Fatal(err)
	}

	vic := mcpSession(t, e.mcpURL(), e.tokenFor(t, "vic", "v"), nil, nil)
	out := mustCall(t, vic, "get_verification", map[string]any{"run_id": r.ID})
	vs := out["verifications"].([]any)
	if len(vs) != 1 {
		t.Fatalf("get_verification: %v", out)
	}
	v := vs[0].(map[string]any)
	if v["step"] != "watch" || v["environment"] != "prod" || v["outcome"] != "running" || v["intervals_total"] != float64(3) {
		t.Fatalf("the verification: %v", v)
	}
	if _, errText := call(t, vic, "mark_verification", map[string]any{"run_id": r.ID, "step": "watch", "outcome": "failed", "reason": "x"}); outcome(errText) != mUnlisted {
		t.Fatalf("a viewer marked it: %q", errText)
	}
	dana := mcpSession(t, e.mcpURL(), e.tokenFor(t, "dana", "v"), nil, nil)
	for tool, args := range map[string]map[string]any{
		"get_verification":  {"run_id": r.ID},
		"mark_verification": {"run_id": r.ID, "step": "watch", "outcome": "failed", "reason": "x"},
	} {
		if _, errText := call(t, dana, tool, args); outcome(errText) != mNotFound {
			t.Errorf("dana: %s gave %q", tool, errText)
		}
	}

	otto := mcpSession(t, e.mcpURL(), e.tokenFor(t, "otto", "v"), nil, nil)
	mustCall(t, otto, "mark_verification", map[string]any{"run_id": r.ID, "step": "watch", "outcome": "failed", "reason": "latency is clearly up on the dashboard"})
	v = mustCall(t, otto, "get_verification", map[string]any{"run_id": r.ID, "step": "watch"})["verifications"].([]any)[0].(map[string]any)
	if v["marked"] != "failed" || v["marked_by"] != "otto" {
		t.Fatalf("after marking: %v", v)
	}
	if _, errText := call(t, otto, "get_verification", map[string]any{"run_id": r.ID, "step": "nope"}); outcome(errText) != mNotFound {
		t.Fatalf("an unknown step: %q", errText)
	}
}
