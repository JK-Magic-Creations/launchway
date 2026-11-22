package anygit

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type identity struct{ p *Provider }

// Whoami names the user the connection sends. It proves nothing: Git has no
// call that says who a credential is (see Capabilities).
func (i identity) Whoami(context.Context) (forge.Whoami, error) {
	c, err := i.p.credential()
	if err != nil {
		return forge.Whoami{}, err
	}
	return forge.Whoami{Principal: forge.Principal{Login: c.user, Unlinked: true}, RateRemaining: -1}, nil
}

type repos struct{ p *Provider }

// List answers an empty page: a Git server has no call that lists repositories.
func (r repos) List(context.Context, string) (forge.Page[forge.Repository], error) {
	return forge.Page[forge.Repository]{Items: []forge.Repository{}}, nil
}

// Get proves the repository and the credential with git ls-remote (a read of
// the server's refs) and reads the default branch from HEAD, when the server
// says it.
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	owner, name, err := r.p.split(path)
	if err != nil {
		return forge.Repository{}, err
	}
	dir, err := scratch()
	if err != nil {
		return forge.Repository{}, err
	}
	defer os.RemoveAll(dir)
	_, head, err := r.p.lsRemote(ctx, path, dir, true, "HEAD")
	if err != nil {
		return forge.Repository{}, err
	}
	u, _ := r.p.gitURL(path)
	repo := forge.Repository{Connection: r.p.conn.ID, Home: owner, Name: strings.TrimSuffix(name, ".git"), Path: owner + "/" + name,
		DefaultBranch: strings.TrimPrefix(head, "refs/heads/"), Visibility: forge.Private, CreatedAt: time.Time{}, UpdatedAt: time.Time{}}
	repo.Clone.HTTPS = u.String()
	return repo, nil
}

func (r repos) Create(context.Context, forge.NewRepository) (forge.Repository, error) {
	return forge.Repository{}, r.p.Caps.Err(forge.CapRepoCreate)
}
func (r repos) Delete(context.Context, string) error { return r.p.Caps.Err(forge.CapRepoCreate) }
func (r repos) SetDefaultBranch(context.Context, string, string) error {
	return r.p.Caps.Err(forge.CapRepoCreate)
}

type content struct{ p *Provider }

// Refs lists the branches and tags the server shows (ls-remote of refs/heads/*
// and refs/tags/*; a peeled tag line is the commit of an annotated tag and is not a ref).
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	dir, err := scratch()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	refs, _, err := c.p.lsRemote(ctx, repo.Path, dir, false, "refs/heads/*", "refs/tags/*")
	if err != nil {
		return nil, err
	}
	out := refs[:0]
	for _, r := range refs {
		if !strings.HasSuffix(r.Name, "^{}") {
			out = append(out, r)
		}
	}
	return out, nil
}

var noRead = &forge.ErrUnsupported{Capability: forge.CapContent, Reason: "Launchway reads commits, files and diffs from its own clone of a plain Git server", Workaround: "Browse the clone in Launchway"}

func (c content) Commit(context.Context, forge.Repository, string) (forge.Commit, error) {
	return forge.Commit{}, noRead
}
func (c content) Tree(context.Context, forge.Repository, string, string) ([]forge.TreeEntry, error) {
	return nil, noRead
}
func (c content) Blob(context.Context, forge.Repository, string, string, int64) ([]byte, error) {
	return nil, noRead
}
func (c content) Compare(context.Context, forge.Repository, string, string) (forge.Comparison, error) {
	return forge.Comparison{}, noRead
}

type access struct{ p *Provider }

// CloneAddresses is the repository's https address on the connection's base address.
func (a access) CloneAddresses(_ context.Context, repo forge.Repository) (forge.CloneAddresses, error) {
	u, err := a.p.gitURL(repo.Path)
	if err != nil {
		return forge.CloneAddresses{}, err
	}
	return forge.CloneAddresses{HTTPS: u.String()}, nil
}

// Credential is the connection's credential, for read and for write alike:
// what it may do is decided by the server's permissions for that account.
func (a access) Credential(_ context.Context, repo forge.Repository, _ bool) (forge.Credential, error) {
	if _, err := a.p.gitURL(repo.Path); err != nil {
		return forge.Credential{}, err
	}
	c, err := a.p.credential()
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: c.user, Secret: c.secret}, nil
}

type events struct{ p *Provider }

// Mode is polling: the repository itself is polled (fetched) on the server's schedule.
func (e events) Mode() forge.EventMode { return forge.EventsPolling }

// Verify refuses every delivery: a plain Git server sends none Launchway can check.
func (e events) Verify(*http.Request, []byte) ([]forge.Event, error) {
	return nil, fmt.Errorf("%w: a plain Git server sends no webhook of a format Launchway checks; this connection is followed by polling, and a push can ask for a poll at once through the Flight's git-hook address", forge.ErrInvalid)
}

// Poll finds nothing of its own: there are no pull requests to see, and pushes
// are followed from the repository, which Launchway fetches on the same schedule.
func (e events) Poll(_ context.Context, _ forge.Repository, cursor string) ([]forge.Event, string, error) {
	return []forge.Event{}, cursor, nil
}
