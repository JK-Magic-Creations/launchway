package policy

import "testing"

// The hash names what policies say, not where they were written: the file's
// line numbers differ from the database's, which has none.
func TestHashIgnoresWhereAPolicyWasWritten(t *testing.T) {
	a := []Policy{{Name: "p", Mode: "enforce", Source: "config", Rules: Rules{MaxTimeout: "1h"}, File: "launchway.yaml", Line: 41}}
	b := []Policy{{Name: "p", Mode: "enforce", Source: "config", Rules: Rules{MaxTimeout: "1h"}}}
	if Hash(a) != Hash(b) {
		t.Errorf("the same policy hashes differently by where it was read: %s and %s", Hash(a), Hash(b))
	}
	b[0].Rules.MaxTimeout = "2h"
	if Hash(a) == Hash(b) {
		t.Error("different policies hash the same")
	}
	if a[0].Line != 41 {
		t.Error("Hash changed its argument")
	}
}
