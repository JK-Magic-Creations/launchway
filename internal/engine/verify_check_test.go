package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// watchHealth verifies an environment with the built-in health proof: no
// Prometheus series is read, the deploy runner calls the service itself.
const watchHealth = `pipeline: shop
steps:
  watch:
    kind: verify
    environment: prod
    duration: 3m
    interval: 1m
    metrics:
      - name: healthy
        source: checks
        check:
          url: http://shop.shop.svc.cluster.local:8080/healthz
          status: [200]
          success: 0.9
`

var checkSources = []HealthSource{{Name: "checks", Type: "check", Projects: []string{"shop"}}, prom[0]}

func openChecks(t *testing.T) (*Engine, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: prodEnv, HealthSources: checkSources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, clk
}

// reportChecks reports interval i as a runner would after calling the
// service 20 times, bad of which failed with status 503.
func reportChecks(t *testing.T, e *Engine, clk *clock, j *Job, i, bad int) *VerifyAck {
	t.Helper()
	vj := j.Verify
	iv := verify.Interval{Index: i, Start: vj.Start + int64(i)*vj.IntervalMS}
	iv.End = iv.Start + vj.IntervalMS
	clk.Add(time.Duration(max(iv.End-clk.Now().UnixMilli(), 0)) * time.Millisecond)
	md := verify.MetricData{Name: "healthy"}
	for k := 0; k < 20; k++ {
		a := verify.Attempt{T: iv.Start + int64(k)*3000, OK: k >= bad, Status: 200, LatencyMS: 12}
		v := 1.0
		if !a.OK {
			a.Status, a.Error, v = 503, "returned 503", 0
		}
		md.Attempts = append(md.Attempts, a)
		md.Canary = append(md.Canary, verify.Point{T: a.T, V: v})
	}
	if bad > 0 {
		md.Note = "3 of 20 requests to /healthz returned 503"
	}
	iv.Metrics = []verify.MetricData{md}
	ack, err := e.VerifyInterval(j.LeaseID, &iv)
	if err != nil {
		t.Fatal(err)
	}
	return ack
}

func TestHealthProofPassesWithoutAnyProbeSeries(t *testing.T) {
	e, clk := openChecks(t)
	id := createShop(t, e, watchHealth, 1)
	j := leaseVerify(t, e)
	if m := j.Verify.Metrics[0]; m.Check == nil || m.Query != "" || m.Check.URL == "" {
		t.Fatalf("job metric: %+v", m)
	}
	if sp := j.Verify.Spec.Metrics[0]; sp.Min == nil || *sp.Min != 0.9 || sp.Direction != verify.LowerIsWorse || sp.Baseline {
		t.Fatalf("spec: %+v", sp)
	}
	var ack *VerifyAck
	for i := 0; i < 3; i++ {
		ack = reportChecks(t, e, clk, j, i, 1) // one failure in twenty: 0.95, above the 0.9 asked for
	}
	if !ack.Final || ack.Verdict.Outcome != verify.Passed {
		t.Fatalf("verdict: %+v", ack.Verdict)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Verify: &VerifyResult{}}); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "watch"); s.Status != StepSucceeded {
		t.Fatalf("step: %+v", s)
	}
	vs, err := e.RunVerifications(id)
	if err != nil || len(vs) != 1 || len(vs[0].Intervals) != 3 {
		t.Fatalf("%v %+v", err, vs)
	}
	if got := vs[0].Intervals[0].Metrics[0].Attempts; len(got) != 20 || got[0].Status != 503 || got[1].Status != 200 {
		t.Fatalf("attempts are not kept: %+v", got)
	}
}

func TestHealthProofFailsWithAPlainSentence(t *testing.T) {
	e, clk := openChecks(t)
	id := createShop(t, e, watchHealth, 1)
	j := leaseVerify(t, e)
	reportChecks(t, e, clk, j, 0, 3)
	ack := reportChecks(t, e, clk, j, 1, 3)
	if !ack.Final || ack.Verdict.Outcome != verify.Failed ||
		!strings.Contains(ack.Verdict.Summary, "3 of 20 requests to /healthz returned 503") {
		t.Fatalf("verdict: %+v", ack.Verdict)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Verify: &VerifyResult{}}); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "watch"); s.Status != StepFailed {
		t.Fatalf("step: %+v", s)
	}
}

func TestHealthProofIsValidatedAgainstTheServer(t *testing.T) {
	e, _ := openChecks(t)
	for _, c := range []struct{ src, want string }{
		{strings.Replace(watchHealth, "source: checks", "source: prometheus", 1), "a check needs a source of type check"},
		{strings.Replace(watchHealth, "source: checks", "source: other", 1), `health source "other" is not configured`},
		{strings.Replace(envVerify, "source: prometheus", "source: checks", 1), "it only runs checks"},
	} {
		_, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, c.src), Source: gitSource})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
}
