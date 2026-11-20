package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// A deploy gate in front of a real Argo CD deployment: a run whose steps
// reported no tests is stopped before anything is committed or synced; a
// waiver and a resume let the same run deploy, and the decision says why.
func TestArgoPolicyGate(t *testing.T) {
	ps, err := policy.ParseFile([]byte(`policies:
  - name: kind-gate
    mode: enforce
    scope: { environments: [kind] }
    rules:
      tests: { min: 1 }
      refs: { branches: [main] }
`), "test.yaml", "config")
	if err != nil {
		t.Fatal(err)
	}
	h := setup(t, engine.PolicyOptions{Inline: ps})
	h.startDeployRunner("deployer-1")
	before := h.gitLog(1)
	image := h.rolloutImage("lw-demo")

	id := h.start(with(demo, goodA))
	r := h.wait(id)
	d := step(r, "deploy")
	if r.Status != engine.RunFailed || d.Status != engine.StepFailed || !d.PolicyBlocked ||
		!strings.Contains(d.Error, "blocked by policy: policy kind-gate (enforce), rule tests: no step before deploy reported test results") {
		t.Fatalf("%s", describe(r))
	}
	if len(r.Deployments) != 0 || h.gitLog(1) != before || h.rolloutImage("lw-demo") != image {
		t.Fatalf("something reached the environment: %s\n%s", describe(r), h.gitLog(1))
	}

	if _, err := h.engine().GrantWaiver(engine.WaiverRequest{Project: "shop", Policy: "kind-gate", Rule: policy.RuleTests, Run: id,
		Reason: "smoke test only release", ExpiresIn: time.Hour, Actor: "sre"}); err != nil {
		t.Fatal(err)
	}
	if err := h.engine().Resume(id, "tester"); err != nil {
		t.Fatal(err)
	}
	r = h.wait(id)
	if r.Status != engine.RunSucceeded || len(r.Deployments) != 1 || r.Deployments[0].Verdict != engine.VerdictDeployed {
		t.Fatalf("%s", describe(r))
	}
	if img := h.rolloutImage("lw-demo"); img != "docker.io/library/busybox:"+goodA {
		t.Fatalf("rollout image %s", img)
	}
	ds, err := h.engine().RunPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	last := ds[len(ds)-1]
	if last.Point != policy.PointDeploy || last.Verdict != policy.VerdictPass || last.Policies[0].Rules[0].Verdict != policy.ResultWaived ||
		last.Policies[0].Rules[0].Waiver.Reason != "smoke test only release" {
		t.Fatalf("%+v", last)
	}
}
