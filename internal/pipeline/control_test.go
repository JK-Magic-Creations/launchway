package pipeline

import (
	"encoding/json"
	"errors"
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

// Where rollout steps and linked approvals meet run inputs, loops, steps
// that run after failures, artifacts and reports: each is refused with its
// line, or, for inputs and loops elsewhere in the pipeline, works.
func TestControlMeetsTriggersAndArtifacts(t *testing.T) {
	withInputs := strings.Replace(gatedFile, "pipeline: shop\n", "pipeline: shop\ninputs:\n  version: { required: true }\n", 1)
	withInputs = strings.Replace(withInputs, "version: { output: build.tag }", "version: { input: version }", 1)
	withInputs = strings.Replace(withInputs, "tag: { output: build.tag }", "tag: { input: version }", 1)
	// A smoke test repeated over regions, inside the deployment.
	withInputs = strings.Replace(withInputs, "  canary-smoke:\n    needs: deploy-prod\n", "  canary-smoke:\n    needs: deploy-prod\n    for_each: { region: [eu, us] }\n", 1)
	p, err := Parse([]byte(withInputs))
	if err != nil {
		t.Fatalf("a gated canary with inputs and a looped smoke test: %v", err)
	}
	if p.Requires != SchemaControl {
		t.Fatalf("requires %d", p.Requires)
	}
	vals, err := ResolveInputs(p, map[string]string{"version": "1.4.2"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Bind(p, vals, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Step("deploy-prod").Version.Literal != "1.4.2" || b.Requires != SchemaControl {
		t.Fatalf("bound: %+v requires %d", b.Step("deploy-prod").Version, b.Requires)
	}
	scope := b.DeploymentScope("deploy-prod")
	if !slices.Contains(scope, "approve-full") || !slices.Contains(scope, "promote") || len(scope) < 6 {
		t.Fatalf("scope with the loop's members: %v", scope)
	}

	for _, tc := range []struct{ name, old, new, want string }{
		{"a looped rollout step", "    action: promote\n", "    action: promote\n    for_each: { r: [a, b] }\n", "a rollout step cannot be repeated by for_each"},
		{"a looped linked approval", "    deployment: deploy-prod\n    message", "    deployment: deploy-prod\n    for_each: { r: [a, b] }\n    message", "an approval cannot be repeated"},
		{"artifacts on a rollout step", "    action: promote\n", "    action: promote\n    artifacts: [{ name: x, paths: [x] }]\n", "a rollout step runs no script, so it takes no artifacts"},
		{"fetch on a rollout step", "    action: promote\n", "    action: promote\n    fetch: [{ step: canary-smoke, artifact: x }]\n", "a rollout step runs no script, so it takes no fetch"},
		{"reports on a rollout step", "    action: promote\n", "    action: promote\n    reports: { tests: [x.xml] }\n", "a rollout step runs no script, so it takes no reports"},
		{"artifacts on a linked approval", "    deployment: deploy-prod\n    message", "    deployment: deploy-prod\n    artifacts: [{ name: x, paths: [x] }]\n    message", "an approval runs nothing, so it takes no artifacts"},
		{"a rollout step with when", "    action: promote\n", "    action: promote\n    when: { upstream: always }\n", "a rollout step has no when"},
		{"a linked approval after failures", "    deployment: deploy-prod\n    message", "    deployment: deploy-prod\n    when: { upstream: always }\n    message", "an approval that names a deployment is asked only after the steps it needs succeeded"},
		{"a step after failures before the promote", "    needs: deploy-prod\n    image: alpine:3.22\n    run: ./smoke.sh", "    needs: deploy-prod\n    when: { upstream: always }\n    image: alpine:3.22\n    run: ./smoke.sh", "could be approved after a failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(gatedFile, tc.old) {
				t.Fatalf("the edit %q did not apply", tc.old)
			}
			_, err := Parse([]byte(strings.Replace(gatedFile, tc.old, tc.new, 1)))
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("no problems: %v", err)
			}
			for _, p := range ps {
				if strings.Contains(p.Message, tc.want) {
					if p.Line == 0 {
						t.Fatalf("no line: %+v", p)
					}
					return
				}
			}
			t.Fatalf("no problem says %q:\n%v", tc.want, err)
		})
	}
}
