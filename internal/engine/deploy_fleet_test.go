package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// A fleet deploy step is refused by the server, with the way to run it, until
// the server schedules it: no run is accepted that nothing could do.
func TestServerRefusesFleetDeployStepsAndSaysWhere(t *testing.T) {
	p, err := pipeline.Parse([]byte(`pipeline: web
steps:
  deploy-prod:
    kind: deploy
    environment: prod
    service: web
    fleet:
      artifact: dist/web
      check: { url: /healthz }
      traffic: { provider: haproxy, admin: "unix:/run/haproxy/admin.sock" }
      ssh: { key: { store: vault, key: fleet/prod } }
`))
	if err != nil {
		t.Fatal(err)
	}
	var out pipeline.Problems
	if !fleetStep(&out, p.Step("deploy-prod"), 3) || len(out) != 1 || !strings.Contains(out[0].Message, "launchway fleet run") {
		t.Fatalf("%v", out)
	}
	if !p.UsesFleet() {
		t.Fatal("UsesFleet is false")
	}
}
