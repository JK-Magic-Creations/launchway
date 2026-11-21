package api

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// ---- repeated delivery ----

// repeatTransport delivers every runner write twice, as a runner does when
// the answer to the first was lost. Reads and the long polls that take work
// are sent once. It answers with the second answer, so a repeat that the
// server refuses would break the step.
type repeatTransport struct {
	next  http.RoundTripper
	sent  atomic.Int32
	again atomic.Int32
}

func isWrite(r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/runner/v1/") || r.Method == http.MethodGet {
		return false
	}
	for _, once := range []string{"/lease", "/watch", "/commands", "/download", "/lookup", "/runs", "/leave"} {
		if strings.HasSuffix(p, once) {
			return false
		}
	}
	return true
}

func (t *repeatTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.sent.Add(1)
	if !isWrite(r) {
		return t.next.RoundTrip(r)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	send := func() (*http.Response, error) {
		c := r.Clone(r.Context())
		c.Body = io.NopCloser(bytes.NewReader(body))
		c.ContentLength = int64(len(body))
		return t.next.RoundTrip(c)
	}
	first, err := send()
	if err != nil {
		return nil, err
	}
	t.again.Add(1)
	if strings.HasSuffix(r.URL.Path, "/chunk") {
		// A chunk sent again is answered with where the file stands; the
		// runner carries on from the first answer.
		if resp, err := send(); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		return first, nil
	}
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	return send()
}

// Every runner write sent twice: a step with logs, outputs, a cache saved in
// two chunks and a result finishes once, with one result and no repeated
// lines.
func TestEveryRunnerWriteIsAppliedOnceWhenDeliveredTwice(t *testing.T) {
	ce := newCacheEnvNoRunner(t)
	rt := &repeatTransport{next: http.DefaultTransport}
	r := ce.newRunner("r1")
	r.Client.HTTP = &http.Client{Transport: rt, Timeout: 60 * time.Second}
	ce.startRunner(t, r)
	text := cachePipelineText("deps", "latest", "success", `  after:
    image: none
    needs: test
    run: echo "after ran"
`)
	d, log := ce.runWith(t, "demo", text, pushMain(ce.head(t)))
	if d.Status != "succeeded" {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	if rt.again.Load() < 5 {
		t.Fatalf("only %d writes were repeated; the test is not exercising the API", rt.again.Load())
	}
	for _, s := range d.StepList {
		if n := countEvents(t, ce.env, d.ID, engine.StepFinished); n != len(d.StepList) {
			t.Fatalf("%d step.finished events for %d steps (%s)", n, len(d.StepList), s.ID)
		}
	}
	if n := lineCount(log, "now n=1"); n != 1 {
		t.Fatalf("the step's own line appears %d times:\n%s", n, log)
	}
	if n := strings.Count(log, "Cache deps: saved "); n != 1 {
		t.Fatalf("the cache was saved %d times:\n%s", n, log)
	}
	list, err := ce.eng.Caches("demo")
	if err != nil || len(list.Entries) != 1 {
		t.Fatalf("cache entries: %+v %v", list, err)
	}
}

// ---- the server restarts while a step streams its log ----

// swappable is a server whose engine can be replaced behind a fixed address,
// as a restart does.
type swappable struct {
	mu sync.Mutex
	h  http.Handler
	ln net.Listener
	hs *httptest.Server
}

func (s *swappable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	if h == nil {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				c.Close()
				return
			}
		}
		http.Error(w, "restarting", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, r)
}

func (s *swappable) set(h http.Handler) { s.mu.Lock(); s.h = h; s.mu.Unlock() }

const streamPipeline = `
pipeline: demo
steps:
  stream:
    image: none
    run: |
      i=1
      while [ $i -le 120 ]; do echo "line $i"; i=$((i+1)); sleep 0.05; done
`

// The server is restarted (the old engine closed, a new one opened on the same
// database) while a step streams 120 lines: afterwards the log has every line
// once and in order, and the run succeeded with one result.
func TestServerRestartDuringLogStreaming(t *testing.T) {
	dir := t.TempDir()
	db := testdb.New(t)
	open := func() (*engine.Engine, http.Handler) {
		eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 30 * time.Second, Logger: quiet()})
		if err != nil {
			t.Fatal(err)
		}
		return eng, (&Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet()}).Handler()
	}
	eng, h := open()
	sw := &swappable{h: h}
	srv := httptest.NewServer(sw)
	t.Cleanup(srv.Close)
	cur := eng
	t.Cleanup(func() { cur.Close() })
	e := &env{eng: eng, srv: srv, dir: dir, db: db}
	r := e.newRunner("r1")
	r.Exec = &executor.Host{}
	e.startRunner(t, r)
	id := e.start(t, streamPipeline)
	waitFor(t, "some lines", 20*time.Second, func() bool {
		lines, _, _ := cur.Logs(id, "stream", 1, 0, 5000)
		return len(lines) > 40
	})

	sw.set(nil) // the process goes away
	if err := cur.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	cur, h = open() // and comes back on the same database
	e.eng = cur
	sw.set(h)

	d := e.wait(t, id, 60*time.Second)
	if d.Status != "succeeded" {
		t.Fatalf("run %s: %+v", d.Status, d.StepList)
	}
	lines, _, err := cur.Logs(id, "stream", 1, 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range lines {
		if strings.HasPrefix(l.Text, "line ") {
			got = append(got, l.Text)
		}
	}
	if len(got) != 120 {
		t.Fatalf("%d numbered lines, want 120", len(got))
	}
	for i, l := range got {
		if want := "line " + itoa(i+1); l != want {
			t.Fatalf("line %d is %q, want %q (a duplicate, a gap or the wrong order)", i, l, want)
		}
	}
	if n := countEvents(t, e, id, engine.StepFinished); n != 1 {
		t.Fatalf("the result was recorded %d times", n)
	}
}

func itoa(i int) string {
	var b [20]byte
	n := len(b)
	for ; i > 0; i /= 10 {
		n--
		b[n] = byte('0' + i%10)
	}
	return string(b[n:])
}

// ---- the database is gone for 30 seconds ----

const longStep = `
pipeline: demo
steps:
  work:
    image: none
    run: |
      echo before
      sleep 38
      echo after
`

// The database is unreachable for 30 seconds in the middle of a running step
// (every database connection dropped, new ones refused, through a TCP
// proxy). Requests wait, leases get a fresh TTL once the database is back, and
// the step completes with its log complete and its result recorded once.
func TestDatabaseGoneForThirtySecondsDuringAStep(t *testing.T) {
	addr, proxy := testdb.ThroughProxy(t, testdb.New(t))
	dir := t.TempDir()
	eng, err := engine.Open(engine.Options{Database: addr, LeaseTTL: 30 * time.Second, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	e := &env{eng: eng, srv: srv, dir: dir, db: addr}
	r := e.newRunner("r1")
	r.Exec = &executor.Host{}
	e.startRunner(t, r)
	id := e.start(t, longStep)
	waitFor(t, "the first line", 30*time.Second, func() bool {
		lines, _, _ := eng.Logs(id, "work", 1, 0, 100)
		for _, l := range lines {
			if l.Text == "before" {
				return true
			}
		}
		return false
	})
	proxy.SetDown(true)
	time.Sleep(30 * time.Second)
	proxy.SetDown(false)
	d := e.wait(t, id, 90*time.Second)
	if d.Status != "succeeded" || d.StepList[0].Attempt != 1 {
		evs, _ := eng.Events(id)
		for _, ev := range evs {
			t.Logf("event %s at %d: %+v", ev.Type, ev.At, ev.Data)
		}
		t.Fatalf("run %s, step attempt %d", d.Status, d.StepList[0].Attempt)
	}
	lines, _, _ := eng.Logs(id, "work", 1, 0, 100)
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	log := strings.Join(texts, "\n")
	if lineCount(log, "before") != 1 || lineCount(log, "after") != 1 {
		t.Fatalf("the log is not complete and once:\n%s", log)
	}
	if n := countEvents(t, e, id, engine.StepFinished); n != 1 {
		t.Fatalf("the result was recorded %d times", n)
	}
}
