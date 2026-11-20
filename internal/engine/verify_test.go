package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// verifiedCanary deploys to the first pause, verifies the canary against
// the stable version, and promotes it, or aborts it and rolls back.
const verifiedCanary = `pipeline: shop
steps:
  build:
    image: x
    run: build
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    timeout: 10m
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } }
    rollout: { name: shop, namespace: shop, until: paused }
  verify-canary:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod
    duration: 4m
    interval: 1m
    on_fail: rollback
    on_inconclusive: pause
    params:
      ns: { deployment: namespace }
      canary: { deployment: canary_hash }
      stable: { deployment: stable_hash }
    metrics:
      - name: error-rate
        source: prometheus
        query: 'sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{canary}}",code=~"5.."}[1m]))'
        stable_query: 'sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{stable}}",code=~"5.."}[1m]))'
        max: 0.05
  promote:
    kind: rollout
    needs: verify-canary
    deployment: deploy-prod
    action: promote
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
    abort_first: true
`

// envVerify watches an environment without a deployment.
const envVerify = `pipeline: shop
steps:
  watch:
    kind: verify
    environment: prod
    duration: 3m
    interval: 1m
    metrics:
      - name: latency
        source: prometheus
        query: 'avg(latency_seconds{service="shop"})'
`

var prom = []HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"shop"}}}

func openVerify(t *testing.T, addr string, clk *clock, envs []Environment, models *ai.Registry) *Engine {
	t.Helper()
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Environments: envs, HealthSources: prom, Models: models})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func newVerifyEngine(t *testing.T, envs []Environment) (*Engine, *clock, string) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	return openVerify(t, addr, clk, envs, nil), clk, addr
}

func leaseVerify(t *testing.T, e *Engine) *Job {
	t.Helper()
	j, err := e.LeaseVerify(context.Background(), deployer, deployer.Targets, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil || j.Verify == nil {
		t.Fatal("expected a verification to lease")
	}
	if _, err := e.Start(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	return j
}

// series is n readings of v inside an interval.
func series(iv verify.Interval, v float64) []verify.Point {
	var out []verify.Point
	for i := 0; i < 4; i++ {
		out = append(out, verify.Point{T: iv.Start + int64(i)*15_000, V: v + float64(i%2)*v*0.05})
	}
	return out
}

// report reads interval i as the deploy runner would, with the canary at
// canary and the stable version at stable, once the interval is over.
func report(t *testing.T, e *Engine, clk *clock, j *Job, i int, canary, stable float64) *VerifyAck {
	t.Helper()
	vj := j.Verify
	iv := verify.Interval{Index: i, Start: vj.Start + int64(i)*vj.IntervalMS}
	iv.End = iv.Start + vj.IntervalMS
	if now := clk.Now().UnixMilli(); now < iv.End {
		clk.Add(time.Duration(iv.End-now) * time.Millisecond)
	}
	for _, m := range vj.Spec.Metrics {
		md := verify.MetricData{Name: m.Name, Canary: series(iv, canary)}
		if m.Stable {
			md.Stable = series(iv, stable)
		}
		iv.Metrics = append(iv.Metrics, md)
	}
	ack, err := e.VerifyInterval(j.LeaseID, &iv)
	if err != nil {
		t.Fatal(err)
	}
	return ack
}

func TestVerifiedCanaryPromotes(t *testing.T) {
	e, clk, _ := newVerifyEngine(t, prodEnv)
	id := createShop(t, e, verifiedCanary, 1)
	toPause(t, e, id)
	j := leaseVerify(t, e)
	if j.Verify.Params["canary"] != "h2" || j.Verify.Params["stable"] != "h1" || j.Verify.Params["ns"] != "shop" {
		t.Fatalf("params: %+v", j.Verify.Params)
	}
	// An interval cannot be reported before it is over.
	early := verify.Interval{Index: 0, Start: j.Verify.Start, End: j.Verify.Start + j.Verify.IntervalMS}
	if _, err := e.VerifyInterval(j.LeaseID, &early); !errors.Is(err, ErrConflict) {
		t.Fatalf("an interval not over yet was taken: %v", err)
	}
	var ack *VerifyAck
	for i := 0; i < 4; i++ {
		ack = report(t, e, clk, j, i, 0.003, 0.003)
		if i < 3 && ack.Final {
			t.Fatalf("final after interval %d: %+v", i, ack.Verdict)
		}
	}
	if !ack.Final || ack.Verdict.Outcome != verify.Passed {
		t.Fatalf("verdict: %+v", ack.Verdict)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Verify: &VerifyResult{}}); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "verify-canary"); s.Status != StepSucceeded || s.Outputs["verdict"] != "passed" {
		t.Fatalf("verify step: %+v", s)
	}
	if s := stepOf(t, e, id, "promote"); s.Status != StepWaitingS {
		t.Fatalf("promote: %s", s.Status)
	}
	vs, err := e.RunVerifications(id)
	if err != nil || len(vs) != 1 || !vs[0].Decided || len(vs[0].Intervals) != 4 {
		t.Fatalf("views: %v %+v", err, vs)
	}
}

func TestFailedVerificationAbortsAndRollsBack(t *testing.T) {
	e, clk, _ := newVerifyEngine(t, prodEnv)
	id := createShop(t, e, verifiedCanary, 1)
	toPause(t, e, id)
	j := leaseVerify(t, e)
	report(t, e, clk, j, 0, 0.003, 0.003)
	if ack := report(t, e, clk, j, 1, 0.042, 0.003); ack.Final {
		t.Fatalf("one failing interval was final: %+v", ack.Verdict)
	}
	ack := report(t, e, clk, j, 2, 0.044, 0.003)
	if !ack.Final || ack.Verdict.Outcome != verify.Failed || !strings.Contains(ack.Verdict.Summary, "error-rate") {
		t.Fatalf("verdict: %+v", ack.Verdict)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Verify: &VerifyResult{}}); err != nil {
		t.Fatal(err)
	}
	s := stepOf(t, e, id, "verify-canary")
	if s.Status != StepFailed || !strings.Contains(s.Error, "verification failed") {
		t.Fatalf("verify step: %+v", s)
	}
	// The rollout is aborted at once; the rollback step reverts.
	c := takeCommand(t, e)
	if c.Action != pipeline.ActionAbort || !strings.Contains(c.Comment, "verify-canary") || c.Expect.PodHash != "h2" {
		t.Fatalf("command: %+v", c)
	}
	if s := stepOf(t, e, id, "rollback-prod"); s.Status != StepQueuedS {
		t.Fatalf("rollback: %s", s.Status)
	}
}

func TestInconclusivePausesForAPerson(t *testing.T) {
	e, clk, _ := newVerifyEngine(t, protectedEnv)
	id := createShop(t, e, verifiedCanary, 1)
	toPause(t, e, id)
	j := leaseVerify(t, e)
	vj := j.Verify
	for i := 0; i < 4; i++ {
		iv := verify.Interval{Index: i, Start: vj.Start + int64(i)*vj.IntervalMS}
		iv.End = iv.Start + vj.IntervalMS
		clk.Add(time.Minute)
		iv.Metrics = []verify.MetricData{{Name: "error-rate", Error: "prometheus: the source did not answer within 10s"}}
		if _, err := e.VerifyInterval(j.LeaseID, &iv); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "verify-canary"); s.Status != StepWaitingS {
		t.Fatalf("not paused: %+v", s)
	}
	appr, err := e.Approvals()
	if err != nil || len(appr) != 1 || appr[0].StepID != "verify-canary" {
		t.Fatalf("approvals: %v %+v", err, appr)
	}
	// The list says it waits for a person, not only that it is inconclusive;
	// the run page has its environment from the deployment.
	if l, err := e.Verifications(VerificationFilter{Environment: "prod"}); err != nil || len(l) != 1 ||
		l[0].Status != string(StepWaitingS) || l[0].Outcome != verify.Inconclusive {
		t.Fatalf("list: %v %+v", err, l)
	}
	if s := stepOf(t, e, id, "verify-canary"); s.Environment != "prod" {
		t.Fatalf("step environment %q", s.Environment)
	}
	// Not by the person who started it, on a protected environment.
	{
		l := *e.live()
		l.selfOK = false
		e.cur.Store(&l)
	}
	starter := ""
	if d, err := e.GetRun(id); err == nil {
		starter = d.Actor
	}
	if starter != "" {
		if err := e.OverrideVerify(OverrideRequest{Run: id, Step: "verify-canary", Actor: starter, Outcome: "passed", Comment: "fine"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("the starter marked it passed: %v", err)
		}
	}
	if err := e.Decide(id, "verify-canary", true, "olga", "dashboards look fine"); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "verify-canary"); s.Status != StepSucceeded || s.Outputs["verdict"] != "passed" {
		t.Fatalf("after approval: %+v", s)
	}
	vs, _ := e.RunVerifications(id)
	if vs[0].Override != "passed" || vs[0].OverrideBy != "olga" {
		t.Fatalf("override not recorded: %+v", vs[0])
	}
}

func TestOverrideWhileRunning(t *testing.T) {
	e, clk, _ := newVerifyEngine(t, prodEnv)
	id := createShop(t, e, verifiedCanary, 1)
	toPause(t, e, id)
	j := leaseVerify(t, e)
	report(t, e, clk, j, 0, 0.003, 0.003)
	if err := e.OverrideVerify(OverrideRequest{Run: id, Step: "verify-canary", Actor: "olga", Outcome: "failed"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an override without a reason: %v", err)
	}
	if err := e.OverrideVerify(OverrideRequest{Run: id, Step: "verify-canary", Actor: "olga", Outcome: "failed", Comment: "customers report errors"}); err != nil {
		t.Fatal(err)
	}
	// The runner's lease is gone.
	iv := verify.Interval{Index: 1, Start: j.Verify.Start + j.Verify.IntervalMS, End: j.Verify.Start + 2*j.Verify.IntervalMS}
	clk.Add(time.Minute)
	if _, err := e.VerifyInterval(j.LeaseID, &iv); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("the runner kept its lease: %v", err)
	}
	if s := stepOf(t, e, id, "verify-canary"); s.Status != StepFailed || !strings.Contains(s.Error, "marked failed by olga") {
		t.Fatalf("verify step: %+v", s)
	}
	if c := takeCommand(t, e); c.Action != pipeline.ActionAbort || c.Actor != "olga" {
		t.Fatalf("command: %+v", c)
	}
}

// A runner that stops is replaced; the intervals it read count.
func TestVerificationTakenOver(t *testing.T) {
	e, clk, _ := newVerifyEngine(t, prodEnv)
	id := createShop(t, e, envVerify, 1)
	j := leaseVerify(t, e)
	report(t, e, clk, j, 0, 0.2, 0)
	clk.Add(2 * ttl)
	e.reap()
	j2 := leaseVerify(t, e)
	if j2.Attempt != 2 || len(j2.Verify.Done) != 1 || j2.Verify.Done[0] != 0 {
		t.Fatalf("second attempt: %d %+v", j2.Attempt, j2.Verify.Done)
	}
	report(t, e, clk, j2, 1, 0.2, 0)
	ack := report(t, e, clk, j2, 2, 0.2, 0)
	if !ack.Final {
		t.Fatalf("not final: %+v", ack.Verdict)
	}
	// No baseline yet and no other check: never a silent pass.
	if ack.Verdict.Outcome != verify.Inconclusive {
		t.Fatalf("verdict: %+v", ack.Verdict)
	}
	if err := e.Finish(j2.LeaseID, Result{Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "watch"); s.Status != StepFailed {
		t.Fatalf("watch: %+v", s)
	}
}

// What a passed verification saw is the next one's baseline.
func TestBaselineFromEarlierVerifications(t *testing.T) {
	e, clk, _ := newVerifyEngine(t, prodEnv)
	src := strings.Replace(strings.Replace(envVerify, "        query:", "        max: 1\n        query:", 1), "duration: 3m", "duration: 4m", 1)
	for n := 1; n <= 4; n++ {
		id := createShop(t, e, src, n)
		j := leaseVerify(t, e)
		if n == 4 {
			if b := j.Verify.Spec.Metrics[0].Baseline; !b {
				t.Fatal("the metric is not compared with a baseline")
			}
		}
		var ack *VerifyAck
		for i := 0; i < 4; i++ {
			v := 0.2 + float64(i)*0.01
			if n == 4 {
				v = 0.9 // under max, but far from what earlier releases did
			}
			ack = report(t, e, clk, j, i, v, 0)
		}
		if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		want := verify.Passed
		if n == 4 {
			want = verify.Failed
		}
		if ack.Verdict.Outcome != want {
			t.Fatalf("run %d: %+v", n, ack.Verdict)
		}
		vs, _ := e.RunVerifications(id)
		if n == 4 && (len(vs[0].Baseline) != 3 || !strings.Contains(vs[0].Verdict.Summary, "baseline")) {
			t.Fatalf("run 4's baseline: %+v / %s", vs[0].Baseline, vs[0].Verdict.Summary)
		}
	}
}

// Several servers reach the same verdict from the same events.
func TestServersAgreeOnTheVerdict(t *testing.T) {
	e, clk, addr := newVerifyEngine(t, prodEnv)
	other := openVerify(t, addr, clk, prodEnv, nil)
	id := createShop(t, e, verifiedCanary, 1)
	toPause(t, e, id)
	j := leaseVerify(t, e)
	report(t, e, clk, j, 0, 0.003, 0.003)
	report(t, e, clk, j, 1, 0.042, 0.003)
	a, _ := e.RunVerifications(id)
	b, _ := other.RunVerifications(id)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("servers disagree:\n%s\n%s", ja, jb)
	}
}

func TestVerifyValidatedAgainstTheServer(t *testing.T) {
	e, _, _ := newVerifyEngine(t, prodEnv)
	for _, c := range []struct{ src, want string }{
		{strings.Replace(envVerify, "source: prometheus", "source: thanos", 1), `health source "thanos" is not configured`},
		{strings.Replace(envVerify, `{service="shop"}`, `{service={{svc}}}`, 1) + "    params: { svc: shop }\n", "must be inside quotes"},
		{strings.Replace(envVerify, "environment: prod", "environment: staging", 1), `environment "staging" is not configured`},
		{envVerify + "    model: local\n", "no AI models configured"},
	} {
		_, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, c.src), Source: gitSource})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
	// Pull-request runs do not read health sources unless the project
	// allows secrets for pull requests.
	src := "pipeline: shop\non: { pull_request: {} }\n" + strings.TrimPrefix(envVerify, "pipeline: shop\n")
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, src), Source: gitSource,
		Trigger: Trigger{Event: pipeline.EventPullRequest, Ref: PullRequestRef(3), PullRequest: 3, SHA: strings.Repeat("a", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, r.ID, "watch"); s.Status != StepFailed || !strings.Contains(s.Error, "pull-request") {
		t.Fatalf("pull-request verify: %+v", s)
	}
}

func TestOlderBuildLeavesVerifyRunsAlone(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	cur := openVerify(t, addr, clk, prodEnv, nil)
	id := createShop(t, cur, envVerify, 1)
	var buf syncBuffer
	old := mustOpenAs(t, addr, clk, schemaVerify-1, &buf)
	d, err := old.GetRun(id)
	if err != nil || !d.NeedsNewerServer {
		t.Fatalf("older build's view: %v %+v", err, d)
	}
	if err := old.Cancel(id, "ann"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("older build canceled it: %v", err)
	}
	if _, err := old.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, envVerify), Source: gitSource}); err == nil {
		t.Fatal("older build created a run with a verify step")
	}
	clk.Add(48 * time.Hour)
	old.expireWaits()
	old.reap()
	if s := stepOf(t, cur, id, "watch"); s.Status != StepQueuedS {
		t.Fatalf("the older build changed the step: %s", s.Status)
	}
	cur.expireWaits()
	if s := stepOf(t, cur, id, "watch"); s.Status != StepFailed || !strings.Contains(s.Error, "no deploy runner") {
		t.Fatalf("the current build did not expire it: %+v", s)
	}
}

const aiPipeline = `pipeline: shop
steps:
  changes:
    image: x
    run: git log
  notes:
    kind: ai
    needs: changes
    model: local
    prompt: Write release notes.
    inputs:
      - { output: changes.log, label: commits }
    schema:
      type: object
      properties: { notes: { type: string }, risk: { type: string } }
      required: [notes, risk]
    max_tokens: 300
  publish:
    image: x
    needs: notes
    run: echo
`

func TestAIStep(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	models, err := ai.NewRegistry([]ai.Model{{Name: "local", Provider: "openai", Endpoint: "http://127.0.0.1:1/v1", Model: "qwen",
		Projects: []string{"shop"}, TokensPerDay: 1000, CallsPerRun: 2}})
	if err != nil {
		t.Fatal(err)
	}
	e := openVerify(t, testdb.New(t), clk, prodEnv, models)
	caller := RunnerInfo{ID: "ai-1", Name: "caller", Features: FeatureArtifacts + " " + FeatureAI}
	id := createShop(t, e, aiPipeline, 1)
	ok(t, e, take(t, e), map[string]string{"log": "abc fix the cart\ndef add coupons"})
	// A runner that cannot call models is not given it.
	if j, err := e.Lease(context.Background(), runner, 0); err != nil || j != nil {
		t.Fatalf("a runner without models took it: %+v %v", j, err)
	}
	j, err := e.Lease(context.Background(), caller, 0)
	if err != nil || j == nil || j.AI == nil {
		t.Fatalf("lease: %+v %v", j, err)
	}
	if j.AI.Model.Model.Name != "local" || j.AI.Inputs[0].Text != "abc fix the cart\ndef add coupons" || j.AI.Model.MaxTokens != 300 {
		t.Fatalf("job: %+v", j.AI)
	}
	if _, err := e.Start(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	err = e.Finish(j.LeaseID, Result{Outcome: "succeeded", Outputs: map[string]string{"text": "{}", "notes": "Coupons", "risk": "low", "evil": "x"},
		AI: &AIRecord{Model: "other", Usage: AIUsage{Calls: 1, InputTokens: 700, OutputTokens: 200}}})
	if err != nil {
		t.Fatal(err)
	}
	s := stepOf(t, e, id, "notes")
	if s.Status != StepSucceeded || s.Outputs["notes"] != "Coupons" || s.Outputs["evil"] != "" {
		t.Fatalf("notes: %+v", s)
	}
	views, err := e.ModelViews(nil)
	if err != nil || len(views) != 1 || len(views[0].UsedToday) != 1 || views[0].UsedToday[0].InputTokens != 700 {
		t.Fatalf("usage: %v %+v", err, views)
	}
	ok(t, e, take(t, e), nil) // publish
	// The day's budget (1000 tokens) is used up by 900 and a bit more.
	id2 := createShop(t, e, aiPipeline, 2)
	ok(t, e, take(t, e), map[string]string{"log": "x"})
	j2, _ := e.Lease(context.Background(), caller, 0)
	if j2 == nil || j2.AI.Model.MaxTokens != 100 {
		t.Fatalf("second call's tokens: %+v", j2)
	}
	if _, err := e.Start(j2.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := e.Finish(j2.LeaseID, Result{Outcome: "succeeded", AI: &AIRecord{Usage: AIUsage{InputTokens: 80, OutputTokens: 50}}}); err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil) // publish
	id3 := createShop(t, e, aiPipeline, 3)
	ok(t, e, take(t, e), map[string]string{"log": "x"})
	if s := stepOf(t, e, id3, "notes"); s.Status != StepFailed || !strings.Contains(s.Error, "tokens_per_day") {
		t.Fatalf("over budget: %+v", s)
	}
	_ = id2
}

// TestVerifyExplanationUsageAndModelAssist: a model's explanation of a
// verdict counts toward the project's daily budget, and with decide:
// model-assisted it may turn the statistics' pass into a fail, recorded.
func TestVerifyExplanationUsageAndModelAssist(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	models, err := ai.NewRegistry([]ai.Model{{Name: "local", Provider: "openai", Endpoint: "http://127.0.0.1:1/v1", Model: "qwen",
		Projects: []string{"shop"}, TokensPerDay: 100000}})
	if err != nil {
		t.Fatal(err)
	}
	e := openVerify(t, testdb.New(t), clk, prodEnv, models)
	p := strings.Replace(verifiedCanary, "    on_inconclusive: pause\n", "    on_inconclusive: pause\n    model: local\n    decide: model-assisted\n", 1)
	id := createShop(t, e, p, 1)
	toPause(t, e, id)
	j := leaseVerify(t, e)
	var ack *VerifyAck
	for i := 0; i < 4; i++ {
		ack = report(t, e, clk, j, i, 0.003, 0.003)
	}
	if !ack.Final || ack.Verdict.Outcome != verify.Passed {
		t.Fatalf("verdict: %+v", ack.Verdict)
	}
	expl := &Explanation{Model: "local", Summary: "errors look fine", Cause: "but the logs show a new panic", Outcome: verify.Failed,
		Changed: true, Usage: &AIUsage{Calls: 1, InputTokens: 1200, OutputTokens: 150}}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Verify: &VerifyResult{Explanation: expl}}); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "verify-canary"); s.Status != StepFailed {
		t.Fatalf("the model's fail was not applied: %+v", s)
	}
	vs, err := e.RunVerifications(id)
	if err != nil || len(vs) != 1 || vs[0].Explanation == nil || !vs[0].Explanation.Changed || vs[0].Verdict.Outcome != verify.Failed {
		t.Fatalf("views: %v %+v", err, vs)
	}
	views, err := e.ModelViews(nil)
	if err != nil || len(views) != 1 || len(views[0].UsedToday) != 1 || views[0].UsedToday[0].InputTokens != 1200 ||
		views[0].UsedToday[0].OutputTokens != 150 || views[0].UsedToday[0].Calls != 1 {
		t.Fatalf("the explanation's usage was not counted: %v %+v", err, views)
	}
}
