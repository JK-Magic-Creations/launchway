package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
)

// syncingArgo stands in for Argo CD with applications that do NOT sync by
// themselves. It does what the real controller does with a sync request:
// the request is the application's operation; it applies the revision at
// the tip of the GitOps repository, reports the operation as Succeeded,
// clears it, and reports the application's workload Synced and Healthy (or
// Degraded, when the file holds a version the test marked bad).
type syncingArgo struct {
	dyn   *dynfake.FakeDynamicClient
	bare  string
	apps  []string
	mu    sync.Mutex
	bad   string
	syncs map[string]int // sync requests completed, by application
}

func newSyncingArgo(t *testing.T, bare string, apps ...string) *syncingArgo {
	t.Helper()
	var objs []runtime.Object
	for _, name := range apps {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": name, "namespace": "argocd"},
			"spec":     map[string]any{"source": map[string]any{"repoURL": bare, "targetRevision": "main", "path": "apps/" + name}},
		}})
	}
	// A sync asks which project the application is in, for its sync windows.
	objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "AppProject",
		"metadata": map[string]any{"name": "default", "namespace": "argocd"},
		"spec":     map[string]any{},
	}})
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		appGVR: "ApplicationList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects"}:  "AppProjectList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}:     "RolloutList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysisruns"}: "AnalysisRunList",
	}, objs...)
	return &syncingArgo{dyn: dyn, bare: bare, apps: apps, syncs: map[string]int{}}
}

func (a *syncingArgo) syncCount(app string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.syncs[app]
}

// run reconciles until ctx ends. Nothing is applied until a sync is asked for.
func (a *syncingArgo) run(ctx context.Context) {
	applied := map[string]string{}
	client := a.dyn.Resource(appGVR).Namespace("argocd")
	for ctx.Err() == nil {
		tipOut, err := exec.Command("git", "-C", a.bare, "rev-parse", "main").Output()
		tip := strings.TrimSpace(string(tipOut))
		for _, name := range a.apps {
			if err != nil {
				break
			}
			obj, gerr := client.Get(ctx, name, metav1.GetOptions{})
			if gerr != nil {
				continue
			}
			op, asked := obj.Object["operation"].(map[string]any)
			if asked {
				applied[name] = tip
				delete(obj.Object, "operation")
			}
			rev := applied[name]
			if rev == "" {
				continue
			}
			content, _ := exec.Command("git", "-C", a.bare, "show", rev+":apps/"+name+"/base.yaml").Output()
			health := "Healthy"
			a.mu.Lock()
			if a.bad != "" && strings.Contains(string(content), a.bad) {
				health = "Degraded"
			}
			a.mu.Unlock()
			ns := name
			status := map[string]any{
				"sync":      map[string]any{"status": "Synced", "revision": rev},
				"health":    map[string]any{"status": health},
				"resources": []any{map[string]any{"group": "apps", "kind": "Deployment", "name": "app", "namespace": ns, "status": "Synced", "health": map[string]any{"status": health}}},
			}
			if asked {
				status["operationState"] = map[string]any{"phase": "Succeeded", "message": "successfully synced", "operation": op,
					"syncResult": map[string]any{"revision": rev}, "startedAt": time.Now().UTC().Format(time.RFC3339), "finishedAt": time.Now().UTC().Format(time.RFC3339)}
				a.mu.Lock()
				a.syncs[name]++
				a.mu.Unlock()
			} else if prev, ok := obj.Object["status"].(map[string]any); ok && prev["operationState"] != nil {
				status["operationState"] = prev["operationState"]
			}
			obj.Object["status"] = status
			client.Update(ctx, obj, metav1.UpdateOptions{})
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// deliveryPipeline is a service flight plan as a project writes it: the
// image stage is taken over by a step that stands in for the builder (this
// machine has no BuildKit), and the watch period is shortened to its minimum (one 30 s interval, after the maneuver's own 30 s warm-up).
const deliveryPipeline = `pipeline: app
steps:
  ci:
    uses: launchway/go-service
    with:
      lint: false
      vuln_scan: false
      secret_scan: false
      analysis: false
      license_scan: false
      verify_for: 30s
      overrides: [ "dev.health_url=%[1]s/healthz", "prod.health_url=%[1]s/healthz" ]
    replace:
      image:
        image: none
        run: |
          echo "tag=1.0.$LAUNCHWAY_RUN_NUMBER" >> "$LAUNCHWAY_OUTPUTS"
          echo "digest=sha256:%[2]s" >> "$LAUNCHWAY_OUTPUTS"
`

// One service flight plan, end to end against a test server: the tests run,
// the image stage (stood in for) names a tag and digest, the deliver
// maneuver writes the digest to the GitOps repository and asks Argo CD for
// a sync, which a fake that completes syncs does; the protected
// environment waits for a person; the health check calls a local fake
// service; and a version whose check fails is put back.
func TestServicePlanDeliversEndToEnd(t *testing.T) {
	need(t, "go", "gofmt", "git")
	dir := t.TempDir()
	db := testdb.New(t)
	store, err := blobstore.NewFilesystem(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 10 * time.Second, Logger: quiet(),
		Artifacts: engine.ArtifactOptions{Store: store},
		Environments: []engine.Environment{
			{Name: "dev", Target: "homelab", Projects: []string{"app"}},
			{Name: "prod", Target: "homelab", Projects: []string{"app"}, Protected: true}},
		HealthSources: []engine.HealthSource{{Name: "checks", Type: "check", Projects: []string{"app"}}}})
	if err != nil {
		t.Fatal(err)
	}
	eng.SetDefaultBranch(func(string) string { return "main" })

	// The service the health check calls: healthy until the test says not.
	var healthy atomic.Bool
	healthy.Store(true)
	var calls atomic.Int64
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/healthz" || !healthy.Load() {
			http.Error(w, "not well", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	}))
	t.Cleanup(svc.Close)

	digest := strings.Repeat("ab", 32)
	files := fixtureFiles(t, "go")
	files["Dockerfile"] = "FROM scratch\n"
	files[".launchway/pipeline.yaml"] = fmt.Sprintf(deliveryPipeline, svc.URL, digest)
	app := testlib.New(t, filepath.Join(dir, "app"), files)
	mgr, err := source.NewManagerWithLibraries(eng, filepath.Join(dir, "data"), []source.Project{{Name: "app", URL: app.Bare}}, nil, quiet())
	if err != nil {
		t.Fatal(err)
	}
	api := &Server{Engine: eng, Projects: mgr, RunnerToken: "secret", Actor: "tester", Log: quiet(),
		RunnerPools: []RunnerPool{{Name: "deploy-homelab", Token: poolToken, Targets: []string{"homelab"}}}}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	e := &env{eng: eng, srv: srv, dir: dir, db: db}
	r := e.newRunner("builder")
	r.Exec = &executor.Host{}
	e.startRunner(t, r)

	// The GitOps repository, and the Argo CD that watches it.
	seed := "# the app\nimage: launchway/app:seed # pinned by Launchway\n"
	bare := newRepo(t, dir, "gitops", map[string]string{"apps/app-dev/base.yaml": seed, "apps/app-prod/base.yaml": seed})
	argo := newSyncingArgo(t, bare, "app-dev", "app-prod")
	actx, acancel := context.WithCancel(context.Background())
	adone := make(chan struct{})
	go func() { defer close(adone); argo.run(actx) }()
	t.Cleanup(func() { acancel(); <-adone })
	agent := &argocd.Agent{
		Config: argocd.Config{
			Targets: map[string]argocd.Target{"homelab": {Kubernetes: argocd.Kubernetes{InCluster: true}, ArgoCDNamespace: "argocd",
				Applications: []argocd.AppRule{{Projects: []string{"app"}, Names: []string{"app-*"}}}}},
			Repos: map[string]argocd.Repo{"gitops": {URL: bare, Paths: []argocd.PathRule{{Projects: []string{"app"}, Files: []string{"apps/**"}}}}},
		},
		DataDir: filepath.Join(dir, "deployer"), Secrets: secrets.NewResolver(),
		Connect: func(string, argocd.Target) (*argocd.Cluster, error) {
			return argocd.NewCluster(argo.dyn, "argocd"), nil
		},
		SeeTimeout: time.Second, RunningTimeout: time.Second,
	}
	if err := agent.Config.Check(); err != nil {
		t.Fatal(err)
	}
	srcs, err := hsource.Open(map[string]hsource.Config{"checks": {Type: hsource.Check, Hosts: []string{"127.0.0.1"}, Projects: []string{"app"}}}, secrets.NewResolver().Lookup)
	if err != nil {
		t.Fatal(err)
	}
	d := &runner.DeployRunner{Client: runner.NewClient(srv.URL, poolToken), Agent: agent,
		Info: engine.RunnerInfo{ID: "deployer-1", Name: "deployer-1", Targets: []string{"homelab"}}, Log: quiet(),
		ObserveEvery: 200 * time.Millisecond, ReportEvery: time.Second, Sources: srcs, Secrets: secrets.NewResolver(), Settle: 3 * time.Second}
	dctx, dcancel := context.WithCancel(context.Background())
	ddone := make(chan struct{})
	go func() { defer close(ddone); d.Run(dctx) }()
	t.Cleanup(func() { dcancel(); <-ddone })

	fail := func(what string, run *engine.RunDetail) {
		t.Helper()
		var b strings.Builder
		for _, s := range run.StepList {
			fmt.Fprintf(&b, "%s=%s(%s|%s|%s) ", s.ID, s.Status, s.Error, s.Reason, s.WaitingFor)
		}
		t.Fatalf("%s: run %s: %s\ndeployments: %+v\n%s", what, run.Status, b.String(), run.Deployments, logsOf(t, e, run))
	}
	file := func(env string) string {
		return gitIn(t, dir, "--git-dir", bare, "show", "main:apps/app-"+env+"/base.yaml")
	}

	// A good version goes to dev, is proven healthy there, waits in prod
	// for a person, and goes on when one approves.
	id := startAppRun(t, e)
	var run *engine.RunDetail
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		var err error
		if run, err = eng.GetRun(id); err != nil {
			t.Fatal(err)
		}
		if stepStatus(run, "ci/deliver[environment=prod]/approve") == engine.StepWaitingS || run.Status.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			fail("the production decision never waited", run)
		}
	}
	if stepStatus(run, "ci/deliver[environment=prod]/approve") != engine.StepWaitingS {
		fail("waiting for the decision", run)
	}
	if got := file("dev"); !strings.Contains(got, "launchway/app:1.0.1") || !strings.Contains(got, "sha256:"+digest) {
		t.Fatalf("dev file after the first delivery:\n%s", got)
	}
	if got := file("prod"); got != strings.TrimSpace(seed) {
		t.Fatalf("prod changed before anyone decided:\n%s", got)
	}
	if stepStatus(run, "ci/deliver[environment=dev]/verify") != engine.StepSucceeded {
		fail("the dev proof", run)
	}
	if code, msg := post(t, srv.URL+"/api/v1/runs/"+id+"/steps/"+url.PathEscape("ci/deliver[environment=prod]/approve")+"/approve", `{"comment":"ship it"}`); code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, msg)
	}
	run = e.wait(t, id, 5*time.Minute)
	if run.Status != engine.RunSucceeded {
		fail("the first run", run)
	}
	if got := file("prod"); !strings.Contains(got, "launchway/app:1.0.1") {
		t.Fatalf("prod file after the decision:\n%s", got)
	}
	if argo.syncCount("app-dev") != 1 || argo.syncCount("app-prod") != 1 {
		t.Fatalf("sync requests completed: dev %d, prod %d", argo.syncCount("app-dev"), argo.syncCount("app-prod"))
	}
	if calls.Load() == 0 {
		t.Fatal("the health check never called the service")
	}
	if len(run.Deployments) != 2 {
		t.Fatalf("deployments: %+v", run.Deployments)
	}
	for _, dep := range run.Deployments {
		if dep.Verdict != engine.VerdictDeployed {
			t.Fatalf("deployment %s: %+v", dep.Environment, dep)
		}
	}

	// A version whose health check fails is put back in dev, and prod is
	// never asked about.
	healthy.Store(false)
	devAfterFirst := file("dev")
	id = startAppRun(t, e)
	run = e.wait(t, id, 5*time.Minute)
	if run.Status != engine.RunFailed {
		fail("the second run should fail", run)
	}
	if v := stepByID(run, "ci/deliver[environment=dev]/verify"); v.Status != engine.StepFailed {
		fail("the dev proof should fail", run)
	}
	if rb := stepByID(run, "ci/deliver[environment=dev]/rollback"); rb.Status != engine.StepSucceeded {
		fail("the dev rollback", run)
	}
	if got := file("dev"); !strings.Contains(got, "launchway/app:1.0.1") || strings.Contains(got, "launchway/app:1.0.2") {
		t.Fatalf("dev after the rollback (was %q):\n%s", devAfterFirst, got)
	}
	if s := stepStatus(run, "ci/deliver[environment=prod]/approve"); s == engine.StepWaitingS || s == engine.StepSucceeded {
		t.Fatalf("prod was asked about after dev failed: %s", s)
	}
	if got := file("prod"); strings.Contains(got, "1.0.2") {
		t.Fatalf("prod received the bad version:\n%s", got)
	}
	var rolledBack bool
	for _, dep := range run.Deployments {
		if dep.Environment == "dev" && dep.Verdict == engine.VerdictRolledBack {
			rolledBack = true
		}
	}
	if !rolledBack {
		t.Fatalf("no dev deployment is rolled back: %+v", run.Deployments)
	}
}
