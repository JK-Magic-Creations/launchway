package pulls

import (
	"slices"
	"strings"
	"testing"
)

func TestOwnersParsing(t *testing.T) {
	doc := ParseOwners("\xef\xbb\xbf# who owns what\n* @ada\n/docs/ @bob @group:writers  # trailing comment\n*.go @cy\n/internal/**/gen.go\n\n")
	if len(doc.Problems) != 0 {
		t.Fatalf("%+v", doc.Problems)
	}
	cases := []struct {
		file string
		want []string
	}{
		{"README.md", []string{"ada"}},
		{"docs/a/b.md", []string{"bob", "group:writers"}},
		{"sub/docs/a.md", []string{"ada"}}, // /docs/ is anchored to the root
		{"main.go", []string{"cy"}},
		{"deep/er/x.go", []string{"cy"}},
		{"internal/gen.go", nil}, // a pattern with no owners makes it unowned again
		{"internal/a/b/gen.go", nil},
		{"internal/a/b/other.go", []string{"cy"}},
	}
	for _, c := range cases {
		if got := doc.OwnersOf(c.file); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.file, got, c.want)
		}
	}
}

func TestOwnersLastMatchWinsAndFolderAnywhere(t *testing.T) {
	doc := ParseOwners("docs/ @a\n/docs/private/ @b\nvendor/ @c\n")
	for file, want := range map[string]string{"docs/x.md": "a", "docs/private/x.md": "b", "pkg/docs/x.md": "a", "a/vendor/x/y.go": "c"} {
		got := ""
		if o := doc.OwnersOf(file); len(o) > 0 {
			got = o[0]
		}
		if got != want {
			t.Errorf("%s: %q, want %q", file, got, want)
		}
	}
}

func TestOwnersProblemsAreSaidPlainlyWithTheirLine(t *testing.T) {
	doc := ParseOwners("good.txt @ada\n!neg @ada\nbad[1] @ada\nnoat.txt ada\nempty @\n../x @ada\nspace.txt @a,b\n" + strings.Repeat("x", 301) + " @a\n")
	lines := map[int]bool{}
	for _, p := range doc.Problems {
		lines[p.Line] = true
		if p.Message == "" || strings.Contains(p.Message, "invalid pull request") {
			t.Errorf("message %q", p.Message)
		}
	}
	for _, n := range []int{2, 3, 4, 5, 6, 7, 8} {
		if !lines[n] {
			t.Errorf("line %d was not reported: %+v", n, doc.Problems)
		}
	}
	if lines[1] || len(doc.Rules) != 1 {
		t.Errorf("the good line counts: %+v", doc.Rules)
	}
	err := doc.Valid()
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "and ") {
		t.Errorf("Valid: %v", err)
	}
	if got := ParseOwners(strings.Repeat("a", maxOwnersBytes+1)); len(got.Problems) != 1 {
		t.Errorf("a file over the limit: %+v", got)
	}
	if err := ParseOwners("").Valid(); err != nil {
		t.Errorf("an empty file is valid: %v", err)
	}
}

func TestMatchPath(t *testing.T) {
	for _, c := range []struct {
		pat, file string
		want      bool
	}{
		{"*.md", "a/b/c.md", true}, {"*.md", "a/b/c.mdx", false}, {"/cmd/**", "cmd/x/y.go", true}, {"/cmd/**", "other/cmd/x.go", false},
		{"docs/", "docs", false}, {"docs/", "docs/a", true}, {"src/*/gen", "src/a/gen", true}, {"src/*/gen", "src/a/b/gen", false},
		{"**/gen.go", "gen.go", true}, {"a?c", "abc", true}, {"a?c", "a/c", false},
	} {
		if got := MatchPath(c.pat, c.file); got != c.want {
			t.Errorf("%s vs %s: %v", c.pat, c.file, got)
		}
	}
}
