package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
)

const workaroundHost = "Do this on the Git host itself"

// Capabilities declares what this provider does. Anything not Native says why
// and what to do instead; the screens print these words.
func Capabilities() forge.Capabilities {
	c := forge.NoCapabilities("this is not built for this host yet", workaroundHost)
	for _, f := range []forge.Capability{forge.CapIdentity, forge.CapRepositories, forge.CapContent, forge.CapEvents,
		forge.CapAccess, forge.CapProposals, forge.CapDrafts, forge.CapResults, forge.CapRules,
		forge.CapTags, forge.CapReleases, forge.CapCommitFile, forge.CapProposalNote} {
		c[f] = forge.Declaration{Support: forge.Native}
	}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them",
		Workaround: "Create the repository on the Git host, then choose it in Launchway"}
	c[forge.CapProposalRef] = forge.Declaration{Support: forge.Native}
	c[forge.CapResultsGate] = forge.Declaration{Support: forge.None,
		Reason:     "the host enforces required checks itself, in its branch protection, and Launchway cannot change them",
		Workaround: "Mark the Launchway status contexts as required in the host's branch protection settings"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.None,
		Reason:     "reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf",
		Workaround: "Review on the Git host"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "merging stays on the host, under its branch protection",
		Workaround: "Merge the pull request on the Git host"}
	c[forge.CapRules] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads the host's branch protection but never changes it",
		Workaround: "Change branch protection on the Git host"}
	return c
}

// RefLayout of pull request heads on the host: refs/pull/<n>/head
// (https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/reviewing-changes-in-pull-requests/checking-out-pull-requests-locally).

// Provider is the provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	c    *Client
	ep   Endpoints
}

// Register adds the kind to a registry. The guard is the product's egress
// allow-list (forge.mirror_allow / egress.allow).
func Register(r *forge.Registry, g *egress.Guard) error {
	return r.Register(forge.KindGitHub, func(c forge.Connection) (forge.Provider, error) { return New(c, g) })
}

// New makes the provider of a connection.
func New(c forge.Connection, g *egress.Guard) (*Provider, error) {
	ep, err := EndpointsOf(c.BaseURL, c.UploadURL)
	if err != nil {
		return nil, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
	}
	cl := NewClient(ep, g, func() (string, error) {
		if c.Credential.IsZero() {
			return "", fmt.Errorf("the connection %s has no credential", c.Name)
		}
		return c.Credential.Resolve()
	})
	return &Provider{Base: forge.Base{K: forge.KindGitHub, Caps: Capabilities(), Mat: forge.Experimental}, conn: c, c: cl, ep: ep}, nil
}

// NewWith makes a provider over explicit endpoints and a token reader: for the
// release publisher of a push mirror, whose address and credential are the
// mirror's and not a connection's. Every other behaviour is the connection's.
func NewWith(c forge.Connection, ep Endpoints, g *egress.Guard, token func() (string, error)) *Provider {
	return &Provider{Base: forge.Base{K: forge.KindGitHub, Caps: Capabilities(), Mat: forge.Experimental}, conn: c, c: NewClient(ep, g, token), ep: ep}
}

// Client returns the connection's client, for the probe and the tests.
func (p *Provider) Client() *Client { return p.c }

// ProposalRefs: refs/pull/<n>/head.
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

// split names owner and repository from a path and enforces the allow-list.
func (p *Provider) split(path string) (owner, repo string, err error) {
	owner, repo, ok := strings.Cut(strings.Trim(path, "/"), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("%w: a repository is named owner/name, not %q", forge.ErrInvalid, path)
	}
	if !p.ownerAllowed(owner) {
		return "", "", fmt.Errorf("%w: the owner %q is not on this connection's allow-list", forge.ErrInvalid, owner)
	}
	return owner, repo, nil
}

func (p *Provider) ownerAllowed(owner string) bool {
	if len(p.conn.Owners) == 0 {
		return true
	}
	for _, o := range p.conn.Owners {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	return false
}

// repoPath is /repos/{owner}/{repo}.
func (p *Provider) repoPath(r forge.Repository) (string, error) {
	o, n, err := p.split(r.Path)
	if err != nil {
		return "", err
	}
	return "/repos/" + esc(o) + "/" + esc(n), nil
}

// ---- identity ----

type identity struct{ p *Provider }

// Whoami: GET /user (https://docs.github.com/en/rest/users/users#get-the-authenticated-user).
// The rate limit comes from the x-ratelimit-remaining header.
func (i identity) Whoami(ctx context.Context) (forge.Whoami, error) {
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	a, err := i.p.c.Header(ctx, "/user", &u)
	if err != nil {
		return forge.Whoami{}, err
	}
	w := forge.Whoami{Principal: forge.Principal{Login: u.Login, Name: u.Name, Unlinked: true}, RateRemaining: -1}
	if n, err := strconv.Atoi(a.Header.Get("X-RateLimit-Remaining")); err == nil {
		w.RateRemaining = n
	}
	if s := a.Header.Get("X-OAuth-Scopes"); s != "" {
		for _, sc := range strings.Split(s, ",") {
			w.Scopes = append(w.Scopes, strings.TrimSpace(sc))
		}
	}
	return w, nil
}

// ---- repositories ----

type repos struct{ p *Provider }

type apiRepo struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	Visibility    string `json:"visibility"`
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
	switch {
	case a.Visibility == "internal":
		v = forge.Internal
	case a.Private || a.Visibility == "private":
		v = forge.Private
	}
	return forge.Repository{Connection: p.conn.ID, Home: a.Owner.Login, Name: a.Name, Path: a.FullName, DefaultBranch: a.DefaultBranch,
		Visibility: v, Archived: a.Archived, Clone: forge.CloneAddresses{HTTPS: a.CloneURL, SSH: a.SSHURL}}
}

// List: GET /user/repos (https://docs.github.com/en/rest/repos/repos#list-repositories-for-the-authenticated-user),
// the repositories the token reaches, narrowed to the connection's owner allow-list. The cursor is a page number.
func (r repos) List(ctx context.Context, cursor string) (forge.Page[forge.Repository], error) {
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return forge.Page[forge.Repository]{}, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
		page = n
	}
	var in []apiRepo
	more, err := r.p.c.CallPage(ctx, "/user/repos?per_page=50&sort=full_name&page="+strconv.Itoa(page), &in)
	if err != nil {
		return forge.Page[forge.Repository]{}, err
	}
	out := forge.Page[forge.Repository]{Items: []forge.Repository{}}
	for _, a := range in {
		if r.p.ownerAllowed(a.Owner.Login) {
			out.Items = append(out.Items, r.p.repository(a))
		}
	}
	if more {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

// Get: GET /repos/{owner}/{repo} (https://docs.github.com/en/rest/repos/repos#get-a-repository).
func (r repos) Get(ctx context.Context, path string) (forge.Repository, error) {
	o, n, err := r.p.split(path)
	if err != nil {
		return forge.Repository{}, err
	}
	var a apiRepo
	st, err := r.p.c.Call(ctx, http.MethodGet, "/repos/"+esc(o)+"/"+esc(n), nil, &a, http.StatusNotFound)
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

// ---- content ----

type content struct{ p *Provider }

// Refs: GET /repos/{o}/{r}/git/matching-refs/heads and /tags
// (https://docs.github.com/en/rest/git/refs#list-matching-references).
func (c content) Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Ref
	for _, kind := range []string{"heads", "tags"} {
		var refs []struct {
			Ref    string `json:"ref"`
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if _, err := c.p.c.Call(ctx, http.MethodGet, rp+"/git/matching-refs/"+kind, nil, &refs); err != nil {
			return nil, err
		}
		for _, r := range refs {
			out = append(out, forge.Ref{Name: r.Ref, Commit: r.Object.SHA})
		}
	}
	return out, nil
}

// Commit: GET /repos/{o}/{r}/commits/{ref} (https://docs.github.com/en/rest/commits/commits#get-a-commit).
func (c content) Commit(ctx context.Context, repo forge.Repository, id string) (forge.Commit, error) {
	rp, err := c.p.repoPath(repo)
	if err != nil {
		return forge.Commit{}, err
	}
	var in struct {
		SHA     string `json:"sha"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name  string    `json:"name"`
				Email string    `json:"email"`
				Date  time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if _, err := c.p.c.Call(ctx, http.MethodGet, rp+"/commits/"+esc(id), nil, &in); err != nil {
		return forge.Commit{}, err
	}
	cm := forge.Commit{ID: in.SHA, Message: in.Commit.Message, CreatedAt: in.Commit.Author.Date.UTC(),
		Author: forge.Principal{Login: in.Commit.Author.Email, Name: in.Commit.Author.Name, Unlinked: true}}
	for _, par := range in.Parents {
		cm.Parents = append(cm.Parents, par.SHA)
	}
	return cm, nil
}

// Tree, Blob and Compare are computed by Launchway from its own clone for
// every provider, as the contract says.
func (c content) Tree(context.Context, forge.Repository, string, string) ([]forge.TreeEntry, error) {
	return nil, &forge.ErrUnsupported{Capability: forge.CapContent, Reason: "Launchway reads files from its own clone", Workaround: "Browse the clone in Launchway"}
}
func (c content) Blob(context.Context, forge.Repository, string, string, int64) ([]byte, error) {
	return nil, &forge.ErrUnsupported{Capability: forge.CapContent, Reason: "Launchway reads files from its own clone", Workaround: "Browse the clone in Launchway"}
}
func (c content) Compare(context.Context, forge.Repository, string, string) (forge.Comparison, error) {
	return forge.Comparison{}, &forge.ErrUnsupported{Capability: forge.CapContent, Reason: "Launchway compares from its own clone", Workaround: "Compare in Launchway"}
}

// ---- access ----

type access struct{ p *Provider }

func (a access) CloneAddresses(_ context.Context, repo forge.Repository) (forge.CloneAddresses, error) {
	return repo.Clone, nil
}

// Credential: the connection's token, as the user name x-access-token
// (https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation#using-an-installation-access-token-to-authenticate).
// The token is not short-lived until an app-installation credential is
// connected (auth_kind app); the caller delivers it only to the run, never to a
// log or the workspace's git configuration.
func (a access) Credential(ctx context.Context, repo forge.Repository, write bool) (forge.Credential, error) {
	if _, _, err := a.p.split(repo.Path); err != nil {
		return forge.Credential{}, err
	}
	tok, err := a.p.c.Token()
	if err != nil {
		return forge.Credential{}, err
	}
	return forge.Credential{Username: "x-access-token", Secret: tok}, nil
}

// ---- rules ----

type rules struct{ p *Provider }

// List: GET /repos/{o}/{r}/branches/{branch}/protection for the default branch
// (https://docs.github.com/en/rest/branches/branch-protection#get-branch-protection).
// An unprotected branch answers 404 "Branch not protected"; that is an empty list.
func (r rules) List(ctx context.Context, repo forge.Repository) ([]forge.RefRule, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return nil, err
	}
	branch := repo.DefaultBranch
	if branch == "" {
		return nil, fmt.Errorf("%w: the repository's default branch is not known", forge.ErrInvalid)
	}
	var in struct {
		RequiredStatusChecks *struct {
			Contexts []string `json:"contexts"`
		} `json:"required_status_checks"`
		RequiredPullRequestReviews *struct {
			Count int `json:"required_approving_review_count"`
		} `json:"required_pull_request_reviews"`
		AllowForcePushes struct {
			Enabled bool `json:"enabled"`
		} `json:"allow_force_pushes"`
	}
	a, err := r.p.c.Header(ctx, rp+"/branches/"+escPath(branch)+"/protection", &in)
	if err != nil {
		if a.Status == http.StatusNotFound && strings.Contains(a.Message(), "not protected") {
			return []forge.RefRule{}, nil
		}
		return nil, err
	}
	rule := forge.RefRule{Pattern: "refs/heads/" + branch, Immutable: !in.AllowForcePushes.Enabled}
	if in.RequiredStatusChecks != nil {
		rule.RequiredResults = in.RequiredStatusChecks.Contexts
	}
	if in.RequiredPullRequestReviews != nil {
		rule.ProposalsOnly = true
		rule.RequiredReviews = in.RequiredPullRequestReviews.Count
	}
	return []forge.RefRule{rule}, nil
}

func (r rules) Set(context.Context, forge.Repository, []forge.RefRule) error {
	return r.p.Caps.Err(forge.CapRules)
}

// Protected says whether pushing straight to the branch is refused by the
// host's protection (a pull request is required), so a direct write-back can
// be refused with a plain message that names the pull-request mode.
func Protected(rs []forge.RefRule, branch string) bool {
	for _, r := range rs {
		if r.Pattern == "refs/heads/"+branch && (r.ProposalsOnly || len(r.RequiredResults) > 0) {
			return true
		}
	}
	return false
}
