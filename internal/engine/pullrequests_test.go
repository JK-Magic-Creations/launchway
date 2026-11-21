package engine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// prTrigger is the trigger of a run of pull request n at a commit.
func prTrigger(n int, sha string) Trigger {
	return Trigger{Event: "pull_request", Ref: PullRequestRef(n), SHA: sha, PullRequest: n, Author: "pat", Actor: "pat"}
}

func createPR(t *testing.T, e *Engine, text string, n int, sha string, secrets bool) string {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, text), Trigger: prTrigger(n, sha),
		Source: Source{Kind: "git", URL: "/srv/repo"}, PullRequestSecrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// prLinear is linear, for pull requests.
const prLinear = "on: { pull_request: {} }\n" + linear

const prText = `pipeline: demo
on:
  pull_request: {}
steps:
  test: {image: x, run: make test}
  pr-only:
    image: x
    needs: test
    when: { event: pull_request }
    run: echo pull request
  deploy:
    image: x
    needs: test
    when: { branch: main }
    run: ./deploy.sh
  on-push:
    image: x
    needs: test
    when: { event: [push, manual] }
    run: ./publish.sh
  publish:
    image: x
    needs: test
    secrets:
      TOKEN: { store: vault, key: apps/registry, field: token }
    run: ./publish.sh
`

// A pull-request run: its steps learn which pull request it is, steps
// guarded by a branch or by other events are skipped, and a step that asks
// for secrets fails before any runner sees it.
func TestPullRequestRun(t *testing.T) {
	e, _ := newEngine(t)
	id := createPR(t, e, prText, 12, "c12", false)
	sum, err := e.Summary(id)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Event != "pull_request" || sum.PullRequest != 12 || sum.Ref != "refs/pull/12/head" || sum.Branch != "" || sum.MinSchema != 9 {
		t.Fatalf("summary %+v", sum)
	}
	j := take(t, e)
	for k, v := range map[string]string{"LAUNCHWAY_EVENT": "pull_request", "LAUNCHWAY_REF": "refs/pull/12/head",
		"LAUNCHWAY_PULL_REQUEST": "12", "LAUNCHWAY_BRANCH": "", "LAUNCHWAY_COMMIT": "c12"} {
		if j.Env[k] != v {
			t.Fatalf("%s = %q, want %q", k, j.Env[k], v)
		}
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "pr-only", "queued", "deploy", "skipped", "on-push", "skipped", "publish", "failed")
	pub := stepOf(t, e, id, "publish")
	if !strings.Contains(pub.Error, "pull-request runs of demo may not read secrets") || !strings.Contains(pub.Error, "TOKEN") || pub.Runner != "" {
		t.Fatalf("publish %+v", pub)
	}
	j = take(t, e)
	if j.StepID != "pr-only" || j.Secrets != nil {
		t.Fatalf("job %+v", j)
	}
	ok(t, e, j, nil)
	none(t, e)
	want(t, statuses(t, e, id), "run", "failed")

	// Resuming keeps the decision made when the run was created.
	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	none(t, e)
	want(t, statuses(t, e, id), "publish", "failed")

	// A re-run of it is a pull-request run too, with the same rule.
	re, err := e.Rerun(RerunRequest{Run: id, From: "publish", Actor: "dana"})
	if err != nil {
		t.Fatal(err)
	}
	if re.PullRequest != 12 || re.Event != "pull_request" {
		t.Fatalf("re-run %+v", re)
	}
	none(t, e)
	want(t, statuses(t, e, re.ID), "run", "failed", "publish", "failed")

	// Other runs are unchanged: no LAUNCHWAY_PULL_REQUEST.
	none(t, e)
	create(t, e, linear, Trigger{Event: "manual", Branch: "main"})
	if j := take(t, e); j.Env["LAUNCHWAY_PULL_REQUEST"] != "" || len(j.Env) == 0 {
		t.Fatalf("manual run's environment: %v", j.Env)
	} else if _, set := j.Env["LAUNCHWAY_PULL_REQUEST"]; set {
		t.Fatal("LAUNCHWAY_PULL_REQUEST is set outside a pull request")
	}
}

// A project that lets pull requests read secrets: the step gets its
// references, and so does a re-run of the run.
func TestPullRequestWithSecretsAllowed(t *testing.T) {
	e, _ := newEngine(t)
	text := "pipeline: demo\non: { pull_request: {} }\nsteps:\n  a:\n    image: x\n    secrets: { T: { store: env, key: K } }\n    run: a\n"
	id := createPR(t, e, text, 3, "c3", true)
	j := take(t, e)
	if j.Secrets["T"].Key != "K" {
		t.Fatalf("job %+v", j)
	}
	ok(t, e, j, nil)
	if _, err := e.Rerun(RerunRequest{Run: id, From: "a", Actor: "dana"}); err != nil {
		t.Fatal(err)
	}
	if j := take(t, e); j.Secrets["T"].Key != "K" {
		t.Fatalf("re-run's job %+v", j)
	}
}

// A pull-request run needs its number and the matching ref.
func TestPullRequestTriggerIsChecked(t *testing.T) {
	e, _ := newEngine(t)
	for name, trig := range map[string]Trigger{
		"no number":       {Event: "pull_request", Ref: "refs/pull/4/head"},
		"another ref":     {Event: "pull_request", Ref: "refs/heads/main", PullRequest: 4},
		"another number":  {Event: "pull_request", Ref: "refs/pull/5/head", PullRequest: 4},
		"with a branch":   {Event: "pull_request", Ref: "refs/pull/4/head", PullRequest: 4, Branch: "main"},
		"the merge ref":   {Event: "pull_request", Ref: "refs/pull/4/merge", PullRequest: 4},
		"negative number": {Event: "pull_request", Ref: "refs/pull/-4/head", PullRequest: -4},
	} {
		_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, prLinear), Trigger: trig, Source: Source{Kind: "dir", Path: "/tmp"}})
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The trigger is right, but the pipeline does not ask for pull requests.
	_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, linear), Trigger: prTrigger(4, "c4"), Source: Source{Kind: "dir", Path: "/tmp"}})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "must ask for pull requests") {
		t.Errorf("a pipeline without on.pull_request: %v", err)
	}
	if runs, _ := e.ListRuns(RunFilter{}); len(runs) != 0 {
		t.Fatalf("runs were created: %d", len(runs))
	}
}

func TestParsePullRequestRef(t *testing.T) {
	for ref, want := range map[string]int{"refs/pull/1/head": 1, "refs/pull/1234/head": 1234, "refs/pull/01/head": 0,
		"refs/pull/1/merge": 0, "refs/pull//head": 0, "refs/pull/x/head": 0, "refs/heads/1": 0, "refs/pull/1/head/x": 0,
		"refs/pull/12345678901/head": 0} {
		got, ok := ParsePullRequestRef(ref)
		if got != want || ok != (want > 0) {
			t.Errorf("%s: %d %v, want %d", ref, got, ok, want)
		}
	}
}

const supersedeText = "supersede: true\n" + linear

// prSupersede is supersedeText, for pull requests.
const prSupersede = "on: { pull_request: {} }\n" + supersedeText

func pushTo(branch, sha string) Trigger {
	return Trigger{Event: "push", Ref: "refs/heads/" + branch, Branch: branch, SHA: sha, Author: "pat"}
}

// A new push run of a pipeline with supersede cancels the older push runs
// of the same ref that are still going, saying why; a runner working on
// one is told to stop. Nothing else is touched.
func TestSupersede(t *testing.T) {
	e, _ := newEngine(t)
	done := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: a}\n", pushTo("main", "s0")) // #1, finished
	ok(t, e, take(t, e), nil)
	old := create(t, e, linear, pushTo("main", "s1")) // #2
	ja := take(t, e)                                  // its a is running
	if ja.RunID != old {
		t.Fatalf("leased %s", ja.RunID)
	}
	manual := create(t, e, linear, Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: "s1"}) // #3
	draft := createDraft(t, e, supersedeText, false)                                                            // #4, a draft of main
	other := create(t, e, linear, pushTo("dev", "s1"))                                                          // #5
	pr := createPR(t, e, prLinear, 9, "p1", false)                                                              // #6, another ref

	// A run without supersede cancels nothing.
	plain := create(t, e, linear, pushTo("main", "s2")) // #7
	want(t, statuses(t, e, old), "run", "running")

	// A manual run of a superseding pipeline supersedes nothing either.
	create(t, e, supersedeText, Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: "s2"}) // #8
	want(t, statuses(t, e, old), "run", "running")

	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, supersedeText), Trigger: pushTo("main", "s3"),
		Source: Source{Kind: "dir", Path: "/tmp"}}) // #9
	if err != nil {
		t.Fatal(err)
	}
	if r.Number != 9 || r.CancelRequested {
		t.Fatalf("new run %+v", r)
	}
	for _, id := range []string{old, plain} {
		sum, _ := e.Summary(id)
		if !sum.CancelRequested || sum.CancelReason != "superseded by run #9" || sum.SupersededBy != r.ID || sum.SupersededByNumber != 9 {
			t.Fatalf("superseded run %+v", sum)
		}
		st, _ := e.State(id)
		if st.SupersededBy == nil || st.SupersededBy.Run != r.ID || st.CancelReason != "superseded by run #9" {
			t.Fatalf("state of the superseded run: %+v %q", st.SupersededBy, st.CancelReason)
		}
	}
	want(t, statuses(t, e, plain), "run", "canceled", "a", "canceled")
	if s := stepOf(t, e, plain, "a"); s.Error != "the run was canceled: superseded by run #9" {
		t.Fatalf("step a of the superseded run: %q", s.Error)
	}
	want(t, statuses(t, e, old), "run", "running", "a", "running", "b", "canceled")
	if cancel, err := e.Heartbeat(ja.LeaseID); err != nil || !cancel {
		t.Fatalf("the runner should be told to stop: %v %v", cancel, err)
	}
	if err := e.Finish(ja.LeaseID, Result{Outcome: "canceled"}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, old), "run", "canceled", "a", "canceled")
	if s := stepOf(t, e, old, "a"); s.Error != "canceled: superseded by run #9" {
		t.Fatalf("step a of the superseded run: %q", s.Error)
	}
	for _, id := range []string{manual, draft, other, pr} {
		if sum, _ := e.Summary(id); sum.CancelRequested || sum.CancelReason != "" || sum.Status.Terminal() {
			t.Fatalf("run #%d was touched: %+v", sum.Number, sum)
		}
	}
	if sum, _ := e.Summary(done); sum.Status != RunSucceeded || sum.CancelReason != "" {
		t.Fatalf("a finished run was touched: %+v", sum)
	}

	// A cancel by hand gives no reason. Resuming a superseded run clears
	// the reason.
	if err := e.Cancel(other, "dana"); err != nil {
		t.Fatal(err)
	}
	if sum, _ := e.Summary(other); !sum.CancelRequested || sum.CancelReason != "" || sum.SupersededBy != "" {
		t.Fatalf("canceled by hand: %+v", sum)
	}
	if err := e.Resume(plain, "dana"); err != nil {
		t.Fatal(err)
	}
	if sum, _ := e.Summary(plain); sum.CancelRequested || sum.CancelReason != "" || sum.SupersededBy != "" || sum.Status != RunRunning {
		t.Fatalf("resumed: %+v", sum)
	}
	// The newest run, and the resumed one, are not touched by a run of
	// another ref.
	create(t, e, supersedeText, pushTo("dev", "s4"))
	if sum, _ := e.Summary(r.ID); sum.CancelRequested {
		t.Fatalf("superseded by a run of another ref: %+v", sum)
	}
}

// Pull requests supersede the older runs of the same pull request, not
// those of other pull requests. Re-runs are never superseded, and runs
// this server does not understand are left alone while the new run is
// still created.
func TestSupersedePullRequests(t *testing.T) {
	e, _ := newEngine(t)
	first := createPR(t, e, prSupersede, 5, "a1", false)
	other := createPR(t, e, prSupersede, 6, "b1", false)
	for j := leaseAs(t, e, runner); j != nil; j = leaseAs(t, e, runner) {
		ok(t, e, j, nil)
	}
	// first and other have finished; a re-run of first is going.
	re, err := e.Rerun(RerunRequest{Run: first, From: "b", Actor: "dana"})
	if err != nil {
		t.Fatal(err)
	}
	second := createPR(t, e, prSupersede, 5, "a2", false)
	strange := createPR(t, e, prLinear, 5, "a3", false)
	appendRaw(t, e, strange, "step.teleported", `{"step":"a"}`)
	third := createPR(t, e, prSupersede, 5, "a4", false)

	if sum, _ := e.Summary(second); sum.CancelReason != fmt.Sprintf("superseded by run #%d: a newer commit was pushed to the same pull request", runNumber(t, e, strange)) {
		t.Fatalf("second: %+v", sum)
	}
	for _, id := range []string{re.ID, strange, other, third} {
		if sum, _ := e.Summary(id); sum.CancelRequested {
			t.Fatalf("run #%d was superseded: %+v", sum.Number, sum)
		}
	}
	if d, _ := e.GetRun(strange); !d.NeedsNewerServer {
		t.Fatal("the run with an unknown event should need a newer server")
	}
}

func runNumber(t *testing.T, e *Engine, id string) int {
	t.Helper()
	sum, err := e.Summary(id)
	if err != nil {
		t.Fatal(err)
	}
	return sum.Number
}

// Superseding happens in the transaction that creates the new run: if it
// fails, the run is not created and nothing is cancelled.
func TestSupersedeIsAllOrNothing(t *testing.T) {
	e, _ := newEngine(t)
	old := create(t, e, linear, pushTo("main", "s1"))
	// The older run's next event number is taken behind the engine's back,
	// as another server writing at the same moment would: storing the
	// cancel conflicts every time, so the creation gives up.
	_, last, err := loadEvents(e.db, old)
	if err != nil {
		t.Fatal(err)
	}
	if e.db.pg {
		t.Skip("uses a SQLite trigger to make storing the cancel fail")
	}
	trigger := `CREATE TRIGGER block_cancel BEFORE INSERT ON events WHEN NEW.run_id = '` + old +
		`' BEGIN SELECT RAISE(ABORT, 'UNIQUE constraint failed: events.run_id, events.run_seq'); END`
	if _, err := e.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	_, err = e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, supersedeText), Trigger: pushTo("main", "s2"),
		Source: Source{Kind: "dir", Path: "/tmp"}})
	if err == nil || !strings.Contains(err.Error(), "kept changing") {
		t.Fatalf("creating the superseding run: %v", err)
	}
	if _, err := e.db.Exec(`DROP TRIGGER block_cancel`); err != nil {
		t.Fatal(err)
	}
	if runs, _ := e.ListRuns(RunFilter{}); len(runs) != 1 {
		t.Fatalf("%d runs; the failed creation left one behind", len(runs))
	}
	if _, now, _ := loadEvents(e.db, old); now != last {
		t.Fatalf("the older run has %d events, had %d", now, last)
	}
	// Once the way is clear, it works, and takes the next number.
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, supersedeText), Trigger: pushTo("main", "s2"),
		Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil || r.Number != 2 {
		t.Fatalf("%+v, %v", r, err)
	}
	want(t, statuses(t, e, old), "run", "canceled")
}

// Several servers creating runs of one ref at the same moment: whatever
// the order, once they are done exactly one run is left going, the newest,
// and every other was superseded by a newer one. With SQLite (one server)
// the creations still race through the engine.
func TestSupersedeRace(t *testing.T) {
	var servers []*Engine
	if a, b, _ := pairOrNot(t); b != nil {
		servers = []*Engine{a, b}
	} else {
		servers = []*Engine{a}
	}
	const runs = 24
	var wg sync.WaitGroup
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := servers[i%len(servers)]
			if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, supersedeText), Trigger: pushTo("main", fmt.Sprint(i)),
				Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	// Meanwhile runners take steps, so that the runs change under the
	// creations.
	stop := make(chan struct{})
	var workers sync.WaitGroup
	for _, e := range servers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				j, err := e.Lease(t.Context(), runner, 0)
				if err != nil {
					t.Error(err)
					return
				}
				if j != nil {
					if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded"}); err != nil && !errors.Is(err, ErrLeaseLost) {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	workers.Wait()
	all, err := servers[0].ListRuns(RunFilter{Limit: 100})
	if err != nil || len(all) != runs {
		t.Fatalf("%d runs, %v", len(all), err)
	}
	newest := all[0]
	for _, r := range all {
		if r.Number > newest.Number {
			newest = r
		}
	}
	for _, r := range all {
		switch {
		case r.ID == newest.ID:
			if r.CancelRequested {
				t.Fatalf("the newest run, #%d, was superseded: %+v", r.Number, r)
			}
		case r.Status == RunSucceeded && !r.CancelRequested:
			// It finished before a newer run was created.
		case !r.CancelRequested || r.SupersededByNumber <= r.Number:
			t.Fatalf("run #%d of %d: %+v", r.Number, runs, r)
		}
	}
}

// pairOrNot is pair on PostgreSQL, and one engine on SQLite.
func pairOrNot(t *testing.T) (a, b *Engine, clk *clock) {
	t.Helper()
	if testdb.Postgres() {
		return pair(t)
	}
	a, clk = newEngine(t)
	return a, nil, clk
}
