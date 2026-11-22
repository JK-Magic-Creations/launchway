package engine

import "testing"

// A change on a branch is compared with the last run of another branch, not only with the run before it on its own.
func TestCompareTestsWithAnotherBranchsRun(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	base := create(t, e, reportsLinear, Trigger{Event: "push", Ref: "refs/heads/main"})
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "fail", "pass"))
	change := create(t, e, reportsLinear, Trigger{Event: "push", Ref: "refs/heads/feature"})
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "fail", "fail", "pass"))
	own, err := e.CompareTests(change)
	if err != nil || own.Previous != nil {
		t.Fatalf("a first run on its own ref has nothing to compare with: %+v %v", own, err)
	}
	cmp, err := e.CompareTestsWith(change, base)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Previous == nil || cmp.Previous.ID != base || cmp.Counts["new_failures"] != 1 || cmp.NewFailures[0].Name != "t0" || cmp.Counts["still_failing"] != 1 {
		t.Fatalf("comparison with the base run: %+v", cmp)
	}
	// A base run that kept no results compares with nothing.
	bare := create(t, e, "pipeline: bare\nsteps:\n  a: {image: x, run: a}\n", Trigger{Event: "push", Ref: "refs/heads/main"})
	if cmp, err := e.CompareTestsWith(change, bare); err != nil || cmp.Previous != nil || len(cmp.NewFailures) != 0 {
		t.Fatalf("base without results: %+v %v", cmp, err)
	}
	if _, err := e.CompareTestsWith(change, "no-such-run"); err == nil {
		t.Fatal("an unknown base run was accepted")
	}
}
