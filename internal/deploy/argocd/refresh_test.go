package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// twoCommits makes a bare repository with two commits on main and returns it with the first and the second.
func twoCommits(t *testing.T) (bare, first, second string) {
	t.Helper()
	bare, first = bareRepo(t)
	work := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("clone", bare, ".")
	os.WriteFile(work+"/a.yaml", []byte("image: team/shop:2\n"), 0o644)
	run("commit", "-am", "second")
	run("push", "origin", "HEAD:main")
	return bare, first, run("rev-parse", "HEAD")
}

// staleController is an Argo CD that caches what the branch points at: the application shows the first commit until
// somebody asks it for a hard refresh, which makes it read the second. A refresh is the only thing it does.
type staleController struct {
	mu        sync.Mutex
	bare      string
	old, tip  string
	refreshed bool
	asked     int
	forbidden bool // the credentials may not change the Application
	after     int  // the controller re-reads only at this many hard refreshes (0: the first)
}

func (c *staleController) app() map[string]any {
	rev := c.old
	if c.refreshed {
		rev = c.tip
	}
	return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd", "resourceVersion": "7"},
		"spec":     map[string]any{"source": map[string]any{"repoURL": c.bare, "targetRevision": "main"}},
		"status":   map[string]any{"sync": map[string]any{"status": "Synced", "revision": rev}, "health": map[string]any{"status": "Healthy"}}}
}

func (c *staleController) setup(t *testing.T) (*Agent, func() int) {
	t.Helper()
	dyn, cl := fakeCluster(t, c.app())
	dyn.PrependReactor("get", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return true, &unstructured.Unstructured{Object: c.app()}, nil
	})
	dyn.PrependReactor("patch", "applications", func(a k8stesting.Action) (bool, runtime.Object, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.forbidden {
			c.asked++
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "shop", errors.New("not allowed"))
		}
		if strings.Contains(string(a.(k8stesting.PatchAction).GetPatch()), `"hard"`) {
			c.asked++
			c.refreshed = c.asked >= max(c.after, 1)
		}
		return true, &unstructured.Unstructured{Object: c.app()}, nil
	})
	a := agentFor(t, c.bare, cl)
	a.RefreshEvery = 5 * time.Millisecond
	return a, func() int { c.mu.Lock(); defer c.mu.Unlock(); return c.asked }
}

func (c *staleController) item(refresh string) engine.WatchItem {
	return engine.WatchItem{Key: "k1", Deployment: "dep_1", Project: "shop", Target: "cluster-a", Application: "shop", Repo: "gitops", Branch: "main",
		Spec: deploy.Spec{Kind: "deploy", Commit: c.tip, Health: "application", Windows: "wait", Refresh: refresh}}
}

// While Argo CD has not noticed the commit, the runner asks it to re-read the repository, and the step says so; once
// it has noticed, nothing more is asked. sync.request: never plays no part: a refresh is not a sync.
func TestAStaleBranchCacheIsRefreshedWhileTheStepWaits(t *testing.T) {
	bare, old, tip := twoCommits(t)
	c := &staleController{bare: bare, old: old, tip: tip}
	a, asked := c.setup(t)
	ctx := context.Background()
	snap := a.Observe(ctx, c.item(""))
	if snap.App == nil || snap.App.Contains == nil || *snap.App.Contains {
		t.Fatalf("the first look should find the commit not noticed: %+v", snap.App)
	}
	v := deploy.Evaluate(c.item("").Spec, snap)
	if v.Outcome != deploy.Continue || !strings.Contains(v.Detail, "waiting for Argo CD to sync commit") || !strings.Contains(v.Detail, "asked Argo CD to re-read the repository") {
		t.Fatalf("the step does not say what it asked for: %+v", v)
	}
	if asked() != 1 {
		t.Fatalf("asked %d times", asked())
	}
	time.Sleep(20 * time.Millisecond)
	snap = a.Observe(ctx, c.item(""))
	if snap.App.Contains == nil || !*snap.App.Contains {
		t.Fatalf("after the refresh the controller should have noticed: %+v", snap.App)
	}
	if got := asked(); got != 1 {
		t.Fatalf("asked again after the commit was noticed: %d", got)
	}
}

// With sync.refresh: never nothing is asked of the controller.
func TestNoRefreshIsAskedWhenTheStepSaysNever(t *testing.T) {
	bare, old, tip := twoCommits(t)
	c := &staleController{bare: bare, old: old, tip: tip}
	a, asked := c.setup(t)
	for i := 0; i < 3; i++ {
		a.Observe(context.Background(), c.item("never"))
		time.Sleep(10 * time.Millisecond)
	}
	if asked() != 0 {
		t.Fatalf("asked %d times although the step says sync.refresh: never", asked())
	}
}

// Without the right to change the Application the runner asks once, keeps waiting, and says exactly which refresh a
// person requests and that the step goes on by itself.
func TestAReadOnlyRunnerSaysWhichRefreshToRequest(t *testing.T) {
	bare, old, tip := twoCommits(t)
	c := &staleController{bare: bare, old: old, tip: tip, forbidden: true}
	a, asked := c.setup(t)
	var last deploy.Snapshot
	for i := 0; i < 4; i++ {
		last = a.Observe(context.Background(), c.item(""))
		time.Sleep(10 * time.Millisecond)
	}
	if asked() != 1 {
		t.Fatalf("a runner that may not refresh tried %d times", asked())
	}
	v := deploy.Evaluate(c.item("").Spec, last)
	for _, want := range []string{"may not refresh shop", "argocd app get shop --hard-refresh", "argocd.argoproj.io/refresh=hard", "picks up by itself"} {
		if !strings.Contains(v.Detail, want) {
			t.Errorf("the message lacks %q:\n%s", want, v.Detail)
		}
	}
	if v.Outcome != deploy.Continue {
		t.Errorf("the step must keep waiting: %+v", v)
	}
}

// The parent application of a paired deployment is refreshed again and again while the step waits for the cluster
// to show the new pin, with the pause between asks; a parent that can never be refreshed ends the wait with the
// refresh a person should request, after a longer default wait than the controller's caches.
func TestTheParentIsRefreshedRepeatedlyAndTheTimeoutNamesTheRefresh(t *testing.T) {
	bare := pairRepo(t)
	ctl := &controller{t: t, bare: bare, looksAfter: 1 << 30, revisionsAt: func(bool) []any { return []any{"1.0.0", "x"} }}
	dyn, cl := fakeCluster(t, ctl.app(false))
	dyn.PrependReactor("get", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		ctl.look()
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		return true, &unstructured.Unstructured{Object: ctl.app(false)}, nil
	})
	root := map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "root", "namespace": "argocd"}, "spec": map[string]any{}}
	if err := dyn.Tracker().Create(appGVR, &unstructured.Unstructured{Object: root}, "argocd"); err != nil {
		t.Fatal(err)
	}
	a := agentFor(t, bare, cl)
	a.PairEvery, a.PairTimeout, a.PairSettle, a.RefreshEvery = 5*time.Millisecond, 300*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	if a.pairTimeoutDefault() < 8*time.Minute {
		t.Errorf("the default parent wait is %v", a.pairTimeoutDefault())
	}
	var lines []string
	_, err := a.Apply(context.Background(), pairJob(), func(f string, args ...any) { lines = append(lines, fmt.Sprintf(f, args...)) })
	if err == nil || !strings.Contains(err.Error(), "argocd app get root --hard-refresh") {
		t.Fatalf("the timeout does not name the refresh: %v", err)
	}
	hard := 0
	for _, p := range patches(dyn) {
		if strings.Contains(asJSONText(p), `"hard"`) {
			hard++
		}
	}
	if hard < 2 {
		t.Fatalf("the parent was asked to refresh %d times, want repeated asks", hard)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Asked Argo CD to refresh the parent application") {
		t.Errorf("the step log does not say it asked:\n%s", strings.Join(lines, "\n"))
	}
}

func asJSONText(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// A deployment of several commits asks for the refresh after the LAST of them: the step that requests the sync asks
// the application (hard) until it shows that commit, again after a pause, and stops asking once it does.
func TestTheSyncStepAsksForARefreshAfterTheLastCommitAndAgainUntilItIsSeen(t *testing.T) {
	bare, old, tip := twoCommits(t)
	c := &staleController{bare: bare, old: old, tip: tip, after: 2}
	a, asked := c.setup(t)
	a.SeeTimeout = 5 * time.Second
	var logs []string
	logf := func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
	if _, err := a.requestSync(context.Background(), job("auto"), tip, logf); err != nil {
		t.Fatal(err)
	}
	if got := asked(); got != 2 {
		t.Fatalf("asked %d hard refreshes, want 2: the first ask was not enough and it asked again, then stopped", got)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "re-read the repository") {
		t.Fatalf("the step does not say it asked: %v", logs)
	}
	// With sync.refresh: never nothing is asked.
	c2 := &staleController{bare: bare, old: old, tip: tip}
	a2, asked2 := c2.setup(t)
	j := job("auto")
	j.Deploy.Sync.Refresh = "never"
	_, _ = a2.requestSync(context.Background(), j, tip, logf)
	if asked2() != 0 {
		t.Fatalf("asked %d times with sync.refresh: never", asked2())
	}
}
