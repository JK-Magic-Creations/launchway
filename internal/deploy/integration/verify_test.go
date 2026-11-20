package integration

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
	"github.com/jk-magic-creations/launchway/internal/verify/source/sourcetest"
)

// verifySteps verify the paused canary against its stable version, then
// promote it, or abort it and roll back.
const verifySteps = `  verify:
    kind: verify
    needs: deploy
    deployment: deploy
    delay: 15s
    duration: 30s
    interval: 10s
    on_fail: abort
    params:
      canary: { deployment: canary_hash }
      stable: { deployment: stable_hash }
    metrics:
      - name: error-rate
        source: prometheus
        query: 'sum(rate(http_requests_total{code="500",pod_template_hash="{{canary}}"}[15s])) / sum(rate(http_requests_total{pod_template_hash="{{canary}}"}[15s]))'
        stable_query: 'sum(rate(http_requests_total{code="500",pod_template_hash="{{stable}}"}[15s])) / sum(rate(http_requests_total{pod_template_hash="{{stable}}"}[15s]))'
        max: 0.05
  promote:
    kind: rollout
    needs: verify
    deployment: deploy
    action: promote-full
    timeout: 5m
  rollback:
    kind: rollback
    deployment: deploy
    needs: [promote]
    abort_first: true
    timeout: 5m
`

// TestArgoVerify drives the control canary with a verification against a
// real Prometheus: a canary whose error rate is like the stable version's
// is promoted by the rollout step that needs the verification; one with
// many errors fails it, which aborts the rollout and rolls back.
func TestArgoVerify(t *testing.T) {
	h := setupControl(t)
	exp := sourcetest.NewExporter(t)
	prom := sourcetest.Prometheus(t, exp.Addr())
	srcs, err := hsource.Open(map[string]hsource.Config{"prometheus": {Type: "prometheus", URL: prom, Projects: []string{"shop"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.stopServer()
	h.sources = []engine.HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"shop"}}}
	h.healthSources = srcs
	h.startServer(nil)
	t.Cleanup(h.stopServer)
	h.startDeployRunner("verify-deployer")
	h.ctlReset("1.36.1")

	// The application's traffic, by pod template, as the test sets it.
	traffic := func(canaryErrors float64) {
		s := h.ctlUntil("paused at the canary", func(s ctlState) bool { return s.phase == "Paused" && s.current != s.stable })
		exp.SetErrorRate(s.stable, 50, 0.1)
		exp.SetErrorRate(s.current, 50, 50*canaryErrors)
	}

	t.Run("a healthy canary is verified and promoted", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.37.0", ", until: paused", "", verifySteps))
		traffic(0.002)
		r := h.wait(id)
		if r.Status != engine.RunSucceeded || step(r, "verify").Outputs["verdict"] != "passed" || r.Deployments[0].Verdict != engine.VerdictDeployed {
			t.Fatalf("%s %s", describe(r), describeCommands(commandsOf(r)))
		}
		if s := h.ctl(); s.phase != "Healthy" || s.image != "docker.io/library/busybox:1.37.0" || s.current != s.stable {
			t.Fatalf("rollout %+v", s)
		}
	})

	t.Run("a canary with errors fails its verification, is aborted and rolled back", func(t *testing.T) {
		id := h.startSrc(ctlPipeline("1.36.1", ", until: paused", "", verifySteps))
		traffic(0.2)
		r := h.wait(id)
		v := step(r, "verify")
		if r.Status != engine.RunFailed || v.Status != engine.StepFailed || !strings.Contains(v.Error, "error-rate") ||
			step(r, "promote").Status != engine.StepSkippedS || step(r, "rollback").Status != engine.StepSucceeded {
			t.Fatalf("%s %s", describe(r), describeCommands(commandsOf(r)))
		}
		var abort *engine.CommandView
		for _, c := range commandsOf(r) {
			if c.Action == pipeline.ActionAbort && c.Origin == engine.OriginVerify {
				c := c
				abort = &c
			}
		}
		if abort == nil || abort.Result != deploy.Applied {
			t.Fatalf("no applied abort from the verification: %s", describeCommands(commandsOf(r)))
		}
		if r.Deployments[0].Verdict != engine.VerdictRolledBack {
			t.Fatalf("verdict %s", r.Deployments[0].Verdict)
		}
		s := h.ctlUntil("back on the stable version", func(s ctlState) bool {
			return s.phase == "Healthy" && s.image == "docker.io/library/busybox:1.37.0" && s.current == s.stable
		})
		t.Logf("rolled back to %s", s.image)
	})
}
