package release

import "testing"

func TestPullRequestCountsItsCommitsMarkers(t *testing.T) {
	feature := Change{Kind: "pull", Ref: 1, Title: "Add the export", Parts: []string{"wip", "feat: export to csv"}}
	if sec, _ := Classify(feature); sec != SecFeature {
		t.Fatalf("a pull request built from a feat: commit is a feature, got %s", sec)
	}
	if p := Propose(SchemeSemver, "1.2.0", []Change{feature}); p.Next != "1.3.0" {
		t.Fatalf("proposed %q, want 1.3.0", p.Next)
	}
	// The pull request's own marker outranks its commits: a label, then a title in the convention.
	if sec, _ := Classify(Change{Title: "Tidy", Labels: []string{"bug"}, Parts: []string{"feat: x"}}); sec != SecFix {
		t.Fatalf("a label outranks the commits: %s", sec)
	}
	if sec, _ := Classify(Change{Title: "fix: crash", Parts: []string{"feat: x"}}); sec != SecFix {
		t.Fatalf("the title outranks the commits: %s", sec)
	}
	if sec, _ := Classify(Change{Title: "Tidy", Parts: []string{"fix!: drop the old flag"}}); sec != SecBreaking {
		t.Fatalf("a breaking commit makes the pull request breaking: %s", sec)
	}
	if sec, _ := Classify(Change{Title: "Tidy", Parts: []string{"wip"}}); sec != SecOther {
		t.Fatalf("no marker anywhere: %s", sec)
	}
}
