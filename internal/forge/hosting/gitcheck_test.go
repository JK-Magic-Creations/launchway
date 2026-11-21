package hosting

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// A git below the minimum is refused with a plain message that names the minimum,
// the version found, and says that nothing else is affected.
func TestGitTooOldNamesTheMinimum(t *testing.T) {
	v, err := gitexec.ParseVersion("git version 2.20.1")
	if err != nil {
		t.Fatal(err)
	}
	msg := GitTooOld(v).Error()
	for _, want := range []string{"needs git " + MinGitText() + " or newer", "git 2.20.1", "everything else works"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not say %q", msg, want)
		}
	}
	if v.AtLeast(MinGit[0], MinGit[1]) {
		t.Errorf("2.20 passes the minimum %s", MinGitText())
	}
}

func TestMinGitIsInTheFeatureTable(t *testing.T) {
	// The minimum can never be below what hosting's features need.
	for _, f := range gitexec.Features {
		if MinGit[0] < f.Major || (MinGit[0] == f.Major && MinGit[1] < f.Minor) {
			t.Errorf("the minimum %s is older than %s needs (%d.%d)", MinGitText(), f.Name, f.Major, f.Minor)
		}
	}
}

// The git on the machine running the tests is at least the minimum (the
// pipeline's image and every developer's machine are newer); the check says so.
func TestCheckGitOnThisMachine(t *testing.T) {
	v, err := CheckGit(context.Background())
	if err != nil {
		t.Skipf("this machine's git is below the minimum: %v", err)
	}
	if v.Raw == "" {
		t.Error("no version was returned")
	}
}
