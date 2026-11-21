package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A runner with the shared token or a pool token cannot say it is an
// enrolled runner: the ID is visible to anyone who can read a project, and
// believing it would overwrite the enrolled runner's row (it would look
// shared, or alive again after a revocation) and bind leases to an ID the
// caller cannot use. Every way to ask for work refuses it, and audits it.
func TestSharedTokenCannotClaimAnEnrolledRunner(t *testing.T) {
	db := testdb.New(t)
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: idTTL, Logger: quiet(),
		Environments:  []engine.Environment{{Name: "prod", Target: "cluster-a", Projects: []string{"shop"}}},
		HealthSources: []engine.HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"shop"}}},
		RunnerPools: append([]engine.RunnerPool{{Name: "deploy-main", Kind: engine.PoolDeploy, Targets: []string{"cluster-a"}}},
			idPools...)})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet(),
		RunnerPools: []RunnerPool{{Name: "deploy-main", Token: poolToken, Targets: []string{"cluster-a"}}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	e := &idEnv{eng: eng, a: srv, b: srv, dir: t.TempDir(), db: db}

	build, buildID := e.enrol(t, e.joinToken(t, srv, `{"pool":"web"}`), "builder", srv, srv)
	deploy, deployID := e.enrol(t, e.joinToken(t, srv, `{"pool":"deploy-main"}`), "deployer", srv, srv)
	ctx := context.Background()
	// Both are known to the server, as runners that have asked for work.
	if _, err := build.Lease(ctx, engine.RunnerInfo{ID: buildID.RunnerID, Name: "builder", Features: engine.FeatureArtifacts}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := deploy.LeaseDeploy(ctx, engine.RunnerInfo{ID: deployID.RunnerID, Name: "deployer", Targets: []string{"cluster-a"}}, 0); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		list, err := eng.Runners()
		if err != nil {
			t.Fatal(err)
		}
		var rows []engine.RunnerView
		for _, r := range list {
			if r.ID == buildID.RunnerID || r.ID == deployID.RunnerID {
				rows = append(rows, r)
			}
		}
		if len(rows) != 2 {
			t.Fatalf("the enrolled runners: %+v", rows)
		}
		b, _ := json.Marshal(rows)
		return string(b)
	}
	before := snapshot()

	// Work for both kinds is waiting.
	run := e.startRun(t, "web-x", "pipeline: p\nsteps:\n  a: {image: none, run: echo hi}\n")
	p, err := pipeline.Parse([]byte(verifyAcceptPipeline))
	if err != nil {
		t.Fatal(err)
	}
	vrun, err := eng.CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
		Source: engine.Source{Kind: "git", URL: "https://git.example/shop.git"}})
	if err != nil {
		t.Fatal(err)
	}

	shared := runner.NewClient(srv.URL, "secret")
	pooled := runner.NewClient(srv.URL, poolToken)
	liar := func(id string) engine.RunnerInfo {
		return engine.RunnerInfo{ID: id, Name: "liar", Targets: []string{"cluster-a"}, Features: engine.FeatureArtifacts + " verify"}
	}
	forbidden := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "is enrolled") {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, err = shared.Lease(ctx, liar(buildID.RunnerID), 0)
	forbidden("lease", err)
	_, err = pooled.LeaseDeploy(ctx, liar(deployID.RunnerID), 0)
	forbidden("deploy lease", err)
	_, err = pooled.LeaseVerify(ctx, liar(deployID.RunnerID), 0)
	forbidden("verification lease", err)

	if got := snapshot(); got != before {
		t.Fatalf("an enrolled runner's row changed:\nbefore %s\nafter  %s", before, got)
	}
	for _, id := range []string{run, vrun.ID} {
		d, err := eng.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range d.StepList {
			if st.Status != engine.StepQueuedS {
				t.Errorf("run %s step %s was taken: %s", id, st.ID, st.Status)
			}
		}
	}
	audited, err := eng.AuditLog(engine.AuditFilter{Action: "runner.identity_refused"})
	if err != nil || len(audited) != 3 {
		t.Fatalf("refusals audited: %+v %v", audited, err)
	}
	for _, a := range audited {
		if a.Outcome != "denied" || (a.Target != buildID.RunnerID && a.Target != deployID.RunnerID) {
			t.Errorf("audit entry: %+v", a)
		}
	}

	// Runners that say who they are not (a new ID), and the enrolled ones
	// as themselves, are unaffected.
	if _, err := shared.Lease(ctx, engine.RunnerInfo{ID: "plain", Name: "plain", Features: engine.FeatureArtifacts}, 0); err != nil {
		t.Fatalf("a shared runner with an ID of its own: %v", err)
	}
	if _, err := pooled.LeaseVerify(ctx, engine.RunnerInfo{ID: "plain-d", Name: "plain-d", Targets: []string{"cluster-a"}}, 0); err != nil {
		t.Fatalf("a pool runner with an ID of its own: %v", err)
	}
	if j, err := build.Lease(ctx, engine.RunnerInfo{ID: buildID.RunnerID, Name: "builder", Features: engine.FeatureArtifacts}, 0); err != nil || j == nil {
		t.Fatalf("the enrolled builder: %v %v", j, err)
	}
}
