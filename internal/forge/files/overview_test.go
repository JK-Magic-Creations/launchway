package files

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLanguagesCountByShareAndLeaveOutVendoredAndGenerated(t *testing.T) {
	s, st := newService(t)
	seed(t, s, st, "main", map[string]string{
		"main.go":                     strings.Repeat("a", 600),
		"web/app.ts":                  strings.Repeat("b", 300),
		"README.md":                   strings.Repeat("c", 100),
		"vendor/lib/lib.go":           strings.Repeat("d", 5000),
		"web/node_modules/x/index.js": strings.Repeat("e", 5000),
		"api/api.pb.go":               strings.Repeat("f", 5000),
		"web/dist/app.min.js":         strings.Repeat("g", 5000),
		"data.bin":                    "zz",
		"docs/legacy/old.go":          strings.Repeat("h", 100),
		".launchway/languages":        "docs/legacy/ exclude\n*.bin language=Binary\nbad line here now\n",
	})
	rep, err := s.Languages(context.Background(), repoID, "main")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Language{}
	for _, l := range rep.Languages {
		got[l.Name] = l
	}
	if rep.Rules != LanguagesFile || got["Go"].Bytes != 600 || got["TypeScript"].Bytes != 300 || got["Markdown"].Bytes != 100 || got["Binary"].Bytes != 2 {
		t.Fatalf("%+v", rep)
	}
	if _, ok := got["JavaScript"]; ok {
		t.Fatalf("a vendored or generated file counted: %+v", rep)
	}
	if rep.Excluded != 5 || rep.TotalBytes != 1002 || rep.Languages[0].Name != "Go" || rep.Languages[0].Share != 59.88 || rep.Truncated {
		t.Fatalf("totals: %+v", rep)
	}
	if _, bad := ParseLanguageRules("a exclude\nb nonsense\nc"); len(bad) != 2 {
		t.Fatalf("lines that cannot be read: %v", bad)
	}
}

func TestContributorsCountPerAuthorAndSayAtLeastWhenTheWalkIsCut(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"a.txt": "1\n"})
	for i, who := range []string{"ada", "bob", "ada"} {
		cur, err := s.Read(ctx, repoID, "main", "a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "a.txt", Content: []byte(strings.Repeat("x", i+2)), Message: "m", ExpectedSHA: cur.SHA, Actor: who}, nil); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.Contributors(ctx, repoID, "main", time.Time{}, 0)
	if err != nil || rep.AtLeast || rep.Walked != 4 || len(rep.Contributors) != 3 {
		t.Fatalf("%+v %v", rep, err)
	}
	if c := rep.Contributors[0]; !strings.HasPrefix(c.Email, "ada@") || c.Commits != 2 {
		t.Fatalf("the most commits first: %+v", rep.Contributors)
	}
	cut, err := s.Contributors(ctx, repoID, "main", time.Time{}, 2)
	if err != nil || !cut.AtLeast || cut.Walked != 2 {
		t.Fatalf("a walk cut at the cap says at least: %+v %v", cut, err)
	}
	none, err := s.Contributors(ctx, repoID, "main", time.Unix(4_000_000_000, 0), 0)
	if err != nil || none.Walked != 0 || len(none.Contributors) != 0 {
		t.Fatalf("a period in the future: %+v %v", none, err)
	}
	exact, err := s.Contributors(ctx, repoID, "main", time.Time{}, 4)
	if err != nil || exact.AtLeast || exact.Walked != 4 {
		t.Fatalf("a walk that ends exactly at the cap is complete: %+v %v", exact, err)
	}
}
