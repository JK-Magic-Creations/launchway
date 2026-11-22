package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const ms = int64(time.Minute / time.Millisecond)

func run(n int, status RunStatus, branch string, at int64) AttentionRun {
	r := AttentionRun{ID: fmt.Sprintf("r%d", n), Project: "api", Number: n, Status: status, Event: "push", Ref: "refs/heads/" + branch, Branch: branch, Actor: "ana", CreatedAt: at * ms}
	if status != RunQueued {
		r.StartedAt = r.CreatedAt
		r.FinishedAt = r.CreatedAt + 5*ms
	}
	return r
}

func pr(n int, status RunStatus, num int, actor string, at int64) AttentionRun {
	r := run(n, status, "feature", at)
	r.Event, r.Ref, r.Actor = "pull_request", fmt.Sprintf("refs/pull/%d/head", num), actor
	return r
}

func TestAttentionSupersededRule(t *testing.T) {
	def := func(string) string { return "main" }
	cases := []struct {
		name     string
		runs     []AttentionRun
		want     []string // keys of the needs
		retrying int
	}{
		{"failed on main, nothing newer", []AttentionRun{run(1, RunFailed, "main", 1)}, []string{"failed:r1"}, 0},
		{"retried and running", []AttentionRun{run(1, RunFailed, "main", 1), run(2, RunRunning, "main", 2)}, nil, 1},
		{"retried and queued", []AttentionRun{run(1, RunFailed, "main", 1), run(2, RunQueued, "main", 2)}, nil, 1},
		{"retried and succeeded", []AttentionRun{run(1, RunFailed, "main", 1), run(2, RunSucceeded, "main", 2)}, nil, 0},
		{"retried and failed again", []AttentionRun{run(1, RunFailed, "main", 1), run(2, RunFailed, "main", 2)}, []string{"failed:r2"}, 0},
		{"different branch does not supersede", []AttentionRun{run(1, RunFailed, "main", 1), run(2, RunSucceeded, "dev", 2)}, []string{"failed:r1"}, 0},
		{"different pull request does not supersede", []AttentionRun{pr(1, RunFailed, 7, "ana", 1), pr(2, RunSucceeded, 8, "ana", 2)}, []string{"failed:r1"}, 0},
		{"same pull request is superseded", []AttentionRun{pr(1, RunFailed, 7, "ana", 1), pr(2, RunRunning, 7, "ana", 2)}, nil, 1},
		{"someone else's feature branch does not matter", []AttentionRun{run(1, RunFailed, "feat-x", 1)}, nil, 0},
		{"someone else's pull request does not matter", []AttentionRun{pr(1, RunFailed, 7, "bob", 1)}, nil, 0},
		{"a canceled newer Sortie does not clear it", []AttentionRun{run(1, RunFailed, "main", 1), run(2, RunCanceled, "main", 2)}, []string{"failed:r1"}, 0},
		{"manual re-run of the same Sortie running", []AttentionRun{run(1, RunFailed, "main", 1), func() AttentionRun { r := run(2, RunRunning, "main", 2); r.RerunOf = "r1"; return r }()}, nil, 1},
		{"a draft is nobody's", []AttentionRun{func() AttentionRun { r := run(1, RunFailed, "main", 1); r.Event = "draft"; return r }()}, nil, 0},
		{"three failures in a row say so", []AttentionRun{run(1, RunFailed, "main", 1), run(2, RunFailed, "main", 2), run(3, RunFailed, "main", 3)}, []string{"failed:r3"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ComputeAttention(AttentionInput{Runs: c.runs, Now: 6 * ms, User: "ana", DefaultBranch: def})
			var got []string
			for _, n := range res.Needs {
				got = append(got, n.Key)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("needs %v, want %v", got, c.want)
			}
			if len(res.Retrying) != c.retrying {
				t.Errorf("retrying %d, want %d", len(res.Retrying), c.retrying)
			}
			for _, n := range res.Needs {
				if n.Why == "" || n.Do == "" {
					t.Errorf("%s says no why or what to do", n.Key)
				}
			}
		})
	}
}

func TestAttentionFollowsAndOwn(t *testing.T) {
	runs := []AttentionRun{run(1, RunFailed, "feat-x", 1)}
	if res := ComputeAttention(AttentionInput{Runs: runs, Now: 10 * ms, Follows: map[string]bool{"api": true}}); len(res.Needs) != 1 {
		t.Errorf("a followed pipeline's failure is listed: %+v", res.Needs)
	}
	if res := ComputeAttention(AttentionInput{Runs: []AttentionRun{pr(1, RunFailed, 3, "ana", 1)}, Now: 10 * ms, User: "ana"}); len(res.Needs) != 1 {
		t.Errorf("your own pull request is listed: %+v", res.Needs)
	}
}

func TestAttentionDeployments(t *testing.T) {
	dep := func(id, verdict string, at int64) AttentionDeployment {
		return AttentionDeployment{ID: id, RunID: "r" + id, Project: "api", Environment: "prod", Service: "web", Verdict: verdict, ClosedAt: at * ms}
	}
	cases := []struct {
		name string
		deps []AttentionDeployment
		want int
	}{
		{"failed, nothing after", []AttentionDeployment{dep("1", VerdictFailed, 1)}, 1},
		{"failed then deployed", []AttentionDeployment{dep("1", VerdictFailed, 1), dep("2", VerdictDeployed, 2)}, 0},
		{"failed then returned", []AttentionDeployment{dep("1", VerdictFailed, 1), dep("2", VerdictRolledBack, 2)}, 0},
		{"return failed", []AttentionDeployment{dep("1", VerdictRollbackFailed, 1)}, 1},
		{"deployed then failed", []AttentionDeployment{dep("1", VerdictDeployed, 1), dep("2", VerdictFailed, 2)}, 1},
	}
	for _, c := range cases {
		res := ComputeAttention(AttentionInput{Deployments: c.deps, Now: 10 * ms})
		if len(res.Needs) != c.want {
			t.Errorf("%s: %d needs, want %d", c.name, len(res.Needs), c.want)
		}
	}
}

func TestAttentionStuckAndDecisions(t *testing.T) {
	var runs []AttentionRun
	for i := 1; i <= 6; i++ { // six passes of 5 minutes
		runs = append(runs, run(i, RunSucceeded, "dev", int64(i)))
	}
	slow := run(10, RunRunning, "main", 100)
	slow.FinishedAt = 0
	q := run(11, RunQueued, "other", 100)
	q.Project = "web"
	res := ComputeAttention(AttentionInput{Runs: append(runs, slow, q), Now: 105 * ms})
	if len(res.Needs) != 0 {
		t.Fatalf("5 minutes in is not stuck: %+v", res.Needs)
	}
	res = ComputeAttention(AttentionInput{Runs: append(runs, slow, q), Now: 140 * ms})
	if len(res.Needs) != 2 {
		t.Fatalf("a slow and a long-queued Sortie are stuck: %+v", res.Needs)
	}
	// A paused Sortie is waiting for a person, not stuck.
	res = ComputeAttention(AttentionInput{Runs: append(runs, slow), Now: 400 * ms, Approvals: []Approval{{RunID: "r10", Project: "api", RunNumber: 10, StepID: "go"}}})
	if len(res.Needs) != 1 || res.Needs[0].Kind != AttnDecision {
		t.Fatalf("only the decision is listed: %+v", res.Needs)
	}
}
