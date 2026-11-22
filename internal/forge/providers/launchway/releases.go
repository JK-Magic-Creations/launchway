package launchway

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// SetTags gives the provider the server's way of writing a tag into a
// repository stored here (forge.TagWriter), which turns on the releases.tags
// capability. Called once while the server is set up, before it serves.
func (p *Provider) SetTags(t forge.TagWriter) {
	p.tags.Store(&tagHolder{t})
	if t != nil {
		p.Caps[forge.CapTags] = forge.Declaration{Support: forge.Native}
		p.Caps[forge.CapReleases] = forge.Declaration{Support: forge.Emulated,
			Reason:     "a release of a repository stored here is Launchway's own record of it, with its notes and files",
			Workaround: "Nothing to do: the release is published on its own page and download address"}
	}
}

type tagHolder struct{ t forge.TagWriter }

func (p *Provider) tagWriter() forge.TagWriter {
	if h := p.tags.Load(); h != nil {
		return h.t
	}
	return nil
}

// Releases is the write-back group. For a repository stored here, tags are
// written by the server (SetTags) and the release itself is Launchway's own
// record, so publishing makes nothing elsewhere; committing a file and the
// pull-request summary comment are not part of it (the server writes files
// itself, and a pull request here carries its own checks).
func (p *Provider) Releases() forge.Releases { return releaseGroup{p: p, Releases: p.Base.Releases()} }

type releaseGroup struct {
	forge.Releases
	p *Provider
}

// CreateTag asks the server's writer for the annotated tag.
func (g releaseGroup) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	w := g.p.tagWriter()
	if w == nil {
		return g.Releases.CreateTag(ctx, repo, t)
	}
	obj, err := w.CreateTag(ctx, repo, t)
	if err != nil {
		return forge.Ref{}, err
	}
	return forge.Ref{Name: "refs/tags/" + t.Name, Commit: obj}, nil
}

// PublishRelease has nothing to make: the release of a repository stored here
// is Launchway's own record. It answers with no address.
func (g releaseGroup) PublishRelease(ctx context.Context, repo forge.Repository, r forge.ReleaseSpec) (string, error) {
	if g.p.tagWriter() == nil {
		return g.Releases.PublishRelease(ctx, repo, r)
	}
	return "", nil
}
