package files

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// History brought from another server keeps its pull requests under refs/imported/pull/: they can be read, by
// that name, like any commit, and nothing can be written to them or made to look like a branch.
func TestImportedPullRefsAreReadableAndReadOnly(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"README.md": "hello\n"})
	main, err := s.Read(ctx, repoID, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Mutate(ctx, repoID, func(_ context.Context, _ map[string]string, _ string) ([]storage.RefUpdate, error) {
		return []storage.RefUpdate{{Name: "refs/imported/pull/7/head", New: main.Commit}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	const ref = "refs/imported/pull/7/head"
	f, err := s.Read(ctx, repoID, ref, "README.md")
	if err != nil || f.Content != "hello\n" {
		t.Fatalf("reading at an imported pull ref: %+v %v", f, err)
	}
	if cs, err := s.Commits(ctx, repoID, ref, "", 5); err != nil || len(cs) != 1 {
		t.Fatalf("the history at an imported pull ref: %v %v", cs, err)
	}
	if _, err := s.Put(ctx, repoID, Write{Branch: ref, Path: "x.txt", Content: []byte("x"), Message: "no", Actor: "ada"}, nil); err == nil {
		t.Fatal("a write to an imported pull ref was accepted")
	} else if !errors.Is(err, forge.ErrInvalid) && !strings.Contains(err.Error(), "refs/") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if _, err := s.CreateBranch(ctx, repoID, "refs/imported/pull/8/head", "main", nil); err == nil {
		t.Fatal("a branch was made inside refs/imported/")
	}
	items, _, err := s.Refs(ctx, repoID, "branch", "", "main", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range items {
		if strings.Contains(b.Name, "imported") {
			t.Errorf("an imported ref is listed as a branch: %s", b.Name)
		}
	}
}
