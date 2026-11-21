package api

import (
	"testing"
	"time"
)

// Without sign-in everyone is the same user, so a starter may approve; the
// page reads this to say exactly why a person is refused when it is no.
func TestApprovalSettingsOverHTTP(t *testing.T) {
	e := setup(t, 5*time.Second)
	var got ApprovalSettings
	getJSON(t, e.srv.URL+"/api/v1/settings/approvals", &got)
	if got.SignIn {
		t.Errorf("sign-in should be off in this setup: %+v", got)
	}
}
