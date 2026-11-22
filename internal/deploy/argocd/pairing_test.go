package argocd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

const childManifest = `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: shop
  namespace: argocd
spec:
  sources:
    - repoURL: https://charts.example
      chart: shop
      targetRevision: 1.0.0
    - repoURL: GITOPS
      targetRevision: main
`

// pairRepo seeds a GitOps repository with the child's manifest and its values.
func pairRepo(t *testing.T) (bare string) {
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
	bare = filepath.Join(dir, "g.git")
	run(dir, "init", "--bare", "-b", "main", bare)
	work := filepath.Join(dir, "w")
	run(dir, "clone", bare, work)
	os.MkdirAll(filepath.Join(work, "apps"), 0o755)
	os.MkdirAll(filepath.Join(work, "values"), 0o755)
	os.WriteFile(filepath.Join(work, "apps", "shop.yaml"), []byte(strings.ReplaceAll(childManifest, "GITOPS", bare)), 0o644)
	os.WriteFile(filepath.Join(work, "values", "shop.yaml"), []byte("image:\n  tag: \"1\"\n"), 0o644)
	run(work, "add", "-A")
	run(work, "commit", "-m", "seed")
	run(work, "push", "origin", "HEAD:main")
	return bare
}

func gitLog(t *testing.T, bare string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%H %s%n%b", "main")
	cmd.Dir = bare
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s", out)
	}
	return string(out)
}

// controller models what the parent and the child application do in the
// cluster: the parent applies the new chart pin only some looks after the
// manifest commit is in Git (it is slow); the child application syncs by
// itself as soon as the values commit is in Git, with whatever pin it has.
type controller struct {
	mu          sync.Mutex
	bare        string
	t           *testing.T
	looksAfter  int // looks at the application after the manifest commit before the parent applies it
	seen        int
	pinNew      bool
	mixedRender bool // the child synced the new values while still on the old pin
	revisionsAt func(pinNew bool) []any
	cmpErr      string // a ComparisonError shown while the old pin is in the spec
	revertMode  bool   // the rollback: the parent applies the OLD pin after the pin revert is in Git
}

func (c *controller) app(wantPin bool) map[string]any {
	pin := "1.0.0"
	if c.pinNew {
		pin = "2.0.0"
	}
	st := map[string]any{"sync": map[string]any{"status": "OutOfSync", "revisions": c.revisionsAt(c.pinNew)}}
	if c.cmpErr != "" && !c.pinNew {
		st["conditions"] = []any{map[string]any{"type": "ComparisonError", "message": c.cmpErr}}
	}
	return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd", "resourceVersion": "7",
			"annotations": map[string]any{"argocd.argoproj.io/tracking-id": "root:argoproj.io/Application:argocd/shop"}},
		"spec": map[string]any{"sources": []any{
			map[string]any{"repoURL": "https://charts.example", "chart": "shop", "targetRevision": pin},
			map[string]any{"repoURL": c.bare, "targetRevision": "main"}}},
		"status": st}
}

func (c *controller) look() {
	c.mu.Lock()
	defer c.mu.Unlock()
	log := gitLog(c.t, c.bare)
	valuesIn := strings.Contains(log, "Launchway-Deployment: dep_1\n")
	pinIn := strings.Contains(log, "Launchway-Deployment: dep_1-pin")
	if c.revertMode {
		if strings.Contains(log, "Launchway-Revert: dep_1-pin") && c.pinNew {
			c.seen++
			if c.seen > c.looksAfter {
				c.pinNew = false
			}
		}
		return
	}
	if pinIn && !c.pinNew {
		c.seen++
		if c.seen > c.looksAfter {
			c.pinNew = true
		}
	}
	if valuesIn && !c.pinNew {
		c.mixedRender = true
	}
}

func pairSetup(t *testing.T, ctl *controller) (*Agent, *Cluster) {
	t.Helper()
	dyn, cl := fakeCluster(t, ctl.app(false))
	dyn.PrependReactor("get", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		ctl.look()
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		return true, &unstructured.Unstructured{Object: ctl.app(false)}, nil
	})
	a := agentFor(t, ctl.bare, cl)
	a.PairEvery, a.PairTimeout, a.PairSettle = 5*time.Millisecond, 5*time.Second, 10*time.Millisecond
	return a, cl
}

func pairJob() *engine.Job {
	j := job("always")
	j.Deploy.Changes = []engine.DeployChange{
		{File: "apps/shop.yaml", Set: "spec.sources[0].targetRevision", Value: "2.0.0"},
		{File: "values/shop.yaml", Set: "image.tag", Value: "2"},
	}
	j.Deploy.Message = "Deploy shop 2"
	return j
}

// The race of the first sandbox deployment: one commit changing the chart pin
// and the image, the child synced on the values before the parent applied the
// pin. With the sequencing, the values are written only after the cluster
// shows the new pin, so no sync can render the new image under the old chart.
func TestPairedDeployWritesValuesOnlyAfterTheClusterShowsThePin(t *testing.T) {
	bare := pairRepo(t)
	ctl := &controller{t: t, bare: bare, looksAfter: 3, revisionsAt: func(n bool) []any {
		if n {
			return []any{"2.0.0", "x"}
		}
		return []any{"1.0.0", "x"}
	}}
	a, _ := pairSetup(t, ctl)
	var lines []string
	facts, err := a.Apply(context.Background(), pairJob(), func(f string, args ...any) { lines = append(lines, fmt.Sprintf(f, args...)) })
	if err != nil {
		t.Fatal(err)
	}
	ctl.look()
	if ctl.mixedRender {
		t.Fatal("the child application could sync the new values while still on the old chart pin")
	}
	log := gitLog(t, bare)
	if !strings.Contains(log, "Launchway-Deployment: dep_1-pin") || !strings.Contains(log, "Launchway-Deployment: dep_1\n") {
		t.Fatalf("two commits expected:\n%s", log)
	}
	// The values commit is the newest one, the one a rollback finds.
	if first, _, _ := strings.Cut(log, "\n"); !strings.Contains(first, "Deploy shop 2") || facts.Commit == "" {
		t.Fatalf("log:\n%s", log)
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"CONTROLLER-ORDERED", "parent application root", "Step 1 of 2", "Step 2 of 2", "shows the new pin"} {
		if !strings.Contains(all, want) {
			t.Errorf("the step log lacks %q:\n%s", want, all)
		}
	}
	if strings.Contains(facts.Sync, "mixed") {
		t.Fatalf("sync: %s", facts.Sync)
	}
}

// If the parent never applies the pin the values are never written.
func TestPairedDeployFailsWithoutWritingValuesWhenTheParentNeverApplies(t *testing.T) {
	bare := pairRepo(t)
	ctl := &controller{t: t, bare: bare, looksAfter: 1 << 30, revisionsAt: func(bool) []any { return []any{"1.0.0", "x"} }}
	a, _ := pairSetup(t, ctl)
	a.PairTimeout = 150 * time.Millisecond
	_, err := a.Apply(context.Background(), pairJob(), func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "NOT written") || !strings.Contains(err.Error(), "root") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(gitLog(t, bare), "Launchway-Deployment: dep_1\n") {
		t.Fatal("the values were written although the cluster never showed the pin")
	}
}

// Without the controller's API the runner still orders the two commits and
// says it is in the fallback mode.
func TestPairedDeployFallsBackToTwoCommitsWithoutTheControllerAPI(t *testing.T) {
	bare := pairRepo(t)
	_, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "other", "namespace": "argocd"}})
	_ = cl
	a := agentFor(t, bare, nil)
	a.Connect = func(string, Target) (*Cluster, error) { return nil, fmt.Errorf("no kubeconfig here") }
	a.PairSettle = 20 * time.Millisecond
	var lines []string
	start := time.Now()
	_, err := a.Apply(context.Background(), pairJob(), func(f string, args ...any) { lines = append(lines, fmt.Sprintf(f, args...)) })
	if err == nil || !strings.Contains(err.Error(), "unavailable") { // the sync request needs the cluster
		t.Fatalf("%v", err)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("no pause between the commits")
	}
	log := gitLog(t, bare)
	if !strings.Contains(log, "dep_1-pin") || !strings.Contains(log, "Launchway-Deployment: dep_1\n") || strings.Index(log, "Launchway-Deployment: dep_1\n") > strings.Index(log, "dep_1-pin") {
		t.Fatalf("commit order (newest first):\n%s", log)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "FALLBACK, two commits with a pause") {
		t.Fatalf("the log must say which mode is in use:\n%s", strings.Join(lines, "\n"))
	}
}

// A sync that already ran with a mixed set of sources is reported plainly and
// followed by a corrective sync.
func TestVerifyPairReportsAMixedRenderAndSyncsAgain(t *testing.T) {
	bare := pairRepo(t)
	ctl := &controller{t: t, bare: bare, pinNew: true, revisionsAt: func(bool) []any { return []any{"1.0.0", "x"} }}
	a, cl := pairSetup(t, ctl)
	_ = cl
	var lines []string
	note, err := a.verifyPair(context.Background(), pairJob(), []pinned{{Index: 0, Chart: "shop", Revision: "2.0.0"}}, "abc", func(f string, args ...any) { lines = append(lines, fmt.Sprintf(f, args...)) })
	if err != nil || !strings.Contains(note, "mixed render") || !strings.Contains(strings.Join(lines, "\n"), "MIXED RENDER") {
		t.Fatalf("%q %v %v", note, err, lines)
	}
}

func TestClassifyRefusesAnApplicationSet(t *testing.T) {
	if manifestKind([]byte("apiVersion: argoproj.io/v1alpha1\nkind: ApplicationSet\nmetadata:\n  name: x\n"), "shop") != "applicationset" {
		t.Fatal("ApplicationSet not recognised")
	}
	if manifestKind([]byte(strings.ReplaceAll(childManifest, "GITOPS", "x")), "shop") != "application" || manifestKind([]byte("image:\n  tag: 1\n"), "shop") != "" {
		t.Fatal("classification")
	}
	if p := parentOf(map[string]any{"metadata": map[string]any{"labels": map[string]any{"app.kubernetes.io/instance": "root"}}}, "shop"); p != "root" {
		t.Fatal(p)
	}
}

func staleApp(bare string) map[string]any {
	return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd", "resourceVersion": "7"},
		"spec": map[string]any{"sources": []any{
			map[string]any{"repoURL": "https://charts.example", "chart": "shop", "targetRevision": "1.0.0"},
			map[string]any{"repoURL": bare, "targetRevision": "main"}},
			"syncPolicy": map[string]any{"automated": map[string]any{}}},
		"status": map[string]any{"sync": map[string]any{"status": "OutOfSync"}, "health": map[string]any{"status": "Missing"},
			"operationState": map[string]any{"phase": "Running", "message": "one or more synchronization tasks completed unsuccessfully",
				"retry": map[string]any{"count": int64(3)},
				"syncResult": map[string]any{
					"sources": []any{
						map[string]any{"repoURL": "https://charts.example", "chart": "shop", "targetRevision": "2.0.0"},
						map[string]any{"repoURL": bare, "targetRevision": "main"}},
					"resources": []any{map[string]any{"kind": "Job", "name": "shop-bootstrap", "hookPhase": "Failed", "message": "usage: launchway <command>"}}}}}}
}

// A sync operation the controller keeps retrying with revisions the application
// no longer points at is named plainly and ended (once), and what the
// controller says is in the snapshot so a person can see why the step waits.
func TestStaleOperationIsDetectedAndEnded(t *testing.T) {
	bare, _ := bareRepo(t)
	dyn, cl := fakeCluster(t, staleApp(bare))
	a := agentFor(t, bare, cl)
	it := engine.WatchItem{Key: "k", Project: "shop", Target: "cluster-a", Application: "shop", Repo: "gitops", Branch: "main"}
	it.Spec.Commit, it.Spec.Health, it.Spec.Windows = "0000000000000000000000000000000000000000", "application", "wait"
	snap := a.Observe(context.Background(), it)
	if snap.Error != "" || snap.App == nil {
		t.Fatalf("%+v", snap)
	}
	app := snap.App
	if !strings.Contains(app.StaleOperation, "started with source 0 at 2.0.0") || !strings.Contains(app.StaleOperation, "now points at 1.0.0") ||
		!strings.Contains(app.Cleared, "ended that operation") {
		t.Fatalf("%+v", app)
	}
	for _, want := range []string{"operation Running", "retry 3", "failing: Job/shop-bootstrap: usage: launchway <command>"} {
		if !strings.Contains(app.Controller, want) {
			t.Errorf("the controller's status lacks %q: %s", want, app.Controller)
		}
	}
	terminated := 0
	for _, p := range patches(dyn) {
		if v, ok := p["operation"]; ok && v == nil {
			terminated++
		}
	}
	if terminated != 1 {
		t.Fatalf("operation removed %d times: %v", terminated, patches(dyn))
	}
	a.Observe(context.Background(), it) // looks again: it is not ended twice
	n := 0
	for _, p := range patches(dyn) {
		if v, ok := p["operation"]; ok && v == nil {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("ended twice: %d", n)
	}
	// A runner told to keep stale operations leaves it alone and says so.
	dyn2, cl2 := fakeCluster(t, staleApp(bare))
	a2 := agentFor(t, bare, cl2)
	a2.KeepStaleOperations = true
	if s := a2.Observe(context.Background(), it); !strings.Contains(s.App.Cleared, "not ended") || len(patches(dyn2)) != 0 {
		t.Fatalf("%+v %v", s.App, patches(dyn2))
	}
}

// A ComparisonError that lasts fails the step with its full text, instead of
// waiting for the timeout; a passing one does not.
func TestComparisonErrorFailsPromptlyWhenItLasts(t *testing.T) {
	bare, _ := bareRepo(t)
	app := staleApp(bare)
	app["status"].(map[string]any)["operationState"] = nil
	app["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "ComparisonError",
		"message": "Failed to load target state: values don't meet the specifications of the schema(s): additional properties 'networkPolicy' not allowed"}}
	_, cl := fakeCluster(t, app)
	a := agentFor(t, bare, cl)
	a.ComparisonGrace = 30 * time.Millisecond
	it := engine.WatchItem{Key: "k", Project: "shop", Target: "cluster-a", Application: "shop", Repo: "gitops", Branch: "main"}
	it.Spec.Commit, it.Spec.Health, it.Spec.Windows = "0000000000000000000000000000000000000000", "application", "wait"
	first := a.Observe(context.Background(), it)
	if v := deploy.Evaluate(it.Spec, first); v.Outcome != deploy.Continue || !strings.Contains(v.Detail, "ComparisonError") {
		t.Fatalf("the first look must wait, and say what the controller says: %+v", v)
	}
	time.Sleep(60 * time.Millisecond)
	v := deploy.Evaluate(it.Spec, a.Observe(context.Background(), it))
	if v.Outcome != deploy.Failed || v.Phase != deploy.PhaseComparison || !strings.Contains(v.Detail, "additional properties 'networkPolicy' not allowed") {
		t.Fatalf("%+v", v)
	}
}

// A rollback of a paired deployment reverts the values first and then the
// application's own manifest, and waits for the cluster to show the old pin.
func TestRollbackOfAPairedDeploymentRevertsValuesThenThePinAndWaits(t *testing.T) {
	bare := pairRepo(t)
	ctl := &controller{t: t, bare: bare, looksAfter: 2, revisionsAt: func(n bool) []any {
		if n {
			return []any{"2.0.0", "x"}
		}
		return []any{"1.0.0", "x"}
	}}
	a, _ := pairSetup(t, ctl)
	if _, err := a.Apply(context.Background(), pairJob(), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	// Now the rollback: the parent applies the old pin only some looks after the pin revert is in Git.
	ctl.mu.Lock()
	ctl.revertMode, ctl.seen = true, 0
	ctl.mu.Unlock()
	rb := pairJob()
	rb.Deploy.Action = engine.ActionRevert
	rb.Deploy.Message = "Roll back shop"
	var lines []string
	facts, err := a.Revert(context.Background(), rb, func(f string, args ...any) { lines = append(lines, fmt.Sprintf(f, args...)) })
	if err != nil || !facts.Reverted {
		t.Fatalf("%v %+v", err, facts)
	}
	ctl.look()
	if ctl.pinNew {
		t.Fatal("the cluster still shows the new pin")
	}
	log := gitLog(t, bare)
	iv, ip := strings.Index(log, "Launchway-Revert: dep_1\n"), strings.Index(log, "Launchway-Revert: dep_1-pin")
	if iv < 0 || ip < 0 || ip > iv { // newest first: the pin revert is newer
		t.Fatalf("revert order (newest first):\n%s", log)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Rollback, step 2 of 2") || !strings.Contains(strings.Join(lines, "\n"), "shows the new pin") {
		t.Fatalf("log:\n%s", strings.Join(lines, "\n"))
	}
}

const oldSchema = `{"type":"object","additionalProperties":false,"properties":{"image":{"type":"object","properties":{"tag":{"type":"string"}}},"replicas":{"type":"integer"}}}`

func TestSchemaRejectsKeysTheOldChartDoesNotKnow(t *testing.T) {
	bad := schemaRejects([]byte(oldSchema), []byte("image:\n  tag: \"1\"\nnetworkPolicy:\n  enabled: true\n"))
	if len(bad) != 1 || bad[0] != "networkPolicy" {
		t.Fatalf("%v", bad)
	}
	if bad := schemaRejects([]byte(oldSchema), []byte("image:\n  tag: \"1\"\nreplicas: 2\n")); len(bad) != 0 {
		t.Fatalf("%v", bad)
	}
	// A schema that allows extra properties refuses nothing.
	if bad := schemaRejects([]byte(`{"type":"object","properties":{}}`), []byte("x: 1\n")); len(bad) != 0 {
		t.Fatalf("%v", bad)
	}
}

// The incident: the values still hold a key only the new chart knows, so the
// rollback must NOT revert the pin. It says why and leaves Git at the new pin.
func TestRollbackDoesNotRevertThePinIntoAChartThatRejectsTheValues(t *testing.T) {
	bare := pairRepo(t)
	ctl := &controller{t: t, bare: bare, looksAfter: 0, revisionsAt: func(n bool) []any {
		if n {
			return []any{"2.0.0", "x"}
		}
		return []any{"1.0.0", "x"}
	}}
	a, _ := pairSetup(t, ctl)
	a.ChartSchema = func(context.Context, map[string]any) ([]byte, error) { return []byte(oldSchema), nil }
	j := pairJob()
	if _, err := a.Apply(context.Background(), j, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	ctl.mu.Lock()
	ctl.revertMode, ctl.seen = true, 0
	ctl.mu.Unlock()
	rb := j
	rb.Deploy.Action = engine.ActionRevert
	// A later commit added a key only the new chart knows: the values revert keeps it.
	work := filepath.Join(t.TempDir(), "w")
	for _, args := range [][]string{{"clone", "-q", bare, work}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	f := filepath.Join(work, "values", "shop.yaml")
	cur, _ := os.ReadFile(f)
	os.WriteFile(f, append([]byte("networkPolicy:\n  enabled: true\n\n\n\n"), cur...), 0o644)
	for _, args := range [][]string{{"commit", "-qam", "network policy"}, {"push", "-q", "origin", "HEAD:main"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	facts, err := a.Revert(context.Background(), rb, func(string, ...any) {})
	var held *HeldError
	if !errorsAs(err, &held) || !strings.Contains(err.Error(), "does not accept these values") || !strings.Contains(err.Error(), "networkPolicy") || !strings.Contains(err.Error(), "NOT reverted") {
		t.Fatalf("%v %+v", err, facts)
	}
	if strings.Contains(gitLog(t, bare), "Launchway-Revert: dep_1-pin") {
		t.Fatal("the pin was reverted")
	}
}

// A rollback whose pin revert leaves the controller unable to render restores
// the pin, so Git names what is running, and says so.
func TestRollbackRestoresThePinWhenTheControllerCannotRender(t *testing.T) {
	bare := pairRepo(t)
	ctl := &controller{t: t, bare: bare, revisionsAt: func(n bool) []any {
		if n {
			return []any{"2.0.0", "x"}
		}
		return []any{"1.0.0", "x"}
	}, cmpErr: "values don't meet the specifications of the schema(s): additional properties 'networkPolicy' not allowed"}
	a, _ := pairSetup(t, ctl)
	a.ComparisonGrace = 50 * time.Millisecond
	if _, err := a.Apply(context.Background(), pairJob(), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	ctl.mu.Lock()
	ctl.revertMode, ctl.seen = true, 0
	ctl.mu.Unlock()
	rb := pairJob()
	rb.Deploy.Action = engine.ActionRevert
	_, err := a.Revert(context.Background(), rb, func(string, ...any) {})
	var held *HeldError
	if !errors.As(err, &held) || !strings.Contains(err.Error(), "additional properties 'networkPolicy' not allowed") || !strings.Contains(err.Error(), "Git again names what is running") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(gitLog(t, bare), "Launchway-Revert: dep_1-pin-restore") {
		t.Fatal("the pin was not restored in Git")
	}
}

func TestDoctorFindsAndClearsAStaleOperationWithNoDeploymentRunning(t *testing.T) {
	bare, _ := bareRepo(t)
	dyn, cl := fakeCluster(t, staleApp(bare))
	a := agentFor(t, bare, cl)
	dg, err := a.Doctor(context.Background(), "cluster-a", "shop", false)
	if err != nil || !strings.Contains(dg.Stale, "started with source 0 at 2.0.0") || dg.Cleared || len(patches(dyn)) != 0 {
		t.Fatalf("%v %+v %v", err, dg, patches(dyn))
	}
	dg, err = a.Doctor(context.Background(), "cluster-a", "shop", true)
	if err != nil || !dg.Cleared || len(patches(dyn)) != 1 {
		t.Fatalf("%v %+v %v", err, dg, patches(dyn))
	}
}
