package pulls

import (
	"strings"
	"testing"
)

func fileByPath(c Comparison, p string) (FileChange, bool) {
	for _, f := range c.Files {
		if f.Path == p {
			return f, true
		}
	}
	return FileChange{}, false
}

func TestFilesShowStatusCountsRenamesAndBinary(t *testing.T) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{
		"keep.txt": "1\n2\n3\n4\n5\n", "gone.txt": "bye\n", "old_name.go": "package a\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n\nfunc C() int { return 3 }\n",
	}, "base")
	w.commit("feature", "main", map[string]string{
		"keep.txt": "1\n2 changed\n3\n4\n5\n6\n", "gone.txt": "", "new.txt": "x\ny\n",
		"old_name.go": "", "new_name.go": "package a\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n\nfunc C() int { return 4 }\n",
		"logo.bin": "PNG\x00\x01\x02binary\x00data",
	}, "work")
	p := w.open("ada", New{Title: "Work", SourceBranch: "feature", TargetBranch: "main"})
	d, err := w.svc.Files(w.ctx, repoID, p, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if d.Total != 5 || len(d.Files) != 5 {
		t.Fatalf("%+v", d.Files)
	}
	if f, _ := fileByPath(d.Comparison, "keep.txt"); f.Status != "modified" || f.Additions != 2 || f.Deletions != 1 {
		t.Errorf("keep.txt: %+v", f)
	}
	if f, _ := fileByPath(d.Comparison, "gone.txt"); f.Status != "deleted" || f.Deletions != 1 {
		t.Errorf("gone.txt: %+v", f)
	}
	if f, _ := fileByPath(d.Comparison, "new.txt"); f.Status != "added" || f.Additions != 2 {
		t.Errorf("new.txt: %+v", f)
	}
	if f, ok := fileByPath(d.Comparison, "new_name.go"); !ok || f.Status != "renamed" || f.OldPath != "old_name.go" || f.Similarity < 50 || f.Additions != 1 || f.Deletions != 1 {
		t.Errorf("rename: %+v", f)
	}
	if f, _ := fileByPath(d.Comparison, "logo.bin"); !f.Binary || f.Status != "added" {
		t.Errorf("binary: %+v", f)
	}
	if d.Additions != 2+1+2+2+1+0-0 && d.Additions < 5 {
		t.Errorf("totals: %+d", d.Additions)
	}
	// The unified patch of one file, and of a renamed file by either name.
	pt, err := w.svc.PatchOf(w.ctx, repoID, p.MergeBase, p.HeadSHA, []string{"keep.txt"}, false, DefaultLimits)
	if err != nil || !strings.Contains(pt.Diff, "+2 changed") || !strings.Contains(pt.Diff, "-2\n") || pt.Truncated || pt.Held {
		t.Fatalf("%+v %v", pt, err)
	}
	pt, err = w.svc.PatchOf(w.ctx, repoID, p.MergeBase, p.HeadSHA, []string{"old_name.go", "new_name.go"}, false, DefaultLimits)
	if err != nil || !strings.Contains(pt.Diff, "rename from old_name.go") || !strings.Contains(pt.Diff, "+func C() int { return 4 }") {
		t.Fatalf("%+v %v", pt, err)
	}
	if pt, err = w.svc.PatchOf(w.ctx, repoID, p.MergeBase, p.HeadSHA, []string{"logo.bin"}, false, DefaultLimits); err != nil || !pt.Binary || pt.Diff != "" {
		t.Fatalf("binary patch: %+v %v", pt, err)
	}
	all, err := w.svc.PatchOf(w.ctx, repoID, p.MergeBase, p.HeadSHA, nil, false, DefaultLimits)
	if err != nil || !strings.Contains(all.Diff, "diff --git a/keep.txt b/keep.txt") || !strings.Contains(all.Diff, "new.txt") {
		t.Fatalf("%+v %v", all, err)
	}
	// Commits of the pull request.
	cs, err := w.svc.Commits(w.ctx, repoID, p)
	if err != nil || len(cs) != 1 || cs[0].Subject != "work" || cs[0].Author != "Dana" || len(cs[0].Parents) != 1 {
		t.Fatalf("%+v %v", cs, err)
	}
}

func TestLargeDiffsAreHeldBackUntilAskedFor(t *testing.T) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{"big.txt": "start\n", "small.txt": "s\n"}, "base")
	var big strings.Builder
	for i := 0; i < 400; i++ {
		big.WriteString("line of text number that is long enough to count toward the byte limit\n")
	}
	w.commit("feature", "main", map[string]string{"big.txt": big.String(), "small.txt": "s2\n"}, "grow")
	p := w.open("ada", New{Title: "Grow", SourceBranch: "feature", TargetBranch: "main"})
	lim := Limits{MaxFiles: 1, MaxPatchBytes: 4 << 10, MaxDiffBytes: 2 << 10, MaxPatchLines: 100, HardBytes: 8 << 10}
	d, err := w.svc.Files(w.ctx, repoID, p, lim)
	if err != nil || d.Total != 2 || len(d.Files) != 1 || !d.Truncated {
		t.Fatalf("the list is cut at the file limit: %+v %v", d, err)
	}
	d, _ = w.svc.Files(w.ctx, repoID, p, Limits{MaxFiles: 10, MaxPatchLines: 100})
	if f, _ := fileByPath(d.Comparison, "big.txt"); !f.TooLarge {
		t.Fatalf("big.txt is not marked too large: %+v", f)
	}
	if f, _ := fileByPath(d.Comparison, "small.txt"); f.TooLarge {
		t.Fatalf("small.txt marked too large")
	}
	held, err := w.svc.PatchOf(w.ctx, repoID, p.MergeBase, p.HeadSHA, []string{"big.txt"}, false, lim)
	if err != nil || !held.Held || held.Diff != "" {
		t.Fatalf("not held: %+v %v", held, err)
	}
	loaded, err := w.svc.PatchOf(w.ctx, repoID, p.MergeBase, p.HeadSHA, []string{"big.txt"}, true, lim)
	if err != nil || loaded.Held || !strings.Contains(loaded.Diff, "+line of text") {
		t.Fatalf("not loaded: %+v %v", loaded, err)
	}
	if !loaded.Truncated || loaded.Bytes > lim.HardBytes {
		t.Fatalf("the hard ceiling did not hold: truncated %v, %d bytes", loaded.Truncated, loaded.Bytes)
	}
	// The unified patch of everything is bounded and says so.
	all, err := w.svc.PatchOf(w.ctx, repoID, p.MergeBase, p.HeadSHA, nil, false, lim)
	if err != nil || !all.Truncated || all.Bytes > lim.MaxDiffBytes {
		t.Fatalf("%v %d %v", all.Truncated, all.Bytes, err)
	}
}

func TestChangesSinceAReviewedRevision(t *testing.T) {
	w, _, head1 := featureWorld(t)
	w.open("ada", New{Title: "Add b", SourceBranch: "feature", TargetBranch: "main"})
	head2 := w.commit("feature", "", map[string]string{"c.txt": "later\n", "b.txt": "new file\nmore\n"}, "more")
	w.push("ada", "feature", head1, head2)
	p := w.get(1)
	s, err := w.svc.ChangesSince(w.ctx, repoID, p, 1, DefaultLimits)
	if err != nil || s.Total != 2 || s.Rebased {
		t.Fatalf("%+v %v", s, err)
	}
	if f, _ := fileByPath(s.Comparison, "c.txt"); f.Status != "added" {
		t.Errorf("%+v", f)
	}
	if _, err := w.svc.ChangesSince(w.ctx, repoID, p, 9, DefaultLimits); err == nil {
		t.Error("an unknown revision was accepted")
	}
	// Before the pull request exists: the compare page.
	bc, err := w.svc.CompareBranches(w.ctx, repoID, "main", "feature", DefaultLimits)
	if err != nil || bc.Ahead != 2 || bc.Behind != 0 || len(bc.Commits) != 2 || bc.Files.Total != 2 || bc.Mergeable.State != MergeClean || bc.Existing != 1 {
		t.Fatalf("%+v %v", bc, err)
	}
	if _, err := w.svc.Resolve(w.ctx, repoID, "HEAD~1"); err == nil {
		t.Error("a revision expression was accepted")
	}
	if _, err := w.svc.Resolve(w.ctx, repoID, "--output=/tmp/x"); err == nil {
		t.Error("an option was accepted")
	}
}
