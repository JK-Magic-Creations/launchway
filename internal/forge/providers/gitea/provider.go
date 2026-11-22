package gitea

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
		forge.CapAccess, forge.CapProposals, forge.CapResults, forge.CapRules,
		forge.CapTags, forge.CapReleases, forge.CapCommitFile, forge.CapProposalNote, forge.CapProposalRef} {
		c[f] = forge.Declaration{Support: forge.Native}
	}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them",
		Workaround: "Create the repository on the Git host, then choose it in Launchway"}
	c[forge.CapDrafts] = forge.Declaration{Support: forge.None,
		Reason:     "the host's API has no draft option when creating or editing a pull request, so Launchway reads a draft but does not make or change one",
		Workaround: "Mark the pull request as draft or ready on the Git host"}
	c[forge.CapResultsGate] = forge.Declaration{Support: forge.None,
		Reason:     "the host enforces required status checks itself, in its branch protection, and Launchway cannot change them",
		Workaround: "Mark the Launchway status contexts as required in the host's branch protection"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.None,
		Reason:     "reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf",
		Workaround: "Review on the Git host"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "merging stays on the host, under its branch protection",
		Workaround: "Merge the pull request on the Git host"}
	c[forge.CapAutoLand] = forge.Declaration{Support: forge.Native, Reason: "the host's own scheduled merge (merge when checks succeed), switched on for a pull request by Launchway; the merge itself stays the host's"}
	c[forge.CapRules] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads the host's branch protection but never changes it",
		Workaround: "Change branch protection on the Git host"}
	return c
}

// Additions is what this host offers beyond the built-in forge.
func Additions() []forge.Addition {
	return []forge.Addition{
		{Key: forge.AdditionAutoMerge, Title: "Merge when checks succeed", Drivable: true,
			What: "The host merges a pull request by itself once its required checks pass (a scheduled merge); Launchway schedules it for a write-back pull request (write_back.merge: auto) and can cancel it"},
		{Key: "branch_protection", Title: "Branch protection rules", What: "The host enforces who may push or merge, required approvals and required status checks; Launchway reads them to refuse a direct write and its statuses can be made required"},
		{Key: "status_page", Title: "Statuses on the pull request", What: "Each Sortie and failed step appears as a commit status on the pull request, with a link back to the Sortie"},
	}
}

// Endpoints are the addresses a connection reaches.
type Endpoints = restkit.Endpoints

// EndpointsOf derives the API address from the install's base address: the API
// is at <base>/api/v1 (basePath "/api/v1" in the host's OpenAPI file). Gitea
// and Forgejo are self-managed, so the base address is required.
func EndpointsOf(base string) (Endpoints, error) {
	var e Endpoints
	b := strings.TrimSuffix(strings.TrimSpace(base), "/")
	if b == "" {
		return e, fmt.Errorf("this kind of host is self-managed: give the address of the install as the base address")
	}
	u, err := restkit.ParseBase(b)
	if err != nil {
		return e, err
	}
	root := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api/v1")
	e.API = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: root + "/api/v1"}
	e.Hosts = map[string]bool{e.API.Host: true}
	return e, nil
}

// Provider is the provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	c    *restkit.Client
}

// Register adds both kinds to a registry.
func Register(r *forge.Registry, g *egress.Guard) error {
	for _, k := range []forge.Kind{forge.KindGitea, forge.KindForgejo} {
		if err := r.Register(k, func(c forge.Connection) (forge.Provider, error) { return New(c, g) }); err != nil {
			return err
		}
	}
	return nil
}

// New makes the provider of a connection of kind gitea or forgejo.
func New(c forge.Connection, g *egress.Guard) (*Provider, error) {
	if c.Kind != forge.KindGitea && c.Kind != forge.KindForgejo {
		return nil, fmt.Errorf("%w: connection: kind %q is not gitea or forgejo", forge.ErrInvalid, c.Kind)
	}
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
		// "API tokens must be prepended with "token" followed by a space"
		// (securityDefinitions.AuthorizationHeaderToken of the OpenAPI file).
		h.Set("Authorization", "token "+tok)
	})
	cl.Headers = map[string]string{"Accept": "application/json"}
	cl.Message = message
	return &Provider{Base: forge.Base{K: c.Kind, Caps: Capabilities(), Mat: forge.Experimental, Adds: Additions()}, conn: c, c: cl}, nil
}

// Client returns the connection's client, for the tests.
func (p *Provider) Client() *restkit.Client { return p.c }

// ProposalRefs: refs/pull/<n>/head, the shape Launchway has always read
// (the host is the one the layout was named for).
func (p *Provider) ProposalRefs() forge.RefLayout { return forge.PullRefLayout }

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

// message reads the host's words from an error body: APIError has "message" and "url".
func message(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if jsonUnmarshal(body, &m) != nil {
		return ""
	}
	return m.Message
}

// split names owner and repository from a path and enforces the owner allow-list.
func (p *Provider) split(path string) (owner, repo string, err error) {
	owner, repo, ok := strings.Cut(strings.Trim(path, "/"), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("%w: a repository is named owner/name, not %q", forge.ErrInvalid, path)
	}
	if !restkit.OwnerAllowed(p.conn, owner) {
		return "", "", fmt.Errorf("%w: the owner %q is not on this connection's allow-list", forge.ErrInvalid, owner)
	}
	return owner, repo, nil
}

// repoPath is /repos/{owner}/{repo}.
func (p *Provider) repoPath(r forge.Repository) (string, error) {
	o, n, err := p.split(r.Path)
	if err != nil {
		return "", err
	}
	return "/repos/" + restkit.Esc(o) + "/" + restkit.Esc(n), nil
}

// more reports whether another page follows: the Link header has rel="next"
// (https://docs.gitea.com/development/api-usage , Pagination).
func more(a restkit.Answer) bool { return strings.Contains(a.Header.Get("Link"), `rel="next"`) }

// pageLimit is the largest page the host gives by default (MAX_RESPONSE_ITEMS defaults to 50).
const pageLimit = 50

type identity struct{ p *Provider }

type apiUser struct {
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	ID       int64  `json:"id"`
}

// Whoami: GET /user (userGetCurrent). The host has no call that tells a token's scopes.
func (i identity) Whoami(ctx context.Context) (forge.Whoami, error) {
	var u apiUser
	if _, err := i.p.c.Get(ctx, "/user", &u); err != nil {
		return forge.Whoami{}, err
	}
	return forge.Whoami{Principal: forge.Principal{Login: u.Login, Name: u.FullName, Unlinked: true}, RateRemaining: -1}, nil
}

type repos struct{ p *Provider }

type apiRepo struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions struct {
		Admin bool `json:"admin"`
		Push  bool `json:"push"`
		Pull  bool `json:"pull"`
	} `json:"permissions"`
}

func (p *Provider) repository(a apiRepo) forge.Repository {
	v := forge.Public
	if a.Private {
		v = forge.Private
	}
	return forge.Repository{Connection: p.conn.ID, Home: a.Owner.Login, Name: a.Name, Path: a.FullName, DefaultBranch: a.DefaultBranch,
		Visibility: v, Archived: a.Archived, Clone: forge.CloneAddresses{HTTPS: a.CloneURL, SSH: a.SSHURL}}
}

// search is GET /repos/search?uid=<the account>&private=true&sort=alpha&page=N&limit=50 (repoSearch): the
// repositories the account owns or contributes to.
func (p *Provider) search(ctx context.Context, page int) ([]apiRepo, bool, error) {
	var me apiUser
	if _, err := p.c.Get(ctx, "/user", &me); err != nil {
		return nil, false, err
	}
	var out struct {
		Data []apiRepo `json:"data"`
	}
	a, err := p.c.Get(ctx, "/repos/search?uid="+strconv.FormatInt(me.ID, 10)+"&private=true&sort=alpha&limit="+strconv.Itoa(pageLimit)+"&page="+strconv.Itoa(page), &out)
	if err != nil {
		return nil, false, err
	}
	return out.Data, more(a) || len(out.Data) == pageLimit, nil
}

// List: the repositories the token's account owns or contributes to, narrowed
// to the owner allow-list. The cursor is a page number.
func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return forge.Page[forge.Repository]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
		page = n
	}
	in, more, err := r.p.search(ctx, page)
	if err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	out := forge.Page[forge.Repository]{Items: []forge.Repository{}}
	for _, a := range in {
		if restkit.OwnerAllowed(r.p.conn, a.Owner.Login) {
			out.Items = append(out.Items, r.p.repository(a))
		}
	}
	if more {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

// Get: GET /repos/{owner}/{repo} (repoGet).
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	o, n, err := r.p.split(path)
	if err != nil {
		return forge.Repository{}, err
	}
	var a apiRepo
	st, err := r.p.c.Call(ctx, http.MethodGet, "/repos/"+restkit.Esc(o)+"/"+restkit.Esc(n), nil, &a, http.StatusNotFound)
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

// Refs: GET /repos/{o}/{r}/branches and /tags (repoListBranches, repoListTags), paged with page and limit.
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Ref
	for _, kind := range []struct{ path, prefix string }{{"/branches", "refs/heads/"}, {"/tags", "refs/tags/"}} {
		for page := 1; page <= 40; page++ {
			var in []struct {
				Name   string `json:"name"`
				Commit struct {
					ID  string `json:"id"`
					SHA string `json:"sha"`
				} `json:"commit"`
			}
			a, err := c.p.c.Get(ctx, rp+kind.path+"?limit="+strconv.Itoa(pageLimit)+"&page="+strconv.Itoa(page), &in)
			if err != nil {
				return nil, err
			}
			for _, r := range in {
				sha := r.Commit.ID // a branch's commit (PayloadCommit) has id
				if sha == "" {
					sha = r.Commit.SHA // a tag's commit (CommitMeta) has sha
				}
				out = append(out, forge.Ref{Name: kind.prefix + r.Name, Commit: sha})
			}
			if !more(a) {
				break
			}
		}
	}
	return out, nil
}

// Commit: GET /repos/{o}/{r}/git/commits/{sha} (repoGetSingleCommit).
func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return forge.Commit{}, err
	}
	var in struct {
		SHA     string `json:"sha"`
		Created string `json:"created"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		} `json:"commit"`
	}
	if _, err := c.p.c.Get(ctx, rp+"/git/commits/"+restkit.Esc(id), &in); err != nil {
		return forge.Commit{}, err
	}
	cm := forge.Commit{ID: in.SHA, Message: in.Commit.Message, CreatedAt: parseTime(in.Created),
		Author: forge.Principal{Login: in.Commit.Author.Email, Name: in.Commit.Author.Name, Unlinked: true}}
	for _, par := range in.Parents {
		cm.Parents = append(cm.Parents, par.SHA)
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

// Credential: the token as the password of an HTTPS clone, with the account's
// own login as the user name (GET /user). The documentation does not describe
// git over HTTPS with a token; this is what the host's own clone address
// accepts. The caller delivers it only to the run.
func (a access) Credential(ctx context.Context, repo forge.Repository, write bool) (forge.Credential, error) {
	if _, _, err := a.p.split(repo.Path); err != nil {
		return forge.Credential{}, err
	}
	var u apiUser
	if _, err := a.p.c.Get(ctx, "/user", &u); err != nil {
		return forge.Credential{}, err
	}
	tok, err := a.p.c.Token()
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: u.Login, Secret: tok}, nil
}

type rules struct{ p *Provider }

// List: GET /repos/{o}/{r}/branch_protections (repoListBranchProtection),
// keeping the rule whose rule_name (or branch_name) is the default branch or a
// pattern that matches it. A rule that disables push, or limits it to a
// whitelist, takes changes through pull requests only.
func (r rules) List(ctx context.Context, repo forge.Repository) ([]forge.RefRule, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	branch := repo.DefaultBranch
	if branch == "" {
		return nil, fmt.Errorf("%w: the repository's default branch is not known", forge.ErrInvalid)
	}
	var in []struct {
		RuleName     string   `json:"rule_name"`
		BranchName   string   `json:"branch_name"`
		EnablePush   bool     `json:"enable_push"`
		PushList     bool     `json:"enable_push_whitelist"`
		ForcePush    *bool    `json:"enable_force_push"`
		StatusCheck  bool     `json:"enable_status_check"`
		Contexts     []string `json:"status_check_contexts"`
		RequiredAppr int      `json:"required_approvals"`
	}
	if _, err := r.p.c.Get(ctx, rp+"/branch_protections", &in); err != nil {
		return nil, err
	}
	out := []forge.RefRule{}
	for _, b := range in {
		name := b.RuleName
		if name == "" {
			name = b.BranchName
		}
		if !matches(name, branch) {
			continue
		}
		rule := forge.RefRule{Pattern: "refs/heads/" + branch, ProposalsOnly: !b.EnablePush || b.PushList, RequiredReviews: b.RequiredAppr}
		if b.ForcePush != nil {
			rule.Immutable = !*b.ForcePush
		}
		if b.StatusCheck {
			rule.RequiredResults = b.Contexts
		}
		out = append(out, rule)
	}
	return out, nil
}

func (r rules) Set(context.Context, forge.Repository, []forge.RefRule) error {
	return r.p.Caps.Err(forge.CapRules)
}
