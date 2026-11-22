package files

import (
	"context"
	"errors"
	"testing"
)

func TestCreateAnnotatedTag(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"README.md": "hello\n"})
	tip, err := s.CreateBranch(ctx, repoID, "probe", "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = tip
	obj, err := s.CreateAnnotatedTag(ctx, repoID, "v1.0.0", "main", "Release 1.0.0\n\nfirst", "Grace Hopper", nil)
	if err != nil || len(obj) != 40 {
		t.Fatalf("%q %v", obj, err)
	}
	if obj == tip {
		t.Fatal("an annotated tag is its own object, not the commit")
	}
	tags, _, err := s.Refs(ctx, repoID, "tag", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range tags {
		if x.Name == "v1.0.0" {
			found = true
			if !x.Annotated || x.Commit != tip || x.Tagger != "Grace Hopper" {
				t.Fatalf("tag: %+v (want annotated, at %s, by Grace Hopper)", x, tip)
			}
		}
	}
	if !found {
		t.Fatal("the tag is not listed")
	}
	if _, err := s.CreateAnnotatedTag(ctx, repoID, "v1.0.0", "main", "again", "x", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a tag that exists is never moved: %v", err)
	}
	if _, err := s.CreateAnnotatedTag(ctx, repoID, "bad name", "main", "m", "x", nil); err == nil {
		t.Fatal("a bad name is refused")
	}
}
