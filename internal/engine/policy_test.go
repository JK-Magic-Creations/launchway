package engine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/policy"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// mustPolicies reads policies as the configuration would hold them.
func mustPolicies(t *testing.T, text string) []policy.Policy {
	t.Helper()
	ps, err := policy.ParseFile([]byte(text), "test.yaml", configSource)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func openPolicy(t *testing.T, addr string, clk *clock, envs []Environment, po PolicyOptions) *Engine {
	t.Helper()
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Environments: envs, Policy: po})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func newPolicyEngine(t *testing.T, envs []Environment, text string) (*Engine, *clock, string) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	return openPolicy(t, addr, clk, envs, PolicyOptions{Inline: mustPolicies(t, text)}), clk, addr
}

// decisions returns a run's policy decisions, checking each one replays to
// itself from what it recorded.
func decisions(t *testing.T, e *Engine, runID string) []PolicyDecision {
	t.Helper()
	ds, err := e.RunPolicy(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		var ps []policy.Policy
		for _, p := range d.Policies {
			if p.Policy.Name != "" && len(p.Rules) > 0 && p.Rules[0].Rule == policy.RuleSource {
				continue
			}
			ps = append(ps, p.Policy)
		}
		again := policy.Evaluate(policy.Set{Sources: []policy.Source{{Name: "replay", Policies: ps}}}, d.Input, d.Waivers, d.At)
		a, _ := json.Marshal(again.Policies)
		b, _ := json.Marshal(d.Evaluation.Policies)
		if string(a) != string(b) && !strings.Contains(string(b), `"rule":"source"`) {
			t.Fatalf("decision %d does not replay:\n%s\n%s", d.Seq, b, a)
		}
	}
	return ds
}

const shapePolicy = `policies:
  - name: pinned-images
    mode: enforce
    scope: { projects: [demo] }
    rules:
      step_images: { digest: true }
`

// A run whose pipeline breaks an enforced run rule is refused: its first
// steps fail with the reason, before any runner sees them, and the decision
// is in its log. A waiver and a resume let it run.
func TestRunPolicyRefusesARun(t *testing.T) {
	e, _, _ := newPolicyEngine(t, nil, shapePolicy)
	id := create(t, e, linear, Trigger{Event: "manual", Actor: "dana"})
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != RunFailed || d.MinSchema != schemaPolicy {
		t.Fatalf("status %s, min_schema %d", d.Status, d.MinSchema)
	}
	first := d.StepList[0]
	if first.Status != StepFailed || !strings.Contains(first.Error, "the run was refused when it started: blocked by policy: policy pinned-images (enforce), rule step_images") ||
		!strings.Contains(first.Error, "not pinned by digest") {
		t.Fatalf("%s: %s %q", first.ID, first.Status, first.Error)
	}
	if j, _ := e.Lease(context.Background(), runner, 0); j != nil {
		t.Fatalf("leased %s", j.StepID)
	}
	ds := decisions(t, e, id)
	if len(ds) != 1 || ds[0].Point != policy.PointRun || ds[0].Verdict != policy.VerdictBlock {
		t.Fatalf("%+v", ds)
	}
	// A waiver for the project, then a resume: judged again, and it runs.
	if _, err := e.GrantWaiver(WaiverRequest{Project: "demo", Policy: "pinned-images", Rule: policy.RuleStepImages, Reason: "base images move next week",
		ExpiresAt: e.now().Add(48 * time.Hour).UnixMilli(), Actor: "sam"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	ds = decisions(t, e, id)
	if len(ds) != 2 || ds[1].Verdict != policy.VerdictPass || ds[1].Policies[0].Verdict != policy.ResultWaived {
		t.Fatalf("%+v", ds[1])
	}
	ok(t, e, take(t, e), nil)
}

// Report mode records the same detail and changes nothing.
func TestReportModeChangesNothing(t *testing.T) {
	e, _, _ := newPolicyEngine(t, nil, strings.Replace(shapePolicy, "enforce", "report", 1))
	id := create(t, e, linear, Trigger{Event: "manual"})
	j := take(t, e)
	if j.RunID != id {
		t.Fatal(j.RunID)
	}
	ds := decisions(t, e, id)
	if len(ds) != 1 || ds[0].Verdict != policy.VerdictWarn || !strings.Contains(ds[0].Reason(), "policy warning: policy pinned-images (report)") {
		t.Fatalf("%+v", ds)
	}
}

// A run no policy governs gets no decision, and stays readable by older
// servers.
func TestUngovernedRunsAreUnchanged(t *testing.T) {
	e, _, _ := newPolicyEngine(t, nil, strings.Replace(shapePolicy, "[demo]", "[other]", 1))
	id := create(t, e, linear, Trigger{Event: "manual"})
	if ds := decisions(t, e, id); len(ds) != 0 {
		t.Fatalf("%+v", ds)
	}
	if sum, _ := e.Summary(id); sum.MinSchema >= schemaPolicy {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}
}

const gatePipeline = `pipeline: shop
steps:
  build:
    image: x
    run: build
    reports: { tests: r.xml }
  approve:
    kind: approval
    needs: build
    message: Ship it?
  deploy-prod:
    kind: deploy
    needs: [build, approve]
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
`

const gatePolicy = `policies:
  - name: prod-gate
    mode: enforce
    scope: { environments: [prod] }
    rules:
      tests: { min: 2 }
      approvals: { min: 1 }
`

const junitTwo = `<testsuite name="s" tests="2"><testcase classname="c" name="a"/><testcase classname="c" name="b"/></testsuite>`

func startGated(t *testing.T, e *Engine, actor string) string {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, gatePipeline), Source: gitSource,
		Trigger: Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: strings.Repeat("a", 40), Actor: actor}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// buildWith runs the build step, sending test reports or none.
func buildWith(t *testing.T, e *Engine, runID string, report string) {
	t.Helper()
	j := take(t, e)
	if j.RunID != runID || j.StepID != "build" {
		t.Fatalf("leased %s/%s", j.RunID, j.StepID)
	}
	zero := 0
	res := Result{Outcome: "succeeded", ExitCode: &zero, Outputs: map[string]string{"tag": "1.0"}}
	if report != "" {
		res.Tests = &CollectedReports{Files: []ReportFile{{Path: "r.xml", Data: []byte(report)}}}
	}
	if err := e.Finish(j.LeaseID, res); err != nil {
		t.Fatal(err)
	}
}

func stepViewOf(t *testing.T, e *Engine, runID, step string) StepView {
	t.Helper()
	d, err := e.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		if s.ID == step {
			return s
		}
	}
	t.Fatalf("no step %s", step)
	return StepView{}
}

// The deploy gate judges the run's evidence before the deployment opens:
// without test results the deploy step fails, saying which policy and rule,
// and nothing is handed to a deploy runner; with them it deploys.
func TestDeployGate(t *testing.T) {
	e, _, _ := newPolicyEngine(t, prodEnv, gatePolicy)
	bad := startGated(t, e, "dana")
	buildWith(t, e, bad, "")
	if err := e.Decide(bad, "approve", true, "sam", ""); err != nil {
		t.Fatal(err)
	}
	s := stepViewOf(t, e, bad, "deploy-prod")
	if s.Status != StepFailed || !strings.Contains(s.Error, "blocked by policy: policy prod-gate (enforce), rule tests: no step before deploy-prod reported test results") {
		t.Fatalf("%s %q", s.Status, s.Error)
	}
	if !s.PolicyBlocked {
		t.Fatal("not marked as blocked by policy")
	}
	noDeploy(t, e, deployer)
	ds := decisions(t, e, bad)
	if len(ds) != 2 || ds[1].Point != policy.PointDeploy || ds[1].Step != "deploy-prod" || ds[1].Environment != "prod" {
		t.Fatalf("%+v", ds)
	}
	// The approval by someone else counted; the evidence is recorded.
	rules := ds[1].Policies[0].Rules
	if rules[1].Rule != policy.RuleApprovals || rules[1].Verdict != policy.ResultPass || rules[1].Evidence[0].Step != "approve" {
		t.Fatalf("%+v", rules[1])
	}

	good := startGated(t, e, "dana")
	buildWith(t, e, good, junitTwo)
	if err := e.Decide(good, "approve", true, "sam", ""); err != nil {
		t.Fatal(err)
	}
	j := leaseDeploy(t, e, deployer)
	if j.RunID != good || j.StepID != "deploy-prod" {
		t.Fatalf("%s/%s", j.RunID, j.StepID)
	}
	ds = decisions(t, e, good)
	if last := ds[len(ds)-1]; last.Verdict != policy.VerdictPass || last.Policies[0].Rules[0].Found != "2 tests from 1 step(s): 2 passed, 0 failed, 0 errors" {
		t.Fatalf("%+v", last.Policies[0].Rules[0])
	}
}

// On a protected environment, the person who started a run may not waive
// its deploy gate: a waiver for the run is refused, and a waiver for the
// project they granted is not applied to their run. Someone else's is.
func TestSelfWaiverOnAProtectedEnvironment(t *testing.T) {
	envs := []Environment{{Name: "prod", Target: "homelab", Projects: []string{"shop"}, Protected: true}}
	clk := &clock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	no := false
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: envs, AllowSelfApproval: &no,
		Policy: PolicyOptions{Inline: mustPolicies(t, gatePolicy)}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	id := startGated(t, e, "dana")
	waive := func(run, by string) error {
		_, err := e.GrantWaiver(WaiverRequest{Project: "shop", Policy: "prod-gate", Rule: policy.RuleTests, Run: run, Reason: "hotfix",
			ExpiresAt: clk.Now().Add(time.Hour).UnixMilli(), Actor: by})
		return err
	}
	if err := waive(id, "dana"); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "environment prod is protected: someone else must waive its deploy gate") {
		t.Fatalf("%v", err)
	}
	if err := waive("", "dana"); err != nil {
		t.Fatal(err)
	}
	buildWith(t, e, id, "")
	if err := e.Decide(id, "approve", true, "sam", ""); err != nil {
		t.Fatal(err)
	}
	s := stepViewOf(t, e, id, "deploy-prod")
	if s.Status != StepFailed || !strings.Contains(s.Error, "rule tests") {
		t.Fatalf("%s %q", s.Status, s.Error)
	}
	ds := decisions(t, e, id)
	if notes := strings.Join(ds[len(ds)-1].Policies[0].Rules[0].Notes, "|"); !strings.Contains(notes, "was granted by dana, who started the run; environment prod is protected") {
		t.Fatalf("notes %q", notes)
	}
	if err := waive(id, "sam"); err != nil {
		t.Fatal(err)
	}
	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	if j := leaseDeploy(t, e, deployer); j.RunID != id || j.StepID != "deploy-prod" {
		t.Fatalf("%s/%s", j.RunID, j.StepID)
	}
	ds = decisions(t, e, id)
	if r := ds[len(ds)-1].Policies[0].Rules[0]; r.Verdict != policy.ResultWaived || r.Waiver.GrantedBy != "sam" {
		t.Fatalf("%+v", r)
	}
}

// A waiver that is in force when the run starts but expires before its
// deployment does not let the deployment through.
func TestWaiverExpiresMidRun(t *testing.T) {
	text := gatePolicy + `  - name: shape
    mode: enforce
    rules:
      step_images: { digest: true }
`
	e, clk, _ := newPolicyEngine(t, prodEnv, text)
	for _, rule := range []struct{ policy, rule string }{{"shape", policy.RuleStepImages}, {"prod-gate", policy.RuleTests}} {
		if _, err := e.GrantWaiver(WaiverRequest{Project: "shop", Policy: rule.policy, Rule: rule.rule, Reason: "short-lived",
			ExpiresAt: clk.Now().Add(10 * time.Minute).UnixMilli(), Actor: "sam"}); err != nil {
			t.Fatal(err)
		}
	}
	id := startGated(t, e, "dana")
	if ds := decisions(t, e, id); ds[0].Verdict != policy.VerdictPass || ds[0].Policies[0].Verdict != policy.ResultWaived {
		t.Fatalf("%+v", ds[0])
	}
	buildWith(t, e, id, "")
	clk.Add(11 * time.Minute)
	if err := e.Decide(id, "approve", true, "sam", ""); err != nil {
		t.Fatal(err)
	}
	s := stepViewOf(t, e, id, "deploy-prod")
	if s.Status != StepFailed || !strings.Contains(s.Error, "rule tests") {
		t.Fatalf("%s %q", s.Status, s.Error)
	}
	ds := decisions(t, e, id)
	notes := strings.Join(ds[1].Policies[0].Rules[0].Notes, "|")
	if !strings.Contains(notes, "expired at 2026-11-18 09:10 UTC") {
		t.Fatalf("notes %q", notes)
	}
}

// Revoking a waiver ends it; the log keeps both.
func TestRevokeWaiver(t *testing.T) {
	e, clk, _ := newPolicyEngine(t, prodEnv, gatePolicy)
	w, err := e.GrantWaiver(WaiverRequest{Project: "shop", Policy: "prod-gate", Rule: policy.RuleTests, Environment: "prod", Reason: "r",
		ExpiresAt: clk.Now().Add(time.Hour).UnixMilli(), Actor: "sam"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.RevokeWaiver("shop", w.ID, "ada", "no longer needed")
	if err != nil || v.State != "revoked" || v.RevokedBy != "ada" {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := e.RevokeWaiver("shop", w.ID, "ada", ""); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := e.RevokeWaiver("web", w.ID, "ada", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	var n int
	e.db.QueryRow(`SELECT COUNT(*) FROM policy_waiver_events WHERE waiver_id = ?`, w.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("%d waiver events", n)
	}
	// Refusals: unknown policy, a rule the policy lacks, no reason, too long.
	bad := []WaiverRequest{
		{Project: "shop", Policy: "nope", Rule: policy.RuleTests, Reason: "r"},
		{Project: "shop", Policy: "prod-gate", Rule: policy.RuleSBOM, Reason: "r"},
		{Project: "shop", Policy: "prod-gate", Rule: policy.RuleTests},
		{Project: "shop", Policy: "prod-gate", Rule: policy.RuleTests, Reason: "r", ExpiresAt: clk.Now().Add(31 * 24 * time.Hour).UnixMilli()},
		{Project: "shop", Policy: "prod-gate", Rule: policy.RuleTests, Reason: "r", Finding: "fp"},
		{Project: "shop", Policy: "prod-gate", Rule: policy.RuleTests, Reason: "r", Environment: "mars"},
		{Project: "shop", Policy: "prod-gate", Rule: policy.RuleSource, Reason: "r"},
	}
	for i, rq := range bad {
		if rq.ExpiresAt == 0 {
			rq.ExpiresAt = clk.Now().Add(time.Hour).UnixMilli()
		}
		if _, err := e.GrantWaiver(rq); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d: %v", i, err)
		}
	}
}

// An image step that would push without the safeguards a policy asks for
// fails before any runner sees it.
func TestImagePushGate(t *testing.T) {
	e, _, _ := newPolicyEngine(t, nil, `policies:
  - name: images
    mode: enforce
    rules:
      image_push: { scan: high, sign: runner, sbom: true }
`)
	id := create(t, e, imagePipeline, Trigger{Event: "manual", SHA: "0123abcd0123abcd0123abcd0123abcd0123abcd"})
	ok(t, e, takeAs(t, e, imageRunner), map[string]string{"version": "1"})
	s := stepViewOf(t, e, id, "image")
	if s.Status != StepFailed || !strings.Contains(s.Error, "rule image_push: its scan fails only on critical") {
		t.Fatalf("%s %q", s.Status, s.Error)
	}
	if j, _ := e.Lease(context.Background(), imageRunner, 0); j != nil {
		t.Fatalf("leased %s", j.StepID)
	}
}

// A Git source that no server has loaded, that was refused, or that has
// not been confirmed for too long fails closed; one loaded and confirmed is
// used, also while a fetch fails, until it goes stale.
func TestPolicySourceFailsClosed(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	e := openPolicy(t, testdb.New(t), clk, nil, PolicyOptions{Sources: []PolicySourceConfig{{Name: "org", Library: "org-policies", Version: "v3",
		Projects: []string{"demo"}, StaleAfter: time.Hour}}})
	refused := func(want string) {
		t.Helper()
		id := create(t, e, linear, Trigger{Event: "manual"})
		s := stepViewOf(t, e, id, "a")
		if s.Status != StepFailed || !strings.Contains(s.Error, "rule source: the policies of source org at v3 could not be loaded: "+want) {
			t.Fatalf("%s %q", s.Status, s.Error)
		}
	}
	refused("no server has loaded it yet")
	if err := e.PolicySourceUnreachable("org", "v3", "dial tcp: connection refused"); err != nil {
		t.Fatal(err)
	}
	refused("it has never been loaded: dial tcp: connection refused")
	if err := e.StorePolicySource("org", "v3", strings.Repeat("c", 40), nil, `policies/a.yaml:7: policies[0].rules.teste: unknown rule "teste"`); err != nil {
		t.Fatal(err)
	}
	refused(`policies/a.yaml:7: policies[0].rules.teste: unknown rule "teste"`)
	ps := mustPolicies(t, strings.Replace(shapePolicy, "enforce", "report", 1))
	if err := e.StorePolicySource("org", "v3", strings.Repeat("d", 40), ps, ""); err != nil {
		t.Fatal(err)
	}
	ran := func() {
		t.Helper()
		id := create(t, e, linear, Trigger{Event: "manual"})
		ds := decisions(t, e, id)
		if len(ds) != 1 || ds[0].Verdict != policy.VerdictWarn || ds[0].Policies[0].Policy.Source != configSource || ds[0].Policies[0].Policy.Name != "pinned-images" {
			t.Fatalf("%+v", ds)
		}
		ok(t, e, take(t, e), nil)
		release(t, e)
	}
	ran()
	// Unreachable for a while: the version loaded is still used...
	clk.Add(30 * time.Minute)
	e.PolicySourceUnreachable("org", "v3", "dial tcp: connection refused")
	ran()
	// ...until it is older than stale_after.
	clk.Add(31 * time.Minute)
	refused("it was last confirmed at 2026-11-18T09:00:00Z, longer ago than 1h0m0s; the last attempt failed: dial tcp: connection refused")
	// A server configured with another version refuses what is stored.
	e.StorePolicySource("org", "v2", strings.Repeat("d", 40), ps, "")
	refused("the version stored is of v2, but this server is configured with v3")
	// Projects the source does not cover are not affected.
	r, err := e.CreateRun(NewRun{Project: "other", Pipeline: parse(t, linear), Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil || r.Status == RunFailed {
		t.Fatalf("%+v %v", r, err)
	}
}

// release hands back whatever the runner could still lease, so a test's
// next run is the next one leased.
func release(t *testing.T, e *Engine) {
	t.Helper()
	for {
		j, _ := e.Lease(context.Background(), runner, 0)
		if j == nil {
			return
		}
		if _, err := e.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		ok(t, e, j, nil)
	}
}

// A decision and what it allows are stored together: a server that stops
// right after leaves a run whose next step any server hands out, and which
// is not judged again.
func TestRestartBetweenDecisionAndStep(t *testing.T) {
	text := strings.Replace(shapePolicy, "enforce", "report", 1)
	e, clk, addr := newPolicyEngine(t, nil, text)
	id := create(t, e, linear, Trigger{Event: "manual"})
	e.Close()
	e2 := openPolicy(t, addr, clk, nil, PolicyOptions{Inline: mustPolicies(t, text)})
	j := take(t, e2)
	if j.RunID != id {
		t.Fatal(j.RunID)
	}
	ok(t, e2, j, nil)
	if ds := decisions(t, e2, id); len(ds) != 1 {
		t.Fatalf("%d decisions", len(ds))
	}
}

// Two servers deciding the same approval at once: one decides it, and the
// deploy gate behind it is judged exactly once (run with
// LAUNCHWAY_TEST_POSTGRES for real concurrency).
func TestTwoServersRaceOnTheSameGate(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	po := PolicyOptions{Inline: mustPolicies(t, gatePolicy)}
	a := openPolicy(t, addr, clk, prodEnv, po)
	b := openPolicy(t, addr, clk, prodEnv, po)
	for range 5 {
		id := startGated(t, a, "dana")
		buildWith(t, a, id, junitTwo)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, e := range []*Engine{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); errs[i] = e.Decide(id, "approve", true, "sam", "") }()
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("both or neither decided: %v / %v", errs[0], errs[1])
		}
		ds := decisions(t, b, id)
		n := 0
		for _, d := range ds {
			if d.Point == policy.PointDeploy {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d deploy decisions", n)
		}
		j := leaseDeploy(t, b, deployer)
		applied(t, a, j, "c0ffee")
		a.Cancel(id, "x")
	}
}

// A server of the previous schema leaves runs a policy governs alone: it
// cannot judge them.
func TestOlderBuildLeavesPolicyRunsAlone(t *testing.T) {
	n := schemaPolicy
	if migrations[n-1].compat != additive {
		t.Fatalf("migration %d must be additive", n)
	}
	clk := &clock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	cur := openPolicy(t, addr, clk, nil, PolicyOptions{Inline: mustPolicies(t, strings.Replace(shapePolicy, "enforce", "report", 1))})
	governed := create(t, cur, linear, Trigger{Event: "manual"})
	if sum, _ := cur.Summary(governed); sum.MinSchema != n {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}
	var buf syncBuffer
	old := mustOpenAs(t, addr, clk, n-1, &buf)
	d, err := old.GetRun(governed)
	if err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d.NotUnderstood, err)
	}
	if err := old.Cancel(governed, "x"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatal(err)
	}
	if j, _ := old.Lease(context.Background(), runner, 0); j != nil {
		t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
	}
	// An older build cannot be given policies at all.
	if _, err := Open(Options{Database: addr, Now: clk.Now, schema: n - 1, Policy: PolicyOptions{Inline: mustPolicies(t, shapePolicy)}}); err == nil ||
		!strings.Contains(err.Error(), "policies need schema 16") {
		t.Fatal(err)
	}
	// The current build carries it on.
	if j := take(t, cur); j.RunID != governed {
		t.Fatal(j.RunID)
	}
}

// A re-run from the deploy step judges the evidence of the steps it
// reused, from the run where they ran.
func TestRerunJudgesReusedEvidence(t *testing.T) {
	e, _, _ := newPolicyEngine(t, prodEnv, gatePolicy)
	id := startGated(t, e, "dana")
	buildWith(t, e, id, junitTwo)
	if err := e.Decide(id, "approve", true, "sam", ""); err != nil {
		t.Fatal(err)
	}
	applied(t, e, leaseDeploy(t, e, deployer), "c0ffee")
	it := watchOf(t, e, deployer, "deploy-prod")
	observe(t, e, deployer, it, healthy(true))
	if d, _ := e.GetRun(id); d.Status != RunSucceeded {
		t.Fatalf("%s", d.Status)
	}
	re, err := e.Rerun(RerunRequest{Run: id, From: "deploy-prod", Actor: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	ds := decisions(t, e, re.ID)
	last := ds[len(ds)-1]
	if last.Point != policy.PointDeploy || last.Verdict != policy.VerdictPass {
		t.Fatalf("%+v", last)
	}
	ev := last.Policies[0].Rules[0].Evidence[0]
	if ev.Run != id || ev.Kind != "tests" {
		t.Fatalf("%+v", ev)
	}
	if !reflect.DeepEqual(last.Input.Evidence.Steps[0].Tests, &policy.TestFact{Total: 2, Passed: 2}) {
		t.Fatalf("%+v", last.Input.Evidence.Steps[0].Tests)
	}
}

func TestPolicyOverview(t *testing.T) {
	e, _, _ := newPolicyEngine(t, nil, shapePolicy)
	create(t, e, linear, Trigger{Event: "manual"})
	o, err := e.PolicyOverview(nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Sources) != 1 || o.Sources[0].Kind != "config" || len(o.Sources[0].Policies) != 1 || len(o.Recent) != 1 || o.Recent[0].Verdict != policy.ResultFail {
		t.Fatalf("%+v", o)
	}
	if o, _ := e.PolicyOverview([]string{"other"}, 5); len(o.Recent) != 0 {
		t.Fatalf("%+v", o.Recent)
	}
}
