package refrules

import (
	"strings"
	"testing"
)

const zero = "0000000000000000000000000000000000000000"

func TestCheck(t *testing.T) {
	pats := []string{"main", "release/*"}
	up := func(ref string) []Update { return []Update{{Name: ref, Old: "a", New: "b"}} }
	for _, c := range []struct {
		name    string
		updates []Update
		admin   bool
		want    string // "" means allowed
	}{
		{"an ordinary branch", up("refs/heads/topic"), false, ""},
		{"a tag", up("refs/tags/main"), false, ""},
		{"a protected branch, not an administrator", up("refs/heads/main"), false, "only an administrator"},
		{"a protected branch, an administrator", up("refs/heads/main"), true, ""},
		{"a pattern", up("refs/heads/release/1.2"), false, "release/1.2 is protected"},
		{"a pattern does not cross a slash", up("refs/heads/release/1/2"), false, ""},
		{"a delete is refused for an administrator too", []Update{{Name: "refs/heads/main", Old: "a", New: zero}}, true, "cannot be deleted"},
		{"one protected ref among several", append(up("refs/heads/topic"), up("refs/heads/main")...), false, "main is protected"},
	} {
		err := Check(pats, c.updates, c.admin)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	for _, bad := range []string{"", "refs/heads/main", "/main", "a b", "x[", "a?"} {
		if Validate(bad) == nil {
			t.Errorf("%q was accepted as a pattern", bad)
		}
	}
}
