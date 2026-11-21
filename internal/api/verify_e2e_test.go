package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
	"github.com/jk-magic-creations/launchway/internal/verify/source/sourcetest"
)

// verifiedDeploy deploys, verifies the new version for 30 seconds against
// Prometheus, ClickHouse and Loki, and rolls back when the verification
// fails.
const verifiedDeploy = `pipeline: shop
steps:
  build:
    image: none
    run: echo tag=%s >> "$LAUNCHWAY_OUTPUTS"
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    timeout: 2m
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } }
  verify-prod:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod
    delay: 5s
    duration: 30s
    interval: 10s
    on_fail: rollback
    params:
      version: { deployment: version }
    metrics:
      - name: error-rate
        source: prometheus
        query: 'sum(rate(http_requests_total{code="500",pod_template_hash="{{version}}"}[15s])) / sum(rate(http_requests_total{pod_template_hash="{{version}}"}[15s]))'
        max: 0.05
        min_points: 1
      - name: checkout-errors
        source: clickhouse
        statement: checkout_errors
        max: 5
    logs:
      - name: app
        source: loki
        query: '{app="shop", version="{{version}}"}'
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [verify-prod]
    timeout: 2m
`

// pushQuietly pushes log lines to Loki, from a goroutine.
func pushQuietly(u string, labels map[string]string, lines []string) error {
	values := make([][2]string, len(lines))
	now := time.Now()
	for i, l := range lines {
		values[i] = [2]string{strconv.FormatInt(now.Add(time.Duration(i)*time.Millisecond).UnixNano(), 10), l}
	}
	body, _ := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": labels, "values": values}}})
	resp, err := http.Post(u+"/loki/api/v1/push", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// chExec runs one ClickHouse statement, from a goroutine.
func chExec(u, user, password, sql string) error {
	req, _ := http.NewRequest(http.MethodPost, u+"/", strings.NewReader(sql))
	req.Header.Set("X-ClickHouse-User", user)
	req.Header.Set("X-ClickHouse-Key", password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// TestVerifyEndToEndWithRealBackends runs a healthy deploy and a regression
// through a real server, build runner and deploy runner, with Prometheus,
// Loki and ClickHouse in containers holding data the test controls. The
// healthy version passes; the regression fails its verification and is
// rolled back.
func TestVerifyEndToEndWithRealBackends(t *testing.T) {
	sourcetest.Skip(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	exp := sourcetest.NewExporter(t)
	promURL := sourcetest.Prometheus(t, exp.Addr())
	lokiURL := sourcetest.Loki(t)
	chURL, chUser, chPassword := sourcetest.ClickHouse(t)
	sourcetest.ClickHouseExec(t, chURL, chUser, chPassword,
		"CREATE TABLE checkout (ts DateTime64(3, 'UTC'), version String, errors UInt32) ENGINE = MergeTree ORDER BY ts")
	t.Setenv("VERIFY_TEST_CH_PASSWORD", chPassword)

	db := testdb.New(t)
	dir := t.TempDir()
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 5 * time.Second, Logger: quiet(),
		Environments:  []engine.Environment{{Name: "prod", Target: "cluster-a", Projects: []string{"shop"}}},
		HealthSources: []engine.HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"shop"}}, {Name: "loki", Type: "loki", Projects: []string{"shop"}}, {Name: "clickhouse", Type: "clickhouse", Projects: []string{"shop"}}}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet(),
		RunnerPools: []RunnerPool{{Name: "deploy-main", Token: poolToken, Targets: []string{"cluster-a"}}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	de := &deployEnv{env: &env{eng: eng, srv: srv, dir: dir, db: db}}
	de.bare = newRepo(t, dir, "gitops", map[string]string{"apps/shop.yaml": "# the shop\nimage: team/shop:1.0.0 # pinned by Launchway\n"})
	de.proj = newRepo(t, dir, "shop", map[string]string{"README": "shop\n"})
	de.argo = newFakeArgo(t, de.bare)
	actx, acancel := context.WithCancel(context.Background())
	adone := make(chan struct{})
	go func() { defer close(adone); de.argo.reconcile(actx) }()
	t.Cleanup(func() { acancel(); <-adone })
	stores := secrets.NewResolver()
	stores.Add("env", secrets.Env{}, nil, []string{"VERIFY_TEST_*"})
	agent := &argocd.Agent{
		Config: argocd.Config{
			Targets: map[string]argocd.Target{"cluster-a": {Kubernetes: argocd.Kubernetes{InCluster: true}, ArgoCDNamespace: "argocd",
				Applications: []argocd.AppRule{{Projects: []string{"shop"}, Names: []string{"shop"}}}}},
			Repos: map[string]argocd.Repo{"gitops": {URL: de.bare, Paths: []argocd.PathRule{{Projects: []string{"shop"}, Files: []string{"apps/**"}}}}},
		},
		DataDir: filepath.Join(dir, "deployer"), Secrets: secrets.NewResolver(),
		Connect: func(string, argocd.Target) (*argocd.Cluster, error) {
			return argocd.NewCluster(de.argo.dyn, "argocd"), nil
		},
		SeeTimeout: time.Second, RunningTimeout: time.Second,
	}
	if err := agent.Config.Check(); err != nil {
		t.Fatal(err)
	}
	de.runner(t, "builder")
	srcs, err := hsource.Open(map[string]hsource.Config{
		"prometheus": {Type: "prometheus", URL: promURL, Projects: []string{"shop"}},
		"loki":       {Type: "loki", URL: lokiURL, Projects: []string{"shop"}},
		"clickhouse": {Type: "clickhouse", URL: chURL, Projects: []string{"shop"},
			Auth: &hsource.Auth{Type: "basic", User: chUser, Secret: pipeline.SecretRef{Store: "env", Key: "VERIFY_TEST_CH_PASSWORD"}},
			Statements: map[string]hsource.Statement{"checkout_errors": {Kind: "metric", Projects: []string{"shop"},
				Params: map[string]string{"version": "String"},
				SQL: "SELECT toUnixTimestamp64Milli(ts) AS t, toFloat64(errors) AS value FROM checkout " +
					"WHERE version = {version:String} AND ts >= {start:DateTime64(3)} AND ts < {end:DateTime64(3)} ORDER BY ts"}}},
	}, stores.Lookup)
	if err != nil {
		t.Fatal(err)
	}
	d := &runner.DeployRunner{Client: runner.NewClient(srv.URL, poolToken), Agent: agent,
		Info: engine.RunnerInfo{ID: "deployer-1", Name: "deployer-1", Targets: []string{"cluster-a"}}, Log: quiet(),
		ObserveEvery: 200 * time.Millisecond, ReportEvery: time.Second, Sources: srcs, Secrets: stores, Settle: 3 * time.Second}
	dctx, dcancel := context.WithCancel(context.Background())
	ddone := make(chan struct{})
	go func() { defer close(ddone); d.Run(dctx) }()
	t.Cleanup(func() { dcancel(); <-ddone })

	// The application: the version that is live serves traffic at the error
	// rate the test sets, writes logs and checkout rows.
	var mu sync.Mutex
	live, errorLine := "", ""
	setLive := func(version string, errRate float64, errLine string) {
		mu.Lock()
		live, errorLine = version, errLine
		mu.Unlock()
		exp.SetErrorRate(version, 50, 50*errRate)
	}
	tctx, tcancel := context.WithCancel(context.Background())
	tdone := make(chan struct{})
	go func() {
		defer close(tdone)
		for n := 0; tctx.Err() == nil; n++ {
			mu.Lock()
			v, el := live, errorLine
			mu.Unlock()
			if v != "" {
				lines := []string{fmt.Sprintf("INFO order %d placed in %dms", 1000+n, 20+n%7), fmt.Sprintf("INFO GET /cart 200 %dms", 3+n%5)}
				if el != "" {
					lines = append(lines, fmt.Sprintf(el, n))
				}
				_ = pushQuietly(lokiURL, map[string]string{"app": "shop", "version": v}, lines)
				errs := 0
				if el != "" {
					errs = 20
				}
				_ = chExec(chURL, chUser, chPassword, fmt.Sprintf("INSERT INTO checkout VALUES (now64(3), '%s', %d)", v, errs))
			}
			select {
			case <-tctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	t.Cleanup(func() { tcancel(); <-tdone })

	start := func(tag string) string {
		p, err := pipeline.Parse([]byte(fmt.Sprintf(verifiedDeploy, tag)))
		if err != nil {
			t.Fatal(err)
		}
		sha := gitIn(t, de.proj, "rev-parse", "main")
		r, err := eng.CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Source: engine.Source{Kind: "git", URL: de.proj},
			Trigger: engine.Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: sha}})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}

	// A healthy version passes.
	setLive("1.1.0", 0.002, "")
	r := waitDeployRun(t, eng, start("1.1.0"), 4*time.Minute)
	vs, _ := eng.RunVerifications(r.ID)
	if r.Status != engine.RunSucceeded || len(vs) != 1 || vs[0].Verdict.Outcome != "passed" {
		t.Fatalf("healthy run %s: %+v\nverifications: %+v", r.Status, r.StepList, vs)
	}
	if len(vs[0].Intervals) != 3 || vs[0].Verdict.Logs[0].Lines == 0 {
		t.Fatalf("what was read: %+v", vs[0].Intervals)
	}

	// A regression fails and is rolled back.
	setLive("1.2.0", 0.12, "ERROR NullPointerException in CartService.total for order %d")
	r = waitDeployRun(t, eng, start("1.2.0"), 4*time.Minute)
	vs, _ = eng.RunVerifications(r.ID)
	v := stepByID(r, "verify-prod")
	if r.Status != engine.RunFailed || v.Status != engine.StepFailed || stepByID(r, "rollback-prod").Status != engine.StepSucceeded {
		t.Fatalf("regression run %s: %+v", r.Status, r.StepList)
	}
	if !strings.Contains(v.Error, "error-rate") && !strings.Contains(v.Error, "NullPointerException") {
		t.Fatalf("the reason names neither the metric nor the new error: %s", v.Error)
	}
	var unknown bool
	for _, c := range vs[0].Verdict.Logs[0].Clusters {
		if c.Class == "unknown" && c.Error && strings.Contains(c.Template, "NullPointerException") {
			unknown = true
		}
	}
	if !unknown {
		t.Fatalf("the new error is not a new cluster: %+v", vs[0].Verdict.Logs)
	}
	content := gitIn(t, dir, "--git-dir", de.bare, "show", "main:apps/shop.yaml")
	if !strings.Contains(content, "team/shop:1.1.0") {
		t.Fatalf("not rolled back:\n%s", content)
	}
}
