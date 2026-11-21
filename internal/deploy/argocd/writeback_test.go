package argocd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

const sha = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func shopCluster(t *testing.T) *Cluster {
	_, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"}})
	return cl
}

// The manifest keeps a readable tag beside the digest, and the rollback
// restores both.
func TestTagAndDigestWriteBackAndRollback(t *testing.T) {
	bare, _ := bareRepo(t)
	a := agentFor(t, bare, shopCluster(t))
	j := job("never")
	j.Deploy.Changes = []engine.DeployChange{{File: "a.yaml", Image: "team/shop", Tag: "20261121-abcdef12", Digest: sha}}
	var logs []string
	log := func(f string, a ...any) { logs = append(logs, f) }
	if _, err := a.Apply(context.Background(), j, log); err != nil {
		t.Fatal(err)
	}
	show := func() string {
		out, err := exec.Command("git", "-C", bare, "show", "main:a.yaml").CombinedOutput()
		if err != nil {
			t.Fatalf("%s", out)
		}
		return string(out)
	}
	if got := show(); got != "image: team/shop:20261121-abcdef12@"+sha+"\n" {
		t.Fatalf("manifest: %q", got)
	}
	rj := job("never")
	rj.Deploy.Action = engine.ActionRevert
	if _, err := a.Revert(context.Background(), rj, log); err != nil {
		t.Fatal(err)
	}
	if got := show(); got != "image: team/shop:1\n" {
		t.Fatalf("the rollback did not restore tag and digest: %q", got)
	}
}

func TestHostedRepositoryNeedsItsOwnCredential(t *testing.T) {
	bare, _ := bareRepo(t)
	a := agentFor(t, bare, shopCluster(t))
	log := func(string, ...any) {}
	j := job("never")
	j.Deploy.Changes = []engine.DeployChange{{File: "a.yaml", Set: "image", Value: "x"}}
	j.Deploy.Environment = "prod"
	// The environment says write_back, the runner's repository is not marked hosted.
	j.Deploy.WriteBack = &engine.WriteBackJob{WriteBack: engine.WriteBack{Repo: "acme/gitops", Mode: "direct"}, Token: "lwd_x.y"}
	if _, err := a.Apply(context.Background(), j, log); err == nil || !strings.Contains(err.Error(), "is not marked hosted: true") {
		t.Fatalf("not hosted: %v", err)
	}
	// Marked hosted, but the environment has no write_back.
	r := a.Config.Repos["gitops"]
	r.Hosted = true
	a.Config.Repos["gitops"] = r
	j.Deploy.WriteBack = nil
	if _, err := a.Apply(context.Background(), j, log); err == nil || !strings.Contains(err.Error(), "has no write_back") {
		t.Fatalf("no write_back: %v", err)
	}
	// The server could not make a credential: its sentence is the failure.
	j.Deploy.WriteBack = &engine.WriteBackJob{WriteBack: engine.WriteBack{Repo: "acme/gitops", Mode: "direct"}, Error: "acme/gitops is not hosted here"}
	if _, err := a.Apply(context.Background(), j, log); err == nil || err.Error() != "acme/gitops is not hosted here" {
		t.Fatalf("server error: %v", err)
	}
}

type fakePulls struct {
	calls  []PullCall
	states []PullState
	n      atomic.Int32
}

func (f *fakePulls) Pull(_ context.Context, c PullCall) (PullState, error) {
	f.calls = append(f.calls, c)
	i := int(f.n.Add(1)) - 1
	return f.states[min(i, len(f.states)-1)], nil
}

func prJob() *engine.Job {
	j := job("never")
	j.Deploy.Message = "Deploy shop 1.2 to prod\n\nBecause."
	j.Deploy.WriteBack = &engine.WriteBackJob{WriteBack: engine.WriteBack{Repo: "acme/gitops", Mode: "pull_request"}, Token: "lwd_tok.mac", WorkBranch: "launchway/deploy/dep_1"}
	return j
}

// The step opens the pull request, says what it waits for, and goes on with
// the commit that landed.
func TestLandPullWaitsForTheMerge(t *testing.T) {
	f := &fakePulls{states: []PullState{
		{Number: 7, URL: "https://ci/pulls/7", State: "open", WaitingFor: "needs 1 more approval"},
		{Number: 7, State: "open", WaitingFor: "needs 1 more approval"},
		{Number: 7, State: "merged", Commit: "abcdef0123456789"},
	}}
	a := &Agent{Pulls: f, PullEvery: time.Millisecond}
	var logs []string
	commit, err := a.landPull(context.Background(), prJob(), func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if err != nil || commit != "abcdef0123456789" {
		t.Fatalf("%q %v", commit, err)
	}
	if f.calls[0].Op != "open" || f.calls[0].Title != "Deploy shop 1.2 to prod" || !strings.Contains(f.calls[0].Body, "\nLaunchway-Deployment: dep_1\n") || f.calls[1].Op != "status" || f.calls[1].Number != 7 {
		t.Fatalf("calls: %+v", f.calls)
	}
	waits := 0
	for _, l := range logs {
		if strings.HasPrefix(l, "Waiting for pull request") {
			waits++
		}
	}
	if waits != 1 {
		t.Fatalf("the same wait was logged %d times: %v", waits, logs)
	}
}

func TestLandPullClosedAndLate(t *testing.T) {
	a := &Agent{Pulls: &fakePulls{states: []PullState{{Number: 7, URL: "https://ci/pulls/7", State: "closed"}}}, PullEvery: time.Millisecond}
	if _, err := a.landPull(context.Background(), prJob(), func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "was closed without being merged, so main was not changed") {
		t.Fatalf("closed: %v", err)
	}
	a = &Agent{Pulls: &fakePulls{states: []PullState{{Number: 7, URL: "https://ci/pulls/7", State: "open", WaitingFor: "a person to merge it"}}}, PullEvery: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := a.landPull(ctx, prJob(), func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "was not merged in time; it waits for a person to merge it") {
		t.Fatalf("late: %v", err)
	}
	a = &Agent{}
	if _, err := a.landPull(context.Background(), prJob(), func(string, ...any) {}); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("no server: %v", err)
	}
}
