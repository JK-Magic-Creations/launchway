package gitexec

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestValidRefAgreesWithGit(t *testing.T) {
	names := []string{
		"main", "feature/x", "a/b/c", "v1.2.3", "release-1.0", "with space", "tab\there", "a..b", "a.lock", "a/b.lock/c",
		".hidden", "a/.hidden", "end.", "end/", "/start", "a//b", "a@{b", "a@b", "@", "x~1", "x^", "x:y", "x?", "x*", "x[", `x\y`,
		"ünï/cödé", "emoji-😀", "ctrl\x01", "del\x7f", "a/b/", "..", ".", "a.", "lock", ".lock", "a/.lock", "HEAD", "-dash",
		"dash-inside", "x/-y", "a b/c", "{curly}", "a@{0}", "a/@",
	}
	for _, n := range names {
		if strings.HasPrefix(n, "-") {
			if err := ValidRef(n); !errors.Is(err, ErrInvalidName) {
				t.Errorf("%q must be refused (leading '-')", n)
			}
			continue
		}
		full := "refs/heads/" + n
		want := exec.Command("git", "check-ref-format", full).Run() == nil
		got := ValidRef(full) == nil
		if got != want {
			t.Errorf("ValidRef(%q) = %v, git says %v", full, got, want)
		}
	}
	for _, n := range []string{"", "-x", "refs/heads/-x"[:0]} {
		if err := ValidRef(n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestBranchSHALiteral(t *testing.T) {
	if err := ValidBranch("main"); err != nil {
		t.Error(err)
	}
	for _, b := range []string{"refs/heads/main", "-x", "a..b", ""} {
		if ValidBranch(b) == nil {
			t.Errorf("branch %q accepted", b)
		}
	}
	if !ValidSHA(strings.Repeat("a", 40)) || !ValidSHA(strings.Repeat("0", 64)) {
		t.Error("good shas refused")
	}
	for _, s := range []string{"", "abc", strings.Repeat("A", 40), strings.Repeat("g", 40), strings.Repeat("a", 41)} {
		if ValidSHA(s) {
			t.Errorf("sha %q accepted", s)
		}
	}
	if p, err := Literal("*.go"); err != nil || p != ":(literal)*.go" {
		t.Errorf("Literal: %q %v", p, err)
	}
	if _, err := Literal(""); err == nil {
		t.Error("empty literal")
	}
	if _, err := Literal("a\x00b"); err == nil {
		t.Error("NUL literal")
	}
}
