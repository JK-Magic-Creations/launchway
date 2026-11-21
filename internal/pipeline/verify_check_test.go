package pipeline

import (
	"strings"
	"testing"
)

func checkPipeline(check string) string {
	return "pipeline: shop\nsteps:\n  watch:\n    kind: verify\n    environment: prod\n    duration: 2m\n    interval: 30s\n    metrics:\n      - name: healthy\n        source: checks\n        check:\n" +
		check
}

func TestHealthCheckParses(t *testing.T) {
	p, err := Parse([]byte(checkPipeline(`          url: https://shop.shop.svc.cluster.local:8443/healthz
          status: [200, 2xx, 301-302]
          json: { path: status, equals: ok }
          headers: { Host: shop.example.com }
          secret_headers: { Authorization: { store: vault, key: apps/shop/health, field: header } }
          timeout: 3s
          every: 2s
          success: 0.95
          server_name: shop.example.com
`)))
	if err != nil {
		t.Fatal(err)
	}
	m := p.Steps[0].Verify.Metrics[0]
	c := m.Check
	if c == nil || c.Kind != "http" || c.Method != "GET" || c.Success != 0.95 || c.Every.D().Seconds() != 2 || c.SecretHeaders["Authorization"].Key != "apps/shop/health" {
		t.Fatalf("check: %+v", c)
	}
	// The metric is the share of healthy calls: lower is worse, at least the success share, no baseline.
	if m.Direction != "lower-is-worse" || m.Min == nil || *m.Min != 0.95 || !m.NoBaseline {
		t.Fatalf("metric: %+v", m)
	}
	for code, want := range map[int]bool{200: true, 204: true, 301: true, 303: false, 503: false} {
		if c.StatusMatches(code) != want {
			t.Errorf("status %d: %v", code, !want)
		}
	}
	if (&HealthCheck{}).StatusMatches(204) != true || (&HealthCheck{}).StatusMatches(404) {
		t.Error("the default is 2xx")
	}
}

func TestHealthCheckMistakesAreSaidPlainly(t *testing.T) {
	for _, c := range []struct{ check, want string }{
		{"          status: [200]\n", "give the address the deploy runner calls"},
		{"          url: ftp://x/y\n", "is not an http or https address"},
		{"          url: http://u:p@x/y\n", "use secret_headers for a credential"},
		{"          url: http://x/y\n          method: POST\n", "a health check never changes anything"},
		{"          url: http://x/y\n          status: [ok]\n", `"ok" is not a status`},
		{"          url: http://x/y\n          success: 2\n", "above 0 and at most 1"},
		{"          url: http://x/y\n          headers: { Authorization: Bearer abc }\n", "put Authorization under secret_headers"},
		{"          url: http://x/y\n          json: { path: status }\n", "json needs both path and equals"},
		{"          url: http://x/y\n          every: 100ms\n", "every is at least 1s"},
		{"          url: http://x/y\n          timeout: 90s\n", "timeout is 1s to 30s"},
		{"          url: http://x/y\n          insecure_tls: true\n", "insecure_tls is for https"},
		{"          kind: tcp\n          url: db.shop\n", "host:port"},
		{"          kind: grpc\n          url: db.shop:1\n", "gRPC health checks are not built in yet"},
		{"          url: http://x/y\n          extra: 1\n", "unknown key"},
	} {
		_, err := Parse([]byte(checkPipeline(c.check)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: want %q, got %v", c.check, c.want, err)
		}
	}
	// A metric with both a query and a check, or a check without a source, is refused.
	src := strings.Replace(checkPipeline("          url: http://x/y\n"), "        source: checks\n", "        source: checks\n        query: up\n", 1)
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "a check or a query, not both") {
		t.Errorf("both: %v", err)
	}
}
