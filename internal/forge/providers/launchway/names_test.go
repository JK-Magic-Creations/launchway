package launchway

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

func TestHostileNamesAreRefused(t *testing.T) {
	bad := []string{"", ".", "..", "../x", "a/../b", "a/b", "/abs", "x.git", "X.GIT", "x.lock", "x.", ".hidden", "-lead", "a..b",
		"na\x00me", "tab\tname", "new\nline", "sp ace", "back\\slash", "ünï", "а-cyrillic", "name\u202eexe", "emoji😀", "a:b", "a*b",
		strings.Repeat("a", 5000), strings.Repeat("a", MaxNameLength+1), "con", "NUL", "API", "git", "Admin", "refs", "projects", "Runs", "tokens"}
	for _, n := range bad {
		if err := ValidSegment(n); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%q was accepted (%v)", n, err)
		}
	}
	for _, n := range []string{"a", "demo", "platform-gitops", "My_Repo.v2", "x1", strings.Repeat("a", MaxNameLength)} {
		if err := ValidSegment(n); err != nil {
			t.Errorf("%q refused: %v", n, err)
		}
	}
	for _, p := range []string{"owner", "a/b/c", "../b", "a/..", "a/b.git", ""} {
		if _, _, err := SplitPath(p); err == nil {
			t.Errorf("path %q was accepted", p)
		}
	}
	if o, n, err := SplitPath("jkhunter/demo"); err != nil || o != "jkhunter" || n != "demo" {
		t.Errorf("%s %s %v", o, n, err)
	}
}
