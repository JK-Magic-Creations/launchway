package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// Verifications are deploy work: only a runner of a deploy pool serving the
// environment's target leases one, and a verification's intervals are
// taken only from the runner that holds its lease, as for every other
// lease (schema 20's lease binding over schema 19's verify calls).
func TestVerificationLeaseIsBoundToItsDeployPool(t *testing.T) {
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), LeaseTTL: 5 * time.Second, Logger: quiet(),
		Environments:  []engine.Environment{{Name: "prod", Target: "homelab", Projects: []string{"shop"}}},
		HealthSources: []engine.HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"shop"}}},
		RunnerPools: []engine.RunnerPool{{Name: "deploy-homelab", Kind: engine.PoolDeploy, Targets: []string{"homelab"}},
			{Name: "deploy-other", Kind: engine.PoolDeploy, Targets: []string{"other"}}}})
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("o", 40)
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet(),
		RunnerPools: []RunnerPool{{Name: "deploy-homelab", Token: poolToken, Targets: []string{"homelab"}}, {Name: "deploy-other", Token: other, Targets: []string{"other"}}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	p, err := pipeline.Parse([]byte(`pipeline: shop
steps:
  watch:
    kind: verify
    environment: prod
    duration: 30s
    interval: 10s
    metrics:
      - { name: latency, source: prometheus, query: 'avg(latency_seconds)', max: 1 }
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
		Source: engine.Source{Kind: "git", URL: "https://git.example/shop.git"}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	info := engine.RunnerInfo{ID: "d1", Name: "d1", Targets: []string{"homelab", "other"}, Features: "verify"}
	shared := runner.NewClient(srv.URL, "secret")
	if _, err := shared.LeaseVerify(ctx, info, 0); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("the shared token leased a verification: %v", err)
	}
	wrong := runner.NewClient(srv.URL, other)
	if j, err := wrong.LeaseVerify(ctx, info, 0); err != nil || j != nil {
		t.Fatalf("a pool of another target leased it: %+v %v", j, err)
	}
	right := runner.NewClient(srv.URL, poolToken)
	j, err := right.LeaseVerify(ctx, info, 0)
	if err != nil || j == nil || j.Verify == nil {
		t.Fatalf("the target's pool: %+v %v", j, err)
	}
	iv := &verify.Interval{Index: 0, Start: j.Verify.Start, End: j.Verify.Start + j.Verify.IntervalMS}
	for name, c := range map[string]*runner.Client{"shared token": shared, "another pool": wrong} {
		if _, err := c.VerifyInterval(ctx, j.LeaseID, iv); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s reported an interval: %v", name, err)
		}
	}
	// The holder may (the interval is not over yet, which the engine says).
	if _, err := right.VerifyInterval(ctx, j.LeaseID, iv); err == nil || strings.Contains(err.Error(), "403") {
		t.Fatalf("the holder: %v", err)
	}
}
