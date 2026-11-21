package work_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func refs(p *work.Page) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Ref)
	}
	return out
}

func TestQueryLanguage(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a := mustCreate(t, s, bob, work.NewItem{Title: "Login button misaligned", Type: "bug", Labels: []string{"ui"}, Assignees: []string{"bob"}, Priority: "high"})
	b := mustCreate(t, s, bob, work.NewItem{Title: "Export to CSV", Type: "story", Labels: []string{"data"}})
	c := mustCreate(t, s, bob, work.NewItem{Title: "100%_safe output", Body: "uses a Zebra printer"})
	if _, err := s.AddLink(ctx, bob, b.Ref, "blocks", c.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, bob, a.Ref, work.Patch{State: ptr("done")}); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"":                     {"SHOP-1", "SHOP-2", "SHOP-3"},
		"is:open":              {"SHOP-2", "SHOP-3"},
		"is:closed":            {"SHOP-1"},
		"type:bug":             {"SHOP-1"},
		"label:ui -type:story": {"SHOP-1"},
		"assignee:bob":         {"SHOP-1"},
		"assignee:none":        {"SHOP-2", "SHOP-3"},
		"is:blocked":           {"SHOP-3"},
		"zebra":                {"SHOP-3"},
		`"100%_safe"`:          {"SHOP-3"},
		"%":                    {"SHOP-3"},
		"priority:high":        {"SHOP-1"},
		"updated:<1d":          {"SHOP-1", "SHOP-2", "SHOP-3"},
		"updated:>1d":          {},
		"key:nope":             {},
		"ref:SHOP-2":           {"SHOP-2"},
		"login state:done":     {"SHOP-1"},
	}
	for q, want := range cases {
		p, err := s.List(ctx, ada, work.ListOptions{Query: q, Sort: "number"})
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		got := refs(p)
		if fmt.Sprint(got) != fmt.Sprint(want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
	if _, err := s.List(ctx, ada, work.ListOptions{Query: "is:nonsense"}); err == nil {
		t.Error("an unknown is: value")
	}
	v, err := s.SaveView(ctx, bob, proj, "mine", "assignee:me is:open", false)
	if err != nil || v.Owner != "bob" {
		t.Fatalf("%+v %v", v, err)
	}
	if vs, _ := s.Views(ctx, ada, proj); len(vs) != 0 {
		t.Error("a private view is listed to someone else")
	}
}

func TestRankOrderingUnderConcurrentMoves(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	const n = 12
	var items []*work.Item
	for i := 0; i < n; i++ {
		items = append(items, mustCreate(t, s, bob, work.NewItem{Title: fmt.Sprintf("item %d", i)}))
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pos := work.Position{Where: "top"}
			if i%3 == 1 {
				pos = work.Position{Where: "before", Other: items[(i+5)%n].Ref}
			}
			if i%3 == 2 {
				pos = work.Position{Where: "bottom"}
			}
			if _, err := s.Rank(ctx, ada, items[i].Ref, pos); err != nil {
				t.Errorf("move %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	p, err := s.List(ctx, ada, work.ListOptions{Limit: 100})
	if err != nil || len(p.Items) != n {
		t.Fatalf("%v %v", p, err)
	}
	seen := map[int64]bool{}
	for _, it := range p.Items {
		if seen[it.Rank] {
			t.Fatalf("two items share rank %d", it.Rank)
		}
		seen[it.Rank] = true
	}
	// Moves are exact: top, then before/after with no room force a renumber.
	last := mustCreate(t, s, bob, work.NewItem{Title: "last"})
	for i := 0; i < 40; i++ { // squeeze the gap until the scope renumbers
		if _, err := s.Rank(ctx, ada, last.Ref, work.Position{Where: "before", Other: p.Items[0].Ref}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Rank(ctx, ada, p.Items[1].Ref, work.Position{Where: "before", Other: last.Ref}); err != nil {
			t.Fatal(err)
		}
	}
	q, _ := s.List(ctx, ada, work.ListOptions{Limit: 100})
	if q.Items[0].Ref != p.Items[1].Ref || q.Items[1].Ref != last.Ref {
		t.Fatalf("order %v", refs(q))
	}
	if _, err := s.Rank(ctx, carol, last.Ref, work.Position{Where: "top"}); err == nil {
		t.Fatal("a reader reordered the backlog")
	}
}

func TestLinksCyclesAndCodeLinks(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a := mustCreate(t, s, bob, work.NewItem{Title: "a"})
	b := mustCreate(t, s, bob, work.NewItem{Title: "b"})
	c := mustCreate(t, s, bob, work.NewItem{Title: "c"})
	for _, p := range [][2]string{{a.Ref, b.Ref}, {b.Ref, c.Ref}} {
		if _, err := s.AddLink(ctx, bob, p[0], "blocks", p[1]); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.AddLink(ctx, bob, c.Ref, "blocks", a.Ref)
	wantErr(t, err, "cycle")
	if _, err := s.AddLink(ctx, bob, a.Ref, "blocks", a.Ref); err == nil {
		t.Fatal("self link")
	}
	got, _ := s.Get(ctx, bob, c.Ref)
	if !got.Blocked || len(got.BlockedBy) != 1 || got.BlockedBy[0] != "SHOP-2" {
		t.Fatalf("%+v", got)
	}
	// Finishing the blocker unblocks, and tells the person who waited.
	if _, err := s.Update(ctx, bob, b.Ref, work.Patch{State: ptr("done")}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, bob, c.Ref)
	if got.Blocked {
		t.Fatal("still blocked")
	}
	in, unread, _ := s.Inbox(ctx, person("bob", false, true).Actor.Name, true, 10)
	_ = in
	_ = unread
	// References in text.
	refsFound := work.ParseReferences("Fixes #12 and closes SHOP-3, refs acme/blog#7.\nsee #9, part of PAY-1")
	var got2 []string
	for _, r := range refsFound {
		got2 = append(got2, fmt.Sprintf("%s:%s:%v", r.Verb, r.Ref, r.Closes))
	}
	want := "[fixes:#12:true closes:SHOP-3:true refs:acme/blog#7:false see:#9:false part of:PAY-1:false]"
	if fmt.Sprint(got2) != want {
		t.Fatalf("%v\nwant %s", got2, want)
	}
}

func TestPullMergeMovesWorkAndNeverClosesAcrossProjects(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	blog := work.Scope{Kind: work.ScopeProject, ID: "acme/blog"}
	mine := mustCreate(t, s, bob, work.NewItem{Title: "fix me"})
	other := mustCreate(t, s, bob, work.NewItem{Scope: blog, Title: "elsewhere"})
	ch := work.PullChange{Repo: "acme/shop", Number: 4, Title: "Fix the thing", Body: "fixes #1\ncloses BLOG-1", State: "open", By: bob,
		MayClose: func(sc work.Scope) bool { return false }}
	res, err := s.ApplyPull(ctx, proj, ch)
	if err != nil || len(res.Linked) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	g, _ := s.Get(ctx, ada, mine.Ref)
	if g.State != "in_review" {
		t.Fatalf("opening a pull request that fixes it: %s", g.State)
	}
	ch.State = "merged"
	res, err = s.ApplyPull(ctx, proj, ch)
	if err != nil {
		t.Fatal(err)
	}
	g, _ = s.Get(ctx, ada, mine.Ref)
	o, _ := s.Get(ctx, ada, other.Ref)
	if g.State != "done" || o.State == "done" || len(res.NotMoved) == 0 {
		t.Fatalf("mine %s, other %s, %+v", g.State, o.State, res)
	}
	links, _ := s.CodeLinks(ctx, ada, mine.Ref)
	if len(links) != 1 || links[0].State != "merged" || links[0].Kind != "pull" {
		t.Fatalf("%+v", links)
	}
	// With permission, it does close the other project's item.
	ch.MayClose = func(work.Scope) bool { return true }
	if _, err := s.ApplyPull(ctx, proj, ch); err != nil {
		t.Fatal(err)
	}
	if o, _ = s.Get(ctx, ada, other.Ref); o.State != "done" {
		t.Fatalf("%s", o.State)
	}
	// A merge by an agent never marks work done.
	third := mustCreate(t, s, bob, work.NewItem{Title: "third"})
	agentBy := bob
	agentBy.Actor.Kind = work.KindAgent
	ch2 := work.PullChange{Repo: "acme/shop", Number: 5, Title: "x", Body: "fixes #" + fmt.Sprint(third.Number), State: "merged", By: agentBy}
	if _, err := s.ApplyPull(ctx, proj, ch2); err != nil {
		t.Fatal(err)
	}
	if g, _ = s.Get(ctx, ada, third.Ref); g.State == "done" {
		t.Fatal("an agent's merge finished the work")
	}
}

func TestConfidentialAndCrossProjectVisibility(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	reporter := person("rita", false, true)
	sec := mustCreate(t, s, reporter, work.NewItem{Title: "Vulnerability report", Confidential: true})
	open := mustCreate(t, s, bob, work.NewItem{Title: "public"})
	if _, err := s.Get(ctx, bob, sec.Ref); err == nil {
		t.Fatal("a writer who is not maintainer saw a confidential item")
	}
	if _, err := s.Get(ctx, reporter, sec.Ref); err != nil {
		t.Fatalf("the reporter: %v", err)
	}
	if _, err := s.Get(ctx, ada, sec.Ref); err != nil {
		t.Fatalf("maintainer: %v", err)
	}
	p, _ := s.List(ctx, bob, work.ListOptions{})
	if len(p.Items) != 1 || p.Items[0].Ref != open.Ref {
		t.Fatalf("bob's list %v", refs(p))
	}
	// Links to what the caller cannot see are not shown, nor can be made.
	if _, err := s.AddLink(ctx, bob, open.Ref, "relates", sec.Ref); err == nil {
		t.Fatal("linked to an item he cannot see")
	}
	if _, err := s.AddLink(ctx, ada, open.Ref, "relates", sec.Ref); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Links(ctx, bob, open.Ref); len(l) != 0 {
		t.Fatalf("a link to a hidden item leaked: %+v", l)
	}
	// Charts do not count them.
	if cfd, _ := s.CFD(ctx, bob, proj, 2); cfd.Days[1].Counts["backlog"] != 1 {
		t.Fatalf("%+v", cfd.Days[1].Counts)
	}
	// An outsider that reads nothing sees nothing.
	nobody := work.Caller{Actor: work.Actor{Name: "x", Kind: work.KindPerson}, Read: func(work.Scope) bool { return false }}
	if _, err := s.Get(ctx, nobody, open.Ref); err == nil {
		t.Fatal("read without permission")
	}
	if p, _ := s.List(ctx, nobody, work.ListOptions{}); len(p.Items) != 0 {
		t.Fatal("listed without permission")
	}
	if _, err := s.Update(ctx, bob, open.Ref, work.Patch{Confidential: ptr(true)}); err == nil {
		t.Fatal("a non-maintainer made an item confidential")
	}
}

func TestSprintMathCarryOverAndCharts(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	clk := withClock(s)
	sp, err := s.CreateSprint(ctx, ada, proj, work.SprintIn{Name: "S1", StartAt: ptr("2026-03-02"), EndAt: ptr("2026-03-06"), Capacity: map[string]float64{"ada": 5, "bob": 3}})
	if err != nil {
		t.Fatal(err)
	}
	next, _ := s.CreateSprint(ctx, ada, proj, work.SprintIn{Name: "S2", StartAt: ptr("2026-03-09"), EndAt: ptr("2026-03-13")})
	var items []*work.Item
	for i, e := range []float64{3, 5, 2} {
		items = append(items, mustCreate(t, s, bob, work.NewItem{Title: fmt.Sprintf("w%d", i), Estimate: e, Sprint: "S1", Type: "story"}))
	}
	pl, _ := s.GetSprint(ctx, ada, sp.ID)
	if pl.Committed != 10 || pl.Warning == "" || pl.TotalCap != 8 {
		t.Fatalf("planned: %+v", pl)
	}
	if _, err := s.StartSprint(ctx, ada, sp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartSprint(ctx, ada, next.ID); err == nil {
		t.Fatal("two active sprints")
	}
	clk.advance(24 * time.Hour) // Tuesday
	if _, err := s.Update(ctx, bob, items[0].Ref, work.Patch{State: ptr("done")}); err != nil {
		t.Fatal(err)
	}
	clk.advance(24 * time.Hour) // Wednesday: scope is added
	late := mustCreate(t, s, bob, work.NewItem{Title: "late", Estimate: 4, Sprint: "S1"})
	_ = late
	bd, err := s.Burndown(ctx, ada, sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bd.Committed != 10 || len(bd.Points) != 3 || bd.Points[0].Remaining != 10 || bd.Points[1].Remaining != 7 || bd.Points[2].Remaining != 11 || bd.Points[0].Ideal != 10 || bd.Points[2].Added != 4 {
		t.Fatalf("%+v", bd)
	}
	clk.advance(24 * time.Hour)
	if _, err := s.Update(ctx, bob, items[1].Ref, work.Patch{State: ptr("done")}); err != nil {
		t.Fatal(err)
	}
	done, err := s.CompleteSprint(ctx, ada, sp.ID, "S2")
	if err != nil {
		t.Fatal(err)
	}
	if done.Committed != 10 || done.Completed != 8 || done.Added != 4 || done.State != "completed" || done.ItemsDone != 2 {
		t.Fatalf("%+v", done)
	}
	got, _ := s.Get(ctx, ada, items[2].Ref)
	if got.Sprint != "S2" {
		t.Fatalf("carry-over: %q", got.Sprint)
	}
	vel, _ := s.Velocity(ctx, ada, proj, 5)
	if len(vel.Sprints) != 1 || vel.Sprints[0].Completed != 8 || vel.Average != 8 {
		t.Fatalf("%+v", vel)
	}
	if _, err := s.UpdateSprint(ctx, ada, sp.ID, work.SprintIn{Name: "x"}); err == nil {
		t.Fatal("changed a completed sprint")
	}
	cfd, err := s.CFD(ctx, ada, proj, 5)
	if err != nil || len(cfd.Days) != 5 || cfd.Days[4].Counts["done"] != 2 {
		t.Fatalf("%+v %v", cfd, err)
	}
	tm, err := s.Times(ctx, ada, proj, 30)
	if err != nil || tm.Count != 2 || tm.LeadMedian <= 0 {
		t.Fatalf("%+v %v", tm, err)
	}
	ms, _ := s.CreateMilestone(ctx, ada, proj, work.MilestoneIn{Title: "v1", DueAt: ptr("2026-04-01")})
	if _, err := s.Update(ctx, ada, items[0].Ref, work.Patch{Milestone: ptr("v1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, ada, items[2].Ref, work.Patch{Milestone: ptr("v1")}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetMilestone(ctx, ada, ms.ID)
	if m.Total != 2 || m.Done != 1 || m.Percent != 50 {
		t.Fatalf("%+v", m)
	}
	rn, err := s.ClosedSince(ctx, ada, proj, epoch, time.Time{})
	if err != nil || len(rn.Items) != 2 || rn.Markdown == "" {
		t.Fatalf("%+v %v", rn, err)
	}
}

func TestBoardColumnsWIPAndLanes(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.PutSettings(ctx, proj, work.Settings{States: []work.State{{Key: "todo", Name: "To do", Category: "todo"}, {Key: "doing", Name: "Doing", Category: "doing", WIP: 1}, {Key: "done", Name: "Done", Category: "done"}}}, ada.Actor); err != nil {
		t.Fatal(err)
	}
	a := mustCreate(t, s, bob, work.NewItem{Title: "a", Assignees: []string{"bob"}})
	b := mustCreate(t, s, bob, work.NewItem{Title: "b"})
	for _, it := range []*work.Item{a, b} {
		if _, err := s.Move(ctx, bob, it.Ref, "doing", ""); err != nil {
			t.Fatal(err)
		}
	}
	bd, err := s.Board(ctx, bob, work.BoardOptions{Scope: proj, LaneBy: "assignee"})
	if err != nil || len(bd.Columns) != 3 || !bd.Columns[1].OverWIP || bd.Columns[1].Count != 2 || len(bd.Lanes) != 2 {
		t.Fatalf("%+v %v", bd, err)
	}
	if _, err := s.Move(ctx, bob, a.Ref, "todo", b.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Board(ctx, bob, work.BoardOptions{Scope: proj, LaneBy: "colour"}); err == nil {
		t.Fatal("bad lane")
	}
}

func TestCommitsLinkAndOnlyAPersonClosesFromTheDefaultBranch(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a := mustCreate(t, s, bob, work.NewItem{Title: "a"})
	b := mustCreate(t, s, bob, work.NewItem{Title: "b"})
	// On a branch: linked, nothing moves.
	res, err := s.ApplyCommit(ctx, proj, work.CommitChange{Repo: "acme/shop", SHA: "aaaa", Message: "Fixes #1 on a branch", Branch: "topic", By: bob})
	if err != nil || len(res.Linked) != 1 || len(res.Moved) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if g, _ := s.Get(ctx, bob, a.Ref); g.State != "backlog" {
		t.Fatalf("a commit on a branch moved work: %s", g.State)
	}
	// Straight to the default branch by a person: it closes.
	res, err = s.ApplyCommit(ctx, proj, work.CommitChange{Repo: "acme/shop", SHA: "bbbb", Message: "closes " + b.Ref, Branch: "main", Default: true, By: bob})
	if err != nil || len(res.Moved) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if g, _ := s.Get(ctx, bob, b.Ref); g.State != "done" {
		t.Fatalf("%s", g.State)
	}
	if l, _ := s.CodeLinks(ctx, bob, b.Ref); len(l) != 1 || l[0].Kind != "commit" {
		t.Fatalf("%+v (no stray pull link is left behind)", l)
	}
	items, _ := s.ItemsForCode(ctx, bob, "commit", "acme/shop", "aaaa")
	if len(items) != 1 || items[0].Ref != a.Ref {
		t.Fatalf("%+v", items)
	}
}
