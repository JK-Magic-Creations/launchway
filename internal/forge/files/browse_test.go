package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func put(t *testing.T, s *Service, p, content, expect, msg string) {
	t.Helper()
	if _, err := s.Put(context.Background(), repoID, Write{Branch: "main", Path: p, Content: []byte(content), Message: msg, ExpectedSHA: expect, Actor: "ada"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestBrowse(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"README.md": "hello\n", "src/a.go": "package a\n// TODO one\n", "src/b.txt": "b\n"})
	put(t, s, "src/c.go", "package c\nfunc Needle() {}\n", "", "add c")
	a, _ := s.Read(ctx, repoID, "main", "src/a.go")
	put(t, s, "src/a.go", "package a\n// TODO two\nline3\n", a.SHA, "change a")
	put(t, s, "docs/x.md", "x\n", "", "add docs")

	t.Run("last commit per entry", func(t *testing.T) {
		it, err := s.ReadWithLast(ctx, repoID, "main", "")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, e := range it.Entries {
			if e.Last == nil {
				t.Fatalf("%s has no last commit", e.Name)
			}
			got[e.Name] = e.Last.Subject
		}
		if got["README.md"] != "first" || got["src"] != "change a" || got["docs"] != "add docs" {
			t.Fatalf("last commits: %v", got)
		}
		if it.LastBounded {
			t.Fatal("a short history is not bounded")
		}
		sub, _ := s.ReadWithLast(ctx, repoID, "main", "src")
		for _, e := range sub.Entries {
			want := map[string]string{"a.go": "change a", "b.txt": "first", "c.go": "add c"}[e.Name]
			if e.Last == nil || e.Last.Subject != want {
				t.Fatalf("src/%s: %+v want %s", e.Name, e.Last, want)
			}
		}
	})

	t.Run("refs, branch create and delete", func(t *testing.T) {
		made, err := s.CreateBranch(ctx, repoID, "feature/x", "main", nil)
		if err != nil || len(made) != 40 {
			t.Fatalf("%q %v", made, err)
		}
		if _, err := s.CreateBranch(ctx, repoID, "feature/x", "main", nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		if _, err := s.CreateBranch(ctx, repoID, "feature", "main", nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("clash: %v", err)
		}
		if _, err := s.CreateBranch(ctx, repoID, "-bad", "main", nil); err == nil {
			t.Fatal("option-like name accepted")
		}
		items, total, err := s.Refs(ctx, repoID, "branch", "", "main", 10)
		if err != nil || total != 2 || len(items) != 2 {
			t.Fatalf("%v %d %v", items, total, err)
		}
		if items[0].Ahead != 0 || items[0].Behind != 0 || items[0].Subject == "" {
			t.Fatalf("counts: %+v", items[0])
		}
		f, _, _ := s.Refs(ctx, repoID, "branch", "FEAT", "main", 10)
		if len(f) != 1 || f[0].Name != "feature/x" {
			t.Fatalf("filter: %v", f)
		}
		was, err := s.DeleteBranch(ctx, repoID, "feature/x", nil)
		if err != nil || was != made {
			t.Fatalf("delete: %q %v", was, err)
		}
		if _, err := s.DeleteBranch(ctx, repoID, "feature/x", nil); err == nil {
			t.Fatal("deleted twice")
		}
		deny := func(ref, old, next string) error { return errors.New("protected") }
		if _, err := s.DeleteBranch(ctx, repoID, "main", deny); err == nil {
			t.Fatal("policy ignored")
		}
	})

	t.Run("blame", func(t *testing.T) {
		b, err := s.BlameOf(ctx, repoID, "main", "src/a.go", 0, 0)
		if err != nil || b.Total != 3 || len(b.Lines) != 3 {
			t.Fatalf("%+v %v", b, err)
		}
		if b.Commits[b.Lines[0].SHA].Subject != "first" || b.Commits[b.Lines[1].SHA].Subject != "change a" || b.Lines[2].Text != "line3" {
			t.Fatalf("blame: %+v", b)
		}
		if b.Commits[b.Lines[1].SHA].Previous == "" {
			t.Fatal("no previous revision for a changed line")
		}
		r, _ := s.BlameOf(ctx, repoID, "main", "src/a.go", 2, 2)
		if len(r.Lines) != 1 || r.Lines[0].N != 2 || r.Next != 3 {
			t.Fatalf("range: %+v", r)
		}
		if _, err := s.BlameOf(ctx, repoID, "main", "nope", 0, 0); err == nil {
			t.Fatal("blame of nothing")
		}
	})

	t.Run("search", func(t *testing.T) {
		r, err := s.Search(ctx, repoID, "main", SearchQuery{Pattern: "needle"})
		if err != nil || r.Matches != 1 || r.Files[0].Path != "src/c.go" || r.Files[0].Matches[0].Line != 2 || len(r.Files[0].Matches[0].Ranges) != 1 {
			t.Fatalf("%+v %v", r, err)
		}
		r, _ = s.Search(ctx, repoID, "main", SearchQuery{Pattern: "needle", Case: true})
		if r.Matches != 0 {
			t.Fatal("case-sensitive matched")
		}
		r, _ = s.Search(ctx, repoID, "main", SearchQuery{Pattern: "TODO (one|two)", Regex: true, Exts: []string{"go"}})
		if r.Matches != 1 {
			t.Fatalf("regex: %+v", r)
		}
		r, _ = s.Search(ctx, repoID, "main", SearchQuery{Pattern: "package", Dir: "src", Exts: []string{"txt"}})
		if r.Matches != 0 {
			t.Fatalf("filters: %+v", r)
		}
		r, _ = s.Search(ctx, repoID, "main", SearchQuery{Pattern: "c.go", In: "path"})
		if len(r.Files) != 1 || !r.Files[0].PathMatch {
			t.Fatalf("path: %+v", r)
		}
		if _, err := s.Search(ctx, repoID, "main", SearchQuery{Pattern: "(", Regex: true}); err == nil {
			t.Fatal("bad regex accepted")
		}
		if _, err := s.Search(ctx, repoID, "main", SearchQuery{Pattern: "x", Dir: "../etc"}); err == nil {
			t.Fatal("path escape accepted")
		}
		if _, err := s.Search(ctx, repoID, "main", SearchQuery{Pattern: "x", Exts: []string{"a:b"}}); err == nil {
			t.Fatal("pathspec magic accepted")
		}
		r, _ = s.Search(ctx, repoID, "main", SearchQuery{Pattern: "-e", Regex: false})
		_ = r // an option-like pattern is only a pattern
	})

	t.Run("delete a file", func(t *testing.T) {
		x, _ := s.Read(ctx, repoID, "main", "docs/x.md")
		if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "docs/x.md", Message: "rm", Delete: true}, nil); err == nil {
			t.Fatal("deleted without expected_sha")
		}
		if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "docs/x.md", Message: "rm", Delete: true, ExpectedSHA: strings.Repeat("a", 40)}, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("a stale delete: %v", err)
		}
		if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "docs/x.md", Message: "remove x", Delete: true, ExpectedSHA: x.SHA}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Read(ctx, repoID, "main", "docs/x.md"); err == nil {
			t.Fatal("still there")
		}
		if _, err := s.Read(ctx, repoID, "main", "docs"); err == nil {
			t.Fatal("the empty folder is still there")
		}
		put(t, s, "docs/x.md", "x\n", "", "add docs")
	})

	t.Run("raw and commit detail", func(t *testing.T) {
		var buf bytes.Buffer
		var gotSize int64
		err := s.Raw(ctx, repoID, "main", "README.md", func(size int64, mode string) io.Writer { gotSize = size; return &buf })
		if err != nil || buf.String() != "hello\n" || gotSize != 6 {
			t.Fatalf("%q %d %v", buf.String(), gotSize, err)
		}
		if err := s.Raw(ctx, repoID, "main", "src", func(int64, string) io.Writer { return io.Discard }); err == nil {
			t.Fatal("raw of a directory")
		}
		cs, _ := s.Commits(ctx, repoID, "main", "", 10)
		d, err := s.CommitDetail(ctx, repoID, cs[0].SHA)
		if err != nil || d.Subject != "add docs" || len(d.ParentSHAs) != 1 || !strings.HasPrefix(d.Message, "add docs") {
			t.Fatalf("%+v %v", d, err)
		}
		root := cs[len(cs)-1]
		if len(root.ParentSHAs) != 0 {
			t.Fatal("root has parents")
		}
	})
}
