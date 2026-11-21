package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	hs "github.com/jk-magic-creations/launchway/internal/verify/source"
)

// The deploy runner calls the service while the interval runs and reports
// every call, with no Prometheus involved; a 500 shows as a plain sentence.
func TestRunnerMakesTheHealthCallsItself(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	srcs, err := hs.Open(map[string]hs.Config{"checks": {Type: hs.Check, Hosts: []string{"127.0.0.1"}, Projects: []string{"shop"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &DeployRunner{Sources: srcs}
	chk := &pipeline.HealthCheck{Kind: "http", URL: srv.URL + "/healthz", Method: "GET", Success: 1,
		Timeout: pipeline.Duration(time.Second), Every: pipeline.Duration(200 * time.Millisecond)}
	for _, c := range []struct {
		code int
		note string
	}{{200, ""}, {500, "requests to /healthz returned 500"}} {
		status = c.code
		start := time.Now().Add(100 * time.Millisecond)
		job := &engine.Job{Project: "shop", Verify: &engine.VerifyJob{Start: start.UnixMilli(), IntervalMS: 800, Intervals: 1,
			Metrics: []engine.VerifyQuery{{Name: "healthy", Source: "checks", Check: chk}}}}
		if !onlyChecks(job.Verify) {
			t.Fatal("a verification of checks only waits for no source")
		}
		ch := d.startChecks(context.Background(), job)[0]
		got := <-ch
		iv := d.readInterval(context.Background(), job, 0, start.UnixMilli(), start.UnixMilli()+800, got)
		md := iv.Metrics[0]
		if md.Error != "" || len(md.Attempts) < 3 || len(md.Canary) != len(md.Attempts) {
			t.Fatalf("%d: %+v", c.code, md)
		}
		if c.note == "" && md.Note != "" || c.note != "" && !strings.Contains(md.Note, c.note) {
			t.Fatalf("%d: note %q", c.code, md.Note)
		}
	}
}
