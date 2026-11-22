package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// fakeServer answers a runner's API the way a server with work queued would:
// leases come from a queue, a heartbeat of an ended lease is refused with 410
// (what the server says when it ended the step), results are recorded.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	queue    []*engine.Job
	finished map[string]engine.Result // by lease
	ended    map[string]bool          // leases the server ended
	hang     map[string]chan struct{} // routes that never answer until closed
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, finished: map[string]engine.Result{}, ended: map[string]bool{}, hang: map[string]chan struct{}{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.mu.Lock()
		for _, ch := range f.hang {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		f.mu.Unlock()
		f.srv.Close()
	})
	return f
}

func (f *fakeServer) enqueue(j *engine.Job) { f.mu.Lock(); f.queue = append(f.queue, j); f.mu.Unlock() }

func (f *fakeServer) end(lease string) { f.mu.Lock(); f.ended[lease] = true; f.mu.Unlock() }

func (f *fakeServer) result(lease string) (engine.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.finished[lease]
	return r, ok
}

// hangOn makes a route never answer (a connection gone quiet).
func (f *fakeServer) hangOn(route string) {
	f.mu.Lock()
	f.hang[route] = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	route := strings.TrimPrefix(r.URL.Path, "/api/runner/v1/")
	var in struct {
		Lease  string        `json:"lease_id"`
		Result engine.Result `json:"result"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	hang := f.hang[route]
	f.mu.Unlock()
	if hang != nil {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
		return
	}
	idle := func() {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}
	switch route {
	case "deploy/lease":
		f.mu.Lock()
		var job *engine.Job
		if len(f.queue) > 0 {
			job, f.queue = f.queue[0], f.queue[1:]
		}
		f.mu.Unlock()
		if job == nil {
			idle()
			return
		}
		_ = json.NewEncoder(w).Encode(job)
	case "heartbeat":
		f.mu.Lock()
		ended := f.ended[in.Lease]
		f.mu.Unlock()
		if ended {
			w.WriteHeader(http.StatusGone)
			return
		}
		fmt.Fprint(w, `{"cancel":false}`)
	case "start":
		fmt.Fprint(w, `{"cancel":false}`)
	case "logs", "leave":
		fmt.Fprint(w, `{}`)
	case "finish":
		f.mu.Lock()
		f.finished[in.Lease] = in.Result
		f.mu.Unlock()
		fmt.Fprint(w, `{}`)
	case "deploy/observe":
		fmt.Fprint(w, `{"results":[]}`)
	default: // watch, commands, inspect, verify/lease: nothing to do
		idle()
	}
}

// stuckAgent is an agent whose target "stuck" cannot be connected to: the
// connection call does not look at any context and returns only when release is
// closed. Revert jobs with abort_first reach it at once; no Git is involved.
func stuckAgent(t *testing.T, release <-chan struct{}) *argocd.Agent {
	t.Helper()
	target := argocd.Target{Kubernetes: argocd.Kubernetes{InCluster: true}, RolloutNamespaces: []string{"shop"},
		Applications: []argocd.AppRule{{Projects: []string{"shop"}, Names: []string{"shop"}}}}
	a := &argocd.Agent{
		Config: argocd.Config{
			Targets: map[string]argocd.Target{"stuck": target, "fine": target},
			Repos:   map[string]argocd.Repo{"gitops": {URL: t.TempDir() + "/none.git", Paths: []argocd.PathRule{{Projects: []string{"shop"}, Files: []string{"**"}}}}},
		},
		DataDir: t.TempDir(), Secrets: secrets.NewResolver(),
		Connect: func(name string, _ argocd.Target) (*argocd.Cluster, error) {
			if name == "stuck" {
				<-release
				return nil, errors.New("released")
			}
			dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(k8sruntime.NewScheme(), map[schema.GroupVersionResource]string{
				{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
				{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}:     "RolloutList",
			})
			return argocd.NewCluster(dyn, "argocd"), nil
		},
	}
	if err := a.Config.Check(); err != nil {
		t.Fatal(err)
	}
	return a
}

// lid is a lease ID that looks like the server's.
func lid(n int) string { return fmt.Sprintf("lease_%020d", n) }

func revertJob(lease, step, target string, deadline time.Time) *engine.Job {
	dj := &engine.DeployJob{Action: engine.ActionRevert, Deployment: "dep-" + step, Service: "shop", Environment: "prod", Target: target,
		Application: "shop", Repo: "gitops", Branch: "main", AbortFirst: true, PodHash: "h",
		Rollout: &pipeline.RolloutRef{Name: "shop", Namespace: "shop"}}
	if !deadline.IsZero() {
		dj.Deadline = deadline.UnixMilli()
	}
	return &engine.Job{LeaseID: lease, LeaseTTL: pipeline.Duration(3 * time.Second), RunID: fmt.Sprintf("run_%020d", len(step)), StepID: step, Project: "shop", Attempt: 1, Deploy: dj}
}

// logSink is a log destination that tests can read while the runner writes.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *logSink) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

func (l *logSink) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func goroutines() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}

// waitFor polls until cond holds; at the limit it fails the test with the stacks
// of all goroutines, which show what everything is waiting for.
func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(testlib.Slow(limit))
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s; goroutines:\n%s", what, limit, goroutines())
}

func startDeploy(t *testing.T, d *DeployRunner) (stopped <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { err := d.Run(ctx); done <- err; done <- err }() // once for the test, once for the cleanup
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testlib.Slow(15 * time.Second)):
		}
	})
	return done
}

// The incident: an action that stops for good (here, in a call that does not
// look at its context) held the runner's only place for ever, and nothing was
// taken after it: no rollback, no later deploy, and no line in the log. The
// runner gives the action up when its time (and a grace) is over, reports it as
// lost for the server to try again, and takes the next one.
func TestAnActionThatNeverStopsIsGivenUpAndTheNextOneIsTaken(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := newFakeServer(t)
	f.enqueue(revertJob(lid(1), "deploy", "stuck", time.Time{}))
	f.enqueue(revertJob(lid(2), "rollback", "fine", time.Time{}))
	f.enqueue(revertJob(lid(3), "rerun", "fine", time.Time{}))
	sink := &logSink{}
	d := &DeployRunner{Client: NewClient(f.srv.URL, "tok"), Agent: stuckAgent(t, release), Info: engine.RunnerInfo{ID: "r", Name: "r", Targets: []string{"stuck", "fine"}},
		Concurrency: 1, Log: sink.logger(), ActionTimeout: 300 * time.Millisecond, ActionGrace: 200 * time.Millisecond, ObserveEvery: 50 * time.Millisecond}
	startDeploy(t, d)

	waitFor(t, 10*time.Second, "the rollback and the next deploy being taken after the stuck action", func() bool {
		_, a := f.result(lid(2))
		_, b := f.result(lid(3))
		return a && b
	})
	r, ok := f.result(lid(1))
	if !ok || r.Outcome != "lost" {
		t.Fatalf("the stuck action was reported as %+v (%v), want lost, which the server tries again", r, ok)
	}
	if !strings.Contains(sink.String(), "did not stop when its time ran out") {
		t.Fatalf("the log says nothing about giving it up:\n%s", sink.String())
	}
	if n := d.vitals().abandoned.Load(); n != 1 {
		t.Fatalf("%d actions abandoned, want 1", n)
	}
}

// When the server ends the step the runner holds (its deadline passed and the
// server failed it), the heartbeat is refused; the runner stops the action then,
// not at the action's own deadline, reports nothing for it, and takes the next.
func TestTheActionStopsWhenTheServerEndedTheStep(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := newFakeServer(t)
	f.enqueue(revertJob(lid(1), "deploy", "stuck", time.Time{}))
	f.enqueue(revertJob(lid(2), "rollback", "fine", time.Time{}))
	f.end(lid(1)) // the server has already failed the step: its next heartbeat is refused
	d := &DeployRunner{Client: NewClient(f.srv.URL, "tok"), Agent: stuckAgent(t, release), Info: engine.RunnerInfo{ID: "r", Name: "r", Targets: []string{"stuck", "fine"}},
		Concurrency: 1, Log: quietLog(), ActionGrace: 200 * time.Millisecond, ObserveEvery: 50 * time.Millisecond}
	startDeploy(t, d)

	waitFor(t, 20*time.Second, "the rollback being taken after the server ended the first step", func() bool {
		_, ok := f.result(lid(2))
		return ok
	})
	if r, ok := f.result(lid(1)); ok {
		t.Fatalf("a result was reported for a step the server had ended: %+v", r)
	}
}

// A loop that is stuck in a call that never returns is seen: /livez fails, the
// watchdog logs an error with the stacks of the goroutines and ends the run with
// ErrStalled, so that whatever supervises the process starts it again.
func TestTheWatchdogEndsARunnerWhoseLeaseLoopIsStuck(t *testing.T) {
	f := newFakeServer(t)
	f.hangOn("deploy/lease")
	c := NewClient(f.srv.URL, "tok")
	c.HTTP = &http.Client{Timeout: time.Hour} // a connection gone quiet, with no timeout of its own
	sink := &logSink{}
	d := &DeployRunner{Client: c, Agent: stuckAgent(t, make(chan struct{})), Info: engine.RunnerInfo{ID: "r", Name: "r", Targets: []string{"fine"}},
		Log: sink.logger(), StallAfter: 400 * time.Millisecond, StatusEvery: -1}
	h := d.Health()
	live := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/livez", nil))
		return rec.Code
	}
	if live() != 200 {
		t.Fatal("not live before it has been stuck")
	}
	stopped := startDeploy(t, d)
	waitFor(t, 5*time.Second, "/livez failing", func() bool { return live() == http.StatusServiceUnavailable })
	select {
	case err := <-stopped:
		if !errors.Is(err, ErrStalled) || !strings.Contains(err.Error(), "deploy lease") {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(testlib.Slow(5 * time.Second)):
		t.Fatalf("the watchdog did not end the runner; goroutines:\n%s", goroutines())
	}
	out := sink.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "goroutine ") || !strings.Contains(out, "LeaseDeploy") {
		t.Fatalf("the error line does not carry the stacks:\n%.2000s", out)
	}
}

// A runner that is working is live, and says what it did now and then.
func TestALiveRunnerAnswersLivezAndLogsAStatusLine(t *testing.T) {
	f := newFakeServer(t)
	sink := &logSink{}
	d := &DeployRunner{Client: NewClient(f.srv.URL, "tok"), Agent: stuckAgent(t, make(chan struct{})), Info: engine.RunnerInfo{ID: "r", Name: "r", Targets: []string{"fine"}},
		Log: sink.logger(), StallAfter: 2 * time.Second, StatusEvery: 100 * time.Millisecond}
	h := d.Health()
	startDeploy(t, d)
	waitFor(t, 10*time.Second, "the status line", func() bool { return strings.Contains(sink.String(), "deploy runner idle: ") })
	if !strings.Contains(sink.String(), "polls, no work taken yet") {
		t.Fatalf("the status line:\n%s", sink.String())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/livez", nil))
	if rec.Code != 200 {
		t.Fatalf("/livez %d: %s", rec.Code, rec.Body)
	}
}

// Waiting for a free place is not a stall while what holds the places is within
// its time; it is one when an action is past the time the runner gives it.
func TestAWaitForAFreeSlotIsStuckOnlyWhenWhatHoldsItIsPastItsTime(t *testing.T) {
	v := newVitals(time.Minute)
	l := v.loop("lease")
	now := time.Now()
	end := v.begin("deploy apply shop", now.Add(time.Hour))
	l.slotWait(true)
	if ok, why := v.check(now.Add(10 * time.Minute)); !ok {
		t.Fatalf("a busy runner was judged stuck: %s", why)
	}
	end()
	end = v.begin("deploy apply shop", now.Add(time.Minute))
	ok, why := v.check(now.Add(10 * time.Minute))
	if ok || !strings.Contains(why, "deploy apply shop") || !strings.Contains(why, "lease") {
		t.Fatalf("ok=%v why=%q", ok, why)
	}
	end()
	l.slotWait(false)
	if ok, _ := v.check(now.Add(10 * time.Minute)); ok {
		t.Fatal("a loop that is not waiting and has not polled for ten minutes was judged live")
	}
	l.polled()
	if ok, why := v.check(time.Now()); !ok {
		t.Fatal(why)
	}
}

// The Station's loop that takes steps has the same pulse, and a lock it waits
// for ends with its context.
func TestAStationsLeaseLoopIsWatchedAndItsLocksSeeTheDeadline(t *testing.T) {
	f := newFakeServer(t)
	f.hangOn("lease")
	c := NewClient(f.srv.URL, "tok")
	c.HTTP = &http.Client{Timeout: time.Hour}
	r := &Runner{Client: c, Exec: &executor.Host{}, Info: engine.RunnerInfo{ID: "s", Name: "s"}, DataDir: t.TempDir(), Log: quietLog(),
		StallAfter: 400 * time.Millisecond, StatusEvery: -1}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- r.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStalled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(testlib.Slow(10 * time.Second)):
		t.Fatalf("the watchdog did not end the Station; goroutines:\n%s", goroutines())
	}

	l := newCtxLock()
	if err := l.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	wctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if err := l.lock(wctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for a held lock: %v", err)
	}
	l.unlock()
	if err := l.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
}
