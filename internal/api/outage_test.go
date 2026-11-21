package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// gate puts a server behind a switch that makes it refuse every request
// ("the server is down"): with a 503, as when its database is gone, or by
// dropping the connection, as when the process is restarting.
type gate struct {
	down atomic.Int32 // 0 up, 1 answers 503, 2 drops the connection
}

func (g *gate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch g.down.Load() {
		case 1:
			w.Header().Set("Retry-After", "5")
			http.Error(w, `{"error":"the database is not available"}`, http.StatusServiceUnavailable)
		case 2:
			if h, ok := w.(http.Hijacker); ok {
				if c, _, err := h.Hijack(); err == nil {
					c.Close()
					return
				}
			}
			http.Error(w, "down", http.StatusServiceUnavailable)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// outageEnv is a server whose runner-facing address can be switched off.
type outageEnv struct {
	*env
	g *gate
}

func setupGated(t *testing.T) *outageEnv {
	t.Helper()
	dir := t.TempDir()
	db := testdb.New(t)
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 30 * time.Second, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{}
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet()}
	srv := httptest.NewServer(g.wrap(s.Handler()))
	t.Cleanup(func() { srv.Close(); eng.Close() })
	return &outageEnv{env: &env{eng: eng, srv: srv, dir: dir, db: db}, g: g}
}

func (e *outageEnv) logText(run, step string) string {
	lines, _, _ := e.eng.Logs(run, step, 1, 0, 5000)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

const outagePipeline = `
pipeline: demo
steps:
  work:
    image: none
    run: |
      echo one
      sleep 2
      echo two
      sleep 1
      echo three
`

func (e *outageEnv) newSpoolRunner(name string) *runner.Runner {
	r := e.newRunner(name)
	r.DataDir = e.dir + "/runner-data" // the same directory for a restarted runner
	r.Exec = &executor.Host{}
	return r
}

// The server is unreachable while a step runs and finishes: the step
// completes, its log is complete and in order, its result is recorded once,
// and the log says the server could not be reached.
func TestStepRidesThroughAnOutage(t *testing.T) {
	for name, mode := range map[string]int32{"503": 1, "connection dropped": 2} {
		t.Run(name, func(t *testing.T) {
			e := setupGated(t)
			e.startRunner(t, e.newSpoolRunner("r1"))
			id := e.start(t, outagePipeline)
			waitFor(t, "the first line", 20*time.Second, func() bool { return strings.Contains(e.logText(id, "work"), "one") })
			e.g.down.Store(mode)
			time.Sleep(3500 * time.Millisecond) // the step ends while the server is gone
			e.g.down.Store(0)
			d := e.wait(t, id, 30*time.Second)
			if d.Status != "succeeded" {
				t.Fatalf("run %s: %+v", d.Status, d.StepList)
			}
			log := e.logText(id, "work")
			for _, want := range []string{"one", "two", "three"} {
				if n := lineCount(log, want); n != 1 {
					t.Errorf("%q appears %d times in the log:\n%s", want, n, log)
				}
			}
			if strings.Count(log, "nothing was lost") != 1 {
				t.Errorf("the log should say once that nothing was lost:\n%s", log)
			}
			if strings.Index(log, "\none\n") > strings.Index(log, "\ntwo\n") || strings.Index(log, "\ntwo\n") > strings.Index(log, "\nthree\n") {
				t.Errorf("lines out of order:\n%s", log)
			}
			if n := countEvents(t, e.env, id, engine.StepFinished); n != 1 {
				t.Errorf("the result was recorded %d times", n)
			}
		})
	}
}

// The runner restarts during the outage: a new one finds the spool and
// delivers the result and the log.
func TestRestartedRunnerDeliversTheSpool(t *testing.T) {
	e := setupGated(t)
	first := e.newSpoolRunner("r1")
	cancel, done := e.startRunner(t, first)
	id := e.start(t, outagePipeline)
	waitFor(t, "the first line", 20*time.Second, func() bool { return strings.Contains(e.logText(id, "work"), "one") })
	e.g.down.Store(1)
	// The step ends while the server is gone; its result is on disk.
	waitFor(t, "the result on disk", 20*time.Second, func() bool {
		return spoolHasResult(e.dir + "/runner-data/spool")
	})
	cancel()
	<-done
	e.startRunner(t, e.newSpoolRunner("r1"))
	time.Sleep(500 * time.Millisecond)
	e.g.down.Store(0)
	d := e.wait(t, id, 30*time.Second)
	if d.Status != "succeeded" {
		t.Fatalf("run %s: %+v", d.Status, d.StepList)
	}
	log := e.logText(id, "work")
	if lineCount(log, "three") != 1 || lineCount(log, "one") != 1 {
		t.Errorf("log:\n%s", log)
	}
	if n := countEvents(t, e.env, id, engine.StepFinished); n != 1 {
		t.Errorf("the result was recorded %d times", n)
	}
}

// lineCount is how many lines of log are exactly text.
func lineCount(log, text string) int {
	n := 0
	for _, l := range strings.Split(log, "\n") {
		if l == text {
			n++
		}
	}
	return n
}

// An outage longer than the 15 seconds a runner used to give up after: the
// step's real outcome is still recorded, once, and not turned into a failure.
func TestOutageLongerThanTheOldRetryWindow(t *testing.T) {
	e := setupGated(t)
	e.startRunner(t, e.newSpoolRunner("r1"))
	id := e.start(t, outagePipeline)
	waitFor(t, "the first line", 20*time.Second, func() bool { return strings.Contains(e.logText(id, "work"), "one") })
	e.g.down.Store(1)
	time.Sleep(17 * time.Second)
	e.g.down.Store(0)
	d := e.wait(t, id, 30*time.Second)
	if d.Status != "succeeded" || d.StepList[0].Attempt != 1 {
		t.Fatalf("run %s: %+v", d.Status, d.StepList)
	}
	if n := countEvents(t, e.env, id, engine.StepFinished); n != 1 {
		t.Errorf("the result was recorded %d times", n)
	}
}
