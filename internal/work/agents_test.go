package work_test

import (
	"context"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func setupAgent(t *testing.T, s *work.Store, in work.NewAgent) (*work.Agent, work.Caller) {
	t.Helper()
	if in.Scope.Kind == "" {
		in.Scope = work.Scope{Kind: work.ScopeInstallation}
	}
	if in.Name == "" {
		in.Name = "docs"
	}
	if in.Projects == nil {
		in.Projects = []string{"acme/*"}
	}
	a, tok, err := s.CreateAgent(context.Background(), ada, in)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || tok[:4] != "lwa_" {
		t.Fatalf("token %q", tok)
	}
	got, err := s.AgentForToken(context.Background(), tok)
	if err != nil || got == nil || got.Name != in.Name {
		t.Fatalf("token lookup: %+v %v", got, err)
	}
	return a, work.Caller{Actor: work.Actor{Name: a.Name, Kind: work.KindAgent, Via: "api"}, Agent: a,
		Read: func(sc work.Scope) bool { return s.AgentCovers(a, sc) }, Write: func(sc work.Scope) bool { return s.AgentCovers(a, sc) }, Maintain: func(work.Scope) bool { return false }}
}

func TestAgentLoopAssignClaimReviewDone(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	clk := withClock(s)
	agent, ac := setupAgent(t, s, work.NewAgent{Labels: []string{"agent:docs"}, Types: []string{"task", "issue"}})
	var fired []string
	s.SetHook(func(_ context.Context, e work.Event) { fired = append(fired, e.Kind) })
	it := mustCreate(t, s, bob, work.NewItem{Title: "Fix typo in README", Labels: []string{"agent:docs"}})
	// Pool work shows in the queue without being assigned.
	q, err := s.Queue(ctx, agent, 5)
	if err != nil || len(q) != 1 || q[0].Ref != it.Ref {
		t.Fatalf("%v %v", q, err)
	}
	claimed, err := s.Claim(ctx, agent, work.ClaimOptions{})
	if err != nil || claimed.Claim == nil || claimed.Claim.Agent != "docs" || claimed.State != "in_progress" {
		t.Fatalf("%+v %v", claimed, err)
	}
	// Nobody else can claim it, and the agent's concurrency is one.
	other := mustCreate(t, s, bob, work.NewItem{Title: "Another", Labels: []string{"agent:docs"}})
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{Item: other.Ref}); err == nil {
		t.Fatal("concurrency limit ignored")
	}
	// An agent may change only what it holds and never finish it.
	if _, err := s.Update(ctx, ac, other.Ref, work.Patch{AddLabels: []string{"x"}}); err == nil {
		t.Fatal("an agent changed work it does not hold")
	}
	if _, err := s.Update(ctx, ac, it.Ref, work.Patch{State: ptr("done")}); err == nil {
		t.Fatal("an agent finished work")
	}
	if _, err := s.Update(ctx, ac, it.Ref, work.Patch{Assignees: &[]string{"bob"}}); err == nil {
		t.Fatal("an agent reassigned work")
	}
	if _, err := s.AddComment(ctx, ac, it.Ref, "working on it"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAttachment(ctx, ac, it.Ref, "transcript.md", "text/markdown", "transcript", []byte("# summary")); err != nil {
		t.Fatal(err)
	}
	clk.advance(20 * time.Minute)
	if _, err := s.Heartbeat(ctx, agent, it.Ref, "halfway", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkCode(ctx, system(), it.Ref, work.CodeLink{Kind: "pull", Repo: "acme/shop", Ref: "3", State: "open"}); err != nil {
		t.Fatal(err)
	}
	rev, err := s.RequestReview(ctx, agent, it.Ref, "docs fixed")
	if err != nil || rev.State != "in_review" || rev.Claim != nil {
		t.Fatalf("%+v %v", rev, err)
	}
	in, unread, _ := s.Inbox(ctx, "bob", true, 10)
	if unread == 0 || in[0].Kind != work.InboxAgentDone {
		t.Fatalf("the person was not told a review is needed: %+v", in)
	}
	aw, _ := s.AgentWorkFor(ctx, bob)
	_ = aw
	// The agent cannot move it on; a person finishes it.
	if _, err := s.Update(ctx, ac, it.Ref, work.Patch{State: ptr("done")}); err == nil {
		t.Fatal("agent done")
	}
	done, err := s.Update(ctx, bob, it.Ref, work.Patch{State: ptr("done")})
	if err != nil || done.State != "done" {
		t.Fatal(err)
	}
	hist, _ := s.History(ctx, it.ID, 0, 100)
	var agentMade int
	for _, e := range hist {
		if e.ActorKind == "agent" {
			agentMade++
		}
	}
	if agentMade < 3 {
		t.Fatalf("the history does not mark the agent's actions as agent-made: %+v", hist)
	}
}

func system() work.Caller { return work.System("test") }

func TestLeaseExpiryFreesTheItem(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	clk := withClock(s)
	agent, _ := setupAgent(t, s, work.NewAgent{Labels: []string{"agent:docs"}})
	it := mustCreate(t, s, bob, work.NewItem{Title: "stuck", Labels: []string{"agent:docs"}})
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{Item: it.Ref, Lease: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	clk.advance(11 * time.Minute)
	g, _ := s.Get(ctx, bob, it.Ref)
	if g.Claim == nil || !g.Claim.Expired {
		t.Fatalf("%+v", g.Claim)
	}
	if _, err := s.Heartbeat(ctx, agent, it.Ref, "", 0); err == nil {
		t.Fatal("heartbeat on an expired lease")
	}
	if n, err := s.SweepLeases(ctx); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	g, _ = s.Get(ctx, bob, it.Ref)
	if g.Claim != nil || g.State != "ready" {
		t.Fatalf("%+v %s", g.Claim, g.State)
	}
	// And another claim works.
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestKillSwitchBudgetsAndScopeLimits(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	withClock(s)
	agent, _ := setupAgent(t, s, work.NewAgent{Labels: []string{"agent:docs"}, Concurrency: 3, PerDay: 2, Projects: []string{"acme/shop"}, BudgetCentsDay: 100})
	a := mustCreate(t, s, bob, work.NewItem{Title: "a", Labels: []string{"agent:docs"}})
	b := mustCreate(t, s, bob, work.NewItem{Title: "b", Labels: []string{"agent:docs"}})
	c := mustCreate(t, s, bob, work.NewItem{Title: "c", Labels: []string{"agent:docs"}})
	foreign := mustCreate(t, s, bob, work.NewItem{Scope: work.Scope{Kind: work.ScopeProject, ID: "acme/blog"}, Title: "not mine", Labels: []string{"agent:docs"}})
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{Item: foreign.Ref}); err == nil {
		t.Fatal("an agent claimed work outside its projects")
	}
	if _, err := s.Update(ctx, bob, foreign.Ref, work.Patch{AddAssignees: []string{"docs"}}); err == nil {
		t.Fatal("assigned an agent outside its projects")
	}
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{Item: a.Ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{Item: b.Ref}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Claim(ctx, agent, work.ClaimOptions{Item: c.Ref})
	wantErr(t, err, "last 24 hours")
	// Reported spending past the budget stops further claims.
	u, err := s.ReportUsage(ctx, agent, a.Ref, work.Usage{Tokens: 1000, Cents: 150})
	if err != nil || !u.Exceeded {
		t.Fatalf("%+v %v", u, err)
	}
	// The kill switch returns what the agent holds, at once, and refuses it.
	off, err := s.UpdateAgent(ctx, bob, "docs", work.AgentPatch{Disabled: ptr(true)})
	if err == nil {
		t.Fatalf("a non-maintainer, non-owner switched an agent off: %+v", off)
	}
	if _, err := s.UpdateAgent(ctx, ada, "docs", work.AgentPatch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Get(ctx, bob, a.Ref)
	if g.Claim != nil || g.State != "ready" {
		t.Fatalf("%+v", g)
	}
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{Item: a.Ref}); err == nil {
		t.Fatal("a switched-off agent claimed")
	}
	if _, err := s.Update(ctx, bob, c.Ref, work.Patch{AddAssignees: []string{"docs"}}); err == nil {
		t.Fatal("assigned a switched-off agent")
	}
}

func TestLevelKillSwitchAndTakeOver(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	withClock(s)
	agent, _ := setupAgent(t, s, work.NewAgent{Labels: []string{"agent:docs"}})
	it := mustCreate(t, s, bob, work.NewItem{Title: "x", Labels: []string{"agent:docs"}})
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{}); err != nil {
		t.Fatal(err)
	}
	// A person takes over: assigned, lease ended, agent no longer assignee.
	g, err := s.TakeOver(ctx, bob, it.Ref)
	if err != nil || g.Claim != nil || len(g.Assignees) != 1 || g.Assignees[0].Name != "bob" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := s.Heartbeat(ctx, agent, it.Ref, "", 0); err == nil {
		t.Fatal("the agent still holds it")
	}
	// A level's kill switch stops every agent there.
	if err := s.PutSettings(ctx, proj, work.Settings{Agents: &work.AgentRules{Enabled: ptr(false)}}, ada.Actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, agent, work.ClaimOptions{Item: it.Ref}); err == nil {
		t.Fatal("claimed under a level kill switch")
	}
	if q, _ := s.Queue(ctx, agent, 5); len(q) != 0 {
		t.Fatalf("queue under kill switch: %v", q)
	}
}

func TestAutonomyGate(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	ok := work.AgentChange{Scope: proj, Agent: "docs", ItemType: "task", Files: []work.ChangedFile{{Path: "docs/a.md"}}, Lines: 10, ChecksPassed: true, ChecksRequired: 1}
	d, err := s.Gate(ctx, ok)
	if err != nil || d.AutoMerge || !d.NeedsHuman || d.Level != 1 || d.Source != "default" {
		t.Fatalf("default is a person approves and merges: %+v %v", d, err)
	}
	// A person's approval lets it merge at level 1.
	ok.HumanApproved = true
	if d, _ = s.Gate(ctx, ok); !d.CanMerge {
		t.Fatalf("%+v", d)
	}
	ok.HumanApproved = false
	// Raising the level needs a reason and listed paths.
	wantErr(t, s.PutSettings(ctx, proj, work.Settings{Agents: &work.AgentRules{Autonomy: &work.Autonomy{Level: 2, Paths: []string{"docs/**"}}}}, ada.Actor), "needs a reason")
	wantErr(t, s.PutSettings(ctx, proj, work.Settings{Agents: &work.AgentRules{Autonomy: &work.Autonomy{Level: 2, Reason: "docs only"}}}, ada.Actor), "lists the paths")
	if err := s.PutSettings(ctx, work.Scope{Kind: work.ScopeInstallation}, work.Settings{Agents: &work.AgentRules{Autonomy: &work.Autonomy{Level: 2, Paths: []string{"docs/**"}, Reason: "docs are low risk"}}}, ada.Actor); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Gate(ctx, ok)
	if !d.AutoMerge || d.NeedsHuman || d.Source != "installation" {
		t.Fatalf("%+v", d)
	}
	st, _ := s.Autonomy(ctx, proj)
	if st.Level != 2 || !st.Available || st.Source != "installation" {
		t.Fatalf("%+v", st)
	}
	refusals := map[string]func(c *work.AgentChange){
		"a path outside the policy": func(c *work.AgentChange) { c.Files = append(c.Files, work.ChangedFile{Path: "main.go"}) },
		"a pipeline file":           func(c *work.AgentChange) { c.Files = []work.ChangedFile{{Path: ".launchway/pipeline.yaml"}} },
		"the autonomy policy":       func(c *work.AgentChange) { c.Files = []work.ChangedFile{{Path: ".launchway/autonomy.yaml"}} },
		"an owners file":            func(c *work.AgentChange) { c.Files = []work.ChangedFile{{Path: "docs/CODEOWNERS"}} },
		"the quarantine file":       func(c *work.AgentChange) { c.Files = []work.ChangedFile{{Path: "docs/quarantine.yaml"}} },
		"a deleted test":            func(c *work.AgentChange) { c.Files = []work.ChangedFile{{Path: "docs/x_test.go", Deleted: true}} },
		"a skipped test":            func(c *work.AgentChange) { c.TestsSkipped = true },
		"a failing check":           func(c *work.AgentChange) { c.ChecksPassed = false },
		"a skipped check":           func(c *work.AgentChange) { c.ChecksSkipped = true },
		"too many lines":            func(c *work.AgentChange) { c.Lines = 5000 },
	}
	for name, mutate := range refusals {
		c := ok
		mutate(&c)
		d, err := s.Gate(ctx, c)
		if err != nil || d.AutoMerge || d.CanMerge {
			t.Errorf("%s was allowed: %+v %v", name, d, err)
		}
	}
	// Protected files need a person even when they approved is not enough to bypass checks.
	c := ok
	c.Files = []work.ChangedFile{{Path: ".launchway/pipeline.yaml"}}
	if d, _ := s.Gate(ctx, c); !d.NeedsHuman {
		t.Error("a pipeline file did not need a person")
	}
	// Levels 3 and 4 are stored but enforced as 2 and reported as not available.
	if err := s.PutSettings(ctx, proj, work.Settings{Agents: &work.AgentRules{Autonomy: &work.Autonomy{Level: 4, Paths: []string{"docs/**"}, Reason: "trying"}}}, ada.Actor); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Autonomy(ctx, proj)
	if st.Available || st.EnforcedLevel != 2 || st.Level != 4 {
		t.Fatalf("%+v", st)
	}
	// Reverts pause autonomy until a person clears it.
	for i := 0; i < 3; i++ {
		if err := s.RecordAgentChange(ctx, proj, "docs", "acme/shop", "1", "reverted"); err != nil {
			t.Fatal(err)
		}
	}
	d, _ = s.Gate(ctx, ok)
	if d.AutoMerge || !d.Paused {
		t.Fatalf("not paused: %+v", d)
	}
	if err := s.ClearPause(ctx, bob, proj, "x"); err == nil {
		t.Fatal("a non-maintainer cleared a pause")
	}
	if err := s.ClearPause(ctx, ada, proj, "cause found and fixed"); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.Gate(ctx, ok); !d.AutoMerge {
		t.Fatalf("%+v", d)
	}
	// Agents off at a level: always a person.
	if err := s.PutSettings(ctx, proj, work.Settings{Agents: &work.AgentRules{Enabled: ptr(false)}}, ada.Actor); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.Gate(ctx, ok); d.AutoMerge {
		t.Fatal("auto merge under the level kill switch")
	}
}

func TestSkipsTestsAndTestPaths(t *testing.T) {
	if !work.SkipsTests([]string{"  t.Skip(\"x\")"}) || !work.SkipsTests([]string{"xit('a', () => {})"}) || work.SkipsTests([]string{"fmt.Println()"}) {
		t.Fatal("skip detection")
	}
	if !work.IsTestPath("internal/a/a_test.go") || !work.IsTestPath("web/src/a.test.tsx") || work.IsTestPath("docs/guide.md") {
		t.Fatal("test paths")
	}
}
