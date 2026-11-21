package work_test

import (
	"context"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func goodVerdict() work.Verdict {
	return work.Verdict{Decision: "approve", ScopeOK: true, Confidence: 0.8, Risks: []string{"none"},
		Criteria: []work.CriterionEvid{{Criterion: "docs build", Met: true, Evidence: "run 12 passed"}}}
}

func enableIndependent(t *testing.T, s *work.Store, differentModel bool, sample int) {
	t.Helper()
	err := s.PutSettings(context.Background(), work.Scope{Kind: work.ScopeInstallation}, work.Settings{Agents: &work.AgentRules{
		Review: &work.AgentReviewPolicy{Mode: work.ReviewIndependent, Paths: []string{"docs/**"}, Reason: "docs are low risk", RequireDifferentModel: differentModel, SamplePercent: sample}}}, ada.Actor)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentReviewIndependenceAndPolicy(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	withClock(s)
	author, _ := setupAgent(t, s, work.NewAgent{Name: "writer", Labels: []string{"agent:docs"}, Model: "alpha-1", Class: "small"})
	rev, _ := setupAgent(t, s, work.NewAgent{Name: "reviewer", Model: "beta-2", Class: "small"})
	same, _ := setupAgent(t, s, work.NewAgent{Name: "twin", Model: "alpha-1", Class: "small"})
	it := mustCreate(t, s, bob, work.NewItem{Title: "docs", Labels: []string{"agent:docs"}})
	if _, err := s.Claim(ctx, author, work.ClaimOptions{}); err != nil {
		t.Fatal(err)
	}
	change := work.AgentChange{Scope: proj, ItemType: "issue", Files: []work.ChangedFile{{Path: "docs/a.md"}}, Lines: 5, ChecksPassed: true, Repo: "acme/shop", Pull: "3"}
	in := work.ReviewInput{Change: change, Repo: "acme/shop", Pull: "3", AuthorAgents: []string{"writer"}, ItemIDs: []string{it.ID}, Verdict: goodVerdict()}

	// Under the default policy an agent's review is advice and does not count.
	r, err := s.SubmitAgentReview(ctx, rev, in)
	if err != nil || r.Counts || r.Advice == "" {
		t.Fatalf("%+v %v", r, err)
	}
	if d, _ := s.Gate(ctx, change); d.CanMerge || d.AgentApproved {
		t.Fatalf("advice merged: %+v", d)
	}
	enableIndependent(t, s, false, 0)
	// The author never reviews its own pull request, nor the lease holder.
	_, err = s.SubmitAgentReview(ctx, author, in)
	wantErr(t, err, "never reviews a pull request it opened")
	in2 := in
	in2.AuthorAgents = nil // the server did not see it as a committer, but it holds the lease
	_, err = s.SubmitAgentReview(ctx, author, in2)
	wantErr(t, err, "held the work")
	// A malformed verdict is refused.
	bad := in
	bad.Verdict.Criteria = nil
	if _, err := s.SubmitAgentReview(ctx, rev, bad); err == nil {
		t.Fatal("a verdict with no criteria")
	}
	// A different agent's approval counts, because the policy allows it here.
	r, err = s.SubmitAgentReview(ctx, rev, in)
	if err != nil || !r.Counts {
		t.Fatalf("%+v %v", r, err)
	}
	d, _ := s.Gate(ctx, change)
	if !d.AgentApproved || !d.CanMerge {
		t.Fatalf("%+v", d)
	}
	// The always-a-person list wins: a pipeline file never counts.
	prot := in
	prot.Change.Files = []work.ChangedFile{{Path: ".launchway/pipeline.yaml"}}
	prot.Pull = "4"
	prot.Change.Pull = "4"
	r, _ = s.SubmitAgentReview(ctx, rev, prot)
	if r.Counts {
		t.Fatalf("a protected file counted: %+v", r)
	}
	if d, _ := s.Gate(ctx, prot.Change); d.CanMerge {
		t.Fatalf("%+v", d)
	}
	// A path outside the policy is advice.
	out := in
	out.Change.Files = []work.ChangedFile{{Path: "main.go"}}
	out.Pull, out.Change.Pull = "5", "5"
	if r, _ := s.SubmitAgentReview(ctx, rev, out); r.Counts {
		t.Fatal("an unlisted path counted")
	}
	// A request for changes withdraws the approvals.
	chg := in
	chg.Verdict.Decision = "request_changes"
	if _, err := s.SubmitAgentReview(ctx, same, chg); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Gate(ctx, change); d.AgentApproved {
		t.Fatal("approval stood after changes were requested")
	}
	// Different model required: the same model's approval is advice.
	enableIndependent(t, s, true, 0)
	in3 := in
	in3.Pull, in3.Change.Pull = "6", "6"
	if r, _ := s.SubmitAgentReview(ctx, same, in3); r.Counts {
		t.Fatalf("the same model's approval counted: %+v", r)
	}
	if r, _ := s.SubmitAgentReview(ctx, rev, in3); !r.Counts {
		t.Fatalf("a different model's approval did not count: %+v", r)
	}
	revs, _ := s.AgentReviews(ctx, "acme/shop", "6")
	if len(revs) != 2 || len(revs[0].Verdict.Criteria) != 1 {
		t.Fatalf("%+v", revs)
	}
}

func TestSamplingSpendAndClassRouting(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	withClock(s)
	enableIndependent(t, s, false, 100)
	if ok, err := s.SampleMerge(ctx, proj, "acme/shop", "9"); err != nil || !ok {
		t.Fatalf("100%% sampling: %v %v", ok, err)
	}
	if ok, _ := s.SampleMerge(ctx, proj, "acme/shop", "9"); !ok {
		t.Fatal("not stable")
	}
	list, _ := s.Samples(ctx, ada, true)
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	if err := s.CompleteSample(ctx, bob, list[0].ID, ""); err == nil {
		t.Fatal("a non-maintainer reviewed a sample")
	}
	if err := s.CompleteSample(ctx, ada, list[0].ID, "fine"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Samples(ctx, ada, true); len(l) != 0 {
		t.Fatal("still pending")
	}

	// Class routing: large work goes only to large agents.
	if err := s.PutSettings(ctx, work.Scope{Kind: work.ScopeInstallation}, work.Settings{
		Classes:         map[string][]string{"large": {"large"}, "small": {"small", "medium", "large"}},
		DifficultyRules: []work.DifficultyRule{{MinEstimate: 8, Difficulty: "large"}, {Label: "trivial", Difficulty: "small"}}}, ada.Actor); err != nil {
		t.Fatal(err)
	}
	small, _ := setupAgent(t, s, work.NewAgent{Name: "little", Labels: []string{"agent:x"}, Class: "small", Concurrency: 3, BudgetCentsDay: 500})
	big, _ := setupAgent(t, s, work.NewAgent{Name: "big", Labels: []string{"agent:x"}, Class: "large"})
	hard := mustCreate(t, s, bob, work.NewItem{Title: "hard", Estimate: 13, Labels: []string{"agent:x"}})
	easy := mustCreate(t, s, bob, work.NewItem{Title: "easy", Labels: []string{"agent:x", "trivial"}})
	if hard.Difficulty != "large" || easy.Difficulty != "small" {
		t.Fatalf("%q %q", hard.Difficulty, easy.Difficulty)
	}
	_, err := s.Claim(ctx, small, work.ClaimOptions{Item: hard.Ref})
	wantErr(t, err, "class")
	if q, _ := s.Queue(ctx, small, 5); len(q) != 1 || q[0].Ref != easy.Ref {
		t.Fatalf("queue %v", q)
	}
	if _, err := s.Claim(ctx, big, work.ClaimOptions{Item: hard.Ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, small, work.ClaimOptions{Item: easy.Ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReportUsage(ctx, small, easy.Ref, work.Usage{Cents: 120, Tokens: 4000}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Spend(ctx, ada)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Kind == "agent" && r.Name == "little" && r.SpentCents == 120 && r.RemainingCents == 380 {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", rows)
	}
	if _, err := s.Update(ctx, bob, hard.Ref, work.Patch{Difficulty: ptr("huge")}); err == nil {
		t.Fatal("bad difficulty")
	}
}
