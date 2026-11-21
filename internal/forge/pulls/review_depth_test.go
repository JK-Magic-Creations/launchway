package pulls

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func blockerCodes(r Readiness) []string {
	var out []string
	for _, b := range r.Blockers {
		out = append(out, b.Code)
	}
	return out
}

func ownersWorld(t *testing.T, owners string) (*world, PullRequest) {
	t.Helper()
	w := newWorld(t)
	w.commit("main", "", map[string]string{"README.md": "hi\n", ".launchway/owners": owners}, "base")
	w.commit("feature", "main", map[string]string{"docs/a.md": "a\n", "src/x.go": "x\n"}, "change")
	w.open("ada", New{Title: "Change", SourceBranch: "feature", TargetBranch: "main"})
	w.svc.Settle()
	return w, w.get(1)
}

func ready(t *testing.T, w *world, role string) Readiness {
	t.Helper()
	r, err := w.svc.Readiness(w.ctx, repoID, "ada/demo", w.get(1), role)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWIPTitleBlocksAMerge(t *testing.T) {
	for title, wip := range map[string]bool{"WIP: x": true, "[WIP] x": true, "draft: x": true, " Draft: x": true, "Add WIP support": false, "Wipe it": false} {
		if IsWIP(title) != wip {
			t.Errorf("%q: %v", title, !wip)
		}
	}
	w, p := mergeWorld(t)
	title := "WIP: Add f and g"
	if _, err := w.svc.Edit(w.ctx, repoID, p, Edit{Title: &title}, "ada"); err != nil {
		t.Fatal(err)
	}
	r := ready(t, w, RoleAdmin)
	if r.CanMerge || !slices.Contains(blockerCodes(r), "wip") || r.CanBypass {
		t.Fatalf("%+v", r)
	}
	if _, err := w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Actor: "grace", ActorRole: RoleAdmin}); err == nil {
		t.Fatal("a work-in-progress title merged")
	}
	title = "Add f and g"
	if _, err := w.svc.Edit(w.ctx, repoID, w.get(1), Edit{Title: &title}, "ada"); err != nil {
		t.Fatal(err)
	}
	if !ready(t, w, RoleOperator).CanMerge {
		t.Fatal("the title is clean now")
	}
}

func TestOwnerApprovalIsRequiredPerChangedFileAndReadFromTheTarget(t *testing.T) {
	w, _ := ownersWorld(t, "/docs/ @bob @group:writers\n*.go @cy\n")
	if err := w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequireOwnerApproval: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	r := ready(t, w, RoleOperator)
	if r.CanMerge || !slices.Contains(blockerCodes(r), "owners") || len(r.NeedsOwners) != 2 {
		t.Fatalf("%+v", r)
	}
	// A reviewer who owns nothing changes nothing.
	_, _ = w.svc.SubmitReview(w.ctx, repoID, w.get(1), "zed", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator})
	if r := ready(t, w, RoleOperator); len(r.NeedsOwners) != 2 {
		t.Fatalf("a stranger's approval counted: %+v", r.NeedsOwners)
	}
	// An owner by name, and one through a group.
	_, _ = w.svc.SubmitReview(w.ctx, repoID, w.get(1), "cy", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator})
	r = ready(t, w, RoleOperator)
	if len(r.NeedsOwners) != 1 || r.NeedsOwners[0].Path != "docs/a.md" || !strings.Contains(r.Why, "group writers") {
		t.Fatalf("%+v / %s", r.NeedsOwners, r.Why)
	}
	_, _ = w.svc.SubmitReview(w.ctx, repoID, w.get(1), "wanda", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator, Groups: []string{"writers"}})
	if r := ready(t, w, RoleOperator); !r.CanMerge {
		t.Fatalf("%+v", r)
	}
	// The pull request cannot give itself an owner: the file is the target's.
	w.commit("feature", "", map[string]string{".launchway/owners": "* @ada\n"}, "own everything")
	w.push("ada", "feature", w.get(1).HeadSHA, w.refs()["refs/heads/feature"])
	w.svc.Settle()
	view, _, err := w.svc.Owners(w.ctx, repoID, w.get(1), []string{"src/x.go"})
	if err != nil || !slices.Equal(view.Files[0].Owners, []string{"cy"}) {
		t.Fatalf("%+v %v", view, err)
	}
}

func TestAuthorsOwnApprovalNeverAnswersForAnOwner(t *testing.T) {
	w, _ := ownersWorld(t, "* @ada\n")
	_ = w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequireOwnerApproval: true}, "ada")
	_, _ = w.svc.SubmitReview(w.ctx, repoID, w.get(1), "ada", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator})
	if r := ready(t, w, RoleOperator); r.CanMerge {
		t.Fatal("an author approved their own change as its owner")
	}
}

func TestABrokenOwnersFileBlocksAndAdminsCannotBypassIt(t *testing.T) {
	w, _ := ownersWorld(t, "docs/ @bob\n!oops @x\n")
	_ = w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequireOwnerApproval: true, BypassAdmins: true}, "ada")
	r := ready(t, w, RoleAdmin)
	if r.CanMerge || r.CanBypass || !slices.Contains(blockerCodes(r), "owners_file") || !strings.Contains(r.Why, "line 2") {
		t.Fatalf("%+v", r)
	}
}

func TestLatestPushMustBeApprovedBySomeoneElse(t *testing.T) {
	w, _ := ownersWorld(t, "")
	_ = w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequireFreshApproval: true}, "ada")
	if r := ready(t, w, RoleOperator); !slices.Contains(blockerCodes(r), "latest_push") {
		t.Fatalf("%+v", r)
	}
	_, _ = w.svc.SubmitReview(w.ctx, repoID, w.get(1), "ada", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator})
	if r := ready(t, w, RoleOperator); r.CanMerge {
		t.Fatal("the pusher approved their own push")
	}
	_, _ = w.svc.SubmitReview(w.ctx, repoID, w.get(1), "bob", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator})
	if r := ready(t, w, RoleOperator); !r.CanMerge {
		t.Fatalf("%+v", r)
	}
	// A new push by bob: his own earlier approval cannot cover it; ada's would.
	before := w.get(1).HeadSHA
	w.commit("feature", "", map[string]string{"more.txt": "m\n"}, "bob pushes")
	w.push("bob", "feature", before, w.refs()["refs/heads/feature"])
	w.svc.Settle()
	if r := ready(t, w, RoleOperator); r.CanMerge {
		t.Fatalf("a push needs a fresh approval: %+v", r)
	}
}

func TestChecksRequiredByPathOnlyWhenMatchingFilesChanged(t *testing.T) {
	w, _ := ownersWorld(t, "")
	w.svc.Runs = &fakeRuns{}
	rule := Rule{Pattern: "main", PathChecks: []PathCheck{{Paths: []string{"/docs/"}, Check: "docs"}, {Paths: []string{"*.rs"}, Check: "cargo"}}}
	if err := w.svc.Store.SetRule(w.ctx, repoID, rule, "ada"); err != nil {
		t.Fatal(err)
	}
	r := ready(t, w, RoleOperator)
	if r.CanMerge || !strings.Contains(r.Why, "`docs`") || strings.Contains(r.Why, "cargo") {
		t.Fatalf("only the docs check applies: %s", r.Why)
	}
	// A pipeline that its own path filter kept from running never blocks.
	w.svc.Runs = &fakeRuns{runs: []RunInfo{{Project: "p", SHA: w.get(1).HeadSHA, PathSkipped: true}}}
	if r := ready(t, w, RoleOperator); !r.CanMerge {
		t.Fatalf("a skipped pipeline blocked forever: %s", r.Why)
	}
	// A bad rule is refused in plain words.
	bad := Rule{Pattern: "main", PathChecks: []PathCheck{{Check: "x"}}}
	if err := w.svc.Store.SetRule(w.ctx, repoID, bad, "ada"); err == nil || !strings.Contains(err.Error(), "paths") {
		t.Fatalf("%v", err)
	}
}

func TestRuleExtrasRoundTripAndCombine(t *testing.T) {
	w := newWorld(t)
	a := Rule{Pattern: "main", RequireOwnerApproval: true, DismissStale: true, MergeQueue: true, PathChecks: []PathCheck{{Paths: []string{"a/"}, Check: "x"}}}
	b := Rule{Pattern: "ma*", RequireFreshApproval: true}
	for _, r := range []Rule{a, b} {
		if err := w.svc.Store.SetRule(w.ctx, repoID, r, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	rules, _ := w.svc.Store.Rules(w.ctx, repoID)
	got, ok := RuleFor(rules, "main")
	if !ok || !got.RequireOwnerApproval || !got.DismissStale || !got.MergeQueue || !got.RequireFreshApproval || len(got.PathChecks) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestReviewersAreAssignedFromOwnersWhenReady(t *testing.T) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{".launchway/owners": "/docs/ @bob @cy\n*.go @dee @group:go\n"}, "base")
	w.commit("feature", "main", map[string]string{"docs/a.md": "a\n", "x.go": "x\n"}, "change")
	set := DefaultSettings
	set.AssignReviewers = AssignOwners
	if err := w.svc.Store.SetSettings(w.ctx, repoID, set, "ada"); err != nil {
		t.Fatal(err)
	}
	// A draft gets nobody until it is ready.
	w.open("ada", New{Title: "Change", SourceBranch: "feature", TargetBranch: "main", Draft: true})
	if p := w.get(1); len(p.Reviewers) != 0 || len(p.ReviewerGroups) != 0 {
		t.Fatalf("%+v", p)
	}
	no := false
	if _, err := w.svc.Edit(w.ctx, repoID, w.get(1), Edit{Draft: &no}, "ada"); err != nil {
		t.Fatal(err)
	}
	p := w.get(1)
	slices.Sort(p.Reviewers)
	if !slices.Equal(p.Reviewers, []string{"bob", "cy", "dee"}) || !slices.Equal(p.ReviewerGroups, []string{"go"}) {
		t.Fatalf("%+v", p)
	}
	// A group's request is answered by a review from a member.
	_, _ = w.svc.SubmitReview(w.ctx, repoID, p, "gus", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator, Groups: []string{"go"}})
	if got := w.get(1); len(got.ReviewerGroups) != 0 {
		t.Fatalf("%+v", got.ReviewerGroups)
	}
}

func TestRoundRobinAndLeastLoadedPickOnePerFile(t *testing.T) {
	for _, mode := range []string{AssignRoundRobin, AssignLeastLoaded} {
		w := newWorld(t)
		w.commit("main", "", map[string]string{".launchway/owners": "* @bob @cy\n"}, "base")
		w.commit("one", "main", map[string]string{"a.txt": "a\n"}, "a")
		w.commit("two", "main", map[string]string{"b.txt": "b\n"}, "b")
		set := DefaultSettings
		set.AssignReviewers = mode
		_ = w.svc.Store.SetSettings(w.ctx, repoID, set, "ada")
		w.open("ada", New{Title: "one", SourceBranch: "one", TargetBranch: "main"})
		w.open("ada", New{Title: "two", SourceBranch: "two", TargetBranch: "main"})
		p1, p2 := w.get(1), w.get(2)
		if len(p1.Reviewers) != 1 || len(p2.Reviewers) != 1 || p1.Reviewers[0] == p2.Reviewers[0] {
			t.Errorf("%s: %v %v: the second request should go to someone else", mode, p1.Reviewers, p2.Reviewers)
		}
	}
}

func TestSeveralSuggestionsAreOneCommitAndOverlapsAreExplained(t *testing.T) {
	w, p := reviewWorld(t)
	a, err := w.svc.CommentOn(w.ctx, repoID, p, "grace", NewComment{Body: "```suggestion\nTWO\n```", Path: "code.go", Line: 2}, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := w.svc.CommentOn(w.ctx, repoID, p, "bob", NewComment{Body: "```suggestion\nSIX\nSIX-B\n```", Path: "code.go", Line: 6}, "")
	over, _ := w.svc.CommentOn(w.ctx, repoID, p, "vic", NewComment{Body: "```suggestion\nX\n```", Path: "code.go", StartLine: 1, Line: 2}, "")
	// Two that touch the same lines: refused, naming both, nothing changes.
	before := w.refs()["refs/heads/feature"]
	if _, err := w.svc.ApplySuggestions(w.ctx, repoID, p, []string{a.ID, over.ID}, "ada"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "@grace") || !strings.Contains(err.Error(), "@vic") {
		t.Fatalf("overlap: %v", err)
	}
	if w.refs()["refs/heads/feature"] != before {
		t.Fatal("a refused batch moved the branch")
	}
	sha, err := w.svc.ApplySuggestions(w.ctx, repoID, p, []string{b.ID, a.ID, a.ID}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.file(sha, "code.go"); got != "l1\nTWO\nl3\nl4\nl5\nSIX\nSIX-B\nl7\nl8\nl9\nl10\n" {
		t.Fatalf("%q", got)
	}
	if parents := strings.Fields(w.show(sha, "%P")); len(parents) != 1 || parents[0] != before {
		t.Fatalf("not one commit on the tip: %v", parents)
	}
	if msg := w.show(sha, "%B"); !strings.Contains(msg, "Apply 2 suggestions") || !strings.Contains(msg, "@bob") {
		t.Fatalf("%q", msg)
	}
	for _, id := range []string{a.ID, b.ID} {
		if c, _ := w.svc.Store.Comment(w.ctx, p.ID, id); c.AppliedSHA != sha || !c.Resolved {
			t.Fatalf("%+v", c)
		}
	}
	if _, err := w.svc.ApplySuggestions(w.ctx, repoID, w.get(1), nil, "ada"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("none: %v", err)
	}
}

func TestViewedMarksAreAPersonsAndResetWhenTheFileChanges(t *testing.T) {
	w, p := reviewWorld(t)
	if err := w.svc.SetViewed(w.ctx, repoID, p, "grace", []string{"code.go"}, true); err != nil {
		t.Fatal(err)
	}
	got, _ := w.svc.Viewed(w.ctx, repoID, p, "grace")
	if len(got) != 1 || got[0] != "code.go" {
		t.Fatalf("%v", got)
	}
	if other, _ := w.svc.Viewed(w.ctx, repoID, p, "bob"); len(other) != 0 {
		t.Fatalf("a mark leaked to another person: %v", other)
	}
	// A commit that leaves the file alone keeps the mark; one that changes it resets it.
	before := w.get(1).HeadSHA
	w.commit("feature", "", map[string]string{"other.txt": "o\n"}, "elsewhere")
	w.push("ada", "feature", before, w.refs()["refs/heads/feature"])
	np := w.get(1)
	if got, _ := w.svc.Viewed(w.ctx, repoID, np, "grace"); len(got) != 1 {
		t.Fatalf("a commit elsewhere reset the mark: %v", got)
	}
	w.commit("feature", "", map[string]string{"code.go": "changed\n"}, "touch it")
	w.push("ada", "feature", np.HeadSHA, w.refs()["refs/heads/feature"])
	np = w.get(1)
	if got, _ := w.svc.Viewed(w.ctx, repoID, np, "grace"); len(got) != 0 {
		t.Fatalf("a changed file is still viewed: %v", got)
	}
	_ = w.svc.SetViewed(w.ctx, repoID, np, "grace", []string{"code.go"}, true)
	_ = w.svc.SetViewed(w.ctx, repoID, np, "grace", []string{"code.go"}, false)
	if got, _ := w.svc.Viewed(w.ctx, repoID, np, "grace"); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if err := w.svc.SetViewed(w.ctx, repoID, np, "grace", []string{"../x"}, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}
