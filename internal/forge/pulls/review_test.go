package pulls

import (
	"errors"
	"strings"
	"testing"
)

const lines10 = "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"

func reviewWorld(t *testing.T) (*world, PullRequest) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{"README.md": "hi\n"}, "base")
	w.commit("feature", "main", map[string]string{"code.go": lines10}, "add code")
	return w, w.open("ada", New{Title: "Code", SourceBranch: "feature", TargetBranch: "main"})
}

func TestReviewCommentsNeedARealLine(t *testing.T) {
	w, p := reviewWorld(t)
	ok, err := w.svc.CommentOn(w.ctx, repoID, p, "grace", NewComment{Body: "why?", Path: "code.go", Line: 3}, "")
	if err != nil || ok.ThreadID != ok.ID || ok.CommitSHA != p.HeadSHA || ok.Side != "new" || ok.Line != 3 || ok.StartLine != 3 {
		t.Fatalf("%+v %v", ok, err)
	}
	for name, n := range map[string]NewComment{
		"past the end":  {Body: "x", Path: "code.go", Line: 11},
		"no file":       {Body: "x", Path: "nope.go", Line: 1},
		"line zero":     {Body: "x", Path: "code.go", Line: 0},
		"backwards":     {Body: "x", Path: "code.go", Line: 2, StartLine: 5},
		"escaping path": {Body: "x", Path: "../etc/passwd", Line: 1},
		"empty":         {Body: " \n", Path: "code.go", Line: 1},
		"unknown side":  {Body: "x", Path: "code.go", Line: 1, Side: "left"},
		"strange sha":   {Body: "x", Path: "code.go", Line: 1, Commit: strings.Repeat("a", 40)},
	} {
		if _, err := w.svc.CommentOn(w.ctx, repoID, p, "grace", n, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A reply joins the thread and carries its anchor; a conversation comment has no anchor.
	re, err := w.svc.CommentOn(w.ctx, repoID, p, "ada", NewComment{Body: "@grace because", ReplyTo: ok.ID}, "")
	if err != nil || re.ThreadID != ok.ID || re.Line != 3 || re.Path != "code.go" {
		t.Fatalf("%+v %v", re, err)
	}
	conv, err := w.svc.CommentOn(w.ctx, repoID, p, "ada", NewComment{Body: "thanks"}, "")
	if err != nil || conv.Path != "" || conv.ThreadID != "" {
		t.Fatalf("%+v %v", conv, err)
	}
	if got := Mentions("hi @grace and @bob-1, mail me@example.com @grace."); len(got) != 2 || got[0] != "grace" || got[1] != "bob-1" {
		t.Errorf("mentions: %v", got)
	}
}

func TestCommentsFollowTheCodeOrBecomeOutdated(t *testing.T) {
	w, p := reviewWorld(t)
	onL8, _ := w.svc.CommentOn(w.ctx, repoID, p, "grace", NewComment{Body: "eight", Path: "code.go", Line: 8}, "")
	onL3, _ := w.svc.CommentOn(w.ctx, repoID, p, "grace", NewComment{Body: "three", Path: "code.go", Line: 3, StartLine: 2}, "")
	// Two lines are added at the top and line 3 is rewritten.
	head2 := w.commit("feature", "", map[string]string{"code.go": "new1\nnew2\nl1\nl2\nCHANGED\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"}, "edit")
	w.push("ada", "feature", p.HeadSHA, head2)
	w.svc.Settle()
	cs, _ := w.svc.Store.Comments(w.ctx, p.ID)
	byID := map[string]Comment{}
	for _, c := range cs {
		byID[c.ID] = c
	}
	if c := byID[onL8.ID]; c.Outdated || c.Line != 10 || c.CommitSHA != head2 || c.OrigLine != 8 {
		t.Errorf("the comment on a line that only moved: %+v", c)
	}
	if c := byID[onL3.ID]; !c.Outdated || c.Line != 3 || c.CommitSHA != p.HeadSHA {
		t.Errorf("the comment on lines that changed: %+v", c)
	}
	for _, tc := range []struct {
		line, want int
		ok         bool
	}{{1, 1, true}, {5, 4, true}, {9, 8, true}} {
		_ = tc
	}
	hs := parseHunks("@@ -2,0 +3,2 @@\n+a\n+b\n@@ -6 +8,0 @@\n-x\n")
	if got, ok := mapLine(hs, 1); !ok || got != 1 {
		t.Errorf("before: %d %v", got, ok)
	}
	if got, ok := mapLine(hs, 4); !ok || got != 6 {
		t.Errorf("after an insertion: %d %v", got, ok)
	}
	if _, ok := mapLine(hs, 6); ok {
		t.Error("a removed line kept its anchor")
	}
	if got, ok := mapLine(hs, 9); !ok || got != 10 {
		t.Errorf("after both: %d %v", got, ok)
	}
}

func TestSuggestionsAreAppliedAsACommitByTheLedger(t *testing.T) {
	w, p := reviewWorld(t)
	body := "Rename:\n\n```suggestion\nBETTER 4\nBETTER 4b\n```\n"
	c, err := w.svc.CommentOn(w.ctx, repoID, p, "grace", NewComment{Body: body, Path: "code.go", Line: 4}, "")
	if err != nil || !c.HasSuggestion || c.Suggestion != "BETTER 4\nBETTER 4b" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, ok := ParseSuggestion("```suggestion\nnever closed"); ok {
		t.Error("an unclosed block is a suggestion")
	}
	if _, err := w.svc.CommentOn(w.ctx, repoID, p, "grace", NewComment{Body: "```suggestion\nx\n```", Path: "code.go", Line: 1, Side: "old"}, ""); err == nil {
		t.Error("a suggestion on the old side")
	}
	sha, err := w.svc.ApplySuggestion(w.ctx, repoID, p, c.ID, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if w.refs()["refs/heads/feature"] != sha {
		t.Fatal("the branch did not move to the suggestion's commit")
	}
	var got string
	_ = w.svc.read(w.ctx, repoID, func(g gitRunner) error { got, _, _ = g.readFile(w.ctx, sha, "code.go"); return nil })
	if got != "l1\nl2\nl3\nBETTER 4\nBETTER 4b\nl5\nl6\nl7\nl8\nl9\nl10\n" {
		t.Fatalf("file after the suggestion:\n%s", got)
	}
	np := w.get(1)
	if np.HeadSHA != sha || np.Revisions != 2 {
		t.Fatalf("the pull request did not follow: %+v", np)
	}
	cc, _ := w.svc.Store.Comment(w.ctx, p.ID, c.ID)
	if cc.AppliedSHA != sha || !cc.Resolved {
		t.Fatalf("%+v", cc)
	}
	if _, err := w.svc.ApplySuggestion(w.ctx, repoID, np, c.ID, "ada"); !errors.Is(err, ErrConflict) {
		t.Fatalf("applying twice: %v", err)
	}
	// A suggestion on a head that has moved on is refused with the reason.
	c2, _ := w.svc.CommentOn(w.ctx, repoID, np, "grace", NewComment{Body: "```suggestion\nz\n```", Path: "code.go", Line: 9}, "")
	moved := w.commit("feature", "", map[string]string{"x.txt": "x\n"}, "other")
	if _, err := w.svc.ApplySuggestion(w.ctx, repoID, np, c2.ID, "ada"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("on a moved branch: %v", err)
	}
	_ = moved
}

func TestReviewsVerdictsDismissalAndRequests(t *testing.T) {
	w, p := reviewWorld(t)
	if err := w.svc.RequestReview(w.ctx, repoID, p, "ada", []string{"grace"}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.RequestReview(w.ctx, repoID, p, "ada", []string{"ada"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("asking the author: %v", err)
	}
	if _, err := w.svc.SubmitReview(w.ctx, repoID, p, "grace", ReviewInput{Verdict: "maybe"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("verdict: %v", err)
	}
	if _, err := w.svc.SubmitReview(w.ctx, repoID, p, "grace", ReviewInput{Verdict: VerdictComment}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty comment review: %v", err)
	}
	// A review with a bad comment stores nothing.
	if _, err := w.svc.SubmitReview(w.ctx, repoID, p, "grace", ReviewInput{Verdict: VerdictApprove, Comments: []NewComment{{Body: "x", Path: "code.go", Line: 99}}}); err == nil {
		t.Fatal("a review with a comment on a missing line was stored")
	}
	if rs, _ := w.svc.Store.Reviews(w.ctx, p.ID); len(rs) != 0 {
		t.Fatalf("%+v", rs)
	}
	r, err := w.svc.SubmitReview(w.ctx, repoID, p, "grace", ReviewInput{Verdict: VerdictApprove, Body: "lgtm", Comments: []NewComment{{Body: "nit", Path: "code.go", Line: 2}}})
	if err != nil || r.CommitSHA != p.HeadSHA || r.State != "submitted" {
		t.Fatalf("%+v %v", r, err)
	}
	if np := w.get(1); len(np.Reviewers) != 0 {
		t.Fatalf("a reviewer who reviewed is still requested: %v", np.Reviewers)
	}
	cs, _ := w.svc.Store.Comments(w.ctx, p.ID)
	if len(cs) != 1 || cs[0].ReviewID != r.ID {
		t.Fatalf("%+v", cs)
	}
	// New commits dismiss the approval (the setting is on by default).
	head2 := w.commit("feature", "", map[string]string{"x.txt": "x\n"}, "more")
	w.push("ada", "feature", p.HeadSHA, head2)
	w.svc.Settle()
	got, _ := w.svc.Store.Review(w.ctx, p.ID, r.ID)
	if got.State != "dismissed" || got.DismissedBy != "system" || !strings.Contains(got.DismissedReason, "New commits") {
		t.Fatalf("%+v", got)
	}
	// With the setting off, an approval survives new commits.
	set := DefaultSettings
	set.DismissStale = false
	if err := w.svc.Store.SetSettings(w.ctx, repoID, set, "ada"); err != nil {
		t.Fatal(err)
	}
	np := w.get(1)
	r2, _ := w.svc.SubmitReview(w.ctx, repoID, np, "bob", ReviewInput{Verdict: VerdictApprove})
	head3 := w.commit("feature", "", map[string]string{"y.txt": "y\n"}, "even more")
	w.push("ada", "feature", head2, head3)
	if got, _ := w.svc.Store.Review(w.ctx, p.ID, r2.ID); got.State != "submitted" {
		t.Fatalf("%+v", got)
	}
	// Manual dismissal needs a reason; threads resolve and reopen.
	if err := w.svc.Dismiss(w.ctx, repoID, np, r2.ID, "ada", " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dismissal without a reason: %v", err)
	}
	if err := w.svc.Dismiss(w.ctx, repoID, np, r2.ID, "ada", "stale"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.ResolveThread(w.ctx, repoID, np, cs[0].ThreadID, "grace", true); err != nil {
		t.Fatal(err)
	}
	if c, _ := w.svc.Store.Comment(w.ctx, p.ID, cs[0].ID); !c.Resolved || c.ResolvedBy != "grace" {
		t.Fatalf("%+v", c)
	}
	if err := w.svc.ResolveThread(w.ctx, repoID, np, cs[0].ThreadID, "grace", false); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.ResolveThread(w.ctx, repoID, np, "nothread", "grace", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestReplaceLines(t *testing.T) {
	for _, tc := range []struct {
		in         string
		s, e       int
		with, want string
	}{
		{"a\nb\nc\n", 2, 2, "B", "a\nB\nc\n"},
		{"a\nb\nc\n", 2, 3, "X\nY", "a\nX\nY\n"},
		{"a\nb\nc\n", 1, 1, "", "b\nc\n"},
		{"a\nb\nc", 3, 3, "C", "a\nb\nC\n"},
	} {
		got, err := replaceLines(tc.in, tc.s, tc.e, tc.with)
		if err != nil || got != tc.want {
			t.Errorf("%q [%d,%d] %q: got %q %v", tc.in, tc.s, tc.e, tc.with, got, err)
		}
	}
	if _, err := replaceLines("a\n", 2, 3, "x"); err == nil {
		t.Error("lines past the end")
	}
}
