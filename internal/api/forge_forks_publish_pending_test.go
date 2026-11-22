package api

import "testing"

// PENDING: enable when the artifact repository merges (it is on another branch and adds a publish credential minted
// for a run). A run of a pull request from a fork is restricted (engine.RestrictFork): it must get no credential
// that publishes to the artifact repository, whatever the fork's pipeline asks for, and an approved first
// contribution is still restricted (approval lets it run, it does not lift the restriction).
//
// When it merges: set mintPublishCredential to the call that makes a run's publish credential (in a test, the
// function the Station is handed it by), remove the Skip, and keep the assertions.
var mintPublishCredential func(t *testing.T, e *authEnv, runID string) (credential string, err error)

func TestRestrictedForkRunGetsNoPublishCredential(t *testing.T) {
	if mintPublishCredential == nil {
		t.Skip("pending: the artifact repository is not on this branch; enable this test when it merges")
	}
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	if r := f.vic.do("POST", f.acme+"/forks", `{}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Open a pull request from the fork whose pipeline asks to publish, run it (restricted), then ask for the
	// run's publish credential: none is made, with a reason that names the restriction.
	t.Fatal("write the journey: open the fork's pull request, take its run id, call mintPublishCredential, expect an error and no credential")
}
