package argocd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

func TestConfigCheck(t *testing.T) {
	good := func() Config {
		return Config{
			Targets: map[string]Target{"homelab": {Kubernetes: Kubernetes{Context: "kind-x"},
				Applications: []AppRule{{Projects: []string{"shop"}, Names: []string{"shop-*"}}}}},
			Repos: map[string]Repo{"gitops": {URL: "https://git.example/gitops.git", Paths: []PathRule{{Projects: []string{"shop"}, Files: []string{"apps/shop/**"}}}}},
		}
	}
	c := good()
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if c.Targets["homelab"].ArgoCDNamespace != "argocd" || c.Repos["gitops"].Username != "launchway" || c.Repos["gitops"].ArgoCDURL != c.Repos["gitops"].URL {
		t.Fatalf("defaults: %+v", c)
	}
	for name, edit := range map[string]func(*Config){
		"no cluster named": func(c *Config) { t := c.Targets["homelab"]; t.Kubernetes = Kubernetes{}; c.Targets["homelab"] = t },
		"both":             func(c *Config) { t := c.Targets["homelab"]; t.Kubernetes.InCluster = true; c.Targets["homelab"] = t },
		"no applications":  func(c *Config) { t := c.Targets["homelab"]; t.Applications = nil; c.Targets["homelab"] = t },
		"credentials in url": func(c *Config) {
			r := c.Repos["gitops"]
			r.URL = "https://user:pass@git.example/x.git"
			c.Repos["gitops"] = r
		},
		"no paths": func(c *Config) { r := c.Repos["gitops"]; r.Paths = nil; c.Repos["gitops"] = r },
	} {
		c := good()
		edit(&c)
		if err := c.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&Config{}).Check(); err == nil {
		t.Error("no targets accepted")
	}
}

func TestRules(t *testing.T) {
	r := Repo{Paths: []PathRule{{Projects: []string{"shop"}, Files: []string{"apps/shop/**", "helm-values/shop.yaml"}}}}
	for file, want := range map[string]bool{
		"apps/shop/base.yaml": true, "apps/shop/a/b/c.yaml": true, "apps/shop": true, "helm-values/shop.yaml": true,
		"apps/other/base.yaml": false, "helm-values/other.yaml": false, "apps/shopping/x.yaml": false,
	} {
		if got := r.FileAllowed("shop", file); got != want {
			t.Errorf("FileAllowed(%s) = %v", file, got)
		}
	}
	if r.FileAllowed("other", "apps/shop/base.yaml") {
		t.Error("another project may change the shop's files")
	}
	tg := Target{Applications: []AppRule{{Projects: []string{"mcp-*"}, Names: []string{"mcp-*"}}}, RolloutNamespaces: []string{"mcp"}}
	if !tg.AppAllowed("mcp-a", "mcp-x") || tg.AppAllowed("shop", "mcp-x") || tg.AppAllowed("mcp-a", "shop") {
		t.Error("AppAllowed")
	}
	if !tg.RolloutAllowed("mcp") || tg.RolloutAllowed("kube-system") {
		t.Error("RolloutAllowed")
	}
}

var appGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

func fakeCluster(t *testing.T, app map[string]any, windows ...map[string]any) (*dynfake.FakeDynamicClient, *Cluster) {
	t.Helper()
	ws := []any{}
	for _, w := range windows {
		ws = append(ws, w)
	}
	proj := map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "AppProject",
		"metadata": map[string]any{"name": "default", "namespace": "argocd"}, "spec": map[string]any{"syncWindows": ws}}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		appGVR: "ApplicationList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}:     "RolloutList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysisruns"}: "AnalysisRunList",
		appProjects: "AppProjectList",
	}, &unstructured.Unstructured{Object: app}, &unstructured.Unstructured{Object: proj})
	return dyn, NewCluster(dyn, "argocd")
}

// bareRepo makes a bare repository with one commit on main and returns its
// path and that commit.
func bareRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(d string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	bare := filepath.Join(dir, "g.git")
	run(dir, "init", "--bare", "-b", "main", bare)
	work := filepath.Join(dir, "w")
	run(dir, "clone", bare, work)
	os.WriteFile(filepath.Join(work, "a.yaml"), []byte("image: team/shop:1\n"), 0o644)
	run(work, "add", "-A")
	run(work, "commit", "-m", "seed")
	run(work, "push", "origin", "HEAD:main")
	return bare, run(work, "rev-parse", "HEAD")
}

func agentFor(t *testing.T, bare string, cl *Cluster) *Agent {
	t.Helper()
	a := &Agent{
		Config: Config{
			Targets: map[string]Target{"homelab": {Kubernetes: Kubernetes{InCluster: true},
				Applications: []AppRule{{Projects: []string{"shop"}, Names: []string{"shop"}}}, RolloutNamespaces: []string{"shop"}}},
			Repos: map[string]Repo{"gitops": {URL: bare, Paths: []PathRule{{Projects: []string{"shop"}, Files: []string{"**"}}}}},
		},
		DataDir: t.TempDir(), Secrets: secrets.NewResolver(),
		Connect:    func(string, Target) (*Cluster, error) { return cl, nil },
		SeeTimeout: 200 * time.Millisecond, RunningTimeout: 200 * time.Millisecond,
	}
	if err := a.Config.Check(); err != nil {
		t.Fatal(err)
	}
	return a
}

func job(request string, resources ...pipeline.Resource) *engine.Job {
	return &engine.Job{Project: "shop", Deploy: &engine.DeployJob{Action: engine.ActionApply, Deployment: "dep_1", Target: "homelab",
		Application: "shop", Repo: "gitops", Branch: "main", Sync: pipeline.Sync{Request: request, Resources: resources, Windows: "wait"}}}
}

// patches returns the merge patches sent to the application.
func patches(dyn *dynfake.FakeDynamicClient) []map[string]any {
	var out []map[string]any
	for _, a := range dyn.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok {
			var m map[string]any
			json.Unmarshal(p.GetPatch(), &m)
			out = append(out, m)
		}
	}
	return out
}

func TestRequestSync(t *testing.T) {
	bare, tip := bareRepo(t)
	app := func(auto bool, sources bool) map[string]any {
		spec := map[string]any{"source": map[string]any{"repoURL": bare, "targetRevision": "main"}}
		status := map[string]any{"sync": map[string]any{"status": "OutOfSync", "revision": tip}}
		if sources {
			spec = map[string]any{"sources": []any{
				map[string]any{"repoURL": "https://charts.example", "chart": "x"},
				map[string]any{"repoURL": bare, "targetRevision": "main"},
			}}
			status = map[string]any{"sync": map[string]any{"status": "OutOfSync", "revisions": []any{"1.0.0", tip}}}
		}
		if auto {
			spec["syncPolicy"] = map[string]any{"automated": map[string]any{}}
		}
		return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": "shop", "namespace": "argocd", "resourceVersion": "7"}, "spec": spec, "status": status}
	}
	logs := func(string, ...any) {}
	ctx := context.Background()

	// auto with an automated application: only a refresh.
	dyn, cl := fakeCluster(t, app(true, false))
	got, err := agentFor(t, bare, cl).requestSync(ctx, job("auto"), tip, logs)
	if err != nil || !strings.Contains(got, "syncs by itself") {
		t.Fatalf("auto/automated: %q %v", got, err)
	}
	ps := patches(dyn)
	if len(ps) != 1 || ps[0]["operation"] != nil || ps[0]["metadata"].(map[string]any)["annotations"].(map[string]any)[RefreshAnnotation] != "normal" {
		t.Fatalf("patches: %v", ps)
	}

	// never: nothing but the refresh.
	dyn, cl = fakeCluster(t, app(false, false))
	if got, err := agentFor(t, bare, cl).requestSync(ctx, job("never"), tip, logs); err != nil || !strings.Contains(got, "never") {
		t.Fatalf("never: %q %v", got, err)
	}

	// auto without automated sync: a sync at the revision Argo CD sees,
	// with the resourceVersion read, selective when resources are listed.
	dyn, cl = fakeCluster(t, app(false, false))
	res := pipeline.Resource{Group: "argoproj.io", Kind: "Rollout", Name: "shop", Namespace: "shop"}
	got, err = agentFor(t, bare, cl).requestSync(ctx, job("auto", res), tip, logs)
	if err != nil || !strings.HasPrefix(got, "requested") {
		t.Fatalf("auto/manual: %q %v", got, err)
	}
	ps = patches(dyn)
	op := ps[len(ps)-1]
	sync := op["operation"].(map[string]any)["sync"].(map[string]any)
	if sync["revision"] != tip || op["metadata"].(map[string]any)["resourceVersion"] != "7" || len(sync["resources"].([]any)) != 1 ||
		op["operation"].(map[string]any)["initiatedBy"].(map[string]any)["username"] != "launchway" {
		t.Fatalf("sync patch: %v", op)
	}

	// A multi-source application: only the write-back source's revision.
	dyn, cl = fakeCluster(t, app(false, true))
	if _, err := agentFor(t, bare, cl).requestSync(ctx, job("always"), tip, logs); err != nil {
		t.Fatal(err)
	}
	ps = patches(dyn)
	sync = ps[len(ps)-1]["operation"].(map[string]any)["sync"].(map[string]any)
	if revs := sync["revisions"].([]any); len(revs) != 2 || revs[0] != "" || revs[1] != tip || sync["sourcePositions"] != nil || sync["revision"] != nil {
		t.Fatalf("multi-source sync: %v", sync)
	}

	// An application that does not deploy from the repository is refused.
	other := app(false, false)
	other["spec"].(map[string]any)["source"].(map[string]any)["repoURL"] = "https://elsewhere.example/x.git"
	_, cl = fakeCluster(t, other)
	if _, err := agentFor(t, bare, cl).requestSync(ctx, job("always"), tip, logs); err == nil || !strings.Contains(err.Error(), "has no source from") {
		t.Fatalf("another repository: %v", err)
	}
	// A missing application is an error saying so.
	_, cl = fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "other", "namespace": "argocd"}})
	if _, err := agentFor(t, bare, cl).requestSync(ctx, job("always"), tip, logs); err == nil || !strings.Contains(err.Error(), "no application shop") {
		t.Fatalf("missing: %v", err)
	}
}

// The agent refuses what its configuration does not allow, before touching
// Git or the cluster.
func TestAgentRefusals(t *testing.T) {
	bare, _ := bareRepo(t)
	_, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"}})
	a := agentFor(t, bare, cl)
	r := a.Config.Repos["gitops"]
	r.Paths = []PathRule{{Projects: []string{"shop"}, Files: []string{"apps/shop/**"}}}
	a.Config.Repos["gitops"] = r
	for _, c := range []struct {
		edit func(j *engine.Job)
		want string
	}{
		{func(j *engine.Job) { j.Project = "other" }, "project other may not deploy application shop"},
		{func(j *engine.Job) { j.Deploy.Target = "prod-eu" }, "does not serve target prod-eu"},
		{func(j *engine.Job) { j.Deploy.Repo = "elsewhere" }, "repository elsewhere is not in this deploy runner's configuration"},
		{func(j *engine.Job) {
			j.Deploy.Changes = []engine.DeployChange{{File: "apps/other/a.yaml", Set: "a", Value: "b"}}
		}, "may not change apps/other/a.yaml"},
	} {
		j := job("auto")
		c.edit(j)
		if _, err := a.Apply(context.Background(), j, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("got %v, want %s", err, c.want)
		}
	}
}

func TestSyncWindowsHoldTheRequest(t *testing.T) {
	bare, tip := bareRepo(t)
	app := map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd", "resourceVersion": "7"},
		"spec":     map[string]any{"source": map[string]any{"repoURL": bare, "targetRevision": "main"}, "destination": map[string]any{"namespace": "shop"}},
		"status":   map[string]any{"sync": map[string]any{"status": "OutOfSync", "revision": tip}}}
	deny := map[string]any{"kind": "deny", "schedule": "* * * * *", "duration": "1h", "applications": []any{"shop"}}
	logs := func(string, ...any) {}

	// fail: refused, and no operation is set.
	dyn, cl := fakeCluster(t, app, deny)
	a := agentFor(t, bare, cl)
	j := job("always")
	j.Deploy.Sync.Windows = "fail"
	if _, err := a.requestSync(context.Background(), j, tip, logs); err == nil || !strings.Contains(err.Error(), "deny sync window") {
		t.Fatalf("windows: fail: %v", err)
	}
	for _, p := range patches(dyn) {
		if p["operation"] != nil {
			t.Fatalf("an operation was set while a window blocked it: %v", p)
		}
	}
	// wait: no operation until the step's time runs out.
	dyn, cl = fakeCluster(t, app, deny)
	a = agentFor(t, bare, cl)
	a.WindowEvery = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := a.requestSync(ctx, job("always"), tip, logs); err == nil || !strings.Contains(err.Error(), "before the step's deadline") {
		t.Fatalf("windows: wait: %v", err)
	}
	for _, p := range patches(dyn) {
		if p["operation"] != nil {
			t.Fatalf("an operation was set while a window blocked it: %v", p)
		}
	}
	// A manual sync passes a deny window that allows manual syncs.
	deny["manualSync"] = true
	_, cl = fakeCluster(t, app, deny)
	if got, err := agentFor(t, bare, cl).requestSync(context.Background(), job("always"), tip, logs); err != nil || !strings.HasPrefix(got, "requested") {
		t.Fatalf("manualSync window: %q %v", got, err)
	}
}

func TestCanSync(t *testing.T) {
	at := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC) // a Monday
	w := func(kind, sched, dur string, manual bool) Window {
		d, _ := time.ParseDuration(dur)
		return Window{Kind: kind, Schedule: sched, Duration: d, Applications: []string{"shop*"}, ManualSync: manual}
	}
	for _, tc := range []struct {
		name    string
		windows []Window
		manual  bool
		allowed bool
	}{
		{"no windows", nil, false, true},
		{"deny now", []Window{w("deny", "0 14 * * *", "1h", false)}, false, false},
		{"deny over", []Window{w("deny", "0 13 * * *", "1h", false)}, false, true},
		{"deny on weekdays", []Window{w("deny", "0 9 * * mon-fri", "8h", false)}, false, false},
		{"deny on weekends", []Window{w("deny", "0 0 * * sat,sun", "24h", false)}, false, true},
		{"deny, manual allowed", []Window{w("deny", "0 14 * * *", "1h", true)}, true, true},
		{"deny, manual allowed, but automatic", []Window{w("deny", "0 14 * * *", "1h", true)}, false, false},
		{"allow open", []Window{w("allow", "*/15 * * * *", "20m", false)}, false, true},
		{"allow closed", []Window{w("allow", "0 2 * * *", "1h", false)}, false, false},
		{"allow closed, manual allowed", []Window{w("allow", "0 2 * * *", "1h", true)}, true, true},
		{"deny wins over allow", []Window{w("allow", "* * * * *", "1h", false), w("deny", "30 14 16 11 *", "1m", false)}, false, false},
		{"another application", []Window{{Kind: "deny", Schedule: "* * * * *", Duration: time.Hour, Applications: []string{"web"}}}, false, true},
		{"by namespace", []Window{{Kind: "deny", Schedule: "* * * * *", Duration: time.Hour, Namespaces: []string{"sho?"}}}, false, false},
	} {
		v, err := CanSync(tc.windows, "shop", "shop", []string{"https://kubernetes.default.svc"}, tc.manual, at)
		if err != nil || v.Allowed != tc.allowed {
			t.Errorf("%s: %+v %v", tc.name, v, err)
		}
	}
	if _, err := parseCron("* * *"); err == nil {
		t.Error("a short schedule was taken")
	}
	if _, err := parseCron("61 * * * *"); err == nil {
		t.Error("minute 61 was taken")
	}
	// Time zones.
	ny := Window{Kind: "deny", Schedule: "0 9 * * *", Duration: time.Hour, TimeZone: "America/New_York", Applications: []string{"*"}}
	if on, _ := ny.active(time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)); !on { // 09:30 in New York
		t.Error("the window in New York's time is not open")
	}
}
