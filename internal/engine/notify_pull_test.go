package engine

import "testing"

// The names the API sends for pull request events are the ones a notification rule may list.
func TestPullNoticeKindsAreNamesRulesMayUse(t *testing.T) {
	for _, k := range []string{"pull.review_requested", "pull.approved", "pull.changes_requested", "pull.mentioned", "pull.queue_failed"} {
		found := false
		for _, e := range notifyEventsKnown() {
			found = found || e == k
		}
		if !found {
			t.Errorf("%s is not an event a rule may name", k)
		}
	}
}
