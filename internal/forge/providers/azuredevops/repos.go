package azuredevops

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type identity struct{ p *Provider }

// PrincipalName is the account a connection test names: the host's reference
// has no call that tells who owns a token.
const PrincipalName = "personal-access-token"

type apiProject struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// projects: GET {organization}/_apis/projects?$top=500 (scope vso.project;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/core/projects/list).
func (p *Provider) projects(ctx context.Context, org string, top int) ([]apiProject, error) {
	var in page[apiProject]
	if _, err := p.c.Get(ctx, p.orgPath(org)+"/_apis/projects"+qs("$top", strconv.Itoa(top)), &in); err != nil {
		return nil, err
	}
	return in.Value, nil
}

// Whoami proves the token by reading the projects of the first allowed
// organization; it cannot name the owner (see Capabilities).
func (i identity) Whoami(ctx context.Context) (forge.Whoami, error) {
	orgs, err := i.p.owners()
	if err != nil {
		return forge.Whoami{}, err
	}
	if len(orgs) == 0 {
		return forge.Whoami{}, fmt.Errorf("%w: no organization is allowed on this connection", forge.ErrInvalid)
	}
	if _, err := i.p.projects(ctx, orgs[0], 1); err != nil {
		return forge.Whoami{}, err
	}
	return forge.Whoami{Principal: forge.Principal{Login: PrincipalName, Unlinked: true}, RateRemaining: -1}, nil
}

type repos struct{ p *Provider }

// apiRepo is a GitRepository (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/repositories/list).
type apiRepo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	IsFork        bool   `json:"isFork"`
	IsDisabled    bool   `json:"isDisabled"`
	RemoteURL     string `json:"remoteUrl"`
	SSHURL        string `json:"sshUrl"`
	WebURL        string `json:"webUrl"`
	Project       struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
}

func (p *Provider) repository(org string, a apiRepo) forge.Repository {
	r := forge.Repository{Connection: p.conn.ID, Name: a.Name, Path: org + "/" + a.Project.Name + "/" + a.Name, Home: org + "/" + a.Project.Name,
		DefaultBranch: trimRef(a.DefaultBranch), Visibility: forge.Private, Archived: a.IsDisabled,
		Ext: map[string]any{"id": a.ID, "web_url": a.WebURL, "fork": a.IsFork}}
	r.Clone.HTTPS = noUser(a.RemoteURL)
	r.Clone.SSH = sshURL(a.SSHURL)
	return r
}

// List: for each organization, each project (GET {org}/_apis/projects) and its
// repositories (GET {org}/{project}/_apis/git/repositories, scope vso.code).
// One page is the repositories of one project; the cursor is
// "<organization index>:<project index>".
func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	oi, pi := 0, 0
	if cursor != "" {
		a, b, ok := strings.Cut(cursor, ":")
		var e1, e2 error
		oi, e1 = strconv.Atoi(a)
		pi, e2 = strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil || oi < 0 || pi < 0 {
			return forge.Page[forge.Repository]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
	}
	out := forge.Page[forge.Repository]{Items: []forge.Repository{}}
	orgs, err := r.p.owners()
	if err != nil {
		return out, err
	}
	for ; oi < len(orgs); oi, pi = oi+1, 0 {
		prs, err := r.p.projects(ctx, orgs[oi], 500)
		if err != nil {
			return forge.Page[forge.Repository]{Items: []forge.Repository{}}, err
		}
		if pi >= len(prs) {
			continue
		}
		var in page[apiRepo]
		if _, err := r.p.c.Get(ctx, r.p.projectPath(orgs[oi], prs[pi].Name)+"/_apis/git/repositories"+qs(), &in); err != nil {
			return forge.Page[forge.Repository]{Items: []forge.Repository{}}, err
		}
		for _, a := range in.Value {
			if a.Project.Name == "" {
				a.Project.Name = prs[pi].Name
			}
			out.Items = append(out.Items, r.p.repository(orgs[oi], a))
		}
		switch {
		case pi+1 < len(prs):
			out.Next = strconv.Itoa(oi) + ":" + strconv.Itoa(pi+1)
		case oi+1 < len(orgs):
			out.Next = strconv.Itoa(oi+1) + ":0"
		}
		return out, nil
	}
	return out, nil
}

// Get: GET {organization}/{project}/_apis/git/repositories/{repository} (scope vso.code).
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	o, pr, name, err := r.p.split(path)
	if err != nil {
		return forge.Repository{}, err
	}
	var a apiRepo
	st, err := r.p.c.Call(ctx, http.MethodGet, r.p.projectPath(o, pr)+"/_apis/git/repositories/"+restkit.Esc(name)+qs(), nil, &a, http.StatusNotFound)
	if err != nil {
		return forge.Repository{}, err
	}
	if st == http.StatusNotFound {
		return forge.Repository{}, fmt.Errorf("%w: repository %s (or the token cannot see it)", forge.ErrNotFound, path)
	}
	if a.Project.Name == "" {
		a.Project.Name = pr
	}
	return r.p.repository(o, a), nil
}

func (r repos) Create(context.Context, forge.NewRepository) (forge.Repository, error) {
	return forge.Repository{}, r.p.Caps.Err(forge.CapRepoCreate)
}
func (r repos) Delete(context.Context, string) error { return r.p.Caps.Err(forge.CapRepoCreate) }
func (r repos) SetDefaultBranch(context.Context, string, string) error {
	return r.p.Caps.Err(forge.CapRepoCreate)
}

// repoID gives the repository's id, which branch policies are asked by: it is
// kept in Ext by Get, and asked of the host when the caller's copy lacks it.
func (p *Provider) repoID(ctx context.Context, repo forge.Repository) (string, error) {
	if id, _ := repo.Ext["id"].(string); id != "" {
		return id, nil
	}
	got, err := p.Repositories().Get(ctx, repo.Path)
	if err != nil {
		return "", err
	}
	id, _ := got.Ext["id"].(string)
	if id == "" {
		return "", fmt.Errorf("the host's answer names no id for %s", repo.Path)
	}
	return id, nil
}

type content struct{ p *Provider }

type apiRef struct {
	Name         string `json:"name"`
	ObjectID     string `json:"objectId"`
	PeeledObject string `json:"peeledObjectId"`
}

// commit is where the ref ends: an annotated tag's peeled commit when the host gives it.
func (a apiRef) commit() string {
	if a.PeeledObject != "" {
		return a.PeeledObject
	}
	return a.ObjectID
}

// refs: GET .../refs?filter=heads/ (or tags/) &peelTags=true&$top=1000 (scope vso.code;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/list, "filter: starts with",
// "$top ... cannot be bigger than 1000", "peelTags: annotated tags will populate the PeeledObjectId property").
func (p *Provider) refs(ctx context.Context, repoPath, filter string) ([]apiRef, error) {
	var in page[apiRef]
	if _, err := p.c.Get(ctx, repoPath+"/refs"+qs("filter", filter, "peelTags", "true", "$top", "1000"), &in); err != nil {
		return nil, err
	}
	return in.Value, nil
}

// Refs lists branches and tags (one page of up to 1000 of each).
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Ref
	for _, f := range []string{"heads/", "tags/"} {
		rs, err := c.p.refs(ctx, rp, f)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			out = append(out, forge.Ref{Name: r.Name, Commit: r.commit()})
		}
	}
	return out, nil
}

// Commit: GET .../commits/{commitId} (scope vso.code;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/commits/get).
func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return forge.Commit{}, err
	}
	var in struct {
		CommitID string   `json:"commitId"`
		Comment  string   `json:"comment"`
		Parents  []string `json:"parents"`
		Author   struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"author"`
	}
	if _, err := c.p.c.Get(ctx, rp+"/commits/"+restkit.Esc(id)+qs(), &in); err != nil {
		return forge.Commit{}, err
	}
	return forge.Commit{ID: in.CommitID, Message: in.Comment, Parents: in.Parents, CreatedAt: parseTime(in.Author.Date),
		Author: forge.Principal{Name: in.Author.Name, Login: in.Author.Email, Unlinked: true}}, nil
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

func (a access) CloneAddresses(_ context.Context, repo forge.Repository) (forge.CloneAddresses, error) {
	return repo.Clone, nil
}

// GitUser is the user name Git is given with the token: the token is the
// password of HTTP Basic and the reference says the user name can be empty, so
// any name will do; this one shows in the host's logs as Launchway.
const GitUser = "launchway"

// Credential: the personal access token is the password of an HTTPS clone or
// push (the same Basic header as the REST API accepts; the page on personal
// access tokens: "A Git operation typically fails the next time it
// authenticates" once a token expires, which says Git authenticates with it).
func (a access) Credential(_ context.Context, repo forge.Repository, _ bool) (forge.Credential, error) {
	if _, _, _, err := a.p.split(repo.Path); err != nil {
		return forge.Credential{}, err
	}
	tok, err := a.p.c.Token()
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: GitUser, Secret: tok}, nil
}
