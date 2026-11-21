package work_test

import (
	"context"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func TestCreateNumbersRefsAndHistory(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a := mustCreate(t, s, bob, work.NewItem{Title: "First", Labels: []string{"UI", "ui", "bug"}, Assignees: []string{"bob"}, Criteria: []string{"it works"}})
	b := mustCreate(t, s, bob, work.NewItem{Title: "Second", Type: "story", Estimate: 3})
	if a.Number != 1 || b.Number != 2 || a.Ref != "SHOP-1" || b.Ref != "SHOP-2" {
		t.Fatalf("%+v %+v", a, b)
	}
	if len(a.Labels) != 2 || a.Labels[0] != "bug" || a.State != "backlog" || len(a.Criteria) != 1 {
		t.Fatalf("%+v", a)
	}
	other := mustCreate(t, s, bob, work.NewItem{Scope: work.Scope{Kind: work.ScopeProject, ID: "acme/blog"}, Title: "Elsewhere"})
	if other.Ref != "BLOG-1" {
		t.Fatalf("numbers are per project: %s", other.Ref)
	}
	got, err := s.Get(ctx, carol, "shop-2")
	if err != nil || got.Title != "Second" || got.Estimate != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	hist, err := s.History(ctx, a.ID, 0, 0)
	if err != nil || len(hist) != 1 || hist[0].Kind != "created" || hist[0].Actor != "bob" {
		t.Fatalf("%+v %v", hist, err)
	}
	if _, err := s.Create(ctx, carol, work.NewItem{Scope: proj, Title: "no"}); err == nil {
		t.Fatal("a reader created an item")
	}
	if _, err := s.Create(ctx, bob, work.NewItem{Scope: proj, Title: "  "}); err == nil {
		t.Fatal("an empty title")
	}
	if _, err := s.Create(ctx, bob, work.NewItem{Scope: proj, Title: "x", Type: "story", Custom: map[string]any{"nope": 1}}); err == nil {
		t.Fatal("an unknown custom field")
	}
}

func TestUpdateVersionsHistoryAndWorkflow(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	it := mustCreate(t, s, bob, work.NewItem{Title: "Fix it", Type: "bug"})
	up, err := s.Update(ctx, bob, it.Ref, work.Patch{ExpectedVersion: it.Version, State: ptr("in_progress"), Priority: ptr("high"), AddLabels: []string{"ui"}})
	if err != nil || up.State != "in_progress" || up.Priority != "high" || up.Version != it.Version+1 {
		t.Fatalf("%+v %v", up, err)
	}
	// A stale version is refused and says so.
	_, err = s.Update(ctx, bob, it.Ref, work.Patch{ExpectedVersion: it.Version, Title: ptr("late")})
	wantErr(t, err, "changed since you read it")
	// Only a person finishes; the default workflow says so.
	agentCaller := bob
	agentCaller.Kind = work.KindAgent
	_, err = s.Update(ctx, agentCaller, it.Ref, work.Patch{State: ptr("done")})
	wantErr(t, err, "only a person")
	done, err := s.Update(ctx, bob, it.Ref, work.Patch{State: ptr("done")})
	if err != nil || done.Resolution != "completed" || done.ClosedAt == "" {
		t.Fatalf("%+v %v", done, err)
	}
	re, err := s.Update(ctx, bob, it.Ref, work.Patch{State: ptr("ready")})
	if err != nil || re.ClosedAt != "" || re.Resolution != "" {
		t.Fatalf("reopening: %+v %v", re, err)
	}
	if _, err := s.Update(ctx, bob, it.Ref, work.Patch{State: ptr("nonsense")}); err == nil {
		t.Fatal("an unknown state")
	}
	hist, _ := s.History(ctx, it.ID, 0, 0)
	kinds := ""
	for _, e := range hist {
		kinds += e.Kind + ","
	}
	if kinds != "created,updated,state,state,state," {
		t.Fatalf("history %q", kinds)
	}
	if _, err := s.Update(ctx, carol, it.Ref, work.Patch{Title: ptr("x")}); err == nil {
		t.Fatal("a reader changed an item")
	}
}

func TestWorkflowConfiguredPerLevel(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	err := s.PutSettings(ctx, proj, work.Settings{Prefix: "SHP",
		States:      []work.State{{Key: "open", Name: "Open", Category: "todo"}, {Key: "doing", Name: "Doing", Category: "doing"}, {Key: "shipped", Name: "Shipped", Category: "done", WIP: 0}},
		Transitions: []work.Transition{{To: "shipped", Who: "maintainer"}}}, ada.Actor)
	if err != nil {
		t.Fatal(err)
	}
	it := mustCreate(t, s, bob, work.NewItem{Title: "x"})
	if it.Ref != "SHP-1" || it.State != "open" || it.Category != "todo" {
		t.Fatalf("%+v", it)
	}
	_, err = s.Update(ctx, bob, it.Ref, work.Patch{State: ptr("shipped")})
	wantErr(t, err, "only a maintainer")
	if _, err := s.Update(ctx, ada, it.Ref, work.Patch{State: ptr("shipped")}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSettings(ctx, work.Scope{Kind: work.ScopeProject, ID: "acme/blog"}, work.Settings{Prefix: "SHP"}, ada.Actor); err == nil {
		t.Fatal("a prefix was given to two projects")
	}
	wantErr(t, s.PutSettings(ctx, proj, work.Settings{Agents: &work.AgentRules{HumanApproval: ptr(false)}}, ada.Actor), "needs a reason")
}

func TestParentsChildrenAndRollup(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	epic := mustCreate(t, s, bob, work.NewItem{Title: "Epic", Type: "epic"})
	st1 := mustCreate(t, s, bob, work.NewItem{Title: "S1", Type: "story", Parent: epic.Ref, Estimate: 5})
	st2 := mustCreate(t, s, bob, work.NewItem{Title: "S2", Type: "story", Parent: epic.Ref, Estimate: 3})
	task := mustCreate(t, s, bob, work.NewItem{Title: "T", Type: "task", Parent: st1.Ref})
	_ = task
	if _, err := s.Update(ctx, bob, st2.Ref, work.Patch{State: ptr("done")}); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get(ctx, bob, epic.Ref)
	if e.Children == nil || e.Children.Total != 3 || e.Children.Done != 1 || e.Children.Points != 8 || e.Children.PointsDone != 3 {
		t.Fatalf("%+v", e.Children)
	}
	if _, err := s.Update(ctx, bob, epic.Ref, work.Patch{Parent: ptr(st1.Ref)}); err == nil {
		t.Fatal("an epic got a parent")
	}
	if _, err := s.Update(ctx, bob, st1.Ref, work.Patch{Parent: ptr(task.Ref)}); err == nil {
		t.Fatal("a task holds nothing")
	}
	if _, err := s.Create(ctx, bob, work.NewItem{Scope: proj, Title: "bad", Type: "story", Parent: st1.Ref}); err == nil {
		t.Fatal("a story under a story")
	}
}
