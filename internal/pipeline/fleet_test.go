package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

const fleetFile = `pipeline: web
steps:
  build:
    image: golang:1.27
    run: go build -o dist/web ./cmd/web
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    service: web
    version: { run: short_sha }
    fleet:
      artifact: dist/web
      max_unavailable: 1
      drain: 45s
      check: { url: /healthz, status: [200], success: 1 }
      traffic: { provider: haproxy, admin: "unix:/run/haproxy/admin.sock" }
      ssh: { key: { store: vault, key: fleet/prod, field: private_key } }
`

func TestFleetStepParsesAndResolves(t *testing.T) {
	p, err := Parse([]byte(fleetFile))
	if err != nil {
		t.Fatal(err)
	}
	f := p.Step("deploy-prod").Fleet
	if f == nil || f.Method != "systemd" || f.Strategy != "rolling" || f.Service != "web" || f.Drain.D().Seconds() != 45 || f.Check.Target0() != "/healthz" {
		t.Fatalf("%+v", f)
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"fleet"`) || strings.Contains(string(b), "argocd") {
		t.Fatalf("%s", b)
	}
}

func TestFleetStepRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"both ways":          {"    fleet:\n", "    argocd: { application: web, write_back: { repo: g, branch: main, changes: [] } }\n    fleet:\n", "not both"},
		"no traffic":         {`      traffic: { provider: haproxy, admin: "unix:/run/haproxy/admin.sock" }` + "\n", "", "load balancer"},
		"no ssh":             {"      ssh: { key: { store: vault, key: fleet/prod, field: private_key } }\n", "", "ssh"},
		"absolute path":      {"artifact: dist/web", "artifact: /etc/passwd", "inside the workspace"},
		"full url":           {"url: /healthz", "url: http://example.com/healthz", "path such as /healthz"},
		"open socket":        {`admin: "unix:/run/haproxy/admin.sock"`, `admin: "tcp:0.0.0.0 ; x:9999"`, "admin is"},
		"bad strategy":       {"      artifact: dist/web", "      strategy: bluegreen\n      artifact: dist/web", "rolling or pathfinder"},
		"pathfinder no plan": {"      artifact: dist/web", "      strategy: pathfinder\n      artifact: dist/web", "steps"},
		"bootc tag":          {"      artifact: dist/web", "      method: bootc\n      image: reg.example/os:latest", "digest"},
	} {
		_, err := Parse([]byte(strings.Replace(fleetFile, tc.from, tc.to, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a problem mentioning %q, got %v", name, tc.want, err)
		}
	}
	// A bootc fleet step takes the image from an earlier step's output.
	ok := strings.Replace(fleetFile, "      artifact: dist/web", "      method: bootc\n      image: { output: build.image }", 1)
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("%v", err)
	}
}
