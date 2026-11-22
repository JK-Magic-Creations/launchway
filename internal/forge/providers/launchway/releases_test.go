package launchway

import (
	"context"
	"errors"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type tagStub struct {
	got forge.TagSpec
	err error
}

func (s *tagStub) CreateTag(_ context.Context, _ forge.Repository, t forge.TagSpec) (string, error) {
	s.got = t
	return "obj123", s.err
}

// The built-in provider's Releases group writes tags through the server's writer and says plainly what it does for a release.
func TestReleasesGroupWritesTagsThroughTheServersWriter(t *testing.T) {
	p := New(forge.Connection{}, nil, nil, Options{})
	ctx := context.Background()
	var unsup *forge.ErrUnsupported
	if _, err := p.Releases().CreateTag(ctx, forge.Repository{}, forge.TagSpec{Name: "v1"}); !errors.As(err, &unsup) {
		t.Fatalf("a provider with no tag writer made a tag: %v", err)
	}
	if p.Capabilities()[forge.CapTags].Support != forge.None {
		t.Fatalf("tags declared before they can be written: %+v", p.Capabilities()[forge.CapTags])
	}
	w := &tagStub{}
	p.SetTags(w)
	ref, err := p.Releases().CreateTag(ctx, forge.Repository{}, forge.TagSpec{Name: "v1.2.0", Commit: "abc", Message: "Release 1.2.0"})
	if err != nil || ref.Commit != "obj123" || ref.Name != "refs/tags/v1.2.0" || w.got.Commit != "abc" {
		t.Fatalf("ref %+v err %v got %+v", ref, err, w.got)
	}
	if link, err := p.Releases().PublishRelease(ctx, forge.Repository{}, forge.ReleaseSpec{Tag: "v1.2.0"}); err != nil || link != "" {
		t.Fatalf("publish: %q %v", link, err)
	}
	if c := p.Capabilities(); c[forge.CapTags].Support != forge.Native || c[forge.CapReleases].Support != forge.Emulated || c[forge.CapReleases].Reason == "" {
		t.Fatalf("capabilities %+v", c)
	}
	w.err = errors.New("the branch rules refuse it")
	if _, err := p.Releases().CreateTag(ctx, forge.Repository{}, forge.TagSpec{Name: "v2"}); err == nil {
		t.Fatal("a refusal was swallowed")
	}
	// Writing a file and the pull-request note are not part of the built-in group.
	if _, err := p.Releases().CommitFile(ctx, forge.Repository{}, forge.FileChange{}); !errors.As(err, &unsup) {
		t.Fatalf("commit file: %v", err)
	}
}
