package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const poolToken = "pool-token-0123456789abcdef0123456789abcdef"

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a bare repository with files on main, and returns its path.
func newRepo(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	bare := filepath.Join(root, name+".git")
	gitIn(t, root, "init", "--bare", "-b", "main", bare)
	work := filepath.Join(root, name+"-seed")
	gitIn(t, root, "clone", bare, work)
	for f, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(work, f)), 0o755)
		os.WriteFile(filepath.Join(work, f), []byte(body), 0o644)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-m", "seed")
	gitIn(t, work, "push", "origin", "HEAD:main")
	return bare
}

// fakeArgo stands in for Argo CD: it keeps the application synced and
// healthy at the tip of the GitOps repository, as an automated application
// would, unless told the next revision is broken.
type fakeArgo struct {
	t    *testing.T
	dyn  *dynfake.FakeDynamicClient
	bare string
	mu   sync.Mutex
	bad  string // a file content that makes the application Degraded
	// frozen stops it from looking at the repository: the application stays
	// at the revision it had, as when Argo CD has not noticed a commit.
	frozen bool
}

var appGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

func newFakeArgo(t *testing.T, bare string) *fakeArgo {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"},
		"spec": map[string]any{"source": map[string]any{"repoURL": bare, "targetRevision": "main", "path": "apps"},
			"syncPolicy": map[string]any{"automated": map[string]any{"selfHeal": true}}},
	}}
	sc := runtime.NewScheme()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(sc, map[schema.GroupVersionResource]string{
		appGVR: "ApplicationList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}:     "RolloutList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysisruns"}: "AnalysisRunList",
	}, app)
	return &fakeArgo{t: t, dyn: dyn, bare: bare}
}

// reconcile does what Argo CD would: compare with the tip and sync.
func (f *fakeArgo) reconcile(ctx context.Context) {
	for ctx.Err() == nil {
		f.mu.Lock()
		frozen := f.frozen
		f.mu.Unlock()
		out, err := exec.Command("git", "-C", f.bare, "rev-parse", "main").Output()
		if err == nil && !frozen {
			tip := strings.TrimSpace(string(out))
			content, _ := exec.Command("git", "-C", f.bare, "show", "main:apps/shop.yaml").Output()
			health := "Healthy"
			f.mu.Lock()
			if f.bad != "" && strings.Contains(string(content), f.bad) {
				health = "Degraded"
			}
			f.mu.Unlock()
			obj, err := f.dyn.Resource(appGVR).Namespace("argocd").Get(ctx, "shop", metav1Get)
			if err == nil {
				obj.Object["status"] = map[string]any{
					"sync":         map[string]any{"status": "Synced", "revision": tip},
					"health":       map[string]any{"status": health},
					"reconciledAt": time.Now().UTC().Format(time.RFC3339),
				}
				f.dyn.Resource(appGVR).Namespace("argocd").Update(ctx, obj, metav1Update)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type deployEnv struct {
	*env
	argo  *fakeArgo
	bare  string
	proj  string
	agent *argocd.Agent
}

// setupDeploy starts a server with an environment and a runner pool, a
// build runner, a deploy runner, and a fake Argo CD.
func setupDeploy(t *testing.T) *deployEnv {
	t.Helper()
	return setupDeployAt(t, testdb.New(t))
}

func setupDeployAt(t *testing.T, db string) *deployEnv {
	t.Helper()
	return setupDeployEnvs(t, db, []engine.Environment{{Name: "prod", Target: "homelab", Projects: []string{"shop"}},
		{Name: "staging", Target: "homelab", Projects: []string{"shop", "web-*"}}})
}

func setupDeployEnvs(t *testing.T, db string, envs []engine.Environment) *deployEnv {
	t.Helper()
	dir := t.TempDir()
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 3 * time.Second, Logger: quiet(), Environments: envs})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet(),
		RunnerPools: []RunnerPool{{Name: "deploy-homelab", Token: poolToken, Targets: []string{"homelab"}}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	de := &deployEnv{env: &env{eng: eng, srv: srv, dir: dir, db: db}}
	de.bare = newRepo(t, dir, "gitops", map[string]string{"apps/shop.yaml": "# the shop\nimage: team/shop:1.0.0 # pinned by Launchway\n"})
	de.proj = newRepo(t, dir, "shop", map[string]string{"README": "shop\n"})
	de.argo = newFakeArgo(t, de.bare)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); de.argo.reconcile(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	de.agent = &argocd.Agent{
		Config: argocd.Config{
			Targets: map[string]argocd.Target{"homelab": {Kubernetes: argocd.Kubernetes{InCluster: true}, ArgoCDNamespace: "argocd",
				Applications: []argocd.AppRule{{Projects: []string{"shop"}, Names: []string{"shop"}}}}},
			Repos: map[string]argocd.Repo{"gitops": {URL: de.bare, Paths: []argocd.PathRule{{Projects: []string{"shop"}, Files: []string{"apps/**"}}}}},
		},
		DataDir: filepath.Join(dir, "deployer"), Secrets: secrets.NewResolver(),
		Connect: func(string, argocd.Target) (*argocd.Cluster, error) {
			return argocd.NewCluster(de.argo.dyn, "argocd"), nil
		},
		SeeTimeout: time.Second, RunningTimeout: time.Second,
	}
	if err := de.agent.Config.Check(); err != nil {
		t.Fatal(err)
	}
	de.runner(t, "builder")
	return de
}

func (de *deployEnv) deployRunner(t *testing.T, name string) context.CancelFunc {
	d := &runner.DeployRunner{Client: runner.NewClient(de.srv.URL, poolToken), Agent: de.agent,
		Info: engine.RunnerInfo{ID: name, Name: name, Targets: []string{"homelab"}}, Log: quiet(),
		ObserveEvery: 200 * time.Millisecond, ReportEvery: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return cancel
}

const shopDeploy = `pipeline: shop
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
  smoke:
    needs: deploy-prod
    image: none
    run: %s
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [smoke]
    timeout: 2m
`

func (de *deployEnv) startShop(t *testing.T, tag, smoke string) string {
	t.Helper()
	p, err := pipeline.Parse([]byte(fmt.Sprintf(shopDeploy, tag, smoke)))
	if err != nil {
		t.Fatal(err)
	}
	sha := gitIn(t, de.proj, "rev-parse", "main")
	r, err := de.eng.CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Source: engine.Source{Kind: "git", URL: de.proj},
		Trigger: engine.Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: sha}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func waitDeployRun(t *testing.T, eng *engine.Engine, id string, d time.Duration) *engine.RunDetail {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		r, err := eng.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status.Terminal() {
			return r
		}
		if time.Now().After(deadline) {
			var b bytes.Buffer
			for _, s := range r.StepList {
				fmt.Fprintf(&b, "%s=%s(%s) ", s.ID, s.Status, s.Error)
			}
			t.Fatalf("run still %s after %s: %s", r.Status, d, b.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func stepByID(r *engine.RunDetail, id string) engine.StepView {
	for _, s := range r.StepList {
		if s.ID == id {
			return s
		}
	}
	return engine.StepView{}
}

func TestDeployEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	de := setupDeploy(t)
	de.deployRunner(t, "deployer-1")

	// A good version: committed, synced, smoke passes.
	id := de.startShop(t, "1.1.0", "true")
	r := waitDeployRun(t, de.eng, id, time.Minute)
	if r.Status != engine.RunSucceeded {
		t.Fatalf("run %s: %+v", r.Status, r.StepList)
	}
	content := gitIn(t, de.dir, "--git-dir", de.bare, "show", "main:apps/shop.yaml")
	if content != "# the shop\nimage: team/shop:1.1.0 # pinned by Launchway" {
		t.Fatalf("file after the deploy:\n%s", content)
	}
	msg := gitIn(t, de.dir, "--git-dir", de.bare, "log", "-1", "--format=%B", "main")
	if !strings.Contains(msg, "Deploy shop 1.1.0 to prod") || !strings.Contains(msg, "Launchway-Deployment: dep_") {
		t.Fatalf("commit message:\n%s", msg)
	}
	if len(r.Deployments) != 1 || r.Deployments[0].Verdict != engine.VerdictDeployed || r.Deployments[0].ObservedBy != "deployer-1" {
		t.Fatalf("deployment: %+v", r.Deployments)
	}

	// Deploying the same version again changes nothing in Git, and is still
	// synced and verified.
	before := gitIn(t, de.dir, "--git-dir", de.bare, "rev-parse", "main")
	r = waitDeployRun(t, de.eng, de.startShop(t, "1.1.0", "true"), time.Minute)
	if r.Status != engine.RunSucceeded || gitIn(t, de.dir, "--git-dir", de.bare, "rev-parse", "main") != before ||
		r.Deployments[0].Changed || r.Deployments[0].Commit != before {
		t.Fatalf("no-change redeploy: %s %+v", r.Status, r.Deployments)
	}

	// A version whose smoke test fails is reverted, and the environment is
	// back on 1.1.0.
	r = waitDeployRun(t, de.eng, de.startShop(t, "1.2.0", "false"), time.Minute)
	if r.Status != engine.RunFailed || stepByID(r, "rollback-prod").Status != engine.StepSucceeded || r.Deployments[0].Verdict != engine.VerdictRolledBack {
		t.Fatalf("rollback run: %s %+v", r.Status, r.Deployments)
	}
	if content := gitIn(t, de.dir, "--git-dir", de.bare, "show", "main:apps/shop.yaml"); !strings.Contains(content, "team/shop:1.1.0") {
		t.Fatalf("file after the rollback:\n%s", content)
	}
	msg = gitIn(t, de.dir, "--git-dir", de.bare, "log", "-1", "--format=%B", "main")
	if !strings.Contains(msg, "Roll back shop in prod to 1.1.0") || !strings.Contains(msg, "Launchway-Revert: dep_") {
		t.Fatalf("revert message:\n%s", msg)
	}
	envs, _ := de.eng.EnvironmentStates(nil)
	if cur := envs[0].Services[0].Current; cur == nil || cur.Version != "1.1.0" {
		t.Fatalf("environment after the rollback: %+v", cur)
	}

	// A version Argo CD reports Degraded fails the deploy step; the
	// rollback reverts it.
	de.argo.mu.Lock()
	de.argo.bad = "team/shop:6.6.6"
	de.argo.mu.Unlock()
	r = waitDeployRun(t, de.eng, de.startShop(t, "6.6.6", "true"), time.Minute)
	if d := stepByID(r, "deploy-prod"); d.Status != engine.StepFailed || !strings.Contains(d.Error, "Degraded") {
		t.Fatalf("degraded deploy: %+v", d)
	}
	if r.Deployments[0].Verdict != engine.VerdictRolledBack {
		t.Fatalf("degraded verdict: %+v", r.Deployments[0])
	}
}

// The deploy runner is killed while it watches a deployment; another one
// takes over once the first's observer lease runs out.
func TestDeployRunnerTakeover(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	de := setupDeploy(t)
	time.Sleep(300 * time.Millisecond) // the fake Argo CD has synced the seed
	de.argo.mu.Lock()
	de.argo.frozen = true // Argo CD does not see the new commit yet
	de.argo.mu.Unlock()
	stopFirst := de.deployRunner(t, "deployer-1")
	id := de.startShop(t, "2.0.0", "true")
	deadline := time.Now().Add(time.Minute)
	for {
		r, _ := de.eng.GetRun(id)
		if len(r.Deployments) == 1 && r.Deployments[0].ObservedBy == "deployer-1" && r.Deployments[0].Phase == "syncing" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never observed waiting for the sync: %+v", r.Deployments)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The first runner dies (it does not say it is leaving: its observer
	// lease must run out).
	stopFirst()
	de.deployRunner(t, "deployer-2")
	de.argo.mu.Lock()
	de.argo.frozen = false
	de.argo.mu.Unlock()
	r := waitDeployRun(t, de.eng, id, time.Minute)
	if r.Status != engine.RunSucceeded || r.Deployments[0].ObservedBy != "deployer-2" {
		t.Fatalf("after the takeover: %s, observed by %q", r.Status, r.Deployments[0].ObservedBy)
	}
}

// Pool tokens: the shared runner token cannot take deploy work, and a
// pool's token takes nothing else.
func TestRunnerPoolTokens(t *testing.T) {
	de := setupDeploy(t)
	call := func(token, path, body string) int {
		req, _ := http.NewRequest("POST", de.srv.URL+"/api/runner/v1/"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	runnerBody := `{"runner": {"id": "x", "name": "x", "targets": ["homelab"]}, "wait_seconds": 0}`
	for _, c := range []struct {
		token, path string
		want        int
	}{
		{"secret", "deploy/lease", http.StatusForbidden},
		{"secret", "deploy/watch", http.StatusForbidden},
		{"secret", "deploy/observe", http.StatusForbidden},
		{poolToken, "lease", http.StatusForbidden},
		{"nope", "deploy/lease", http.StatusUnauthorized},
		{poolToken, "deploy/lease", http.StatusNoContent},
		{poolToken, "deploy/watch", http.StatusOK},
	} {
		if got := call(c.token, c.path, runnerBody); got != c.want {
			t.Errorf("%s with token %.6s: %d, want %d", c.path, c.token, got, c.want)
		}
	}
	// A deploy action's lease cannot be finished with the shared token.
	id := de.startShop(t, "1.0.1", "true")
	var job *engine.Job
	deadline := time.Now().Add(30 * time.Second)
	for job == nil && time.Now().Before(deadline) {
		j, err := runner.NewClient(de.srv.URL, poolToken).LeaseDeploy(context.Background(),
			engine.RunnerInfo{ID: "manual", Name: "manual", Targets: []string{"homelab", "elsewhere"}}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		job = j
	}
	if job == nil {
		t.Fatalf("no deploy action for run %s", id)
	}
	body, _ := json.Marshal(map[string]any{"lease_id": job.LeaseID, "result": map[string]any{"outcome": "succeeded"}})
	if got := call("secret", "finish", string(body)); got != http.StatusForbidden {
		t.Fatalf("finishing a deploy lease with the shared token: %d", got)
	}
	if got := call("secret", "heartbeat", string(body)); got != http.StatusForbidden {
		t.Fatalf("heartbeat of a deploy lease with the shared token: %d", got)
	}
	if got := call(poolToken, "heartbeat", string(body)); got != http.StatusOK {
		t.Fatalf("heartbeat of a deploy lease with the pool token: %d", got)
	}
}

var (
	metav1Get    = metav1.GetOptions{}
	metav1Update = metav1.UpdateOptions{}
	_            = executor.Host{}
)

// TestDeployDemoData leaves a database with deployments in every state, for
// working on the UI: LAUNCHWAY_DEMO_DEPLOY_DB=/path/to/launchway.db. It is
// skipped otherwise.
func TestDeployDemoData(t *testing.T) {
	path := os.Getenv("LAUNCHWAY_DEMO_DEPLOY_DB")
	if path == "" {
		t.Skip("set LAUNCHWAY_DEMO_DEPLOY_DB to a file to write demo data to")
	}
	os.Remove(path)
	t.Setenv(testdb.EnvVar, "")
	de := setupDeployAt(t, path)
	de.deployRunner(t, "deployer-1")
	waitDeployRun(t, de.eng, de.startShop(t, "1.0.0", "true"), time.Minute)
	waitDeployRun(t, de.eng, de.startShop(t, "1.1.0", "true"), time.Minute)
	waitDeployRun(t, de.eng, de.startShop(t, "1.2.0", "false"), time.Minute)
	de.argo.mu.Lock()
	de.argo.bad = "team/shop:1.3.0"
	de.argo.mu.Unlock()
	waitDeployRun(t, de.eng, de.startShop(t, "1.3.0", "true"), time.Minute)
	// One deployment left waiting for Argo CD, and one waiting for its lock.
	de.argo.mu.Lock()
	de.argo.bad, de.argo.frozen = "", true
	de.argo.mu.Unlock()
	open := de.startShop(t, "1.4.0", "true")
	deadline := time.Now().Add(time.Minute)
	for {
		r, _ := de.eng.GetRun(open)
		if len(r.Deployments) == 1 && r.Deployments[0].Phase == "syncing" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the open deployment was never observed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	de.startShop(t, "1.5.0", "true")
	time.Sleep(2 * time.Second)
}
