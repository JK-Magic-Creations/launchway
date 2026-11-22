package work_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func TestDecisionRecordsLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	withClock(s)
	d1 := mustCreate(t, s, ada, work.NewItem{Type: work.TypeDecision, Title: "Use PostgreSQL for the ledger", Body: "## Options\n\n- PostgreSQL\n- SQLite"})
	d2 := mustCreate(t, s, ada, work.NewItem{Type: work.TypeDecision, Title: "Use a managed database"})
	task := mustCreate(t, s, ada, work.NewItem{Title: "Migrate the ledger"})
	blocked := mustCreate(t, s, ada, work.NewItem{Title: "Pick a backup plan"})

	v, err := s.Decision(ctx, bob, d1.Ref)
	if err != nil || v.Status != work.DecisionProposed || v.DecidedBy != "" {
		t.Fatalf("a new decision: %+v %v", v, err)
	}
	if _, err := s.Decision(ctx, bob, task.Ref); err == nil {
		t.Fatal("an ordinary item was read as a decision")
	}
	// What it decides and blocks: links from the decision; only a decision record decides.
	if _, err := s.AddLink(ctx, ada, d1.Ref, work.LinkDecides, task.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLink(ctx, ada, task.Ref, work.LinkDecides, d1.Ref); err == nil || !strings.Contains(err.Error(), "only a decision record decides") {
		t.Fatalf("an item decided: %v", err)
	}
	if _, err := s.AddLink(ctx, ada, d1.Ref, work.LinkBlocks, blocked.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkCode(ctx, ada, d1.Ref, work.CodeLink{Kind: work.CodePull, Repo: "acme/shop", Ref: "7", Title: "Move to PostgreSQL", State: "merged"}); err != nil {
		t.Fatal(err)
	}

	// Accepting: a person who may change work; an answer; once.
	if _, err := s.AcceptDecision(ctx, carol, d1.Ref, "PostgreSQL"); err == nil {
		t.Fatal("a reader answered a decision")
	}
	agent := bob
	agent.Kind = work.KindAgent
	if _, err := s.AcceptDecision(ctx, agent, d1.Ref, "PostgreSQL"); err == nil || !strings.Contains(err.Error(), "only a person") {
		t.Fatalf("an agent answered: %v", err)
	}
	if _, err := s.AcceptDecision(ctx, bob, d1.Ref, "  "); err == nil {
		t.Fatal("an empty answer")
	}
	v, err = s.AcceptDecision(ctx, bob, d1.Ref, "PostgreSQL, one instance per environment.")
	if err != nil || v.Status != work.DecisionAccepted || v.DecidedBy != "bob" || v.DecidedAt == "" || len(v.Decides) != 1 || v.Decides[0].Ref != task.Ref ||
		len(v.Blocks) != 1 || len(v.Pulls) != 1 || v.Pulls[0].Number != "7" {
		t.Fatalf("accepted: %+v %v", v, err)
	}
	if _, err := s.AcceptDecision(ctx, bob, d1.Ref, "again"); err == nil || !strings.Contains(err.Error(), "already decided") {
		t.Fatalf("accepted twice: %v", err)
	}
	if it, _ := s.Get(ctx, bob, d1.Ref); it.State != "done" || it.ClosedAt == "" {
		t.Fatalf("an accepted decision is finished: %+v", it)
	}

	// Superseding: by an accepted decision other than itself; never the other way round.
	if _, err := s.SupersedeDecision(ctx, bob, d1.Ref, d1.Ref); err == nil {
		t.Fatal("a decision replaced itself")
	}
	if _, err := s.SupersedeDecision(ctx, bob, d1.Ref, d2.Ref); err == nil || !strings.Contains(err.Error(), "must be accepted") {
		t.Fatalf("replaced by a proposal: %v", err)
	}
	if _, err := s.SupersedeDecision(ctx, bob, d2.Ref, d1.Ref); err == nil || !strings.Contains(err.Error(), "not an accepted decision") {
		t.Fatalf("a proposal was superseded: %v", err)
	}
	if _, err := s.AcceptDecision(ctx, bob, d2.Ref, "A managed database."); err != nil {
		t.Fatal(err)
	}
	v, err = s.SupersedeDecision(ctx, bob, d1.Ref, d2.Ref)
	if err != nil || v.Status != work.DecisionSuperseded || v.SupersededBy != d2.Ref {
		t.Fatalf("superseded: %+v %v", v, err)
	}
	if _, err := s.SupersedeDecision(ctx, bob, d2.Ref, d1.Ref); err == nil {
		t.Fatal("a loop of replacements")
	}
	if n, _ := s.Decision(ctx, bob, d2.Ref); len(n.Supersedes) != 1 || n.Supersedes[0] != d1.Ref {
		t.Fatalf("the replacement says what it replaced: %+v", n)
	}

	// Lists and the query term.
	all, err := s.ListDecisions(ctx, carol, nil, "", "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	acc, _ := s.ListDecisions(ctx, carol, nil, "", work.DecisionAccepted)
	if len(acc) != 1 || acc[0].Item != d2.Ref || acc[0].DecidedBy != "bob" {
		t.Fatalf("accepted: %+v", acc)
	}
	if _, err := s.ListDecisions(ctx, carol, nil, "", "maybe"); err == nil {
		t.Fatal("a status that does not exist")
	}
	pg, err := s.List(ctx, carol, work.ListOptions{Query: "decision:superseded"})
	if err != nil || pg.Total != 1 || pg.Items[0].Ref != d1.Ref {
		t.Fatalf("the decision: term: %+v %v", pg, err)
	}
	if pg, _ = s.List(ctx, carol, work.ListOptions{Query: "decision:bogus"}); pg.Total != 0 {
		t.Fatalf("a status that does not exist matched: %d", pg.Total)
	}
	if pg, _ = s.List(ctx, carol, work.ListOptions{Repository: "acme/shop", Query: "type:decision"}); pg.Total != 2 {
		t.Fatalf("the repository's decisions: %d", pg.Total)
	}

	// The export: accepted and superseded, no proposals, a file each.
	d3 := mustCreate(t, s, ada, work.NewItem{Type: work.TypeDecision, Title: "Still open"})
	files, err := s.ExportDecisions(ctx, carol, "acme/shop", "docs/decisions")
	if err != nil || len(files) != 2 {
		t.Fatalf("export: %+v %v", files, err)
	}
	var first string
	for _, f := range files {
		if strings.Contains(f.Content, d3.Title) {
			t.Fatalf("a proposal was exported: %s", f.Path)
		}
		if f.Item == d1.Ref {
			first = f.Content
			if f.Path != "docs/decisions/"+strings.ToLower(d1.Ref)+"-use-postgresql-for-the-ledger.md" {
				t.Fatalf("the path: %s", f.Path)
			}
		}
	}
	for _, want := range []string{"# " + d1.Ref + ": Use PostgreSQL for the ledger", "Status: Superseded by " + d2.Ref, "Decided: 2026-03-02 by bob", "PostgreSQL, one instance per environment.", "- " + task.Ref + " Migrate the ledger", "acme/shop#7 Move to PostgreSQL", "- PostgreSQL"} {
		if !strings.Contains(first, want) {
			t.Errorf("the export of %s lacks %q:\n%s", d1.Ref, want, first)
		}
	}
}

func TestADecisionOfAConfidentialItemIsNotExportedToThoseWhoCannotSeeIt(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	withClock(s)
	d := mustCreate(t, s, ada, work.NewItem{Type: work.TypeDecision, Title: "Secret plan", Confidential: true})
	if _, err := s.AcceptDecision(ctx, ada, d.Ref, "Do it."); err != nil {
		t.Fatal(err)
	}
	reader := person("zed", false, false)
	if files, _ := s.ExportDecisions(ctx, reader, "acme/shop", "docs"); len(files) != 0 {
		t.Fatalf("a confidential decision was exported to someone who cannot see it: %+v", files)
	}
	if files, _ := s.ExportDecisions(ctx, ada, "acme/shop", "docs"); len(files) != 1 {
		t.Fatalf("the maintainer's export: %+v", files)
	}
}
