// Package integration runs deploy and rollback steps against a real kind
// cluster with Argo CD and Argo Rollouts (deploy/test/argo, make test-argo).
// It is skipped unless LAUNCHWAY_TEST_ARGO_CONTEXT names a kind context.
package integration

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
)

const poolToken = "integration-pool-token-0123456789abcdef0123"

// The deploy runner acts as this service account (deploy/test/argo/deployer-rbac.yaml and
// admission-policy.yaml), so the least access it is given is what is tested.
const deployerUser = "system:serviceaccount:argocd:lw-deployer"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type harness struct {
	t        *testing.T
	kctx     string
	dir      string
	db       string
	addr     string
	gitURL   string // through the port-forward
	gitUser  string
	gitPass  string
	project  string // the project's own repository (local, bare)
	proxy    *apiProxy
	agent    *argocd.Agent
	mu       sync.Mutex
	eng      *engine.Engine
	httpSrv  *http.Server
	stopSrv  func()
	log      *slog.Logger
	runnerWG sync.WaitGroup
	policy   engine.PolicyOptions // delivery policies the server judges runs by
	// sources and healthSources, when set, are the health sources the
	// server knows and the deploy runner reads (verify steps).
	sources       []engine.HealthSource
	healthSources *hsource.Sources
}

func (h *harness) kubectl(args ...string) string {
	h.t.Helper()
	out, err := exec.Command("kubectl", append([]string{"--context", h.kctx}, args...)...).CombinedOutput()
	if err != nil {
		h.t.Fatalf("kubectl %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (h *harness) authArgs() []string {
	tok := base64.StdEncoding.EncodeToString([]byte(h.gitUser + ":" + h.gitPass))
	return []string{"-c", "http.extraHeader=Authorization: Basic " + tok}
}

// portForward forwards a free local port to a service and returns it.
func portForward(t *testing.T, kctx, ns, svc string, port int) string {
	t.Helper()
	cmd := exec.Command("kubectl", "--context", kctx, "-n", ns, "port-forward", "svc/"+svc, fmt.Sprintf(":%d", port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	re := regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			go io.Copy(io.Discard, out)
			return m[1]
		}
	}
	t.Fatal("kubectl port-forward printed no port")
	return ""
}

// apiProxy forwards TCP to the cluster's API server, and can be switched
// off, which closes every connection: the API server becomes unreachable.
type apiProxy struct {
	ln       net.Listener
	upstream string
	mu       sync.Mutex
	off      bool
	conns    map[net.Conn]bool
}

func newProxy(t *testing.T, upstream string) *apiProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &apiProxy{ln: ln, upstream: upstream, conns: map[net.Conn]bool{}}
	t.Cleanup(func() { ln.Close(); p.setOff(true) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			off := p.off
			if !off {
				p.conns[c] = true
			}
			p.mu.Unlock()
			if off {
				c.Close()
				continue
			}
			go func() {
				u, err := net.Dial("tcp", p.upstream)
				if err != nil {
					c.Close()
					return
				}
				p.mu.Lock()
				p.conns[u] = true
				p.mu.Unlock()
				go func() { io.Copy(u, c); u.Close() }()
				io.Copy(c, u)
				c.Close()
			}()
		}
	}()
	return p
}

func (p *apiProxy) setOff(off bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.off = off
	if off {
		for c := range p.conns {
			c.Close()
		}
		p.conns = map[net.Conn]bool{}
	}
}

func setup(t *testing.T, policies ...engine.PolicyOptions) *harness {
	kctx := os.Getenv("LAUNCHWAY_TEST_ARGO_CONTEXT")
	if kctx == "" {
		t.Skip("set LAUNCHWAY_TEST_ARGO_CONTEXT to a kind context with deploy/test/argo installed (make test-argo)")
	}
	if !strings.HasPrefix(kctx, "kind-") {
		t.Fatalf("refusing context %q: these tests only run against kind clusters", kctx)
	}
	h := &harness{t: t, kctx: kctx, dir: t.TempDir(),
		gitUser: env("LAUNCHWAY_TEST_ARGO_GIT_USER", "lwtest"), gitPass: env("LAUNCHWAY_TEST_ARGO_GIT_PASSWORD", "lwtest-not-a-secret"),
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	h.db = filepath.Join(h.dir, "launchway.db")
	port := portForward(t, kctx, env("LAUNCHWAY_TEST_ARGO_GIT_NAMESPACE", "lw-git"), env("LAUNCHWAY_TEST_ARGO_GIT_SERVICE", "git"), 8080)
	h.gitURL = "http://127.0.0.1:" + port + "/" + h.gitUser + "/gitops.git"

	// Start from the seed: every application Synced and Healthy on its first version.
	seed := exec.Command("../../../deploy/test/argo/seed.sh", "--context", kctx, h.gitURL)
	seed.Stdout, seed.Stderr = os.Stderr, os.Stderr
	if err := seed.Run(); err != nil {
		t.Fatalf("seeding the test repository: %v", err)
	}

	// The project's own repository, from which build and smoke steps run.
	bare := filepath.Join(h.dir, "shop.git")
	gitRun(t, h.dir, "init", "--bare", "-b", "main", bare)
	work := filepath.Join(h.dir, "shop")
	gitRun(t, h.dir, "clone", bare, work)
	os.WriteFile(filepath.Join(work, "README"), []byte("shop\n"), 0o644)
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "shop")
	gitRun(t, work, "push", "origin", "HEAD:main")
	h.project = bare

	// The cluster's API, through a proxy that can be switched off.
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kctx}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	upstream := strings.TrimPrefix(cfg.Host, "https://")
	h.proxy = newProxy(t, upstream)
	os.Setenv("LW_TEST_GIT_PASSWORD", h.gitPass)
	resolver := secrets.NewResolver()
	resolver.Add("env", secrets.Env{}, nil, nil)
	h.agent = &argocd.Agent{
		Config: argocd.Config{
			Targets: map[string]argocd.Target{"kind": {Kubernetes: argocd.Kubernetes{Context: kctx}, ArgoCDNamespace: "argocd",
				Applications:      []argocd.AppRule{{Projects: []string{"shop"}, Names: []string{"demo", "demo-manual", "plain", "demo-multi"}}},
				RolloutNamespaces: []string{"lw-demo", "lw-demo-manual", "lw-demo-multi"}}},
			Repos: map[string]argocd.Repo{"gitops": {URL: h.gitURL,
				ArgoCDURL:   "http://" + env("LAUNCHWAY_TEST_ARGO_GIT_SERVICE", "git") + "." + env("LAUNCHWAY_TEST_ARGO_GIT_NAMESPACE", "lw-git") + ".svc:8080/" + h.gitUser + "/gitops.git",
				Credentials: &pipeline.SecretRef{Store: "env", Key: "LW_TEST_GIT_PASSWORD"}, Username: h.gitUser,
				Paths: []argocd.PathRule{{Projects: []string{"shop"}, Files: []string{"apps/**"}}}}},
		},
		DataDir: filepath.Join(h.dir, "deployer"), Secrets: resolver,
		Connect: func(string, argocd.Target) (*argocd.Cluster, error) {
			c := *cfg
			c.Host = "https://" + h.proxy.ln.Addr().String()
			c.TLSClientConfig.ServerName = "localhost"
			c.Impersonate.UserName = deployerUser
			c.Timeout = 10 * time.Second
			dyn, err := dynamic.NewForConfig(&c)
			if err != nil {
				return nil, err
			}
			return argocd.NewCluster(dyn, "argocd"), nil
		},
	}
	if err := h.agent.Config.Check(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	if len(policies) > 0 {
		h.policy = policies[0]
	}
	h.startServer(ln)
	t.Cleanup(h.stopServer)
	h.startBuildRunner()
	return h
}

func (h *harness) startServer(ln net.Listener) {
	h.t.Helper()
	if ln == nil {
		var err error
		for i := 0; i < 50; i++ {
			if ln, err = net.Listen("tcp", h.addr); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			h.t.Fatal(err)
		}
	}
	eng, err := engine.Open(engine.Options{Database: h.db, LeaseTTL: 6 * time.Second, Logger: h.log,
		Environments: []engine.Environment{{Name: "kind", Target: "kind", Projects: []string{"shop"}}}, Policy: h.policy, HealthSources: h.sources})
	if err != nil {
		h.t.Fatal(err)
	}
	s := &api.Server{Engine: eng, RunnerToken: "build-token", Actor: "tester", Log: h.log,
		RunnerPools: []api.RunnerPool{{Name: "deploy-kind", Token: poolToken, Targets: []string{"kind"}}}}
	hs := &http.Server{Handler: s.Handler()}
	go hs.Serve(ln)
	h.mu.Lock()
	h.eng, h.httpSrv = eng, hs
	h.stopSrv = func() {
		s.Drain()
		s.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(ctx)
		eng.Close()
	}
	h.mu.Unlock()
}

func (h *harness) stopServer() {
	h.mu.Lock()
	stop := h.stopSrv
	h.stopSrv = nil
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (h *harness) engine() *engine.Engine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.eng
}

func (h *harness) startBuildRunner() {
	r := &runner.Runner{Client: runner.NewClient("http://"+h.addr, "build-token"), Exec: &executor.Host{},
		Info: engine.RunnerInfo{ID: "builder", Name: "builder"}, DataDir: filepath.Join(h.dir, "builder"), Concurrency: 2, Log: h.log}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	h.t.Cleanup(func() { cancel(); <-done })
}

// startDeployRunner starts a deploy runner; stop kills it (without waiting
// for anything it is doing to be handed back).
func (h *harness) startDeployRunner(name string) (stop func()) {
	d := &runner.DeployRunner{Client: runner.NewClient("http://"+h.addr, poolToken), Agent: h.agent,
		Info: engine.RunnerInfo{ID: name, Name: name, Targets: []string{"kind"}}, Log: h.log,
		ObserveEvery: time.Second, ReportEvery: 3 * time.Second, Sources: h.healthSources, Settle: 3 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	h.t.Cleanup(stop)
	return stop
}

// deployRun describes a run of the demo application.
type deployRun struct {
	app, dir, ns string // Argo CD application, its directory under apps/, its namespace
	tag          string
	extra        []string // more changes, as YAML flow mappings
	smoke        string   // the smoke step's script
	windows      string   // sync.windows; default wait
}

const pipelineTmpl = `pipeline: shop
steps:
  build:
    image: none
    run: echo tag=%s >> "$LAUNCHWAY_OUTPUTS"
  deploy:
    kind: deploy
    needs: build
    environment: kind
    service: %s
    version: { output: build.tag }
    timeout: 5m
    argocd:
      application: %s
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/%s/rollout.yaml, image: docker.io/library/busybox, tag: { output: build.tag } }
%s      sync: { windows: %s }
    rollout: { name: demo, namespace: %s }
  smoke:
    needs: deploy
    image: none
    run: %s
  rollback:
    kind: rollback
    deployment: deploy
    needs: [smoke]
    timeout: 5m
`

func (h *harness) start(r deployRun) string {
	h.t.Helper()
	extra := ""
	for _, e := range r.extra {
		extra += "          - " + e + "\n"
	}
	if r.smoke == "" {
		r.smoke = "true"
	}
	if r.windows == "" {
		r.windows = "wait"
	}
	src := fmt.Sprintf(pipelineTmpl, r.tag, r.app, r.app, r.dir, extra, r.windows, r.ns, r.smoke)
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		h.t.Fatalf("%v\n%s", err, src)
	}
	sha := gitRun(h.t, h.dir, "--git-dir", h.project, "rev-parse", "main")
	run, err := h.engine().CreateRun(engine.NewRun{Project: "shop", Pipeline: p, Source: engine.Source{Kind: "git", URL: h.project},
		Trigger: engine.Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: sha}})
	if err != nil {
		h.t.Fatal(err)
	}
	return run.ID
}

// wait waits for a run to end, generously: canaries, analysis jobs and
// Argo CD polls take their time on a small machine.
func (h *harness) wait(id string) *engine.RunDetail {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Minute)
	for {
		r, err := h.engine().GetRun(id)
		if err == nil && r.Status.Terminal() {
			return r
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %s did not end: %s", id, describe(r))
		}
		time.Sleep(time.Second)
	}
}

// until waits for a condition on a run.
func (h *harness) until(id, what string, cond func(*engine.RunDetail) bool) *engine.RunDetail {
	h.t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	for {
		r, err := h.engine().GetRun(id)
		if err == nil && cond(r) {
			return r
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %s: never %s: %s", id, what, describe(r))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func describe(r *engine.RunDetail) string {
	if r == nil {
		return "(no run)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run %s;", r.Status)
	for _, s := range r.StepList {
		fmt.Fprintf(&b, " %s=%s(%s)", s.ID, s.Status, s.Error)
	}
	for _, d := range r.Deployments {
		fmt.Fprintf(&b, "; deployment %s %s %s: %s [observed by %s]", d.ID, d.Verdict, d.Phase, d.Detail, d.ObservedBy)
	}
	return b.String()
}

func step(r *engine.RunDetail, id string) engine.StepView {
	for _, s := range r.StepList {
		if s.ID == id {
			return s
		}
	}
	return engine.StepView{}
}

// rolloutImage returns the image the rollout's stable pods run.
func (h *harness) rolloutImage(ns string) string {
	return h.kubectl("-n", ns, "get", "rollout", "demo", "-o", "jsonpath={.spec.template.spec.containers[0].image}")
}

func (h *harness) rolloutState(ns string) string {
	return h.kubectl("-n", ns, "get", "rollout", "demo", "-o", "jsonpath={.status.phase} {.status.stableRS}={.status.currentPodHash}")
}

func (h *harness) gitShow(path string) string {
	h.t.Helper()
	clone := filepath.Join(h.dir, "inspect")
	os.RemoveAll(clone)
	gitRun(h.t, h.dir, append(h.authArgs(), "clone", "-q", "--branch", "main", h.gitURL, clone)...)
	b, _ := os.ReadFile(filepath.Join(clone, path))
	return string(b)
}

func (h *harness) gitLog(n int) string {
	h.t.Helper()
	h.gitShow("README.md")
	return gitRun(h.t, filepath.Join(h.dir, "inspect"), "log", fmt.Sprintf("-%d", n), "--format=%H %s%n%b")
}

// pushForeign commits an unrelated change to the GitOps repository.
func (h *harness) pushForeign(file, content string) {
	h.t.Helper()
	clone := filepath.Join(h.dir, "foreign")
	os.RemoveAll(clone)
	gitRun(h.t, h.dir, append(h.authArgs(), "clone", "-q", "--branch", "main", h.gitURL, clone)...)
	os.MkdirAll(filepath.Dir(filepath.Join(clone, file)), 0o755)
	os.WriteFile(filepath.Join(clone, file), []byte(content), 0o644)
	gitRun(h.t, clone, "add", "-A")
	gitRun(h.t, clone, "commit", "-q", "-m", "a foreign change")
	gitRun(h.t, clone, append(h.authArgs(), "push", "-q", "origin", "HEAD:main")...)
}

const (
	goodA = "1.36.1" // the seed's tag
	goodB = "1.37.0"
)

var demo = deployRun{app: "demo", dir: "demo", ns: "lw-demo"}

func with(r deployRun, tag string, extra ...string) deployRun {
	r.tag, r.extra = tag, extra
	return r
}

// TestArgo runs the scenarios in order on one cluster: each starts from
// where the one before left the demo application, healthy.
func TestArgo(t *testing.T) {
	h := setup(t)
	stopFirst := h.startDeployRunner("deployer-1")

	t.Run("happy canary", func(t *testing.T) {
		r := h.wait(h.start(with(demo, goodB)))
		if r.Status != engine.RunSucceeded || r.Deployments[0].Verdict != engine.VerdictDeployed || !r.Deployments[0].Changed {
			t.Fatalf("%s", describe(r))
		}
		if img := h.rolloutImage("lw-demo"); img != "docker.io/library/busybox:"+goodB {
			t.Fatalf("rollout image %s", img)
		}
		if st := h.rolloutState("lw-demo"); !strings.HasPrefix(st, "Healthy ") {
			t.Fatalf("rollout %s", st)
		}
		if log := h.gitLog(1); !strings.Contains(log, "Deploy demo "+goodB+" to kind") || !strings.Contains(log, "Launchway-Deployment: "+r.Deployments[0].ID) {
			t.Fatalf("commit:\n%s", log)
		}
		var sawCanary bool
		for _, e := range r.Deployments[0].Timeline {
			sawCanary = sawCanary || e.Phase == "rollout_paused" || strings.Contains(e.Detail, "analysis running") || strings.Contains(e.Detail, "canary at 50%")
		}
		if !sawCanary {
			t.Errorf("the timeline never showed the canary: %+v", r.Deployments[0].Timeline)
		}
	})

	t.Run("no-change redeploy", func(t *testing.T) {
		before := h.gitLog(1)
		r := h.wait(h.start(with(demo, goodB)))
		if r.Status != engine.RunSucceeded || r.Deployments[0].Changed || h.gitLog(1) != before {
			t.Fatalf("%s", describe(r))
		}
	})

	t.Run("analysis fails, revert to healthy on the previous version", func(t *testing.T) {
		r := h.wait(h.start(with(demo, goodA, `{ file: apps/demo/rollout.yaml, set: "spec.strategy.canary.steps[2].analysis.args[0].value", value: fail }`)))
		d := step(r, "deploy")
		if d.Status != engine.StepFailed || !strings.Contains(d.Error, "aborted") || !strings.Contains(d.Error, "analysis") {
			t.Fatalf("deploy step: %s", describe(r))
		}
		if step(r, "rollback").Status != engine.StepSucceeded || r.Deployments[0].Verdict != engine.VerdictRolledBack || r.Status != engine.RunFailed {
			t.Fatalf("%s", describe(r))
		}
		if img := h.rolloutImage("lw-demo"); img != "docker.io/library/busybox:"+goodB {
			t.Fatalf("after the rollback the rollout runs %s", img)
		}
		if st := h.rolloutState("lw-demo"); !strings.HasPrefix(st, "Healthy ") {
			t.Fatalf("rollout %s", st)
		}
		if body := h.gitShow("apps/demo/rollout.yaml"); !strings.Contains(body, `value: "pass"`) {
			t.Fatalf("the revert did not restore the verdict:\n%s", body)
		}
	})

	t.Run("degraded (crash loop), reverted", func(t *testing.T) {
		r := h.wait(h.start(with(demo, goodA, `{ file: apps/demo/rollout.yaml, set: "spec.template.spec.containers[0].env[0].value", value: crash }`)))
		if d := step(r, "deploy"); d.Status != engine.StepFailed || !(strings.Contains(d.Error, "Degraded") || strings.Contains(d.Error, "aborted")) {
			t.Fatalf("deploy step: %s", describe(r))
		}
		if r.Deployments[0].Verdict != engine.VerdictRolledBack {
			t.Fatalf("%s", describe(r))
		}
		if st := h.rolloutState("lw-demo"); !strings.HasPrefix(st, "Healthy ") {
			t.Fatalf("rollout %s", st)
		}
	})

	t.Run("a foreign commit between the bump and the revert", func(t *testing.T) {
		id := h.start(func() deployRun { r := with(demo, goodA); r.smoke = "sleep 5; exit 1"; return r }())
		h.until(id, "deployed", func(r *engine.RunDetail) bool { return step(r, "deploy").Status == engine.StepSucceeded })
		h.pushForeign("notes/foreign.txt", "not Launchway's\n")
		r := h.wait(id)
		if step(r, "rollback").Status != engine.StepSucceeded || r.Deployments[0].Verdict != engine.VerdictRolledBack {
			t.Fatalf("%s", describe(r))
		}
		if h.gitShow("notes/foreign.txt") != "not Launchway's\n" {
			t.Fatal("the foreign commit was lost")
		}
		if img := h.rolloutImage("lw-demo"); img != "docker.io/library/busybox:"+goodB {
			t.Fatalf("after the rollback the rollout runs %s", img)
		}
	})

	t.Run("deploy runner killed mid-observation; another takes over", func(t *testing.T) {
		id := h.start(with(demo, goodA, `{ file: apps/demo/rollout.yaml, set: "spec.strategy.canary.steps[2].analysis.args[1].value", value: "25" }`))
		h.until(id, "observed by deployer-1", func(r *engine.RunDetail) bool {
			return len(r.Deployments) > 0 && r.Deployments[0].ObservedBy == "deployer-1" && step(r, "deploy").WaitKind == engine.WaitObserve
		})
		stopFirst()
		h.startDeployRunner("deployer-2")
		r := h.wait(id)
		if r.Status != engine.RunSucceeded || r.Deployments[0].ObservedBy != "deployer-2" {
			t.Fatalf("%s", describe(r))
		}
	})

	t.Run("server restarted mid-wait", func(t *testing.T) {
		id := h.start(with(demo, goodB, `{ file: apps/demo/rollout.yaml, set: "spec.strategy.canary.steps[2].analysis.args[1].value", value: "0" }`))
		h.until(id, "watching", func(r *engine.RunDetail) bool { return step(r, "deploy").WaitKind == engine.WaitObserve })
		h.stopServer()
		time.Sleep(5 * time.Second)
		h.startServer(nil)
		r := h.wait(id)
		if r.Status != engine.RunSucceeded {
			t.Fatalf("%s", describe(r))
		}
	})

	t.Run("API server unreachable, then back", func(t *testing.T) {
		// A long analysis keeps the canary going while the API is away.
		id := h.start(with(demo, goodA, `{ file: apps/demo/rollout.yaml, set: "spec.strategy.canary.steps[2].analysis.args[1].value", value: "40" }`))
		h.until(id, "watching", func(r *engine.RunDetail) bool { return step(r, "deploy").WaitKind == engine.WaitObserve })
		h.proxy.setOff(true)
		r := h.until(id, "unobserved", func(r *engine.RunDetail) bool {
			return len(r.Deployments) > 0 && r.Deployments[0].Phase == "unobserved"
		})
		if !strings.Contains(r.Deployments[0].Detail, "could not see the application") {
			t.Fatalf("detail: %s", r.Deployments[0].Detail)
		}
		if step(r, "deploy").Status.Terminal() {
			t.Fatalf("an unreachable API ended the step: %s", describe(r))
		}
		time.Sleep(5 * time.Second)
		h.proxy.setOff(false)
		r = h.wait(id)
		if r.Status != engine.RunSucceeded {
			t.Fatalf("%s", describe(r))
		}
	})

	t.Run("deny sync window: fail, then wait until it closes", func(t *testing.T) {
		manual := deployRun{app: "demo-manual", dir: "manual", ns: "lw-demo-manual"}
		window := `{"spec":{"syncWindows":[{"kind":"deny","schedule":"* * * * *","duration":"1h","applications":["demo-manual"],"manualSync":false}]}}`
		h.kubectl("-n", "argocd", "patch", "appproject", "lw-test", "--type=merge", "-p", window)
		defer h.kubectl("-n", "argocd", "patch", "appproject", "lw-test", "--type=merge", "-p", `{"spec":{"syncWindows":null}}`)
		f := with(manual, goodB)
		f.windows = "fail"
		r := h.wait(h.start(f))
		if d := step(r, "deploy"); d.Status != engine.StepFailed || !strings.Contains(d.Error, "deny sync window") {
			t.Fatalf("windows: fail: %s", describe(r))
		}
		if op := h.kubectl("-n", "argocd", "get", "application", "demo-manual", "-o", "jsonpath={.operation}"); op != "" {
			t.Fatalf("an operation was set while the window was active: %s", op)
		}
		// The rollback reverted the commit; nothing was synced, so nothing
		// changed in the cluster.
		if img := h.rolloutImage("lw-demo-manual"); img != "docker.io/library/busybox:"+goodA {
			t.Fatalf("the cluster changed under a deny window: %s", img)
		}
		id := h.start(with(manual, goodB))
		h.until(id, "held by the window", func(r *engine.RunDetail) bool {
			lines, _, _ := h.engine().Logs(id, "deploy", 0, 0, 0)
			for _, l := range lines {
				if strings.Contains(l.Text, "Waiting until a window allows it") {
					return true
				}
			}
			return false
		})
		h.kubectl("-n", "argocd", "patch", "appproject", "lw-test", "--type=merge", "-p", `{"spec":{"syncWindows":null}}`)
		r = h.wait(id)
		if r.Status != engine.RunSucceeded || h.rolloutImage("lw-demo-manual") != "docker.io/library/busybox:"+goodB {
			t.Fatalf("after the window: %s", describe(r))
		}
	})

	t.Run("sync fails, then the revert syncs", func(t *testing.T) {
		manual := deployRun{app: "demo-manual", dir: "manual", ns: "lw-demo-manual"}
		r := h.wait(h.start(with(manual, goodB, `{ file: apps/manual/service.yaml, set: "spec.ports[0].targetPort", value: "not a port!" }`)))
		d := step(r, "deploy")
		if d.Status != engine.StepFailed || !strings.Contains(d.Error, "could not sync") {
			t.Fatalf("deploy step: %s", describe(r))
		}
		if step(r, "rollback").Status != engine.StepSucceeded || r.Deployments[0].Verdict != engine.VerdictRolledBack {
			t.Fatalf("%s", describe(r))
		}
	})
}
