package pipeline

import (
	"strings"
	"testing"
)

const preTrafficBase = `pipeline: shop
steps:
  build: { image: none, run: "echo tag=1 >> $LAUNCHWAY_OUTPUTS" }
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    argocd:
      application: shop
      write_back: { repo: gitops, branch: main, changes: [ { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } } ] }
    rollout: ROLLOUT
%s`

func warnings(t *testing.T, rollout, extra string) Problems {
	t.Helper()
	src := strings.Replace(strings.Replace(preTrafficBase, "ROLLOUT", rollout, 1), "%s", extra, 1)
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return DeliveryWarnings(p)
}

const verifyStep = `  verify-prod:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod
    duration: 5m
    interval: 1m
    metrics:
      - { name: healthy, source: checks, check: { url: "http://shop-canary.shop.svc:8080/healthz", headers: { Host: shop.example.com }, success: 0.95 } }
`

const promoteStep = `  promote-prod:
    kind: rollout
    needs: verify-prod
    deployment: deploy-prod
    action: promote
`

func TestRealTrafficToAnUnverifiedVersionIsWarnedAbout(t *testing.T) {
	w := warnings(t, "{ name: shop, namespace: shop }", "")
	if len(w) != 1 || w[0].Code != CodeTrafficUnverified || !strings.Contains(w[0].Message, "unverified: true") || w[0].Line == 0 {
		t.Fatalf("no verify step: %+v", w)
	}
	w = warnings(t, "{ name: shop, namespace: shop }", verifyStep)
	if len(w) != 1 || w[0].Code != CodeTrafficBeforeVerify || !strings.Contains(w[0].Message, "until: paused") {
		t.Fatalf("verify after traffic: %+v", w)
	}
}

func TestTheSafePatternAndAChosenRiskAreQuiet(t *testing.T) {
	if w := warnings(t, "{ name: shop, namespace: shop, until: paused }", verifyStep+promoteStep); len(w) != 0 {
		t.Fatalf("until: paused with a verify step: %+v", w)
	}
	if w := warnings(t, "{ name: shop, namespace: shop, unverified: true }", ""); len(w) != 0 {
		t.Fatalf("unverified: true: %+v", w)
	}
	p, err := Parse([]byte(strings.Replace(strings.Replace(preTrafficBase, "ROLLOUT", "{ name: shop, namespace: shop, unverified: true }", 1), "%s", "", 1)))
	if err != nil || p.Steps[1].Rollout == nil || !p.Steps[1].Rollout.Unverified {
		t.Fatalf("the key is not read: %v", err)
	}
}
