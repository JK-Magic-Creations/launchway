package work_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

func vulns(n int, start int) []work.Proposal {
	var out []work.Proposal
	for i := start; i < start+n; i++ {
		out = append(out, work.Proposal{DedupeKey: fmt.Sprintf("dep-%d", i), GroupKey: "vulnerable-dependencies", GroupTitle: "Update {n} dependencies with known vulnerabilities",
			Title: fmt.Sprintf("lib%d 1.0 (CVE-%d)", i, i), Severity: 3, Evidence: []string{fmt.Sprintf("https://reports.example/%d", i)}})
	}
	return out
}

func TestProposalsGroupReviewDismissAndRerun(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	b := work.Batch{Source: "security", RunID: "r1", ReportURL: "https://reports.example/r1", Complete: true, Proposals: vulns(14, 0)}
	b.Proposals = append(b.Proposals, work.Proposal{DedupeKey: "docker-1", GroupKey: "root-containers", GroupTitle: "{n} Dockerfiles run as root", Title: "a/Dockerfile", Severity: 2},
		work.Proposal{DedupeKey: "docker-2", GroupKey: "root-containers", GroupTitle: "{n} Dockerfiles run as root", Title: "b/Dockerfile", Severity: 2},
		work.Proposal{DedupeKey: "typo", Title: "A typo in the README", Severity: 0})
	// A reader's batch is refused; so is an agent's.
	if _, err := s.Propose(ctx, carol, proj, b); err == nil {
		t.Fatal("a reader filed proposals")
	}
	agentCaller := bob
	agentCaller.Kind = work.KindAgent
	if _, err := s.Propose(ctx, agentCaller, proj, b); err == nil {
		t.Fatal("an agent filed proposals")
	}
	res, err := s.Propose(ctx, bob, proj, b)
	if err != nil || res.New != 17 || res.Opened != 0 || res.Waiting != 3 {
		t.Fatalf("by default nothing is filed without a review: %+v %v", res, err)
	}
	if p, _ := s.List(ctx, ada, work.ListOptions{}); p.Total != 0 {
		t.Fatalf("items were made without a review: %d", p.Total)
	}
	gs, _ := s.Proposals(ctx, bob, proj, "pending")
	if len(gs) != 3 || gs[0].Group != "vulnerable-dependencies" || gs[0].Count != 14 {
		t.Fatalf("%+v", gs)
	}
	// A reader or an agent cannot review; a person who writes can.
	if _, err := s.AcceptProposals(ctx, agentCaller, proj, "security", []string{"vulnerable-dependencies"}, nil); err == nil {
		t.Fatal("an agent accepted")
	}
	if n, err := s.DismissProposals(ctx, bob, proj, "security", []string{"one:typo"}, "not worth it"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	acc, err := s.AcceptProposals(ctx, bob, proj, "security", []string{"vulnerable-dependencies", "root-containers"}, map[string]work.GroupEdit{"root-containers": {Title: "Run containers as a non-root user"}})
	if err != nil || acc.Opened != 2 {
		t.Fatalf("%+v %v", acc, err)
	}
	kp, _ := s.List(ctx, bob, work.ListOptions{Query: "key:proposal:security:vulnerable-dependencies"})
	ref := kp.Items[0].Ref
	it, err := s.Get(ctx, bob, ref)
	if err != nil || it.Title != "Update 14 dependencies with known vulnerabilities" || len(it.Criteria) != 14 || it.Reporter != "source:security" {
		t.Fatalf("%+v %v", it, err)
	}
	hasLabel := func(it *work.Item, l string) bool {
		for _, x := range it.Labels {
			if x == l {
				return true
			}
		}
		return false
	}
	if !hasLabel(it, "machine-proposed") || !hasLabel(it, "source:security") {
		t.Fatalf("not marked as machine-proposed: %v", it.Labels)
	}
	// Re-running: no duplicates; dismissed stays dismissed; fixed are ticked; new are added.
	b2 := work.Batch{Source: "security", RunID: "r2", Complete: true, Proposals: append(vulns(10, 4), work.Proposal{DedupeKey: "typo", Title: "A typo in the README"},
		work.Proposal{DedupeKey: "dep-99", GroupKey: "vulnerable-dependencies", GroupTitle: "Update {n} dependencies with known vulnerabilities", Title: "lib99 (CVE-99)", Severity: 4},
		work.Proposal{DedupeKey: "docker-1", GroupKey: "root-containers", GroupTitle: "{n} Dockerfiles run as root", Title: "a/Dockerfile", Severity: 2})}
	res, err = s.Propose(ctx, bob, proj, b2)
	if err != nil || res.New != 1 || res.Dismissed != 1 || res.Fixed != 5 || res.Opened != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if p, _ := s.List(ctx, ada, work.ListOptions{}); p.Total != 2 {
		t.Fatalf("a re-run made duplicates: %d items", p.Total)
	}
	it, _ = s.Get(ctx, bob, ref)
	done := 0
	for _, c := range it.Criteria {
		if c.Done {
			done++
		}
	}
	if len(it.Criteria) != 15 || done != 4 {
		t.Fatalf("criteria %d, ticked %d", len(it.Criteria), done)
	}
	// When nothing is left, the item closes with a comment, and reopens if it returns.
	res, err = s.Propose(ctx, bob, proj, work.Batch{Source: "security", RunID: "r3", Complete: true, Proposals: []work.Proposal{{DedupeKey: "typo", Title: "A typo in the README"}}})
	if err != nil || res.Closed != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	it, _ = s.Get(ctx, bob, ref)
	cs, _ := s.Comments(ctx, bob, ref)
	if it.State != "done" || len(cs) == 0 || cs[len(cs)-1].Author != "launchway" {
		t.Fatalf("%s %+v", it.State, cs)
	}
	res, _ = s.Propose(ctx, bob, proj, work.Batch{Source: "security", RunID: "r4", Complete: true, Proposals: vulns(1, 3)})
	if res.Reopened != 1 {
		t.Fatalf("%+v", res)
	}
	if it, _ = s.Get(ctx, bob, ref); it.State != "backlog" {
		t.Fatalf("%s", it.State)
	}
}

func TestProposalCapsFoldIntoOneSummaryAndAutoAccept(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.PutSettings(ctx, proj, work.Settings{Proposals: &work.ProposalRules{MaxItems: 3, Reason: "findings of the scan are filed at once",
		AutoAccept: []work.AutoAccept{{Source: "health", MinSeverity: 1}}}}, ada.Actor); err != nil {
		t.Fatal(err)
	}
	var ps []work.Proposal
	for i := 0; i < 25; i++ {
		ps = append(ps, work.Proposal{DedupeKey: fmt.Sprintf("k%d", i), Title: fmt.Sprintf("finding %d", i), Severity: 1 + i%4})
	}
	res, err := s.Propose(ctx, bob, proj, work.Batch{Source: "health", RunID: "1", ReportURL: "https://reports.example/h", Complete: true, Proposals: ps})
	if err != nil || res.Opened != 4 || res.Folded != 22 {
		t.Fatalf("3 items and one summary: %+v %v", res, err)
	}
	p, _ := s.List(ctx, ada, work.ListOptions{Sort: "number"})
	if p.Total != 4 || p.Items[3].Title != "health: and 22 more findings" || len(p.Items[0].Labels) < 2 {
		t.Fatalf("%d %v", p.Total, refs(p))
	}
	sum, _ := s.Get(ctx, ada, p.Items[3].Ref)
	if len(sum.Criteria) != 22 || sum.Body == "" {
		t.Fatalf("%+v", sum)
	}
	// The same run again changes nothing.
	res, _ = s.Propose(ctx, bob, proj, work.Batch{Source: "health", RunID: "2", ReportURL: "https://reports.example/h", Complete: true, Proposals: ps})
	if res.Opened != 0 {
		t.Fatalf("%+v", res)
	}
	if q, _ := s.List(ctx, ada, work.ListOptions{}); q.Total != 4 {
		t.Fatalf("%d", q.Total)
	}
	// Another source still needs a review, and an invalid setting is refused.
	res, _ = s.Propose(ctx, bob, proj, work.Batch{Source: "other", RunID: "1", Proposals: ps[:2]})
	if res.Opened != 0 || res.Waiting != 2 {
		t.Fatalf("%+v", res)
	}
}
