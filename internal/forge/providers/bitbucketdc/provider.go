package bitbucketdc

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

// Capabilities declares what this provider does.
func Capabilities() forge.Capabilities {
	c := forge.NoCapabilities("this is not built for this host yet", "Do this on the Git host itself")
	for _, f := range []forge.Capability{forge.CapIdentity, forge.CapRepositories, forge.CapContent, forge.CapEvents,
		forge.CapAccess, forge.CapProposals, forge.CapDrafts, forge.CapResults, forge.CapRules, forge.CapAutoLand,
		forge.CapTags, forge.CapCommitFile, forge.CapProposalNote} {
		c[f] = forge.Declaration{Support: forge.Native}
	}
	c[forge.CapProposalRef] = forge.Declaration{Support: forge.None,
		Reason:     "the host's reference names no ref for a pull request's head; Launchway reads a pull request's head commit from the API and fetches it by its hash",
		Workaround: "Nothing to do: Sorties for pull requests start from the commit the host names"}
	c[forge.CapReleases] = forge.Declaration{Support: forge.None,
		Reason:     "Bitbucket has no release object: it has tags, and a tag carries no release notes or files",
		Workaround: "Cut the release in Launchway: the tag is made on Bitbucket, and the release page with its notes and files stays in Launchway, which serves the files"}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them",
		Workaround: "Create the repository on Bitbucket, then choose it in Launchway"}
	c[forge.CapResultsGate] = forge.Declaration{Support: forge.None,
		Reason:     "the host enforces required builds itself, as a merge check of the repository, and Launchway cannot change them",
		Workaround: "Add the \"required builds\" merge check on the repository's settings on Bitbucket"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.None,
		Reason:     "reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf",
		Workaround: "Review on Bitbucket"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "merging stays on the host, under its merge checks",
		Workaround: "Merge the pull request on Bitbucket, or let Launchway ask for the host's auto-merge (write_back.merge: auto)"}
	c[forge.CapRules] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads the host's branch permissions and merge checks but never changes them",
		Workaround: "Change branch permissions and merge checks on Bitbucket"}
	return c
}

// Additions is what this host offers beyond the built-in forge.
func Additions() []forge.Addition {
	return []forge.Addition{
		{Key: forge.AdditionAutoMerge, Title: "Auto-merge", Drivable: true,
			What: "The host merges a pull request itself once every merge check passes; Launchway asks for it on a write-back pull request (write_back.merge: auto), when the repository allows auto-merge in its settings"},
		{Key: "merge_checks", Title: "Merge checks", What: "The host can require approvals, successful builds and completed tasks before a pull request merges; set on the repository, and Launchway reads them to refuse a direct write"},
		{Key: "default_reviewers", Title: "Default reviewers", What: "The host adds reviewers to each new pull request by rule; set up on the host"},
		{Key: "build_statuses", Title: "Builds on the pull request", What: "Each Sortie and failed step appears as a build status on the commit and its pull request, with a link back to the Sortie"},
	}
}

// Endpoints are the addresses a connection reaches.
type Endpoints = restkit.Endpoints

// EndpointsOf gives the REST address of an install: <base>/rest (the install
// may live below a context path, which the base keeps). The base address is
// required: this product is self-managed. The address of Bitbucket Cloud is
// refused with the kind to use for it.
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
	if h := strings.ToLower(u.Hostname()); h == "bitbucket.org" || h == "api.bitbucket.org" {
		return e, fmt.Errorf("%s is Bitbucket Cloud, which has a different API: use the kind bitbucket for it", h)
	}
	root := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/rest")
	e.API = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: root + "/rest"}
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
	return r.Register(forge.KindBitbucketDataCenter, func(c forge.Connection) (forge.Provider, error) { return New(c, g) })
}

// New makes the provider of a connection.
func New(c forge.Connection, g *egress.Guard) (*Provider, error) {
	ep, err := EndpointsOf(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
	}
	return NewAt(c, ep, g), nil
}

// NewAt makes a provider over explicit endpoints.
func NewAt(c forge.Connection, ep Endpoints, g *egress.Guard) *Provider {
	cl := restkit.NewClient(ep, g, func() (string, error) {
		if c.Credential.IsZero() {
			return "", fmt.Errorf("the connection %s has no credential", c.Name)
		}
		return c.Credential.Resolve()
	}, func(h http.Header, tok string) {
		// An HTTP access token as a Bearer token: "curl -H 'Authorization: Bearer TOKEN'
		// https://example.com/rest/api/latest/..." (https://confluence.atlassian.com/bitbucketserver/http-access-tokens-939515499.html).
		h.Set("Authorization", "Bearer "+tok)
	})
	cl.Headers = map[string]string{"Accept": "application/json"}
	cl.Message = message
	return &Provider{Base: forge.Base{K: forge.KindBitbucketDataCenter, Caps: Capabilities(), Mat: forge.Experimental, Adds: Additions()}, conn: c, c: cl}
}

// Client returns the connection's client, for the tests.
func (p *Provider) Client() *restkit.Client { return p.c }

// root is the install's own address without /rest (the web address, where clone addresses live).
func (p *Provider) root() string {
	return strings.TrimSuffix(strings.TrimSuffix(p.c.EP.API.String(), "/"), "/rest")
}

func (p *Provider) Identity() forge.Identity         { return identity{p} }
func (p *Provider) Repositories() forge.Repositories { return repos{p} }
func (p *Provider) Content() forge.Content           { return content{p} }
func (p *Provider) Proposals() forge.Proposals       { return proposals{p} }
func (p *Provider) Results() forge.Results           { return results{p} }
func (p *Provider) Land() forge.Land                 { return land{p: p, baseLand: p.Base.Land()} }
func (p *Provider) Rules() forge.Rules               { return rules{p} }
func (p *Provider) Events() forge.Events             { return events{p} }
func (p *Provider) Access() forge.Access             { return access{p} }
func (p *Provider) Releases() forge.Releases         { return releases{p} }

// message reads the host's words from an error body: {"errors": [{"message": "..."}]}
// (the error answers of every operation in the reference).
func message(body []byte) string {
	var m struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if jsonUnmarshal(body, &m) != nil || len(m.Errors) == 0 {
		return ""
	}
	return m.Errors[0].Message
}

// split names project key and repository slug and enforces the allow-list of project keys.
func (p *Provider) split(path string) (key, slug string, err error) {
	key, slug, ok := strings.Cut(strings.Trim(path, "/"), "/")
	if !ok || key == "" || slug == "" || strings.Contains(slug, "/") {
		return "", "", fmt.Errorf("%w: a repository is named PROJECTKEY/name, not %q", forge.ErrInvalid, path)
	}
	if !restkit.OwnerAllowed(p.conn, key) {
		return "", "", fmt.Errorf("%w: the project %q is not on this connection's allow-list", forge.ErrInvalid, key)
	}
	return key, slug, nil
}

// repoPath is /api/latest/projects/{projectKey}/repos/{repositorySlug}.
func (p *Provider) repoPath(r forge.Repository) (string, error) {
	return p.repoPathOf(r.Path)
}

func (p *Provider) repoPathOf(path string) (string, error) {
	k, s, err := p.split(path)
	if err != nil {
		return "", err
	}
	return "/api/latest/projects/" + restkit.Esc(k) + "/repos/" + restkit.Esc(s), nil
}

// page is the wrapper of every collection (size, limit, isLastPage, start, nextPageStart, values).
type page[T any] struct {
	Values        []T  `json:"values"`
	IsLastPage    bool `json:"isLastPage"`
	NextPageStart int  `json:"nextPageStart"`
}
