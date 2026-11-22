package pulls

import (
	"strings"
	"testing"
)

// The merge commit of a write-back pull request carries the pull request's Launchway trailers, once each, and a
// message that already holds them (a squash with {body}) is not given them twice.
func TestMergeMessageCarriesTheWriteBackTrailers(t *testing.T) {
	body := "Deploy shop 2.0\n\nLaunchway-Deployment: dep_1\nLaunchway-Flight: shop\nLaunchway-Run: run_9\nLaunchway-Environment: prod\n\nOpened by the deploy step."
	got := withLaunchwayTrailers("Merge pull request #7 from deploy/x\n\nDeploy shop 2.0\n", body)
	want := "Merge pull request #7 from deploy/x\n\nDeploy shop 2.0\n\nLaunchway-Deployment: dep_1\nLaunchway-Flight: shop\nLaunchway-Run: run_9\nLaunchway-Environment: prod\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if again := withLaunchwayTrailers(got, body); again != got {
		t.Fatalf("trailers were added twice: %q", again)
	}
	if got := withLaunchwayTrailers("Merge", "no trailers here"); got != "Merge" {
		t.Fatalf("%q", got)
	}
	if got := withLaunchwayTrailers("Merge", "Launchway-Run: run_9 and more"); !strings.HasSuffix(got, "Launchway-Run: run_9 and more\n") {
		t.Fatalf("%q", got)
	}
}
