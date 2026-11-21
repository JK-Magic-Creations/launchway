package pulls

import "testing"

// A step repeated by for_each is a group of checks: requiring the step
// requires every member, and a member can be required alone.
func TestRequiredCheckTreatsAMatrixAsOne(t *testing.T) {
	checks := []Check{
		{Name: "ci", Status: CheckFailed},
		{Name: "ci/test[go=1.26]", Status: CheckPassed},
		{Name: "ci/test[go=1.27]", Status: CheckFailed},
		{Name: "ci/lint", Status: CheckPassed},
	}
	for _, tc := range []struct {
		required string
		want     string
	}{
		{"ci/test", CheckFailed},          // the group: its worst member
		{"test", CheckFailed},             // a bare name matches the step of that name
		{"ci/test[go=1.26]", CheckPassed}, // a member named on its own
		{"ci/test[go=1.27]", CheckFailed},
	} {
		got, ok := findCheck(checks, tc.required)
		if !ok || got.Status != tc.want {
			t.Errorf("%s: %+v %v, want %s", tc.required, got, ok, tc.want)
		}
		if !isRequired([]string{tc.required}, "ci/test[go=1.26]") && tc.required != "ci/test[go=1.27]" {
			t.Errorf("%s should require ci/test[go=1.26]", tc.required)
		}
	}
	if isRequired([]string{"ci/test[go=1.26]"}, "ci/test[go=1.27]") {
		t.Error("a member named on its own must not require its siblings")
	}
}
