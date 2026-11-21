package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// These tests measure what a person watching a Sortie sees: how long after a
// step prints a line it can be read through the public stream.

type seen struct {
	step string
	line engine.LogLine
	at   time.Time
}

// watch reads the run's server-sent events from srv and sends each log line
// with the time it arrived.
func watch(t *testing.T, srv *httptest.Server, runID string) (<-chan seen, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream?run="+runID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan seen, 1<<16)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		ev := ""
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event: "):
				ev = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: ") && ev == "log":
				var b engine.LogBatch
				if json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &b) == nil {
					now := time.Now()
					for _, ln := range b.Lines {
						out <- seen{b.Step, ln, now}
					}
				}
			}
		}
	}()
	return out, cancel
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[min(len(d)-1, int(float64(len(d))*p))]
}

// stepStamped prints "tick N <unix ms>" every 200 ms for 10 s.
const tickPipeline = `
pipeline: demo
steps:
  tick:
    image: none
    run: |
      i=0
      while [ $i -lt 50 ]; do
        echo "tick $i $(date +%s%3N)"
        i=$((i+1))
        sleep 0.2
      done
`

func latencies(t *testing.T, ch <-chan seen) []time.Duration {
	var lat []time.Duration
	for s := range ch {
		f := strings.Fields(s.line.Text)
		if len(f) == 3 && f[0] == "tick" {
			ms, _ := strconv.ParseInt(f[2], 10, 64)
			lat = append(lat, s.at.Sub(time.UnixMilli(ms)))
		}
		if len(lat) == 50 {
			break
		}
	}
	return lat
}

func TestLiveLineLatencyLocalExecutor(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	id := e.start(t, tickPipeline)
	ch, cancel := watch(t, e.srv, id)
	defer cancel()
	lat := latencies(t, ch)
	if len(lat) < 50 {
		t.Fatalf("only %d of 50 lines arrived on the stream", len(lat))
	}
	p50, p95 := percentile(lat, 0.5), percentile(lat, 0.95)
	t.Logf("host executor, one server: p50 %s, p95 %s, max %s over %d lines", p50, p95, percentile(lat, 1), len(lat))
	if p95 > 2*time.Second {
		t.Fatalf("p95 %s is over the 2 s bound", p95)
	}
	e.wait(t, id, 30*time.Second)
}

// Output without a newline is shown after a short idle time.
func TestLivePartialLineThroughTheServer(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	id := e.start(t, `
pipeline: demo
steps:
  bar:
    image: none
    run: |
      printf 'progress 42%%'
      sleep 4
      printf ' ... done\n'
`)
	ch, cancel := watch(t, e.srv, id)
	defer cancel()
	start := time.Now()
	for s := range ch {
		if strings.HasPrefix(s.line.Text, "progress 42%") {
			if d := s.at.Sub(start); d > 3*time.Second {
				t.Fatalf("the partial line took %s, and the step was still running", d)
			}
			e.wait(t, id, 30*time.Second)
			return
		}
	}
	t.Fatal("the partial line never came")
}

// A step that prints 100,000 lines: the server keeps up, every line is in the
// full log, and the stream does not stall the step.
func TestLiveChattyStep(t *testing.T) {
	if testing.Short() {
		t.Skip("chatty")
	}
	e := setup(t, 10*time.Second)
	e.runner(t, "r1")
	id := e.start(t, `
pipeline: demo
steps:
  chat:
    image: none
    run: |
      i=0
      while [ $i -lt 100000 ]; do echo "line $i"; i=$((i+1)); done
`)
	ch, cancel := watch(t, e.srv, id) // a watcher that reads, as a browser does
	go func() {
		for range ch {
		}
	}()
	defer cancel()
	d := e.wait(t, id, 120*time.Second)
	if d.Status != "succeeded" {
		t.Fatalf("status %s", d.Status)
	}
	total := 0
	after := 0
	for {
		ls, _, err := e.eng.Logs(id, "chat", 1, after, 5000)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range ls {
			if strings.HasPrefix(l.Text, "line ") {
				total++
			}
		}
		if len(ls) < 5000 {
			break
		}
		after = ls[len(ls)-1].Seq
	}
	if total != 100000 {
		t.Fatalf("%d of 100000 lines are in the full log", total)
	}
}

// A line appended through server A reaches a browser connected to server B.
func TestLiveAcrossTwoServers(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("two servers share a PostgreSQL database: set " + testdb.EnvVar)
	}
	e := setupIdentities(t)
	// The runner talks to server A; the watcher is on server B.
	dir := t.TempDir()
	r := &runner.Runner{Client: runner.NewClient(e.a.URL, "secret"), Exec: &executor.Host{},
		Info: engine.RunnerInfo{ID: "r1", Name: "r1"}, DataDir: filepath.Join(dir, "r1"), Concurrency: 1, Log: quiet()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	p, err := pipeline.Parse([]byte(tickPipeline))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	run, err := e.eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
		Source: engine.Source{Kind: "dir", Path: work}})
	if err != nil {
		t.Fatal(err)
	}
	ch, stop := watch(t, e.b, run.ID)
	defer stop()
	lat := latencies(t, ch)
	if len(lat) < 50 {
		t.Fatalf("only %d of 50 lines reached the other server's stream", len(lat))
	}
	p50, p95 := percentile(lat, 0.5), percentile(lat, 0.95)
	t.Logf("two servers: p50 %s, p95 %s", p50, p95)
	if p95 > 2*time.Second {
		t.Fatalf("p95 %s", p95)
	}
}

// fakeCluster stands in for the Kubernetes executor: output reaches the
// sink in pieces from a remote stream, as the exec stream delivers it.
type fakeCluster struct{ executor.Host }

func (*fakeCluster) Name() string { return "kubernetes" }

func (*fakeCluster) Run(ctx context.Context, s executor.Spec, out executor.Sink) (int, error) {
	for i := 0; i < 50; i++ {
		out("stdout", fmt.Sprintf("tick %d %d", i, time.Now().UnixMilli()))
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return 0, nil
}

func TestLiveLineLatencyFakeKubernetes(t *testing.T) {
	e := setup(t, 5*time.Second)
	r := e.newRunner("k1")
	r.Exec = &fakeCluster{}
	e.startRunner(t, r)
	id := e.start(t, tickPipeline)
	ch, cancel := watch(t, e.srv, id)
	defer cancel()
	lat := latencies(t, ch)
	if len(lat) < 50 {
		t.Fatalf("only %d of 50 lines arrived", len(lat))
	}
	p50, p95 := percentile(lat, 0.5), percentile(lat, 0.95)
	t.Logf("fake Kubernetes executor: p50 %s, p95 %s", p50, p95)
	if p95 > 2*time.Second {
		t.Fatalf("p95 %s", p95)
	}
}
