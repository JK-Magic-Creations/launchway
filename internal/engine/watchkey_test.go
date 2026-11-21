package engine

import "testing"

// A step of a template has slashes in its id; the watch key still splits
// into its five parts, so a deploy runner's observation of it is taken.
func TestWatchKeyWithSlashesInTheStepID(t *testing.T) {
	for _, step := range []string{"deploy", "ci/deploy", "ci/deliver[environment=dev]/deploy"} {
		key := watchKey("dep_1", "run_1", step, 2, "abc123")
		got := splitWatchKey(key)
		want := []string{"dep_1", "run_1", step, "2", "abc123"}
		if len(got) != 5 {
			t.Fatalf("%q split into %v", key, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q part %d is %q, want %q", key, i, got[i], want[i])
			}
		}
	}
}
