package pipeline

import (
	"strings"
	"testing"
)

const verifyPipeline = `pipeline: shop
steps:
  build:
    image: alpine:3.22
    run: echo tag=1.2.3 >> $LAUNCHWAY_OUTPUTS
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
  verify-canary:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod
    delay: 30s
    duration: 5m
    interval: 1m
    sensitivity: high
    on_fail: rollback
    on_inconclusive: pause
    params:
      ns: { deployment: namespace }
      canary: { deployment: canary_hash }
      stable: { deployment: stable_hash }
      sha: { run: sha }
    metrics:
      - name: error-rate
        source: prometheus
        query: 'sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{canary}}",code=~"5.."}[1m]))'
        stable_query: 'sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{stable}}",code=~"5.."}[1m]))'
        max: 0.01
        fail_fast: { max: 0.2 }
    logs:
      - name: app
        source: loki
        query: '{namespace="{{ns}}"} |= "error" | line_format "{{.msg}}"'
      - name: es
        source: search
        index: logs-shop
        query:
          match: { service: "{{ns}}" }
    model: local
  promote:
    kind: rollout
    needs: verify-canary
    deployment: deploy-prod
    action: promote
  notes:
    kind: ai
    needs: verify-canary
    model: local
    instructions: You write short release notes.
    prompt: Summarise the verification.
    inputs:
      - { output: verify-canary.summary, label: verdict }
    schema:
      type: object
      properties: { notes: { type: string }, risk: { type: string, enum: [low, high] } }
      required: [notes, risk]
    max_tokens: 500
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
`

func TestParseVerifyAndAI(t *testing.T) {
	p, err := Parse([]byte(verifyPipeline))
	if err != nil {
		t.Fatal(err)
	}
	if p.Requires != SchemaVerify {
		t.Errorf("requires %d", p.Requires)
	}
	v := p.Step("verify-canary")
	if v.Verify == nil || v.Verify.Interval.D().Minutes() != 1 || len(v.Verify.Metrics) != 1 || len(v.Verify.Logs) != 2 {
		t.Fatalf("verify: %+v", v.Verify)
	}
	if v.Verify.Params["canary"].Fact != "canary_hash" {
		t.Errorf("params: %+v", v.Verify.Params)
	}
	if es := v.Verify.Logs[1].Query; !strings.HasPrefix(es, `{"match"`) {
		t.Errorf("es query: %s", es)
	}
	if p.ContainerFor(v) != "" || p.ContainerFor(p.Step("notes")) != "" {
		t.Error("verify and ai steps run in no container")
	}
	if !contains(p.DeploymentScope("deploy-prod"), "verify-canary") {
		t.Error("verify-canary is inside the deployment")
	}
	ai := p.Step("notes")
	if got := AISchemaOutputs(ai.AI.Schema); strings.Join(got, ",") != "notes,risk" {
		t.Errorf("schema outputs %v", got)
	}
	if len(ai.Artifacts) != 1 || ai.Artifacts[0].Name != AIResponseArtifact {
		t.Errorf("artifacts %+v", ai.Artifacts)
	}
}

func TestParseVerifyProblems(t *testing.T) {
	for name, tc := range map[string]struct{ edit, want string }{
		"undeclared placeholder": {`stable: { deployment: stable_hash }`, `uses {{stable}}, which is not under params`},
		"abort without rollout":  {`rollout: { name: shop, namespace: shop, until: paused }`, `names no rollout`},
		"bad sensitivity":        {`sensitivity: high`, `write low, medium or high`},
		"uneven window":          {`duration: 5m`, `whole number of intervals`},
		"verify output":          {`- { output: verify-canary.summary, label: verdict }`, `that step's outputs are verdict, summary`},
		"ai output":              {`needs: [promote]`, `that ai step's outputs are`},
	} {
		t.Run(name, func(t *testing.T) {
			text := verifyPipeline
			switch name {
			case "undeclared placeholder":
				text = strings.Replace(text, "      "+tc.edit+"\n", "", 1)
			case "abort without rollout":
				text = strings.Replace(text, tc.edit, "", 1)
				text = strings.Replace(text, "on_fail: rollback", "on_fail: abort", 1)
			case "bad sensitivity":
				text = strings.Replace(text, tc.edit, "sensitivity: extreme", 1)
			case "uneven window":
				text = strings.Replace(text, tc.edit, "duration: 150s", 1)
			case "verify output":
				text = strings.Replace(text, tc.edit, "- { output: verify-canary.score, label: verdict }", 1)
			case "ai output":
				text = strings.Replace(text, "  rollback-prod:", "  after:\n    image: alpine\n    needs: notes\n    run: true\n  deploy-x:\n    kind: deploy\n    needs: after\n    environment: dev\n    version: { output: notes.nope }\n    argocd: { application: x, write_back: { repo: gitops, branch: main, changes: [ { file: a.yaml, set: a.b, value: v } ] } }\n  rollback-prod:", 1)
			}
			_, err := Parse([]byte(text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
