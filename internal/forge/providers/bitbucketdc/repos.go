package bitbucketdc

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type identity struct{ p *Provider }

// Whoami: the reference has no call that names the caller, so the token is
// judged by what it can do: GET /api/latest/repos?limit=1 (a token that is
// wrong, revoked or expired is refused with 401), and the product's name and
// version from GET /api/latest/application-properties (which needs no
// authentication). The principal is the token itself.
func (i identity) Whoami(ctx context.Context) (forge.Whoami, error) {
	var in page[apiRepo]
	if _, err := i.p.c.Get(ctx, "/api/latest/repos?limit=1", &in); err != nil {
		return forge.Whoami{}, err
	}
	name := "Bitbucket Data Center"
	var props struct {
		Version string `json:"version"`
	}
	if _, err := i.p.c.Get(ctx, "/api/latest/application-properties", &props, http.StatusForbidden, http.StatusNotFound); err == nil && props.Version != "" {
		name += " " + props.Version
	}
	return forge.Whoami{Principal: forge.Principal{Login: "access-token", Name: name, Unlinked: true}, RateRemaining: -1}, nil
}

type repos struct{ p *Provider }

// apiRepo is a repository as the host answers it (RestRepository).
type apiRepo struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Public  bool   `json:"public"`
	Archive bool   `json:"archived"`
	Project struct {
		Key string `json:"key"`
	} `json:"project"`
	Links struct {
		Clone []struct {
			Name string `json:"name"`
			Href string `json:"href"`
		} `json:"clone"`
	} `json:"links"`
}

func (p *Provider) repository(a apiRepo) forge.Repository {
	v := forge.Private
	if a.Public {
		v = forge.Public
	}
	r := forge.Repository{Connection: p.conn.ID, Path: a.Project.Key + "/" + a.Slug, Home: a.Project.Key, Name: a.Slug, Visibility: v, Archived: a.Archive}
	for _, l := range a.Links.Clone {
		switch l.Name {
		case "http", "https":
			r.Clone.HTTPS = l.Href
		case "ssh":
			r.Clone.SSH = l.Href
		}
	}
	if r.Clone.HTTPS == "" { // the layout the reference's own example shows: <base>/scm/<project key>/<slug>.git
		r.Clone.HTTPS = p.root() + "/scm/" + strings.ToLower(a.Project.Key) + "/" + a.Slug + ".git"
	}
	return r
}

// List: for each allowed project key, GET /api/latest/repos?projectkey=KEY
// (the search resource; "projectkey" is available since 8.0; implicit read
// permission), 50 to a page; without an allow-list, the repositories the token
// can read. The cursor is "<key index>:<start>".
func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	ki, start := 0, 0
	if cursor != "" {
		a, b, ok := strings.Cut(cursor, ":")
		var e1, e2 error
		ki, e1 = strconv.Atoi(a)
		start, e2 = strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil || ki < 0 || start < 0 {
			return forge.Page[forge.Repository]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
	}
	keys := r.p.conn.Owners
	if len(keys) == 0 {
		keys = []string{""}
	}
	out := forge.Page[forge.Repository]{Items: []forge.Repository{}}
	if ki >= len(keys) {
		return out, nil
	}
	q := url.Values{"limit": {"50"}, "start": {strconv.Itoa(start)}}
	if keys[ki] != "" {
		q.Set("projectkey", keys[ki])
	}
	var in page[apiRepo]
	if _, err := r.p.c.Get(ctx, "/api/latest/repos?"+q.Encode(), &in); err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	for _, a := range in.Values {
		out.Items = append(out.Items, r.p.repository(a))
	}
	switch {
	case !in.IsLastPage:
		out.Next = strconv.Itoa(ki) + ":" + strconv.Itoa(in.NextPageStart)
	case ki+1 < len(keys):
		out.Next = strconv.Itoa(ki+1) + ":0"
	}
	return out, nil
}

// Get: GET /api/latest/projects/{projectKey}/repos/{repositorySlug} (REPO_READ)
// and the configured default branch, GET .../default-branch (REPO_READ; the
// repository's own answer does not carry it).
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	rp, err := r.p.repoPathOf(path)
	if err != nil {
		return forge.Repository{}, err
	}
	var a apiRepo
	st, err := r.p.c.Call(ctx, http.MethodGet, rp, nil, &a, http.StatusNotFound)
	if err != nil {
		return forge.Repository{}, err
	}
	if st == http.StatusNotFound {
		return forge.Repository{}, fmt.Errorf("%w: repository %s (or the token cannot see it)", forge.ErrNotFound, path)
	}
	repo := r.p.repository(a)
	var d struct {
		ID string `json:"id"`
	}
	if _, err := r.p.c.Get(ctx, rp+"/default-branch", &d, http.StatusNotFound); err != nil {
		return forge.Repository{}, err
	}
	repo.DefaultBranch = strings.TrimPrefix(d.ID, "refs/heads/")
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

type apiRef struct {
	ID           string `json:"id"`
	LatestCommit string `json:"latestCommit"`
}

// Refs: GET .../branches and GET .../tags (the "find" resources, REPO_READ),
// 100 to a page; a ref's id is its full name ("refs/heads/main").
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Ref
	for _, kind := range []string{"/branches", "/tags"} {
		start := 0
		for n := 0; n < 40; n++ {
			var in page[apiRef]
			if _, err := c.p.c.Get(ctx, rp+kind+"?limit=100&start="+strconv.Itoa(start), &in); err != nil {
				return nil, err
			}
			for _, r := range in.Values {
				out = append(out, forge.Ref{Name: r.ID, Commit: r.LatestCommit})
			}
			if in.IsLastPage {
				break
			}
			start = in.NextPageStart
		}
	}
	return out, nil
}

// Commit: GET .../commits/{commitId} (REPO_READ).
func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return forge.Commit{}, err
	}
	var in struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"emailAddress"`
		} `json:"author"`
		AuthorTimestamp int64 `json:"authorTimestamp"`
		Parents         []struct {
			ID string `json:"id"`
		} `json:"parents"`
	}
	if _, err := c.p.c.Get(ctx, rp+"/commits/"+restkit.Esc(id), &in); err != nil {
		return forge.Commit{}, err
	}
	cm := forge.Commit{ID: in.ID, Message: in.Message, CreatedAt: fromMillis(in.AuthorTimestamp),
		Author: forge.Principal{Name: strings.TrimSpace(in.Author.Name + " <" + in.Author.Email + ">"), Unlinked: true}}
	for _, par := range in.Parents {
		cm.Parents = append(cm.Parents, par.ID)
	}
	return cm, nil
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

// Credential: for a project or repository HTTP access token, "set the username
// to x-token-auth" and the token is the password
// (https://confluence.atlassian.com/bitbucketserver/http-access-tokens-939515499.html,
// "Git over HTTPS"). A personal access token is used with its owner's own user
// name there, which a connection does not know: use a project or repository token.
func (a access) Credential(_ context.Context, repo forge.Repository, _ bool) (forge.Credential, error) {
	if _, _, err := a.p.split(repo.Path); err != nil {
		return forge.Credential{}, err
	}
	tok, err := a.p.c.Token()
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: "x-token-auth", Secret: tok}, nil
}
