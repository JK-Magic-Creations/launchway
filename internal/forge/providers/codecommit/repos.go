package codecommit

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type identity struct{ p *Provider }

// Whoami proves the credentials by listing the repositories (the user guide
// lists codecommit:ListRepositories as the permission the console needs to show
// them) and names the access key or role session, as no call Launchway uses
// names the owner of a key.
func (i identity) Whoami(ctx context.Context) (forge.Whoami, error) {
	s, err := i.p.session(ctx)
	if err != nil {
		return forge.Whoami{}, err
	}
	var out struct {
		Repositories []struct{} `json:"repositories"`
	}
	if err := i.p.invoke(ctx, "ListRepositories", nil, &out); err != nil {
		return forge.Whoami{}, err
	}
	return forge.Whoami{Principal: forge.Principal{Login: s.who, Unlinked: true}, RateRemaining: -1}, nil
}

type repos struct{ p *Provider }

// apiRepo is RepositoryMetadata
// (https://docs.aws.amazon.com/codecommit/latest/APIReference/API_RepositoryMetadata.html);
// the ARN's field is named "Arn" there.
type apiRepo struct {
	AccountID    string  `json:"accountId"`
	ARN          string  `json:"Arn"`
	CloneHTTP    string  `json:"cloneUrlHttp"`
	CloneSSH     string  `json:"cloneUrlSsh"`
	Created      float64 `json:"creationDate"`
	Modified     float64 `json:"lastModifiedDate"`
	DefaultBr    string  `json:"defaultBranch"`
	Description  string  `json:"repositoryDescription"`
	RepositoryID string  `json:"repositoryId"`
	Name         string  `json:"repositoryName"`
}

func (p *Provider) repository(a apiRepo) forge.Repository {
	r := forge.Repository{Connection: p.conn.ID, Name: a.Name, Path: a.AccountID + "/" + a.Name, Home: a.AccountID, DefaultBranch: a.DefaultBr,
		Visibility: forge.Private, CreatedAt: epoch(a.Created), UpdatedAt: epoch(a.Modified),
		Ext: map[string]any{"id": a.RepositoryID, "arn": a.ARN, "region": p.ep.Region, "description": a.Description}}
	r.Clone.HTTPS = a.CloneHTTP
	r.Clone.SSH = a.CloneSSH
	return r
}

// batch reads the metadata of up to 100 repositories: BatchGetRepositories (names
// of at most 100 characters matching [\w\.-]+, at most 100 names; permission
// codecommit:BatchGetRepositories on each repository).
func (p *Provider) batch(ctx context.Context, names []string) ([]apiRepo, error) {
	var out struct {
		Repositories []apiRepo `json:"repositories"`
	}
	if err := p.invoke(ctx, "BatchGetRepositories", map[string]any{"repositoryNames": names}, &out); err != nil {
		return nil, err
	}
	return out.Repositories, nil
}

// List: ListRepositories (a page of up to 1,000 names; nextToken continues;
// permission codecommit:ListRepositories, resource *) and then the metadata of
// each in batches of 100. Only repositories of the owners list are shown.
func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	in := map[string]any{"sortBy": "repositoryName", "order": "ascending"}
	if cursor != "" {
		in["nextToken"] = cursor
	}
	var out struct {
		Repositories []struct {
			Name string `json:"repositoryName"`
		} `json:"repositories"`
		Next string `json:"nextToken"`
	}
	page := forge.Page[forge.Repository]{Items: []forge.Repository{}}
	if err := r.p.invoke(ctx, "ListRepositories", in, &out); err != nil {
		return page, err
	}
	for i := 0; i < len(out.Repositories); i += 100 {
		var names []string
		for _, x := range out.Repositories[i:min(i+100, len(out.Repositories))] {
			names = append(names, x.Name)
		}
		metas, err := r.p.batch(ctx, names)
		if err != nil {
			return forge.Page[forge.Repository]{Items: []forge.Repository{}}, err
		}
		for _, m := range metas {
			if restkitAllowed(r.p, m.AccountID) {
				page.Items = append(page.Items, r.p.repository(m))
			}
		}
	}
	page.Next = out.Next
	return page, nil
}

func restkitAllowed(p *Provider, account string) bool {
	if len(p.conn.Owners) == 0 {
		return true
	}
	for _, o := range p.conn.Owners {
		if o == account {
			return true
		}
	}
	return false
}

// Get reads one repository's metadata with BatchGetRepositories; a name the
// host does not find ("repositoriesNotFound") is not found.
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	acct, name, err := r.p.split(path)
	if err != nil {
		return forge.Repository{}, err
	}
	metas, err := r.p.batch(ctx, []string{name})
	if err != nil {
		return forge.Repository{}, err
	}
	if len(metas) != 1 || metas[0].AccountID != acct {
		return forge.Repository{}, fmt.Errorf("%w: repository %s (or the credentials cannot see it in this region)", forge.ErrNotFound, path)
	}
	return r.p.repository(metas[0]), nil
}

func (r repos) Create(context.Context, forge.NewRepository) (forge.Repository, error) {
	return forge.Repository{}, r.p.Caps.Err(forge.CapRepoCreate)
}
func (r repos) Delete(context.Context, string) error { return r.p.Caps.Err(forge.CapRepoCreate) }
func (r repos) SetDefaultBranch(context.Context, string, string) error {
	return r.p.Caps.Err(forge.CapRepoCreate)
}

type content struct{ p *Provider }

// name of a repository for the calls that take repositoryName.
func (p *Provider) repoName(repo forge.Repository) (string, error) {
	_, name, err := p.split(repo.Path)
	return name, err
}

// branches lists every branch (ListBranches, permission codecommit:ListBranches)
// and the commit each ends at (GetBranch, codecommit:GetBranch).
func (p *Provider) branches(ctx context.Context, name string) ([]forge.Ref, error) {
	var refs []forge.Ref
	next := ""
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return nil, errTooManyPages
		}
		in := map[string]any{"repositoryName": name}
		if next != "" {
			in["nextToken"] = next
		}
		var out struct {
			Branches []string `json:"branches"`
			Next     string   `json:"nextToken"`
		}
		if err := p.invoke(ctx, "ListBranches", in, &out); err != nil {
			return nil, err
		}
		for _, b := range out.Branches {
			c, err := p.tip(ctx, name, b)
			if err != nil {
				return nil, err
			}
			refs = append(refs, forge.Ref{Name: "refs/heads/" + b, Commit: c})
		}
		if next = out.Next; next == "" || len(refs) > 2000 {
			return refs, nil
		}
	}
}

// maxPages bounds every loop that follows a host's continuation token: a host
// that keeps answering with a token (and no data) cannot hold a call forever.
const maxPages = 200

var errTooManyPages = errors.New("the host keeps sending more pages than a repository has; the listing is stopped")

// tip is the commit a branch ends at: GetBranch.
func (p *Provider) tip(ctx context.Context, name, branch string) (string, error) {
	var out struct {
		Branch struct {
			CommitID string `json:"commitId"`
		} `json:"branch"`
	}
	if err := p.invoke(ctx, "GetBranch", map[string]any{"repositoryName": name, "branchName": branch}, &out); err != nil {
		return "", err
	}
	return out.Branch.CommitID, nil
}

// Refs lists the branches; the API has no call that lists tags.
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	name, err := c.p.repoName(repo)
	if err != nil {
		return nil, err
	}
	return c.p.branches(ctx, name)
}

// Commit: GetCommit (permission codecommit:GetCommit); a date is "seconds zone".
func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	name, err := c.p.repoName(repo)
	if err != nil {
		return forge.Commit{}, err
	}
	var out struct {
		Commit struct {
			ID      string   `json:"commitId"`
			Message string   `json:"message"`
			Parents []string `json:"parents"`
			Author  struct {
				Name  string `json:"name"`
				Email string `json:"email"`
				Date  string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := c.p.invoke(ctx, "GetCommit", map[string]any{"repositoryName": name, "commitId": id}, &out); err != nil {
		return forge.Commit{}, err
	}
	m := out.Commit
	return forge.Commit{ID: m.ID, Message: m.Message, Parents: m.Parents, CreatedAt: gitDate(m.Author.Date),
		Author: forge.Principal{Name: m.Author.Name, Login: m.Author.Email, Unlinked: true}}, nil
}

func (c content) Tree(context.Context, forge.Repository, string, string) ([]forge.TreeEntry, error) {
	return nil, &forge.ErrUnsupported{Capability: forge.CapContent, Reason: "Launchway reads files from its own clone", Workaround: "Browse the clone in Launchway"}
}
func (c content) Blob(context.Context, forge.Repository, string, string, int64) ([]byte, error) {
	return nil, &forge.ErrUnsupported{Capability: forge.CapContent, Reason: "Launchway reads files from its own clone", Workaround: "Browse the clone in Launchway"}
}
func (c content) Compare(context.Context, forge.Repository, string, string) (forge.Comparison, error) {
	return forge.Comparison{}, &forge.ErrUnsupported{Capability: forge.CapContent, Reason: "Launchway compares from its own clone", Workaround: "Compare in Launchway"}
}

type access struct{ p *Provider }

// CloneAddresses: the HTTPS address the host gives (cloneUrlHttp), on the
// connection's Git endpoint only.
func (a access) CloneAddresses(_ context.Context, repo forge.Repository) (forge.CloneAddresses, error) {
	if _, err := a.p.gitURL(repo); err != nil {
		return forge.CloneAddresses{}, err
	}
	return repo.Clone, nil
}

// gitURL checks the repository's clone address is on the connection's own Git
// endpoint (the credentials are signed for that host and are sent nowhere else).
func (p *Provider) gitURL(repo forge.Repository) (*url.URL, error) {
	u, err := url.Parse(repo.Clone.HTTPS)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !p.ep.Test) {
		return nil, fmt.Errorf("the repository %s has no usable HTTPS clone address", repo.Path)
	}
	if u.Hostname() != p.ep.GitHost {
		return nil, fmt.Errorf("the clone address names %s, which is not this connection's Git endpoint; the credentials are not signed for it", u.Hostname())
	}
	if _, name, err := p.split(repo.Path); err != nil {
		return nil, err
	} else if u.Path != "/v1/repos/"+name {
		return nil, fmt.Errorf("the clone address %s is not /v1/repos/%s", u.Path, name)
	}
	return u, nil
}
