package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
)

const verifyAcceptPipeline = `pipeline: shop
steps:
  watch:
    kind: verify
    environment: prod
    duration: 30s
    interval: 10s
    metrics:
      - { name: latency, source: prometheus, query: 'avg(latency_seconds)', max: 1 }
`

// verifyAcceptEnv is a server with a queued verification of project shop
// in prod, and a Prometheus that counts the requests made to it.
type verifyAcceptEnv struct {
	eng   *engine.Engine
	srv   *httptest.Server
	run   string
	reads atomic.Int32
	prom  *httptest.Server
}

func setupVerifyAccept(t *testing.T) *verifyAcceptEnv {
	t.Helper()
	e := &verifyAcceptEnv{}
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), LeaseTTL: 5 * time.Second, Logger: quiet(),
		Environments:  []engine.Environment{{Name: "prod", Target: "cluster-a", Projects: []string{"shop"}}},
		HealthSources: []engine.HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"shop"}}},
		RunnerPools:   []engine.RunnerPool{{Name: "deploy-main", Kind: engine.PoolDeploy, Targets: []string{"cluster-a"}}}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet(),
		RunnerPools: []RunnerPool{{Name: "deploy-main", Token: poolToken, Targets: []string{"cluster-a"}}}}
	e.eng, e.srv = eng, httptest.NewServer(s.Handler())
	e.prom = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.reads.Add(1)
		http.Error(w, "not for this test", http.StatusInternalServerError)
	}))
	t.Cleanup(func() { e.srv.Close(); e.prom.Close(); eng.Close() })
	p, err := pipeline.Parse([]byte(verifyAcceptPipeline))
	if err != nil {
		t.Fatal(err)
	}
	r, err := eng.CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
		Source: engine.Source{Kind: "git", URL: "https://git.example/shop.git"}})
	if err != nil {
		t.Fatal(err)
	}
	e.run = r.ID
	return e
}

func (e *verifyAcceptEnv) step(t *testing.T) engine.StepView {
	t.Helper()
	d, err := e.eng.GetRun(e.run)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		if s.ID == "watch" {
			return s
		}
	}
	t.Fatal("no step watch")
	return engine.StepView{}
}

// A deploy runner's accept: section limits what it verifies, as it limits
// what it deploys. With the server not knowing it (a runner that did not
// say), the verification is handed over and the runner's own check refuses
// it before it asks any health source anything: the step fails, saying
// who refused, without retries, and the refusal is audited.
func TestDeployRunnerRefusesVerificationItsAcceptSectionExcludes(t *testing.T) {
	e := setupVerifyAccept(t)
	srcs, err := hsource.Open(map[string]hsource.Config{
		"prometheus": {Type: "prometheus", URL: e.prom.URL, Projects: []string{"shop"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &runner.DeployRunner{Client: runner.NewClient(e.srv.URL, poolToken), Agent: &argocd.Agent{}, Log: quiet(),
		// The server is told nothing of the accept: section (an empty one),
		// so only the runner's own check stands between it and the sources.
		Info:   engine.RunnerInfo{ID: "d1", Name: "d1", Targets: []string{"cluster-a"}, Accept: &accept.Config{}},
		Accept: &accept.Config{Projects: []string{"web-*"}}, Sources: srcs, Settle: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(30 * time.Second)
	var s engine.StepView
	for {
		if s = e.step(t); s.Status == engine.StepFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the verification was not refused: %+v", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.Attempt != 1 || !strings.Contains(s.Error, "the runner refused the step") || !strings.Contains(s.Error, "accept.projects") {
		t.Fatalf("the step: %+v", s)
	}
	if n := e.reads.Load(); n != 0 {
		t.Fatalf("the refusing runner made %d requests to its health source", n)
	}
	// The server audits the refusal just after it ends the step.
	var list []engine.AuditEntry
	for end := time.Now().Add(10 * time.Second); len(list) == 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if list, err = e.eng.AuditLog(engine.AuditFilter{Action: "runner.refused"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(list) != 1 || list[0].Project != "shop" {
		t.Fatalf("refusals audited: %+v", list)
	}
}

// The environment is checked as well, and the server does not hand a
// verification to a runner that said it accepts neither the project nor the
// environment: another runner takes it.
func TestServerOffersVerificationsOnlyToRunnersThatAcceptThem(t *testing.T) {
	e := setupVerifyAccept(t)
	ctx := context.Background()
	c := runner.NewClient(e.srv.URL, poolToken)
	for name, acc := range map[string]*accept.Config{
		"project":     {Projects: []string{"web-*"}},
		"environment": {Environments: []string{"staging"}},
	} {
		info := engine.RunnerInfo{ID: "d-" + name, Name: name, Targets: []string{"cluster-a"}, Features: "verify", Accept: acc}
		if j, err := c.LeaseVerify(ctx, info, 0); err != nil || j != nil {
			t.Fatalf("a runner that accepts only another %s was offered it: %+v %v", name, j, err)
		}
	}
	if s := e.step(t); s.Status != engine.StepQueuedS {
		t.Fatalf("the step: %+v", s)
	}
	// The step waits, and the run says why instead of leaving it unexplained.
	if d, err := e.eng.GetRun(e.run); err != nil || !strings.Contains(d.PoolHint, "accepts it") || !strings.Contains(d.PoolHint, "accept.") {
		t.Fatalf("the run does not say why its verification waits: %q %v", d.PoolHint, err)
	}
	info := engine.RunnerInfo{ID: "d-ok", Name: "ok", Targets: []string{"cluster-a"}, Features: "verify",
		Accept: &accept.Config{Projects: []string{"shop"}, Environments: []string{"prod"}}}
	if j, err := c.LeaseVerify(ctx, info, 0); err != nil || j == nil || j.Verify == nil {
		t.Fatalf("a runner that accepts it: %+v %v", j, err)
	}
}
