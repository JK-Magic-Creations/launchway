package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// A command the deploy runner could not apply is audited twice and
// honestly: the request is recorded as a request, and the outcome, written
// when the runner reports, says failed with the reason. The run's
// deployment shows the command failed with the same words.
func TestFailedCommandIsAuditedAsFailed(t *testing.T) {
	e, base := controlSetup(t)
	ada := e.signIn(t, "ada")
	if r := ada.do(http.MethodPost, base+"promote-full", `{"comment":"ship it"}`); r.status != http.StatusCreated {
		t.Fatalf("%d %s", r.status, r.body)
	}
	dr := engine.RunnerInfo{ID: "d", Name: "deployer", Targets: []string{"kind"}}
	job, err := e.eng.TakeCommand(context.Background(), dr, dr.Targets, 0)
	if err != nil || job == nil {
		t.Fatalf("take: %+v %v", job, err)
	}
	why := "this deploy runner may not change the status of rollout shop/shop; an administrator must list it in delivery.deployRunner.rbac.controlRollouts"
	res := engine.CommandResult{Take: job.Take, Result: deploy.CommandFailed, Detail: why}
	if err := e.eng.FinishCommand(dr, dr.Targets, res); err != nil {
		t.Fatal(err)
	}
	e.api.recordCommandOutcome(httptest.NewRequest(http.MethodPost, "/", nil), dr, job.Take)

	log, err := e.eng.AuditLog(engine.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var request, outcome *engine.AuditEntry
	for i, a := range log {
		switch a.Action {
		case "deployment.promote_full":
			request = &log[i]
		case "deployment.command_outcome":
			outcome = &log[i]
		}
	}
	if request == nil || !strings.HasPrefix(request.Detail, "requested") {
		t.Fatalf("the request entry must say it is a request: %+v", request)
	}
	if outcome == nil || outcome.Outcome != "failed" || !strings.Contains(outcome.Detail, "rbac.controlRollouts") || outcome.Project != "pay-shop" {
		t.Fatalf("the outcome entry must say failed and why: %+v", outcome)
	}
}
