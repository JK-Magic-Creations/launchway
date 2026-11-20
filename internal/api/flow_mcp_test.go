package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The MCP run tools show a step that failed and was allowed to as such, and
// the run's failed_steps do not list it.
func TestMCPDescribeRunAllowedFailure(t *testing.T) {
	c := &mcpCall{s: &Server{}}
	d := &engine.RunDetail{RunSummary: engine.RunSummary{ID: "run_1", Number: 3, Project: "demo", Status: engine.RunSucceeded,
		Steps: []engine.StepBrief{{ID: "lint", Status: engine.StepFailed, Allowed: true}, {ID: "test", Status: engine.StepSucceeded}}},
		StepList: []engine.StepView{
			{ID: "lint", Status: engine.StepFailed, Attempt: 1, AllowedFailure: true, OnFailure: "continue"},
			{ID: "test", Status: engine.StepSucceeded, Attempt: 1},
		}}
	out := c.describeRun(d)
	if len(out.Run.Failed) != 0 || len(out.Run.Allowed) != 1 {
		t.Fatalf("brief %+v", out.Run)
	}
	b := c.brief(&d.RunSummary)
	if len(b.Failed) != 0 || len(b.Allowed) != 1 || b.Allowed[0] != "lint" {
		t.Fatalf("failed_steps must not list an allowed failure: %+v", b)
	}
	if !out.Steps[0].AllowedFailure || !strings.Contains(out.Steps[0].Why, "allowed") {
		t.Fatalf("%+v", out.Steps[0])
	}
	if line := progressLine(d); strings.Contains(line, "1 failed,") || !strings.Contains(line, "1 failed (allowed)") {
		t.Fatalf("%s", line)
	}
}
