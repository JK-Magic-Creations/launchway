package gitlab

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

// Capabilities declares what this provider does. Anything not Native says why
// and what to do instead; the screens print these words.
func Capabilities() forge.Capabilities {
	c := forge.NoCapabilities("this is not built for this host yet", "Do this on the Git host itself")
	for _, f := range []forge.Capability{forge.CapIdentity, forge.CapRepositories, forge.CapContent, forge.CapEvents,
		forge.CapAccess, forge.CapProposals, forge.CapResults, forge.CapRules,
		forge.CapTags, forge.CapReleases, forge.CapCommitFile, forge.CapProposalNote, forge.CapProposalRef} {
		c[f] = forge.Declaration{Support: forge.Native}
	}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection is narrow on purpose: it reads and writes projects that exist and cannot create or delete them",
		Workaround: "Create the project on the Git host, then choose it in Launchway"}
	c[forge.CapDrafts] = forge.Declaration{Support: forge.None,
		Reason:     "the host's API documentation for creating and updating a merge request has no draft parameter, so Launchway reads a draft but does not make or change one",
		Workaround: "Mark the merge request as draft or ready on the Git host"}
	c[forge.CapResultsGate] = forge.Declaration{Support: forge.None,
		Reason:     "the host enforces required pipelines and approvals itself, in the project's merge request settings and protected branches, and Launchway cannot change them",
		Workaround: "Require the Launchway status name in the project's merge request settings on the Git host"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.None,
		Reason:     "reviews stay on the host; Launchway shows a merge request and reports on it but does not review on the host's behalf",
		Workaround: "Review on the Git host"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "merging stays on the host, under its protected branches and approval rules",
		Workaround: "Merge the merge request on the Git host"}
	c[forge.CapAutoLand] = forge.Declaration{Support: forge.Native, Reason: "the host's own auto-merge (merge when the pipeline succeeds), switched on for a merge request by Launchway; the merge itself stays the host's"}
	c[forge.CapRules] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads the host's protected branch but never changes it",
		Workaround: "Change protected branches on the Git host"}
	return c
}

// Additions is what this host offers beyond the built-in forge.
func Additions() []forge.Addition {
	return []forge.Addition{
		{Key: forge.AdditionAutoMerge, Title: "Auto-merge", Drivable: true,
			What: "The host merges a merge request by itself once its checks pass (its auto-merge, formerly merge when pipeline succeeds); Launchway switches it on for a write-back merge request (write_back.merge: auto), so a deploy's merge request lands with no one waiting on it"},
		{Key: "merge_trains", Title: "Merge trains", What: "The host can queue merge requests in a merge train and test each with those ahead of it before it lands; set it up on the host"},
		{Key: "approval_rules", Title: "Approval rules", What: "The host enforces who must approve a merge request (approval rules, code owners); Launchway's statuses can be required alongside them"},
		{Key: "status_page", Title: "Statuses on the merge request", What: "Each Sortie and failed step appears as a commit status on the merge request, with a link back to the Sortie"},
	}
}

// Endpoints are the addresses a connection reaches.
type Endpoints = restkit.Endpoints

// EndpointsOf derives the API address from a connection's base address: empty
// or the public service's own address gives the public API; any other address
// is a self-managed install. In both the API is at <base>/api/v4
// (https://docs.gitlab.com/api/rest/ : "https://gitlab.example.com/api/v4/...").
func EndpointsOf(base string) (Endpoints, error) {
	var e Endpoints
	b := strings.TrimSuffix(strings.TrimSpace(base), "/")
	if b == "" {
		b = "https://gitlab.com"
	}
	u, err := restkit.ParseBase(b)
	if err != nil {
		return e, err
	}
	root := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api/v4")
	e.API = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: root + "/api/v4"}
	e.Hosts = map[string]bool{e.API.Host: true}
	return e, nil
}

// Provider is the provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	c    *restkit.Client
}

// Register adds the kind to a registry.
func Register(r *forge.Registry, g *egress.Guard) error {
	return r.Register(forge.KindGitLab, func(c forge.Connection) (forge.Provider, error) { return New(c, g) })
}

// New makes the provider of a connection.
func New(c forge.Connection, g *egress.Guard) (*Provider, error) {
	ep, err := EndpointsOf(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
	}
	cl := restkit.NewClient(ep, g, func() (string, error) {
		if c.Credential.IsZero() {
			return "", fmt.Errorf("the connection %s has no credential", c.Name)
		}
		return c.Credential.Resolve()
	}, func(h http.Header, tok string) {
		// "Pass the token using the PRIVATE-TOKEN header (recommended)"
		// (https://docs.gitlab.com/api/rest/authentication/).
		h.Set("PRIVATE-TOKEN", tok)
	})
	cl.Headers = map[string]string{"Accept": "application/json"}
	cl.Message = message
	return &Provider{Base: forge.Base{K: forge.KindGitLab, Caps: Capabilities(), Mat: forge.Experimental, Adds: Additions()}, conn: c, c: cl}, nil
}

// Client returns the connection's client, for the tests.
func (p *Provider) Client() *restkit.Client { return p.c }

// ProposalRefs: refs/merge-requests/<iid>/head
// (https://docs.gitlab.com/user/project/merge_requests/merge_request_troubleshooting/ :
// "the merge request head ref (refs/merge-requests/:iid/head)").
func (p *Provider) ProposalRefs() forge.RefLayout {
	return forge.RefLayout{Prefix: "refs/merge-requests", Suffix: "/head"}
}

func (p *Provider) Identity() forge.Identity         { return identity{p} }
func (p *Provider) Repositories() forge.Repositories { return repos{p} }
func (p *Provider) Content() forge.Content           { return content{p} }
func (p *Provider) Proposals() forge.Proposals       { return proposals{p} }
func (p *Provider) Results() forge.Results           { return results{p} }
func (p *Provider) Rules() forge.Rules               { return rules{p} }
func (p *Provider) Events() forge.Events             { return events{p} }
func (p *Provider) Access() forge.Access             { return access{p} }
func (p *Provider) Releases() forge.Releases         { return releases{p} }
func (p *Provider) Land() forge.Land                 { return land{p: p, baseLand: p.Base.Land()} }

// message reads the host's words from an error body: {"message": "..."} or
// {"error": "..."} (the shapes the documentation's examples show).
func message(body []byte) string {
	var m struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if jsonUnmarshal(body, &m) != nil {
		return ""
	}
	switch v := m.Message.(type) {
	case string:
		return v
	case nil:
	default:
		return fmt.Sprint(v)
	}
	return m.Error
}

// split names the project path and enforces the owner allow-list. The owner
// is the top-level group or user: the first element of the path.
func (p *Provider) split(path string) (string, error) {
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("%w: a project is named group/name, not %q", forge.ErrInvalid, path)
	}
	for _, s := range parts {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("%w: a project is named group/name, not %q", forge.ErrInvalid, path)
		}
	}
	if !restkit.OwnerAllowed(p.conn, parts[0]) {
		return "", fmt.Errorf("%w: the owner %q is not on this connection's allow-list", forge.ErrInvalid, parts[0])
	}
	return path, nil
}

// projectPath is /projects/{url-encoded path}: "make sure that the
// NAMESPACE/PROJECT_PATH is URL-encoded" (https://docs.gitlab.com/api/rest/).
func (p *Provider) projectPath(r forge.Repository) (string, error) {
	path, err := p.split(r.Path)
	if err != nil {
		return "", err
	}
	return "/projects/" + restkit.Esc(path), nil
}

type identity struct{ p *Provider }

// Whoami: GET /user (the authenticated user; the "read_user" scope grants it:
// https://docs.gitlab.com/security/tokens/access_token_scopes/) and, for the
// token's scopes, GET /personal_access_tokens/self
// (https://docs.gitlab.com/api/personal_access_tokens/).
func (i identity) Whoami(ctx context.Context) (forge.Whoami, error) {
	var u struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if _, err := i.p.c.Get(ctx, "/user", &u); err != nil {
		return forge.Whoami{}, err
	}
	w := forge.Whoami{Principal: forge.Principal{Login: u.Username, Name: u.Name, Unlinked: true}, RateRemaining: -1}
	if t, ok := i.p.tokenSelf(ctx); ok {
		w.Scopes = t.Scopes
	}
	return w, nil
}

type tokenSelf struct {
	Scopes    []string `json:"scopes"`
	ExpiresAt *string  `json:"expires_at"`
	Active    bool     `json:"active"`
}

// tokenSelf reads the token's own record; a project or group token has none
// (the documentation covers personal access tokens only), which is not an error.
func (p *Provider) tokenSelf(ctx context.Context) (tokenSelf, bool) {
	var t tokenSelf
	a, err := p.c.Get(ctx, "/personal_access_tokens/self", &t, http.StatusNotFound, http.StatusForbidden)
	if err != nil || a.Status >= 400 {
		return t, false
	}
	return t, true
}

type repos struct{ p *Provider }

type apiProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	Visibility        string `json:"visibility"`
	Archived          bool   `json:"archived"`
	DefaultBranch     string `json:"default_branch"`
	HTTPURL           string `json:"http_url_to_repo"`
	SSHURL            string `json:"ssh_url_to_repo"`
	WebURL            string `json:"web_url"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
	Permissions struct {
		Project *struct {
			Level int `json:"access_level"`
		} `json:"project_access"`
		Group *struct {
			Level int `json:"access_level"`
		} `json:"group_access"`
	} `json:"permissions"`
}

// level is the role the token holds on the project: the larger of the
// project's and the group's (permissions.project_access, group_access).
func (a apiProject) level() int {
	n := 0
	if a.Permissions.Project != nil {
		n = max(n, a.Permissions.Project.Level)
	}
	if a.Permissions.Group != nil {
		n = max(n, a.Permissions.Group.Level)
	}
	return n
}

func topOwner(path string) string { o, _, _ := strings.Cut(path, "/"); return o }

func (p *Provider) repository(a apiProject) forge.Repository {
	v := forge.Public
	switch a.Visibility {
	case "internal":
		v = forge.Internal
	case "private":
		v = forge.Private
	}
	return forge.Repository{Connection: p.conn.ID, Home: a.Namespace.FullPath, Name: a.Path, Path: a.PathWithNamespace, DefaultBranch: a.DefaultBranch,
		Visibility: v, Archived: a.Archived, Clone: forge.CloneAddresses{HTTPS: a.HTTPURL, SSH: a.SSHURL}}
}

// List: GET /projects?membership=true&order_by=path&sort=asc&per_page=50&page=N
// (https://docs.gitlab.com/api/projects/ , "List all projects"), the projects the
// token is a member of, narrowed to the owner allow-list. The cursor is a page number;
// another page follows when the x-next-page header is not empty
// (https://docs.gitlab.com/api/rest/ , Pagination).
func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return forge.Page[forge.Repository]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
		page = n
	}
	var in []apiProject
	a, err := r.p.c.Get(ctx, "/projects?membership=true&order_by=path&sort=asc&per_page=50&page="+strconv.Itoa(page), &in)
	if err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	out := forge.Page[forge.Repository]{Items: []forge.Repository{}}
	for _, p := range in {
		if restkit.OwnerAllowed(r.p.conn, topOwner(p.PathWithNamespace)) {
			out.Items = append(out.Items, r.p.repository(p))
		}
	}
	if a.Header.Get("X-Next-Page") != "" {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

// Get: GET /projects/:id with the URL-encoded path (https://docs.gitlab.com/api/projects/ , "Get a single project").
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	pp, err := r.p.split(path)
	if err != nil {
		return forge.Repository{}, err
	}
	var a apiProject
	st, err := r.p.c.Call(ctx, http.MethodGet, "/projects/"+restkit.Esc(pp), nil, &a, http.StatusNotFound)
	if err != nil {
		return forge.Repository{}, err
	}
	if st == http.StatusNotFound {
		return forge.Repository{}, fmt.Errorf("%w: project %s (or the token cannot see it)", forge.ErrNotFound, path)
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

// Refs: GET /projects/:id/repository/branches and /repository/tags
// (https://docs.gitlab.com/api/branches/ , https://docs.gitlab.com/api/tags/).
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	pp, err := c.p.projectPath(repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Ref
	for _, kind := range []struct{ path, prefix string }{{"/repository/branches", "refs/heads/"}, {"/repository/tags", "refs/tags/"}} {
		for page := 1; page <= 20; page++ {
			var in []struct {
				Name   string `json:"name"`
				Target string `json:"target"`
				Commit struct {
					ID string `json:"id"`
				} `json:"commit"`
			}
			a, err := c.p.c.Get(ctx, pp+kind.path+"?per_page=100&page="+strconv.Itoa(page), &in)
			if err != nil {
				return nil, err
			}
			for _, r := range in {
				out = append(out, forge.Ref{Name: kind.prefix + r.Name, Commit: r.Commit.ID})
			}
			if a.Header.Get("X-Next-Page") == "" {
				break
			}
		}
	}
	return out, nil
}

// Commit: GET /projects/:id/repository/commits/:sha
// (https://docs.gitlab.com/api/commits/ , "Get a single commit").
func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	pp, err := c.p.projectPath(repo)
	if err != nil {
		return forge.Commit{}, err
	}
	var in struct {
		ID         string   `json:"id"`
		ParentIDs  []string `json:"parent_ids"`
		Message    string   `json:"message"`
		AuthorName string   `json:"author_name"`
		Email      string   `json:"author_email"`
		Date       string   `json:"authored_date"`
	}
	if _, err := c.p.c.Get(ctx, pp+"/repository/commits/"+restkit.Esc(id), &in); err != nil {
		return forge.Commit{}, err
	}
	cm := forge.Commit{ID: in.ID, Message: in.Message, Parents: in.ParentIDs,
		Author: forge.Principal{Login: in.Email, Name: in.AuthorName, Unlinked: true}}
	cm.CreatedAt = parseTime(in.Date)
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

// Credential: the connection's token as the password of an HTTPS clone. The
// documentation's example is https://<username>:<token>@gitlab.example.com/... and
// does not say which user name; any non-empty name is used ("launchway").
// The caller delivers it only to the run, never to a log or the workspace's git configuration.
func (a access) Credential(ctx context.Context, repo forge.Repository, write bool) (forge.Credential, error) {
	if _, err := a.p.split(repo.Path); err != nil {
		return forge.Credential{}, err
	}
	tok, err := a.p.c.Token()
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: "launchway", Secret: tok}, nil
}

type rules struct{ p *Provider }

// List: GET /projects/:id/protected_branches/:name for the default branch
// (https://docs.gitlab.com/api/protected_branches/). A branch that is not
// protected answers 404; that is an empty list. A branch whose push access
// level is 0 ("No access") takes changes only through merge requests.
func (r rules) List(ctx context.Context, repo forge.Repository) ([]forge.RefRule, error) {
	pp, err := r.p.projectPath(repo)
	if err != nil {
		return nil, err
	}
	branch := repo.DefaultBranch
	if branch == "" {
		return nil, fmt.Errorf("%w: the project's default branch is not known", forge.ErrInvalid)
	}
	var in struct {
		AllowForcePush bool `json:"allow_force_push"`
		CodeOwners     bool `json:"code_owner_approval_required"`
		Push           []struct {
			Level int `json:"access_level"`
		} `json:"push_access_levels"`
	}
	a, err := r.p.c.Get(ctx, pp+"/protected_branches/"+restkit.Esc(branch), &in, http.StatusNotFound)
	if err != nil {
		return nil, err
	}
	if a.Status == http.StatusNotFound {
		return []forge.RefRule{}, nil
	}
	rule := forge.RefRule{Pattern: "refs/heads/" + branch, Immutable: !in.AllowForcePush}
	rule.ProposalsOnly = len(in.Push) > 0
	for _, l := range in.Push {
		if l.Level != 0 {
			rule.ProposalsOnly = false
		}
	}
	if in.CodeOwners {
		rule.RequiredReviews = 1
	}
	return []forge.RefRule{rule}, nil
}

func (r rules) Set(context.Context, forge.Repository, []forge.RefRule) error {
	return r.p.Caps.Err(forge.CapRules)
}
