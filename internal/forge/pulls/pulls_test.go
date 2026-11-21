package pulls

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// A repository with main and a feature branch one commit ahead.
func featureWorld(t *testing.T) (*world, string, string) {
	w := newWorld(t)
	base := w.commit("main", "", map[string]string{"README.md": "hello\n", "a.txt": "one\ntwo\nthree\n"}, "first")
	head := w.commit("feature", "main", map[string]string{"b.txt": "new file\n"}, "add b")
	return w, base, head
}

func TestOpenRecordsTheHeadInTheLedgerAndNumbersPerRepository(t *testing.T) {
	w, base, head := featureWorld(t)
	p := w.open("ada", New{Title: "Add b", Body: "# Why\n\nBecause.", SourceBranch: "feature", TargetBranch: "main", Labels: []string{"bug", "bug", " "}, Reviewers: []string{"grace"}})
	if p.Number != 1 || p.State != Open || p.HeadSHA != head || p.BaseSHA != base || p.MergeBase != base || p.Author != "ada" {
		t.Fatalf("%+v", p)
	}
	if !slices.Equal(p.Labels, []string{"bug"}) || !slices.Equal(p.Reviewers, []string{"grace"}) || p.Revisions != 1 {
		t.Fatalf("labels, reviewers, revisions: %+v", p)
	}
	if got := w.refs()[PullRef(1)]; got != head {
		t.Fatalf("refs/pull/1/head is %q, want %s", got, head)
	}
	// The same branches again: the sentence names the pull request.
	_, err := w.svc.Open(w.ctx, repoID, "ada", New{Title: "again", SourceBranch: "feature", TargetBranch: "main"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("a second pull request between the same branches: %v", err)
	}
	w.commit("other", "main", map[string]string{"c.txt": "c\n"}, "add c")
	if p2 := w.open("ada", New{Title: "C", SourceBranch: "other", TargetBranch: "main"}); p2.Number != 2 {
		t.Fatalf("second number: %d", p2.Number)
	}
	// A pull request with nothing to merge, a missing branch, a fork.
	for name, n := range map[string]New{
		"nothing to merge": {Title: "x", SourceBranch: "main", TargetBranch: "feature"},
		"missing":          {Title: "x", SourceBranch: "nope", TargetBranch: "main"},
		"fork":             {Title: "x", SourceBranch: "other", TargetBranch: "main", SourceRepo: "ada/fork"},
		"same":             {Title: "x", SourceBranch: "main", TargetBranch: "main"},
		"no title":         {Title: " ", SourceBranch: "other", TargetBranch: "main"},
	} {
		if _, err := w.svc.Open(w.ctx, repoID, "ada", n); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMergeabilityIsJudgedInTheBackgroundAndRedoneWhenABranchMoves(t *testing.T) {
	w, base, _ := featureWorld(t)
	p := w.open("ada", New{Title: "Add b", SourceBranch: "feature", TargetBranch: "main"})
	// Right after opening, the judgment may not exist yet: never a refusal.
	if p.Mergeability.State == MergeConflicts {
		t.Fatalf("a judgment before git ran: %+v", p.Mergeability)
	}
	p = w.get(1)
	if p.Mergeability.State != MergeClean || p.Mergeability.Behind != 0 || p.Mergeability.Ahead != 1 {
		t.Fatalf("clean: %+v", p.Mergeability)
	}
	// The target moves on, touching something else: still clean, now behind.
	newBase := w.commit("main", "", map[string]string{"c.txt": "c\n"}, "main moves")
	w.push("bob", "main", base, newBase)
	p = w.get(1)
	if p.BaseSHA != newBase || p.Mergeability.State != MergeClean || p.Mergeability.Behind != 1 || p.Revisions != 2 {
		t.Fatalf("behind: %+v", p)
	}
	// Both change the same lines of a.txt: conflicts, and the file is named.
	w.commit("feature", "", map[string]string{"a.txt": "one\nTWO FEATURE\nthree\n"}, "feature edits a")
	conflicting := w.commit("main", "", map[string]string{"a.txt": "one\nTWO MAIN\nthree\n"}, "main edits a")
	w.push("bob", "main", newBase, conflicting)
	w.push("ada", "feature", p.HeadSHA, w.refs()["refs/heads/feature"])
	p = w.get(1)
	if p.Mergeability.State != MergeConflicts || !slices.Equal(p.Mergeability.Files, []string{"a.txt"}) {
		t.Fatalf("conflicts: %+v", p.Mergeability)
	}
	// A judgment made for another head is not served as this head's.
	if err := w.svc.Store.MoveTips(w.ctx, p.ID, p.HeadSHA, base, p.MergeBase); err != nil {
		t.Fatal(err)
	}
	got, _ := w.svc.Store.Get(w.ctx, repoID, 1)
	if got.Mergeability.State != MergeChecking {
		t.Fatalf("a stale judgment is shown as %q", got.Mergeability.State)
	}
}

func TestEachUpdateIsARevisionAndAForcePushKeepsTheOldHead(t *testing.T) {
	w, _, head1 := featureWorld(t)
	w.open("ada", New{Title: "Add b", SourceBranch: "feature", TargetBranch: "main"})
	head2 := w.commit("feature", "", map[string]string{"b.txt": "second\n"}, "edit b")
	w.push("ada", "feature", head1, head2)
	// A force-push rewrites the branch: the new head does not contain head2.
	head3 := w.commit("alt", "main", map[string]string{"b.txt": "rewritten\n"}, "rewritten b")
	w.force("feature", head3)
	w.push("ada", "feature", head2, head3)
	p := w.get(1)
	revs, err := w.svc.Store.Revisions(w.ctx, p.ID)
	if err != nil || len(revs) != 3 {
		t.Fatalf("%v %v", revs, err)
	}
	if revs[1].Forced || !revs[2].Forced || revs[0].HeadSHA != head1 || revs[1].HeadSHA != head2 || revs[2].HeadSHA != head3 {
		t.Fatalf("revisions: %+v", revs)
	}
	refs := w.refs()
	if refs[PullRef(1)] != head3 {
		t.Fatalf("pull ref %s", refs[PullRef(1)])
	}
	// The earlier heads are still refs, so the objects are kept and "changes since" works.
	for i, want := range []string{head1, head2, head3} {
		if got := refs[RevisionRef(1, i+1)]; got != want {
			t.Errorf("revision ref %d is %q, want %s", i+1, got, want)
		}
	}
}

func TestMergedByHandIsNoticed(t *testing.T) {
	w, base, head := featureWorld(t)
	w.open("ada", New{Title: "Add b", SourceBranch: "feature", TargetBranch: "main"})
	w.force("main", head) // somebody fast-forwards main to the feature
	w.push("bob", "main", base, head)
	p := w.get(1)
	if p.State != Merged || p.MergeCommit != head || p.MergeMethod != "manual" || p.MergedBy != "bob" || p.MergedAt.IsZero() {
		t.Fatalf("%+v", p)
	}
}
