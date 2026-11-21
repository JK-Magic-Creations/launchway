package pulls

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeRuns struct{ runs []RunInfo }

func (f *fakeRuns) RunsOf(_ context.Context, _, _ string, n int) ([]RunInfo, error) {
	return f.runs, nil
}

func (w *world) show(rev string, format string) string {
	w.t.Helper()
	var out string
	if err := w.svc.read(w.ctx, repoID, func(g gitRunner) error {
		var err error
		out, err = g.out(w.ctx, "show", "-s", "--format="+format, rev)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) file(rev, p string) string {
	w.t.Helper()
	var out string
	_ = w.svc.read(w.ctx, repoID, func(g gitRunner) error { out, _, _ = g.readFile(w.ctx, rev, p); return nil })
	return out
}

func mergeWorld(t *testing.T) (*world, PullRequest) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{"README.md": "hi\n"}, "base")
	w.commit("feature", "main", map[string]string{"f.txt": "feature\n"}, "add f")
	w.commit("feature", "", map[string]string{"g.txt": "more\n"}, "add g")
	w.open("ada", New{Title: "Add f and g", Body: "Because we need them.", SourceBranch: "feature", TargetBranch: "main"})
	w.svc.Settle()
	return w, w.get(1)
}

func TestMergeMethods(t *testing.T) {
	for _, method := range []string{MethodMerge, MethodSquash, MethodRebase} {
		t.Run(method, func(t *testing.T) {
			w, p := mergeWorld(t)
			// The target moves too, so a rebase has something to rebase onto.
			newBase := w.commit("main", "", map[string]string{"other.txt": "o\n"}, "main moves")
			w.push("bob", "main", p.BaseSHA, newBase)
			p = w.get(1)
			res, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: method, Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"})
			if err != nil {
				t.Fatal(err)
			}
			tip := w.refs()["refs/heads/main"]
			if res.Commit != tip || res.Pull.State != Merged || res.Pull.MergeMethod != method || res.Pull.MergedBy != "grace" || res.Pull.MergeCommit != tip {
				t.Fatalf("%+v tip %s", res.Pull, tip)
			}
			for _, f := range []string{"f.txt", "g.txt", "other.txt", "README.md"} {
				if w.file(tip, f) == "" {
					t.Errorf("%s is missing after the merge", f)
				}
			}
			parents := strings.Fields(w.show(tip, "%P"))
			switch method {
			case MethodMerge:
				if len(parents) != 2 || parents[0] != newBase || parents[1] != p.HeadSHA {
					t.Errorf("merge parents %v", parents)
				}
				if got := w.show(tip, "%B"); !strings.Contains(got, "Merge pull request #1 from feature") || !strings.Contains(got, "Add f and g") {
					t.Errorf("merge message %q", got)
				}
			case MethodSquash:
				if len(parents) != 1 || parents[0] != newBase {
					t.Errorf("squash parents %v", parents)
				}
				if got := w.show(tip, "%B"); !strings.Contains(got, "Add f and g (#1)") || !strings.Contains(got, "Because we need them.") || !strings.Contains(got, "Co-authored-by: Dana <dana@example.com>") {
					t.Errorf("squash message %q", got)
				}
				if got := w.show(tip, "%an"); got != "grace" {
					t.Errorf("squash author %q", got)
				}
			case MethodRebase:
				if got := w.show(tip, "%s"); got != "add g" {
					t.Errorf("rebased tip %q", got)
				}
				if got := w.show(tip+"~1", "%s|%an|%P"); !strings.HasPrefix(got, "add f|Dana|"+newBase) {
					t.Errorf("rebased first commit %q", got)
				}
			}
			// The source branch stays unless asked.
			if w.refs()["refs/heads/feature"] == "" {
				t.Error("the source branch was deleted without being asked")
			}
			if _, err := w.svc.Merge(w.ctx, repoID, res.Pull, MergeInput{Actor: "grace"}); !errors.Is(err, ErrConflict) {
				t.Errorf("merging twice: %v", err)
			}
		})
	}
}

func TestMergeRefusals(t *testing.T) {
	w, p := mergeWorld(t)
	set := DefaultSettings
	set.AllowRebase, set.AllowSquash = false, false
	if err := w.svc.Store.SetSettings(w.ctx, repoID, set, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: MethodSquash, Actor: "grace"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "allows merge") {
		t.Fatalf("a method the repository does not allow: %v", err)
	}
	if err := w.svc.Store.SetSettings(w.ctx, repoID, Settings{DefaultMethod: "merge"}, "ada"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("settings that allow nothing: %v", err)
	}
	if _, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{ExpectHead: strings.Repeat("a", 40), Actor: "grace"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "changed while you were looking") {
		t.Fatalf("a stale view: %v", err)
	}
	d := true
	if _, err := w.svc.Edit(w.ctx, repoID, p, Edit{Draft: &d}, "ada"); err != nil {
		t.Fatal(err)
	}
	_, err := w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Actor: "grace"})
	var nr *NotReadyError
	if !errors.As(err, &nr) || !strings.Contains(nr.Readiness.Why, "it is a draft") {
		t.Fatalf("a draft: %v", err)
	}
	if w.refs()["refs/heads/main"] != p.BaseSHA {
		t.Fatal("a refused merge moved the target")
	}
}

// The test that matters most: a push lands on the target between the moment the
// merge read the refs and the moment it commits.
func TestAPushDuringAMergeIsNeverLost(t *testing.T) {
	w, p := mergeWorld(t)
	var pushed string
	landed := false
	w.svc.beforeCommit = func() {
		if !landed {
			landed = true
			pushed = w.commit("main", "", map[string]string{"pushed.txt": "pushed meanwhile\n"}, "a push during the merge")
		}
	}
	res, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: MethodMerge, Actor: "grace", RepoPath: "ada/demo"})
	if err != nil {
		t.Fatal(err)
	}
	tip := w.refs()["refs/heads/main"]
	if tip != res.Commit {
		t.Fatalf("tip %s, merge %s", tip, res.Commit)
	}
	// The merge was made again on the new tip: the pushed commit is its first parent.
	if parents := strings.Fields(w.show(tip, "%P")); parents[0] != pushed {
		t.Fatalf("the merge is not on top of the push: %v (push %s)", parents, pushed)
	}
	if w.file(tip, "pushed.txt") == "" || w.file(tip, "f.txt") == "" {
		t.Fatal("the push or the merge was lost")
	}

	// A target that never stops moving: the merge fails cleanly and changes nothing.
	w2, p2 := mergeWorld(t)
	var n atomic.Int32
	var last string
	w2.svc.beforeCommit = func() { last = w2.commit("main", "", map[string]string{"x.txt": string(rune('a' + n.Add(1)))}, "busy") }
	_, err = w2.svc.Merge(w2.ctx, repoID, p2, MergeInput{Actor: "grace"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "kept moving") {
		t.Fatalf("a target that never stops moving: %v", err)
	}
	if n.Load() != mergeAttempts || w2.refs()["refs/heads/main"] != last {
		t.Fatalf("attempts %d, tip %s want %s", n.Load(), w2.refs()["refs/heads/main"], last)
	}
	if got := w2.get(1); got.State != Open {
		t.Fatalf("a failed merge left the pull request %s", got.State)
	}
}

func TestMergeCanDeleteTheSourceBranchInTheSameTransaction(t *testing.T) {
	w, p := mergeWorld(t)
	yes := true
	res, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Actor: "grace", DeleteBranch: &yes})
	if err != nil || !res.BranchDeleted {
		t.Fatalf("%+v %v", res, err)
	}
	refs := w.refs()
	if _, ok := refs["refs/heads/feature"]; ok {
		t.Fatal("the branch is still there")
	}
	// The pull ref keeps the commits fetchable.
	if refs[PullRef(1)] != p.HeadSHA {
		t.Fatalf("pull ref: %s", refs[PullRef(1)])
	}
	// A protected source is never deleted.
	w3, p3 := mergeWorld(t)
	if err := w3.svc.Store.SetRule(w3.ctx, repoID, Rule{Pattern: "feature", NoSelfApproval: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	res, err = w3.svc.Merge(w3.ctx, repoID, p3, MergeInput{Actor: "grace", DeleteBranch: &yes})
	if err != nil || res.BranchDeleted || w3.refs()["refs/heads/feature"] == "" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRulesSaySWhatIsMissing(t *testing.T) {
	w, p := mergeWorld(t)
	runs := &fakeRuns{}
	w.svc.Runs = runs
	if err := w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequiredApprovals: 1, RequiredChecks: []string{"test"}, RequireResolved: true, NoSelfApproval: true, BypassAdmins: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	why := func(role string) Readiness {
		r, err := w.svc.Readiness(w.ctx, repoID, "ada/demo", w.get(1), role)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := why(RoleOperator)
	if r.CanMerge || r.Why != "needs 1 more approval; `test` has not passed" || r.CanBypass {
		t.Fatalf("%+v", r)
	}
	// The author's own approval does not count.
	if _, err := w.svc.SubmitReview(w.ctx, repoID, p, "ada", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator}); err != nil {
		t.Fatal(err)
	}
	if r := why(RoleOperator); !strings.HasPrefix(r.Why, "needs 1 more approval") || r.Approvals != 0 {
		t.Fatalf("self approval: %+v", r)
	}
	// A viewer's approval does not count toward a rule that needs an operator.
	if _, err := w.svc.SubmitReview(w.ctx, repoID, p, "vera", ReviewInput{Verdict: VerdictApprove, Role: RoleViewer}); err != nil {
		t.Fatal(err)
	}
	if r := why(RoleOperator); r.Approvals != 0 {
		t.Fatalf("a viewer's approval counted: %+v", r)
	}
	if _, err := w.svc.SubmitReview(w.ctx, repoID, p, "grace", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator, Comments: []NewComment{{Body: "check this", Path: "f.txt", Line: 1}}}); err != nil {
		t.Fatal(err)
	}
	r = why(RoleOperator)
	if r.Approvals != 1 || r.Why != "1 conversation is not resolved; `test` has not passed" {
		t.Fatalf("%+v", r)
	}
	cs, _ := w.svc.Store.Comments(w.ctx, p.ID)
	if err := w.svc.ResolveThread(w.ctx, repoID, p, cs[0].ThreadID, "grace", true); err != nil {
		t.Fatal(err)
	}
	runs.runs = []RunInfo{{ID: "r1", Project: "demo", Number: 1, Pipeline: "ci", Status: "failed", SHA: p.HeadSHA, Created: 1,
		Steps: []StepInfo{{Name: "test", Status: "failed"}, {Name: "lint", Status: "succeeded"}}}}
	r = why(RoleOperator)
	if r.Why != "`test` failed" || r.CanMerge {
		t.Fatalf("%+v", r)
	}
	// An administrator may bypass the review rules, with a reason, which is recorded.
	r = why(RoleAdmin)
	if !r.CanBypass {
		t.Fatalf("%+v", r)
	}
	if _, err := w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Actor: "root", ActorRole: RoleAdmin, RepoPath: "ada/demo"}); err == nil || !strings.Contains(err.Error(), "must say why") {
		t.Fatalf("a bypass without a reason: %v", err)
	}
	if _, err := w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo", Bypass: "because"}); err == nil {
		t.Fatal("an operator bypassed the rules")
	}
	res, err := w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Actor: "root", ActorRole: RoleAdmin, RepoPath: "ada/demo", Bypass: "the test is flaky, fixed in #7"})
	if err != nil || len(res.Bypassed) != 1 || !strings.Contains(res.Bypassed[0], "`test` failed") {
		t.Fatalf("%+v %v", res, err)
	}
	cs, _ = w.svc.Store.Comments(w.ctx, p.ID)
	last := cs[len(cs)-1]
	if !strings.Contains(last.Body, "past the rules") || !strings.Contains(last.Body, "flaky") || last.Author != "root" {
		t.Fatalf("the bypass was not recorded: %+v", last)
	}
	if len(res.Pull.LinkedRuns) != 1 || res.Pull.LinkedRuns[0] != "r1" {
		t.Fatalf("linked runs: %v", res.Pull.LinkedRuns)
	}
}

func TestRuleValidationAndCombination(t *testing.T) {
	for _, r := range []Rule{{Pattern: ""}, {Pattern: "refs/heads/main"}, {Pattern: "main", RequiredApprovals: 99}, {Pattern: "main", ApprovalRole: "viewer"}, {Pattern: "main", RequireSigned: true}} {
		r := r
		if err := r.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted: %v", r, err)
		}
	}
	got, ok := RuleFor([]Rule{
		{Pattern: "main", RequiredApprovals: 1, RequiredChecks: []string{"a"}, ApprovalRole: RoleOperator, BypassAdmins: true},
		{Pattern: "m*", RequiredApprovals: 2, RequiredChecks: []string{"b", "a"}, ApprovalRole: RoleAdmin, RequireUpToDate: true, BypassAdmins: false},
		{Pattern: "other", RequiredApprovals: 9},
	}, "main")
	if !ok || got.RequiredApprovals != 2 || got.ApprovalRole != RoleAdmin || !slices.Equal(got.RequiredChecks, []string{"a", "b"}) || !got.RequireUpToDate || got.BypassAdmins {
		t.Fatalf("%+v", got)
	}
	if _, ok := RuleFor([]Rule{{Pattern: "main"}}, "dev"); ok {
		t.Fatal("a rule covered a branch it does not name")
	}
	if !matchesCheck("test", "ci/test") || matchesCheck("ci/test", "other/test") || !matchesCheck("ci", "ci") || matchesCheck("test", "ci") {
		t.Fatal("check names")
	}
}

func TestUpToDateRuleAndAutoMerge(t *testing.T) {
	w, p := mergeWorld(t)
	w.svc.Paths = func(context.Context, string) (string, error) { return "ada/demo", nil }
	if err := w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequiredApprovals: 1, RequireUpToDate: true, NoSelfApproval: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	// Auto-merge waits for the approval.
	if err := w.svc.SetAutoMerge(w.ctx, repoID, p, MethodSquash, "ada"); err != nil {
		t.Fatal(err)
	}
	if got := w.get(1); got.State != Open || got.AutoMerge != MethodSquash {
		t.Fatalf("%+v", got)
	}
	// The target moves: the approval is then not enough, the branch is behind.
	nb := w.commit("main", "", map[string]string{"n.txt": "n\n"}, "main moves")
	w.push("bob", "main", p.BaseSHA, nb)
	if _, err := w.svc.SubmitReview(w.ctx, repoID, w.get(1), "grace", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator}); err != nil {
		t.Fatal(err)
	}
	w.svc.Settle()
	got := w.get(1)
	if got.State != Open {
		t.Fatalf("auto-merge merged a pull request that is behind: %+v", got)
	}
	r, _ := w.svc.Readiness(w.ctx, repoID, "ada/demo", got, RoleOperator)
	if r.Why != "it is 1 commit behind main and must be up to date; update the branch" {
		t.Fatalf("%q", r.Why)
	}
	// The author brings the branch up to date; the pull request then merges by itself.
	w.force("feature", w.commit("feature2", "main", map[string]string{"f.txt": "feature\n", "g.txt": "more\n"}, "rebuilt on main"))
	w.push("ada", "feature", got.HeadSHA, w.refs()["refs/heads/feature"])
	if _, err := w.svc.SubmitReview(w.ctx, repoID, w.get(1), "grace", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator}); err != nil {
		t.Fatal(err)
	}
	w.svc.Settle()
	done := w.get(1)
	if done.State != Merged || done.MergeMethod != MethodSquash || done.MergedBy != "ada" {
		t.Fatalf("auto-merge: %+v", done)
	}
}

func TestCloseReopenRetargetEditRevert(t *testing.T) {
	w, p := mergeWorld(t)
	w.commit("release", "main", map[string]string{"r.txt": "r\n"}, "release branch")
	if err := w.svc.Retarget(w.ctx, repoID, p, "release", "ada"); err != nil {
		t.Fatal(err)
	}
	p = w.get(1)
	if p.TargetBranch != "release" || p.BaseSHA != w.refs()["refs/heads/release"] || p.Revisions != 2 || p.Mergeability.State != MergeClean {
		t.Fatalf("%+v", p)
	}
	if err := w.svc.Retarget(w.ctx, repoID, p, "feature", "ada"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retarget to the source: %v", err)
	}
	title := "New title"
	np, err := w.svc.Edit(w.ctx, repoID, p, Edit{Title: &title, Labels: &[]string{"x"}}, "ada")
	if err != nil || np.Title != title || !slices.Equal(np.Labels, []string{"x"}) {
		t.Fatalf("%+v %v", np, err)
	}
	if err := w.svc.Close(w.ctx, repoID, p, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Close(w.ctx, repoID, w.get(1), "ada"); !errors.Is(err, ErrConflict) {
		t.Fatalf("closing twice: %v", err)
	}
	// Reopening takes the source branch's tip as it is now.
	h2 := w.commit("feature", "", map[string]string{"late.txt": "l\n"}, "late")
	if err := w.svc.Reopen(w.ctx, repoID, w.get(1), "ada"); err != nil {
		t.Fatal(err)
	}
	if got := w.get(1); got.State != Open || got.HeadSHA != h2 || got.ClosedBy != "" {
		t.Fatalf("%+v", got)
	}
	// Merge, then revert: a new pull request that undoes it.
	res, err := w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Actor: "grace", Method: MethodMerge, RepoPath: "ada/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Reopen(w.ctx, repoID, res.Pull, "ada"); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopening a merged pull request: %v", err)
	}
	rev, err := w.svc.Revert(w.ctx, repoID, res.Pull, "grace")
	if err != nil {
		t.Fatal(err)
	}
	if rev.Number != 2 || rev.Reverts != 1 || rev.SourceBranch != "revert-pr-1" || rev.TargetBranch != "release" || !strings.HasPrefix(rev.Title, "Revert ") {
		t.Fatalf("%+v", rev)
	}
	w.svc.Settle()
	if _, err := w.svc.Merge(w.ctx, repoID, w.get(2), MergeInput{Actor: "grace", Method: MethodSquash, RepoPath: "ada/demo"}); err != nil {
		t.Fatal(err)
	}
	tip := w.refs()["refs/heads/release"]
	if w.file(tip, "f.txt") != "" || w.file(tip, "late.txt") != "" || w.file(tip, "r.txt") == "" {
		t.Fatalf("the revert did not undo the merge: f=%q late=%q r=%q", w.file(tip, "f.txt"), w.file(tip, "late.txt"), w.file(tip, "r.txt"))
	}
}

func TestEventsAreEmittedForEveryStateChange(t *testing.T) {
	w, p := mergeWorld(t)
	var got []string
	w.svc.OnEvent = func(_ context.Context, e Event) { got = append(got, e.Action) }
	_ = w.svc.RequestReview(w.ctx, repoID, p, "ada", []string{"grace"})
	_, _ = w.svc.CommentOn(w.ctx, repoID, p, "grace", NewComment{Body: "hi"}, "")
	_, _ = w.svc.SubmitReview(w.ctx, repoID, p, "grace", ReviewInput{Verdict: VerdictApprove})
	_ = w.svc.Close(w.ctx, repoID, p, "ada")
	_ = w.svc.Reopen(w.ctx, repoID, w.get(1), "ada")
	h := w.commit("feature", "", map[string]string{"z": "z\n"}, "z")
	w.push("ada", "feature", p.HeadSHA, h)
	_, _ = w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Actor: "grace"})
	want := []string{"review_requested", "commented", "review_submitted", "closed", "reopened", "review_dismissed", "synchronize", "merged"}
	if !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}

// A required check that a path rule kept from running (a step skipped by
// changed, or a pull request that started no run because of paths_ignore)
// is satisfied: nothing needed to run. A plainly skipped step is not.
func TestPathSkippedChecksSatisfyRequiredChecks(t *testing.T) {
	w, _ := mergeWorld(t)
	runs := &fakeRuns{}
	w.svc.Runs = runs
	if err := w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequiredChecks: []string{"build/image", "build/test"}}, "ada"); err != nil {
		t.Fatal(err)
	}
	head := w.get(1).HeadSHA
	blockers := func() string {
		r, err := w.svc.Readiness(w.ctx, repoID, "ada/demo", w.get(1), RoleOperator)
		if err != nil {
			t.Fatal(err)
		}
		return r.Why
	}
	runs.runs = []RunInfo{{ID: "r1", Pipeline: "build", Status: "succeeded", SHA: head, Steps: []StepInfo{
		{Name: "test", Status: "succeeded"}, {Name: "image", Status: "skipped", SkipKind: "paths"}}}}
	if why := blockers(); strings.Contains(why, "`build/image`") || strings.Contains(why, "`build/test`") {
		t.Fatalf("a step skipped by a path rule blocks: %q", why)
	}
	runs.runs[0].Steps[1].SkipKind = "condition"
	if why := blockers(); !strings.Contains(why, "`build/image` was skipped") {
		t.Fatalf("a step skipped by its condition must still block: %q", why)
	}
	// No run at all, because a path filter kept it from starting.
	runs.runs = []RunInfo{{SHA: head, PathSkipped: true}}
	if why := blockers(); strings.Contains(why, "`build/") {
		t.Fatalf("a pull request kept from running by paths blocks: %q", why)
	}
	runs.runs = nil
	if why := blockers(); !strings.Contains(why, "`build/image` has not passed") {
		t.Fatalf("with no run and no path rule it must block: %q", why)
	}
}
