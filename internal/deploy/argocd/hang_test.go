package argocd

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// stacks is a dump of every goroutine, for the failure message of a test that
// found something stuck.
func stacks() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}

// withinOrDump runs f in a goroutine and fails the test, with the stacks of all
// goroutines, if it has not returned within limit.
func withinOrDump(t *testing.T, limit time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(testlib.Slow(limit)):
		t.Fatalf("%s did not return within %s; goroutines:\n%s", what, limit, stacks())
	}
}

// A manifest whose chart is in the GitOps repository itself (a source with a
// path): reading the old chart's values schema opens that same repository.
const gitChartManifest = `apiVersion: argoproj.io/v1alpha1
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
      path: charts/shop
`

// The hang of the deploy runner: the rollback of a deployment that wrote two
// commits checks the old chart's values schema while it holds the clone of the
// GitOps repository, and reading the schema of a chart that lives in that same
// repository asked for the clone again. The clone's lock could not be taken
// twice, did not look at the deadline, and was never released: the rollback
// stopped for good, and every later action and observation of the repository
// waited behind it.
func TestRollbackOfAPairedDeploymentWithTheChartInTheSameRepositoryDoesNotWaitForItself(t *testing.T) {
	bare := pairRepoWith(t, gitChartManifest)
	ctl := &controller{t: t, bare: bare, looksAfter: 1, revisionsAt: func(n bool) []any {
		if n {
			return []any{"2.0.0", "x"}
		}
		return []any{"1.0.0", "x"}
	}}
	a, _ := pairSetup(t, ctl)
	if _, err := a.Apply(context.Background(), pairJob(), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	ctl.mu.Lock()
	ctl.revertMode, ctl.seen = true, 0
	ctl.mu.Unlock()
	rb := pairJob()
	rb.Deploy.Action = engine.ActionRevert
	rb.Deploy.Message = "Roll back shop"
	var facts *engine.DeployFacts
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), testlib.Slow(20*time.Second))
	defer cancel()
	withinOrDump(t, 10*time.Second, "the rollback", func() { facts, err = a.Revert(ctx, rb, func(string, ...any) {}) })
	if err != nil || !facts.Reverted {
		t.Fatalf("%v %+v", err, facts)
	}
	if !strings.Contains(gitLog(t, bare), "Launchway-Revert: dep_1-pin") {
		t.Fatal("the pin was not reverted")
	}
	// The clone is free again.
	_, _, unlock, err := a.repo(ctx, "shop", "gitops", "main", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

// Waiting for a clone another step holds ends with the waiter's context, with
// an error that says what it waited for; the holder is not disturbed.
func TestWaitingForAClonedRepositoryEndsWithTheContext(t *testing.T) {
	bare, _ := bareRepo(t)
	_, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"}})
	a := agentFor(t, bare, cl)
	_, _, unlock, err := a.repo(context.Background(), "shop", "gitops", "main", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var werr error
	withinOrDump(t, 5*time.Second, "waiting for the clone", func() {
		_, _, _, werr = a.repo(ctx, "shop", "gitops", "main", func(string, ...any) {})
	})
	if !errors.Is(werr, context.DeadlineExceeded) || !strings.Contains(werr.Error(), "another step holds it") {
		t.Fatalf("%v", werr)
	}
	unlock()
	unlock() // a second call is harmless
	_, _, again, err := a.repo(context.Background(), "shop", "gitops", "main", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	again()
}
