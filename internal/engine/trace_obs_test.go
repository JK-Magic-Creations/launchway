package engine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/metrics"
	"github.com/jk-magic-creations/launchway/internal/trace"
)

// A secret that a step is given (in its environment and in its command) must appear in no span the
// server exports and in no metric. Spans carry an allow-list of attributes, and metrics carry only
// closed label sets; this runs a real run and reads everything both sent.
func TestASecretOfAStepIsNeverInSpansOrMetrics(t *testing.T) {
	const secret = "hunter2-s3cret-VALUE-9f8e7d"
	var mu sync.Mutex
	var sent bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent.Write(b)
		mu.Unlock()
	}))
	defer srv.Close()
	trace.Configure(context.Background(), &trace.Config{Endpoint: srv.URL, Ratio: 1})
	defer trace.Configure(context.Background(), nil)

	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  build:
    image: x
    run: |
      curl -H "Authorization: Bearer `+secret+`" https://example.test
    env: {API_TOKEN: `+secret+`}
`, Trigger{Event: "push", Branch: "main", SHA: "0123456789abcdef"})
	j := take(t, e)
	if j.Traceparent == "" {
		t.Fatal("the Station was given no trace context while tracing is on")
	}
	if !strings.Contains(j.Traceparent, trace.RunTraceID(id)) {
		t.Fatalf("the trace context %q is not the run's trace", j.Traceparent)
	}
	ok(t, e, j, nil)
	trace.Flush()

	mu.Lock()
	spans := sent.String()
	mu.Unlock()
	for _, want := range []string{trace.RunTraceID(id), `"name":"run"`, `"name":"step"`, `"name":"queue wait"`} {
		if !strings.Contains(spans, want) {
			t.Errorf("exported spans lack %s:\n%s", want, spans)
		}
	}
	if strings.Contains(spans, secret) || strings.Contains(spans, "curl") || strings.Contains(spans, "API_TOKEN") {
		t.Fatalf("a secret, a command or an environment name reached the exported spans:\n%s", spans)
	}
	var m bytes.Buffer
	if err := metrics.Default.Write(&m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.String(), secret) || strings.Contains(m.String(), "curl") || strings.Contains(m.String(), id) {
		t.Fatal("a secret, a command or a run id reached the metrics")
	}
	if !strings.Contains(m.String(), `launchway_runs_finished_total{outcome="succeeded"}`) {
		t.Errorf("the finished run was not counted:\n%s", m.String())
	}
}
