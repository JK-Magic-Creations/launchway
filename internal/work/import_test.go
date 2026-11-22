package work_test

import (
	"context"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func TestImportStoriesIsIdempotentAndKeepsStructure(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	docs := []work.StoryDoc{
		{Key: "S01-01", Epic: "E01", Title: "First", Body: "**Goal**\n\nx\n\n**Acceptance criteria**\n\n- [ ] one\n- [x] two\n\n**How to verify**\n", Labels: []string{"story", "area/x"}, Milestone: "Next"},
		{Key: "S01-02", Epic: "E01", Title: "Second", DependsOn: []string{"S01-01"}, Milestone: "Later"},
		{Key: "E01", Title: "The epic", Labels: []string{"epic"}},
		{Key: "S01-03", Epic: "E99", Title: "Orphan"},
	}
	pre, err := s.ImportStories(ctx, ada, proj, docs, true, false)
	if err != nil || !pre.Preview || pre.Created != 4 {
		t.Fatalf("%+v %v", pre, err)
	}
	if p, _ := s.List(ctx, ada, work.ListOptions{}); p.Total != 0 {
		t.Fatal("a preview stored something")
	}
	r1, err := s.ImportStories(ctx, ada, proj, docs, false, false)
	if err != nil || r1.Created != 4 || r1.Links != 1 || len(r1.Warnings) != 1 {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, err := s.ImportStories(ctx, ada, proj, docs, false, false)
	if err != nil || r2.Created != 0 || r2.Existing != 4 || r2.Links != 0 {
		t.Fatalf("not idempotent: %+v %v", r2, err)
	}
	p, _ := s.List(ctx, ada, work.ListOptions{Query: "key:S01-01"})
	if p.Total != 1 {
		t.Fatal("key query")
	}
	it, _ := s.Get(ctx, ada, p.Items[0].Ref)
	if it.Type != "story" || it.ParentRef == "" || len(it.Criteria) != 2 || it.Milestone != "Next" || it.Blocked {
		t.Fatalf("%+v", it)
	}
	second, _ := s.List(ctx, ada, work.ListOptions{Query: "key:S01-02"})
	if !second.Items[0].Blocked {
		t.Fatal("dependency lost")
	}
	if _, err := s.ImportStories(ctx, bob, proj, docs, false, false); err == nil {
		t.Fatal("a non-maintainer imported")
	}
}

func TestPromoteArchiveKeepsNumbersAuthorsAndIsIdempotent(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if _, err := s.PromoteArchive(ctx, ada, proj, "repo1", "acme/shop", false); err == nil {
		t.Fatal("promoted nothing")
	}
	seedArchive(t, s)
	pre, err := s.PromoteArchive(ctx, ada, proj, "repo1", "acme/shop", true)
	if err != nil || pre.Created != 2 || pre.Comments != 2 {
		t.Fatalf("%+v %v", pre, err)
	}
	r, err := s.PromoteArchive(ctx, ada, proj, "repo1", "acme/shop", false)
	if err != nil || r.Created != 2 || r.Comments != 2 || r.Links != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	it, err := s.Get(ctx, ada, "SHOP-7")
	if err != nil || it.Title != "Old bug" || it.Reporter != "zoe" || it.State != "done" || it.CreatedAt != "2024-01-02T03:04:05Z" || len(it.Labels) != 1 || it.Type != "bug" {
		t.Fatalf("%+v %v", it, err)
	}
	cs, _ := s.Comments(ctx, ada, "SHOP-7")
	if len(cs) != 2 || cs[0].Author != "bob" || cs[0].CreatedAt != "2024-01-03T00:00:00Z" {
		t.Fatalf("%+v", cs)
	}
	links, _ := s.CodeLinks(ctx, ada, "SHOP-7")
	if len(links) != 1 || links[0].Ref != "9" {
		t.Fatalf("%+v", links)
	}
	again, err := s.PromoteArchive(ctx, ada, proj, "repo1", "acme/shop", false)
	if err != nil || again.Created != 0 || again.Existing != 2 || again.Comments != 0 || again.Links != 0 {
		t.Fatalf("not idempotent: %+v %v", again, err)
	}
	// The next new item continues after the highest imported number.
	n := mustCreate(t, s, bob, work.NewItem{Title: "new"})
	if n.Number != 12 {
		t.Fatalf("number %d", n.Number)
	}
}
