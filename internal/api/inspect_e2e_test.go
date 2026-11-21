package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/deploy/inspect"
	"github.com/jk-magic-creations/launchway/internal/deploy/inspect/inspectfake"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// fakeLogs gives each pod two lines (one with a planted secret), and when
// followed a new line every 60 ms.
type fakeLogs struct{}

func (fakeLogs) Open(ctx context.Context, ns, pod, c string, o inspect.LogOptions) (io.ReadCloser, error) {
	stamp := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339Nano) }
	first := fmt.Sprintf("%s hello from %s\n%s login password=%s\n", stamp(-2*time.Second), pod, stamp(-time.Second), inspectfake.CanaryPassword)
	if !o.Follow {
		return io.NopCloser(strings.NewReader(first)), nil
	}
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		pw.Write([]byte(first))
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(60 * time.Millisecond):
			}
			if _, err := fmt.Fprintf(pw, "%s tick %d token=%s\n", stamp(0), i, inspectfake.CanaryToken); err != nil {
				return
			}
		}
	}()
	return pr, nil
}

var metav1Create = metav1.CreateOptions{}

type inspectEnv struct {
	*deployEnv
	runID, dep string
	srv2       *httptest.Server
	eng2       *engine.Engine
}

// setupInspect runs a delivery to the fake Argo CD, then starts a deploy
// runner that does nothing but answer inspect requests against a fake
// cluster holding the canary fixture, connected to the first server; a
// second server on the same database answers the viewers.
func setupInspect(t *testing.T, cfg argocd.Inspect) *inspectEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	de := setupDeploy(t)
	stopDeployer := de.deployRunner(t, "deployer-1")
	r := waitDeployRun(t, de.eng, de.startShop(t, "1.1.0", "true"), time.Minute)
	if r.Status != engine.RunSucceeded || len(r.Deployments) != 1 {
		t.Fatalf("run %s: %+v", r.Status, r.StepList)
	}
	stopDeployer() // it would answer inspect requests too, without the settings under test
	fake := inspectfake.New(inspect.KindLists(), false)
	if _, err := fake.Resource(appGVR).Namespace("argocd").Create(context.Background(), inspectfake.Application(), metav1Create); err != nil {
		t.Fatal(err)
	}
	agent := &argocd.Agent{
		Config: argocd.Config{Targets: map[string]argocd.Target{"homelab": {Kubernetes: argocd.Kubernetes{InCluster: true}, ArgoCDNamespace: "argocd",
			Applications: []argocd.AppRule{{Projects: []string{"shop"}, Names: []string{"shop"}}}, RolloutNamespaces: []string{"shop"}, Inspect: cfg}}},
		DataDir: t.TempDir(), Secrets: secrets.NewResolver(),
		Connect: func(string, argocd.Target) (*argocd.Cluster, error) {
			c := argocd.NewCluster(fake, "argocd")
			c.Logs = fakeLogs{}
			return c, nil
		},
	}
	d := &runner.DeployRunner{Client: runner.NewClient(de.srv.URL, poolToken), Agent: agent,
		Info: engine.RunnerInfo{ID: "inspector", Name: "inspector", Targets: []string{"homelab"}}, Log: quiet()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.RunInspections(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	// A second server on the same database serves the viewers.
	eng2, err := engine.Open(engine.Options{Database: de.db, Logger: quiet(), Environments: de.eng.Environments()})
	if err != nil {
		t.Fatal(err)
	}
	s2 := &Server{Engine: eng2, RunnerToken: "secret", Actor: "tester", Log: quiet(),
		RunnerPools: []RunnerPool{{Name: "deploy-homelab", Token: poolToken, Targets: []string{"homelab"}}}}
	srv2 := httptest.NewServer(s2.Handler())
	t.Cleanup(func() { srv2.Close(); eng2.Close() })
	return &inspectEnv{deployEnv: de, runID: r.ID, dep: r.Deployments[0].ID, srv2: srv2, eng2: eng2}
}

func (ie *inspectEnv) url(path string) string {
	return ie.srv2.URL + "/api/v1/runs/" + ie.runID + "/deployments/" + ie.dep + "/inspect" + path
}

func inspGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func inspPost(t *testing.T, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Launchway-CSRF", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The tree, a manifest and a log tail asked of one server, answered by a
// runner connected to another; nothing secret comes out; controls need a
// reason and respect the Airfield's bounds.
func TestInspectThroughTwoServers(t *testing.T) {
	ie := setupInspect(t, argocd.Inspect{Read: true, Logs: true, Control: true, ScaleMax: 10})
	code, body := inspGet(t, ie.url(""))
	if code != 200 {
		t.Fatalf("tree: %d %s", code, body)
	}
	for _, s := range []string{inspectfake.CanaryPassword, inspectfake.CanarySecret, inspectfake.CanaryToken, inspectfake.Base64Of(inspectfake.CanarySecret)} {
		if strings.Contains(body, s) {
			t.Fatalf("the tree carries %q", s)
		}
	}
	var v TreeView
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.Tree == nil || len(v.Tree.Nodes) < 15 || v.Tree.Rollout == nil || v.Tree.Rollout.Strategy != "canary" {
		t.Fatalf("tree view: %v %+v", err, v.Tree)
	}
	if !v.CanLogs || !v.CanControl || len(v.Actions) == 0 {
		t.Fatalf("rights: %+v", v)
	}

	code, body = inspGet(t, ie.url("/manifest?object=ConfigMap/shop/shop-config"))
	if code != 200 || strings.Contains(body, inspectfake.CanaryPassword) || !strings.Contains(body, "LOG_LEVEL") {
		t.Fatalf("manifest: %d %s", code, body)
	}
	if code, body = inspGet(t, ie.url("/manifest?object=ConfigMap/shop/someone-elses")); code != http.StatusBadGateway || !strings.Contains(body, "does not belong") {
		t.Fatalf("an object outside the release: %d %s", code, body)
	}

	code, body = inspGet(t, ie.url("/logs?role=canary"))
	if code != 200 || !strings.Contains(body, "canary/shop-bbb-1") || strings.Contains(body, inspectfake.CanaryPassword) || !strings.Contains(body, "hello from shop-bbb-1") {
		t.Fatalf("logs: %d %s", code, body)
	}

	// Controls: a reason is required; scale respects the HPA and bounds.
	if code, body = inspPost(t, ie.url("/control"), `{"action":"restart","object":"Rollout/shop/shop"}`); code != http.StatusBadRequest {
		t.Fatalf("restart without a reason: %d %s", code, body)
	}
	if code, body = inspPost(t, ie.url("/control"), `{"action":"restart","object":"Rollout/shop/shop","reason":"stuck pod"}`); code != 200 || !strings.Contains(body, "applied") {
		t.Fatalf("restart: %d %s", code, body)
	}
	if code, body = inspPost(t, ie.url("/control"), `{"action":"scale","object":"Rollout/shop/shop","replicas":5,"reason":"load"}`); code != http.StatusConflict || !strings.Contains(body, "autoscaler") {
		t.Fatalf("scale with an HPA: %d %s", code, body)
	}
	if code, body = inspPost(t, ie.url("/control"), `{"action":"delete","object":"Rollout/shop/shop","reason":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown action: %d %s", code, body)
	}
}

// A followed log reaches a viewer on the other server, redacted, and ends
// when the viewer leaves.
func TestFollowedLogsAcrossTwoServers(t *testing.T) {
	ie := setupInspect(t, argocd.Inspect{Read: true, Logs: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ie.url("/logs/stream?pod=shop-bbb-1"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", err, resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	got := 0
	var all strings.Builder
	for sc.Scan() && got < 4 {
		line := sc.Text()
		all.WriteString(line + "\n")
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "tick") {
			got++
		}
	}
	resp.Body.Close()
	if got < 4 {
		t.Fatalf("only %d ticks arrived:\n%s", got, all.String())
	}
	if s := all.String(); strings.Contains(s, inspectfake.CanaryToken) || strings.Contains(s, inspectfake.CanaryPassword) {
		t.Fatalf("the stream carries a secret:\n%s", s)
	}
	// The request ends when the viewer is gone (the server cancels it, and
	// the runner's next chunk is told to stop).
	deadline := time.Now().Add(15 * time.Second)
	for {
		n, err := ie.eng.OpenInspects("homelab")
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d followed logs still open after the viewer left", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Without the Airfield's say-so nothing is read: the answer names the
// setting.
func TestInspectOffByDefault(t *testing.T) {
	ie := setupInspect(t, argocd.Inspect{})
	code, body := inspGet(t, ie.url(""))
	if code != http.StatusBadGateway || !strings.Contains(body, "inspect.read") {
		t.Fatalf("tree with inspect off: %d %s", code, body)
	}
}

// Without the logs setting, logs say how to turn them on; controls too.
func TestInspectLogsAndControlsNeedTheirOwnSettings(t *testing.T) {
	ie := setupInspect(t, argocd.Inspect{Read: true})
	if code, body := inspGet(t, ie.url("")); code != 200 {
		t.Fatalf("tree: %d %s", code, body)
	} else {
		var v TreeView
		_ = json.Unmarshal([]byte(body), &v)
		for _, a := range v.Actions {
			if a.OK || a.Why == "" {
				t.Fatalf("an action offered with controls off: %+v", a)
			}
		}
	}
	if code, body := inspGet(t, ie.url("/logs?pod=shop-bbb-1")); code != http.StatusBadGateway || !strings.Contains(body, "inspect.logs") {
		t.Fatalf("logs off: %d %s", code, body)
	}
	if code, body := inspPost(t, ie.url("/control"), `{"action":"restart","object":"Rollout/shop/shop","reason":"x"}`); code != http.StatusBadGateway || !strings.Contains(body, "inspect.control") {
		t.Fatalf("controls off: %d %s", code, body)
	}
}

// answerInspects plays the deploy runner on the engine: it answers every
// request with one document.
func answerInspects(t *testing.T, e *authEnv, doc string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := engine.RunnerInfo{ID: "d", Name: "deployer", Targets: []string{"kind"}}
		for ctx.Err() == nil {
			j, err := e.eng.TakeInspect(ctx, r, r.Targets, 300*time.Millisecond)
			if err != nil || j == nil {
				continue
			}
			_ = e.eng.FinishInspect(r, r.Targets, j.ID, doc, "")
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

// Who may see and do what: the tree and manifests for viewers; logs and
// controls for operators of the deployment's environment; every request
// of logs and controls in the audit trail, with who and what.
func TestInspectPermissionsAndAudit(t *testing.T) {
	e, base := controlSetup(t)
	answerInspects(t, e, `{"tree":{"namespaces":["shop"],"at":1,"nodes":[]},"actions":[{"action":"restart","ok":true}],"lines":[],"text":"kind: ConfigMap\n","result":"applied","detail":"restart Rollout/shop/shop"}`)
	ada, dana, vic := e.signIn(t, "ada"), e.signIn(t, "dana"), e.signIn(t, "vic")
	in := base + "inspect"

	var v TreeView
	vic.get(in).json(t, &v)
	if v.CanLogs || v.CanControl || len(v.Actions) != 1 || v.Actions[0].OK || !strings.Contains(v.Actions[0].Why, "operator role") {
		t.Fatalf("a viewer's tree view: %+v", v)
	}
	if r := vic.get(in + "/manifest?object=ConfigMap/shop/c"); r.status != 200 {
		t.Fatalf("a viewer's manifest: %d %s", r.status, r.body)
	}
	for name, r := range map[string]response{
		"viewer logs":      vic.get(in + "/logs?pod=p"),
		"viewer stream":    vic.get(in + "/logs/stream?pod=p"),
		"viewer control":   vic.do(http.MethodPost, in+"/control", `{"action":"restart","object":"Rollout/shop/shop","reason":"x"}`),
		"staging operator": dana.get(in + "/logs?pod=p"),
		"staging control":  dana.do(http.MethodPost, in+"/control", `{"action":"restart","object":"Rollout/shop/shop","reason":"x"}`),
	} {
		if r.status != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	dana.get(in).json(t, &v)
	if v.CanControl || !strings.Contains(v.Why, "environment prod") || !v.Protected {
		t.Errorf("dana's tree view: %+v", v)
	}
	if r := ada.get(in + "/logs?pod=p"); r.status != 200 {
		t.Fatalf("an operator's logs: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, in+"/control", `{"action":"restart","object":"Rollout/shop/shop","reason":"stuck pod"}`); r.status != 200 {
		t.Fatalf("an operator's restart: %d %s", r.status, r.body)
	}
	seen := map[string]engine.AuditEntry{}
	for _, a := range e.auditEntries(t) {
		seen[a.User+" "+a.Action+" "+a.Outcome] = a
	}
	for _, want := range []string{"vic deployment.inspect ok", "vic deployment.manifest ok", "vic deployment.logs denied", "vic deployment.control denied",
		"dana deployment.logs denied", "ada deployment.logs ok", "ada deployment.control ok"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("audit %q is missing; have %v", want, keys(seen))
		}
	}
	if a := seen["ada deployment.control ok"]; !strings.Contains(a.Target, "Rollout/shop/shop") || !strings.Contains(a.Detail, "stuck pod") {
		t.Errorf("the control entry: %+v", a)
	}
	// A reason is required for promote full.
	if r := ada.do(http.MethodPost, base+"promote-full", ``); r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "say why") {
		t.Errorf("promote full without a reason: %d %s", r.status, r.body)
	}
}

func keys(m map[string]engine.AuditEntry) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
