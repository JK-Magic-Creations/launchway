package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

// Capabilities declares what this provider does.
func Capabilities() forge.Capabilities {
	c := forge.NoCapabilities("this is not built for this host yet", "Do this on the Git host itself")
	for _, f := range []forge.Capability{forge.CapIdentity, forge.CapRepositories, forge.CapContent, forge.CapEvents,
		forge.CapAccess, forge.CapProposals, forge.CapDrafts, forge.CapResults, forge.CapRules,
		forge.CapTags, forge.CapCommitFile, forge.CapProposalNote, forge.CapProposalRef} {
		c[f] = forge.Declaration{Support: forge.Native}
	}
	c[forge.CapProposalRef] = forge.Declaration{Support: forge.None,
		Reason:     "the host publishes no ref for a pull request's head; Launchway reads a pull request's head commit from the API and fetches it by its hash",
		Workaround: "Nothing to do: Sorties for pull requests start from the commit the host names"}
	c[forge.CapReleases] = forge.Declaration{Support: forge.None,
		Reason:     "Bitbucket has no release object: it has tags and a list of downloadable files per repository, and neither carries release notes",
		Workaround: "Cut the release in Launchway: the tag is made on Bitbucket, and the release page with its notes and files stays in Launchway, which serves the files"}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them",
		Workaround: "Create the repository on Bitbucket, then choose it in Launchway"}
	c[forge.CapResultsGate] = forge.Declaration{Support: forge.None,
		Reason:     "the host enforces required builds itself, in a branch restriction, and Launchway cannot change them",
		Workaround: "Add a branch restriction that requires passing builds on Bitbucket"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.None,
		Reason:     "reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf",
		Workaround: "Review on Bitbucket"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "merging stays on the host, under its branch restrictions",
		Workaround: "Merge the pull request on Bitbucket"}
	c[forge.CapAutoLand] = forge.Declaration{Support: forge.None,
		Reason:     "the host's API has no call to switch auto-merge on for a pull request (its branch restriction \"allow auto merge when builds pass\" is a setting of the repository)",
		Workaround: "Turn auto-merge on in the repository's branch restrictions on Bitbucket, or set write_back.merge to person and merge the pull request there"}
	c[forge.CapRules] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads the host's branch restrictions but never changes them",
		Workaround: "Change branch restrictions on Bitbucket"}
	return c
}

// Additions is what this host offers beyond the built-in forge.
func Additions() []forge.Addition {
	return []forge.Addition{
		{Key: "merge_checks", Title: "Merge checks", What: "The host can require passing builds, approvals, resolved comments and no requested changes before a pull request merges; set them in the repository's branch restrictions"},
		{Key: "default_reviewers", Title: "Default reviewers", What: "The host adds reviewers to each new pull request by rule; set up on the host"},
		{Key: "build_statuses", Title: "Builds on the pull request", What: "Each Sortie and failed step appears as a build status on the pull request, with a link back to the Sortie"},
	}
}

// Endpoints are the addresses a connection reaches.
type Endpoints = restkit.Endpoints

// EndpointsOf gives the API address of Bitbucket Cloud. Only the public
// service is covered: an empty address or https://bitbucket.org. Any other
// address is refused with the kind to use for a self-managed install.
func EndpointsOf(base string) (Endpoints, error) {
	var e Endpoints
	if testAPI != "" { // tests of the service run against a fake host
		u, err := url.Parse(testAPI)
		if err != nil {
			return e, err
		}
		e.API = u
		e.Hosts = map[string]bool{u.Host: true}
		return e, nil
	}
	switch strings.TrimSuffix(strings.TrimSpace(base), "/") {
	case "", "https://bitbucket.org":
	default:
		return e, fmt.Errorf("only Bitbucket Cloud (https://bitbucket.org) is covered by this kind; a self-managed Bitbucket Data Center has a different API: use the kind bitbucket-datacenter for it")
	}
	e.API = &url.URL{Scheme: "https", Host: "api.bitbucket.org", Path: "/2.0"}
	e.Hosts = map[string]bool{e.API.Host: true}
	return e, nil
}

var testAPI string

// UseTestAPI points every connection at a fake host's API address; it returns
// the function that restores the public service. For tests only: nothing in
// the product calls it.
func UseTestAPI(api string) (restore func()) {
	old := testAPI
	testAPI = api
	return func() { testAPI = old }
}

// Provider is the provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	c    *restkit.Client
}

// Register adds the kind to a registry.
func Register(r *forge.Registry, g *egress.Guard) error {
	return r.Register(forge.KindBitbucket, func(c forge.Connection) (forge.Provider, error) { return New(c, g) })
}

// New makes the provider of a connection.
func New(c forge.Connection, g *egress.Guard) (*Provider, error) {
	ep, err := EndpointsOf(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
	}
	return NewAt(c, ep, g), nil
}

// NewAt makes a provider over explicit endpoints (the tests' fake host).
func NewAt(c forge.Connection, ep Endpoints, g *egress.Guard) *Provider {
	cl := restkit.NewClient(ep, g, func() (string, error) {
		if c.Credential.IsZero() {
			return "", fmt.Errorf("the connection %s has no credential", c.Name)
		}
		return c.Credential.Resolve()
	}, func(h http.Header, tok string) {
		// "the access token placed as a Bearer token in the request Authorization header"
		// (https://developer.atlassian.com/cloud/bitbucket/rest/intro/).
		h.Set("Authorization", "Bearer "+tok)
	})
	cl.Headers = map[string]string{"Accept": "application/json"}
	cl.Message = message
	return &Provider{Base: forge.Base{K: forge.KindBitbucket, Caps: Capabilities(), Mat: forge.Experimental, Adds: Additions()}, conn: c, c: cl}
}

// Client returns the connection's client, for the tests.
func (p *Provider) Client() *restkit.Client { return p.c }

func (p *Provider) Identity() forge.Identity         { return identity{p} }
func (p *Provider) Repositories() forge.Repositories { return repos{p} }
func (p *Provider) Content() forge.Content           { return content{p} }
func (p *Provider) Proposals() forge.Proposals       { return proposals{p} }
func (p *Provider) Results() forge.Results           { return results{p} }
func (p *Provider) Rules() forge.Rules               { return rules{p} }
func (p *Provider) Events() forge.Events             { return events{p} }
func (p *Provider) Access() forge.Access             { return access{p} }
func (p *Provider) Releases() forge.Releases         { return releases{p} }

// message reads the host's words from an error body: {"type": "error", "error": {"message": "..."}}.
func message(body []byte) string {
	var m struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if jsonUnmarshal(body, &m) != nil {
		return ""
	}
	return m.Error.Message
}

// split names workspace and repository slug and enforces the allow-list of workspaces.
func (p *Provider) split(path string) (ws, slug string, err error) {
	ws, slug, ok := strings.Cut(strings.Trim(path, "/"), "/")
	if !ok || ws == "" || slug == "" || strings.Contains(slug, "/") {
		return "", "", fmt.Errorf("%w: a repository is named workspace/name, not %q", forge.ErrInvalid, path)
	}
	if !restkit.OwnerAllowed(p.conn, ws) {
		return "", "", fmt.Errorf("%w: the workspace %q is not on this connection's allow-list", forge.ErrInvalid, ws)
	}
	return ws, slug, nil
}

// repoPath is /repositories/{workspace}/{repo_slug}.
func (p *Provider) repoPath(r forge.Repository) (string, error) {
	w, s, err := p.split(r.Path)
	if err != nil {
		return "", err
	}
	return "/repositories/" + restkit.Esc(w) + "/" + restkit.Esc(s), nil
}

// page is the wrapper of every collection (size, page, pagelen, next, values).
type page[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

type identity struct{ p *Provider }

type apiUser struct {
	Nickname    string `json:"nickname"`
	DisplayName string `json:"display_name"`
	AccountID   string `json:"account_id"`
}

func (u apiUser) login() string {
	if u.Nickname != "" {
		return u.Nickname
	}
	return u.DisplayName
}

// Whoami: GET /user (scope account). A repository, project or workspace access
// token has no user and the host refuses it; that is not an error here: the
// account is then the token itself.
func (i identity) Whoami(ctx context.Context) (forge.Whoami, error) {
	var u apiUser
	a, err := i.p.c.Get(ctx, "/user", &u, http.StatusForbidden, http.StatusUnauthorized+0, http.StatusNotFound)
	if err != nil {
		return forge.Whoami{}, err
	}
	if a.Status >= 400 {
		return forge.Whoami{Principal: forge.Principal{Login: "access-token", Unlinked: true}, RateRemaining: -1}, nil
	}
	return forge.Whoami{Principal: forge.Principal{Login: u.login(), Name: u.DisplayName, Unlinked: true}, RateRemaining: -1}, nil
}

type repos struct{ p *Provider }

type apiRepo struct {
	FullName   string `json:"full_name"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	IsPrivate  bool   `json:"is_private"`
	MainBranch *struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
	Links struct {
		Clone []struct {
			Name string `json:"name"`
			Href string `json:"href"`
		} `json:"clone"`
	} `json:"links"`
}

func (p *Provider) repository(a apiRepo) forge.Repository {
	v := forge.Public
	if a.IsPrivate {
		v = forge.Private
	}
	r := forge.Repository{Connection: p.conn.ID, Path: a.FullName, Name: a.Slug, Visibility: v}
	r.Home, _, _ = strings.Cut(a.FullName, "/")
	if r.Name == "" {
		r.Name = a.Name
	}
	if a.MainBranch != nil {
		r.DefaultBranch = a.MainBranch.Name
	}
	for _, l := range a.Links.Clone {
		switch l.Name {
		case "https":
			r.Clone.HTTPS = l.Href
		case "ssh":
			r.Clone.SSH = l.Href
		}
	}
	return r
}

// workspaces are the ones the connection may use: its allow-list, or else GET
// /user/workspaces (scope account), whose answer lists each as workspace.slug.
func (p *Provider) workspaces(ctx context.Context) ([]string, error) {
	if len(p.conn.Owners) > 0 {
		return p.conn.Owners, nil
	}
	var in page[struct {
		Workspace struct {
			Slug string `json:"slug"`
		} `json:"workspace"`
	}]
	if _, err := p.c.Get(ctx, "/user/workspaces?pagelen=50", &in); err != nil {
		return nil, err
	}
	var out []string
	for _, w := range in.Values {
		out = append(out, w.Workspace.Slug)
	}
	return out, nil
}

// List: for each workspace, GET /repositories/{workspace}?role=member
// (scope repository), 50 to a page. The cursor is "<workspace index>:<page>".
func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	wi, pg := 0, 1
	if cursor != "" {
		a, b, ok := strings.Cut(cursor, ":")
		var e1, e2 error
		wi, e1 = strconv.Atoi(a)
		pg, e2 = strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil || wi < 0 || pg < 1 {
			return forge.Page[forge.Repository]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
	}
	ws, err := r.p.workspaces(ctx)
	if err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	out := forge.Page[forge.Repository]{Items: []forge.Repository{}}
	if wi >= len(ws) {
		return out, nil
	}
	var in page[apiRepo]
	if _, err := r.p.c.Get(ctx, "/repositories/"+restkit.Esc(ws[wi])+"?role=member&pagelen=50&page="+strconv.Itoa(pg), &in); err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	for _, a := range in.Values {
		out.Items = append(out.Items, r.p.repository(a))
	}
	switch {
	case in.Next != "":
		out.Next = strconv.Itoa(wi) + ":" + strconv.Itoa(pg+1)
	case wi+1 < len(ws):
		out.Next = strconv.Itoa(wi+1) + ":1"
	}
	return out, nil
}

// Get: GET /repositories/{workspace}/{repo_slug} (scope repository).
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	w, s, err := r.p.split(path)
	if err != nil {
		return forge.Repository{}, err
	}
	var a apiRepo
	st, err := r.p.c.Call(ctx, http.MethodGet, "/repositories/"+restkit.Esc(w)+"/"+restkit.Esc(s), nil, &a, http.StatusNotFound)
	if err != nil {
		return forge.Repository{}, err
	}
	if st == http.StatusNotFound {
		return forge.Repository{}, fmt.Errorf("%w: repository %s (or the token cannot see it)", forge.ErrNotFound, path)
	}
	return r.p.repository(a), nil
}

func (r repos) Create(context.Context, forge.NewRepository) (forge.Repository, error) {
	return forge.Repository{}, r.p.Caps.Err(forge.CapRepoCreate)
}
func (r repos) Delete(context.Context, string) error { return r.p.Caps.Err(forge.CapRepoCreate) }
func (r repos) SetDefaultBranch(context.Context, string, string) error {
	return r.p.Caps.Err(forge.CapRepoCreate)
}

type content struct{ p *Provider }

// Refs: GET /repositories/{w}/{r}/refs/branches and /refs/tags (scope repository), 100 to a page.
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Ref
	for _, kind := range []struct{ path, prefix string }{{"/refs/branches", "refs/heads/"}, {"/refs/tags", "refs/tags/"}} {
		for pg := 1; pg <= 40; pg++ {
			var in page[struct {
				Name   string `json:"name"`
				Target struct {
					Hash string `json:"hash"`
				} `json:"target"`
			}]
			if _, err := c.p.c.Get(ctx, rp+kind.path+"?pagelen=100&page="+strconv.Itoa(pg), &in); err != nil {
				return nil, err
			}
			for _, r := range in.Values {
				out = append(out, forge.Ref{Name: kind.prefix + r.Name, Commit: r.Target.Hash})
			}
			if in.Next == "" {
				break
			}
		}
	}
	return out, nil
}

// Commit: GET /repositories/{w}/{r}/commit/{commit} (scope repository).
func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return forge.Commit{}, err
	}
	var in struct {
		Hash    string `json:"hash"`
		Date    string `json:"date"`
		Message string `json:"message"`
		Author  struct {
			Raw string `json:"raw"`
		} `json:"author"`
		Parents []struct {
			Hash string `json:"hash"`
		} `json:"parents"`
	}
	if _, err := c.p.c.Get(ctx, rp+"/commit/"+restkit.Esc(id), &in); err != nil {
		return forge.Commit{}, err
	}
	cm := forge.Commit{ID: in.Hash, Message: in.Message, CreatedAt: parseTime(in.Date), Author: forge.Principal{Name: in.Author.Raw, Unlinked: true}}
	for _, par := range in.Parents {
		cm.Parents = append(cm.Parents, par.Hash)
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

// Credential: an access token is the password of an HTTPS clone with the
// literal user name x-token-auth (https://developer.atlassian.com/cloud/bitbucket/rest/intro/ ,
// "Repository cloning": git clone https://x-token-auth:{access_token}@bitbucket.org/user/repo.git).
func (a access) Credential(ctx context.Context, repo forge.Repository, write bool) (forge.Credential, error) {
	if _, _, err := a.p.split(repo.Path); err != nil {
		return forge.Credential{}, err
	}
	tok, err := a.p.c.Token()
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: "x-token-auth", Secret: tok}, nil
}
