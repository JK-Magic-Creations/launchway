package pipeline

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// gatedFile is the design's gated canary: deploy to the first pause, smoke,
// approve, promote, and roll back (aborting first) if any of it fails.
const gatedFile = `pipeline: shop
steps:
  build:
    image: alpine:3.22
    run: ./ci/build.sh
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } }
    rollout: { name: shop, namespace: shop, until: paused }
    on_cancel: abort
  canary-smoke:
    needs: deploy-prod
    image: alpine:3.22
    run: ./smoke.sh
  approve-full:
    kind: approval
    needs: canary-smoke
    deployment: deploy-prod
    message: Promote shop to 100%?
  promote:
    kind: rollout
    needs: approve-full
    deployment: deploy-prod
    action: promote
    until: healthy
    timeout: 20m
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
    abort_first: true
`

func TestGatedCanaryParses(t *testing.T) {
	p, err := Parse([]byte(gatedFile))
	if err != nil {
		t.Fatal(err)
	}
	if p.Requires != SchemaControl || !p.UsesSchemaControl() || !p.UsesSchema11() {
		t.Fatalf("requires %d", p.Requires)
	}
	d, pr, ap, rb := p.Step("deploy-prod"), p.Step("promote"), p.Step("approve-full"), p.Step("rollback-prod")
	if !d.StopsAtPause() || d.OnCancel != "abort" {
		t.Fatalf("deploy: %+v %+v", d.Rollout, d.OnCancel)
	}
	if !pr.IsRolloutStep() || !pr.IsDeployKind() || pr.Action != ActionPromote || pr.Until != "healthy" || pr.Timeout.D().Minutes() != 20 {
		t.Fatalf("promote: %+v", pr)
	}
	if !ap.LinksDeployment() || !rb.AbortFirst {
		t.Fatal("approval link or abort_first lost")
	}
	scope := p.DeploymentScope("deploy-prod")
	if !slices.Equal(scope, []string{"deploy-prod", "canary-smoke", "approve-full", "promote", "rollback-prod"}) {
		t.Fatalf("scope %v", scope)
	}
	if c := p.RunContainers(); len(c) != 1 {
		t.Fatalf("a rollout step got a container: %+v", c)
	}
	// Stored and read back, the marker makes older servers refuse it.
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"requires":14`) {
		t.Fatalf("no marker: %s", b)
	}
	// A phase-1 pipeline gets no marker.
	p1, err := Parse([]byte(deployFile))
	if err != nil || p1.Requires != 0 || p1.UsesSchemaControl() {
		t.Fatalf("phase-1 pipeline marked: %v %d", err, p1.Requires)
	}
}

func TestControlValidation(t *testing.T) {
	for _, tc := range []struct{ name, old, new, want string }{
		{"rollout step without action", "    action: promote\n", "", "line 29: steps.promote.action: say what to do"},
		{"bad action", "action: promote", "action: pause", "write one of promote, promote-full, abort, retry"},
		{"abort takes no until", "action: promote\n    until: healthy", "action: abort\n    until: healthy", "line 34: steps.promote.until: promote: an abort step ends once the rollout is aborted"},
		{"rollout step must need", "    needs: approve-full\n    deployment: deploy-prod\n    action", "    needs: build\n    deployment: deploy-prod\n    action", "promote: must need deploy-prod, directly or through other steps"},
		{"rollout step names a non-deploy", "    deployment: deploy-prod\n    action", "    deployment: canary-smoke\n    action", `line 32: steps.promote.deployment: promote: deployment "canary-smoke" is not a deploy step`},
		{"rollout step needs a rollout", "    rollout: { name: shop, namespace: shop, until: paused }\n    on_cancel: abort\n", "", "deploy-prod names no rollout, so there is nothing to promote"},
		{"approval names nothing", "    deployment: deploy-prod\n    message", "    deployment: nope\n    message", `approve-full: there is no step "nope"`},
		{"until paused and pauses", "until: paused }", "until: paused, pauses: promote }", "with until: paused the step ends at the first one"},
		{"rollout step has no when", "    action: promote\n", "    action: promote\n    when: { branch: main }\n", "a rollout step has no when"},
		{"plain step takes no deployment", "    run: ./smoke.sh\n", "    run: ./smoke.sh\n    deployment: deploy-prod\n", "a step that runs a script takes no deployment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == "" {
				return
			}
			if !strings.Contains(gatedFile, tc.old) {
				t.Fatalf("the edit %q did not apply", tc.old)
			}
			_, err := Parse([]byte(strings.Replace(gatedFile, tc.old, tc.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v\nwant %s", err, tc.want)
			}
		})
	}
	// until: paused without any rollout step to carry the rollout on.
	noPromote := strings.Replace(gatedFile, "    rollout: { name: shop, namespace: shop, until: paused }", "    rollout: { name: shop, namespace: shop, until: paused, pauses: wait }", 1)
	if _, err := Parse([]byte(noPromote)); err == nil {
		t.Fatal("until: paused with pauses was accepted")
	}
	plain := strings.Replace(gatedFile, "    abort_first: true\n", "", 1)
	plain = strings.Replace(plain, "    on_cancel: abort\n", "", 1)
	plain = strings.Replace(plain, "    rollout: { name: shop, namespace: shop, until: paused }\n", "", 1)
	if _, err := Parse([]byte(plain)); err == nil || !strings.Contains(err.Error(), "names no rollout") {
		t.Fatalf("a rollout step on a deployment without a rollout: %v", err)
	}
}
