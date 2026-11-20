package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A deploy step a policy blocks never opens its deployment, so the
// verification that watches it never starts: it is never queued for a
// deploy runner and records no verify event.
func TestPolicyBlockedDeployStartsNoVerification(t *testing.T) {
	clk := newClock()
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: prodEnv, HealthSources: prom,
		Policy: PolicyOptions{Inline: mustPolicies(t, `policies:
  - name: prod-evidence
    mode: enforce
    scope: { environments: [prod] }
    rules:
      tests: { min: 1 }
`)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, verifiedCanary), Trigger: Trigger{Event: "manual", Actor: "dana"}, Source: gitSource})
	if err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), map[string]string{"tag": "v2"}) // build, with no test results
	got := statuses(t, e, r.ID)
	if got["deploy-prod"] != "failed" || got["verify-canary"] != "skipped" {
		t.Fatalf("statuses %v", got)
	}
	d, err := e.GetRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		if s.ID == "deploy-prod" && !s.PolicyBlocked {
			t.Fatalf("deploy-prod failed, but not by policy: %+v", s)
		}
	}
	evs, _ := e.Events(r.ID)
	for _, ev := range evs {
		switch ev.Type {
		case VerifyStarted, VerifyInterval, VerifyVerdict, DeployOpened:
			t.Fatalf("a policy-blocked deploy led to %s", ev.Type)
		}
	}
	if j, _ := e.Lease(t.Context(), deployer, 0); j != nil {
		t.Fatalf("a deploy runner was given %s/%s", j.RunID, j.StepID)
	}
}

// A failed verification aborts the rollout and its rollback step reverts;
// neither is judged by policy (rollbacks and aborts return to what ran
// before), even under an enforced deploy rule, while the deploy step was.
func TestVerificationRollbackIsNotJudged(t *testing.T) {
	clk := newClock()
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: prodEnv, HealthSources: prom,
		Policy: PolicyOptions{Inline: mustPolicies(t, `policies:
  - name: prod-evidence
    mode: enforce
    scope: { environments: [prod] }
    rules:
      tests: { min: 2 }
`)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	id := createShop(t, e, strings.Replace(verifiedCanary, "    run: build\n", "    run: build\n    reports: { tests: r.xml }\n", 1), 1)
	buildWith(t, e, id, junitTwo)
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	if r := observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), paused()); !r.Accepted {
		t.Fatalf("observation: %+v", r)
	}
	j := leaseVerify(t, e)
	report(t, e, clk, j, 0, 0.003, 0.003)
	report(t, e, clk, j, 1, 0.042, 0.003)
	if ack := report(t, e, clk, j, 2, 0.044, 0.003); !ack.Final || ack.Verdict.Outcome != "failed" {
		t.Fatalf("verdict: %+v", ack.Verdict)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Verify: &VerifyResult{}}); err != nil {
		t.Fatal(err)
	}
	if c := takeCommand(t, e); c.Action != "abort" {
		t.Fatalf("command: %+v", c)
	}
	if s := stepOf(t, e, id, "rollback-prod"); s.Status != StepQueuedS {
		t.Fatalf("rollback: %s", s.Status)
	}
	judged := map[string]int{}
	evs, _ := e.Events(id)
	for _, ev := range evs {
		if ev.Type == PolicyEvaluated {
			judged[ev.Data.Step]++
		}
	}
	if judged["deploy-prod"] == 0 || judged["rollback-prod"] != 0 || judged["verify-canary"] != 0 {
		t.Fatalf("policy decisions by step: %v", judged)
	}
}
