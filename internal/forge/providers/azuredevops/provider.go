package azuredevops

import (
	"encoding/base64"
	"encoding/json"
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
	c[forge.CapIdentity] = forge.Declaration{Support: forge.Emulated,
		Reason:     "the host's reference has no call that names the owner of a personal access token (only a profile call on a separate host that a token with the scopes Launchway needs does not have), so the account is shown as the token itself and its use is proved by reading the allowed organizations",
		Workaround: "Nothing to do: the connection test names the token as \"personal-access-token\" and shows what it can read"}
	c[forge.CapEvents] = forge.Declaration{Support: forge.Emulated,
		Reason:     "service hooks carry no signature of the body: a delivery is authenticated by a secret that you put in the subscription as its basic-authentication password or in an HTTP header, which is a shared secret sent with every delivery and weaker than a signature (it does not cover the payload, and it is only as private as the HTTPS channel)",
		Workaround: "Use an HTTPS address for the webhook, give the subscription the connection's webhook secret, and keep polling on (it is the fallback when a hook is missed); nothing in a payload is trusted without asking the host again"}
	c[forge.CapProposalRef] = forge.Declaration{Support: forge.None,
		Reason:     "the reference names no ref that publishes a pull request's head; Launchway reads a pull request's head commit from the API",
		Workaround: "Nothing to do: Sorties for pull requests start from the commit the host names, and a pull request from a fork is read but its head is not fetched with the mirror"}
	c[forge.CapReleases] = forge.Declaration{Support: forge.None,
		Reason:     "Git on Azure DevOps has no release object: it has tags (the product's \"releases\" belong to its pipelines, not to Git)",
		Workaround: "Cut the release in Launchway: the annotated tag is made on the host, and the release page with its notes and files stays in Launchway, which serves the files"}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them",
		Workaround: "Create the repository on Azure DevOps, then choose it in Launchway"}
	c[forge.CapResultsGate] = forge.Declaration{Support: forge.None,
		Reason:     "the host enforces required checks itself, in branch policies (a build validation or a status check on genre/name), and Launchway cannot change them",
		Workaround: "Add a status check policy for the status named launchway/launchway/ci (genre/name) to the branch on Azure DevOps"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.None,
		Reason:     "reviews and votes stay on the host; Launchway shows a pull request and reports on it but does not vote on the host's behalf",
		Workaround: "Review and vote on Azure DevOps"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "merging stays on the host, under its branch policies",
		Workaround: "Complete the pull request on Azure DevOps, or let Launchway set the host's auto-complete (write_back.merge: auto)"}
	c[forge.CapRules] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads the host's branch policies but never changes them",
		Workaround: "Change branch policies on Azure DevOps"}
	return c
}

// Additions is what this host offers beyond the built-in forge.
func Additions() []forge.Addition {
	return []forge.Addition{
		{Key: forge.AdditionAutoMerge, Title: "Auto-complete", Drivable: true,
			What: "The host completes a pull request itself once every required policy passes; Launchway sets it on a write-back pull request (write_back.merge: auto) with the merge strategy of the Flight; cancelling it is done on the host"},
		{Key: "branch_policies", Title: "Branch policies", What: "The host can require a minimum number of approvers, named reviewers, a passing build validation or a status check before a pull request completes; set on the host, and Launchway reads them to refuse a direct write"},
		{Key: "required_reviewers", Title: "Required reviewers", What: "The host adds reviewers to each pull request by path or rule and blocks completion without their approval; set up on the host"},
		{Key: "build_validation", Title: "Build validation", What: "A branch policy can run a build for each pull request and block completion until it passes; Launchway's own statuses can stand in as a status check policy"},
		{Key: "pull_request_statuses", Title: "Statuses on the pull request", What: "Each Sortie and failed step appears as a status on the commit and on the pull request, with a link back to the Sortie"},
	}
}

// Endpoints are the addresses a connection reaches. Collection is "" for the
// cloud service, where the organization is the first element of every path;
// for the server product it is the collection's name, which the base address
// already carries.
type Endpoints struct {
	restkit.Endpoints
	Collection string
}

// EndpointsOf gives the address of a connection: the cloud service for an
// empty base address or https://dev.azure.com, and otherwise the server
// product, whose base address ends with the collection.
func EndpointsOf(base string) (Endpoints, error) {
	var e Endpoints
	if testAPI != "" { // tests of the service run against a fake host
		u, err := url.Parse(testAPI)
		if err != nil {
			return e, err
		}
		e.API, e.Collection = u, testCollection
		e.Hosts = map[string]bool{u.Host: true}
		return e, nil
	}
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" || base == "https://dev.azure.com" {
		e.API = &url.URL{Scheme: "https", Host: "dev.azure.com"}
		e.Hosts = map[string]bool{e.API.Host: true}
		return e, nil
	}
	u, err := restkit.ParseBase(base)
	if err != nil {
		return e, err
	}
	if u.Host == "dev.azure.com" || strings.HasSuffix(u.Host, ".visualstudio.com") {
		return e, fmt.Errorf("the cloud service's base address is https://dev.azure.com (or empty): the organization is named in each repository (organization/project/repository) and in the owners list")
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return e, fmt.Errorf("the server product's base address must end with the collection, for example https://devops.example.com/tfs/DefaultCollection")
	}
	e.API = u
	e.Collection = segs[len(segs)-1]
	e.Hosts = map[string]bool{u.Host: true}
	return e, nil
}

var (
	testAPI        string
	testCollection string
)

// UseTestAPI points every connection at a fake host's address; collection is ""
// for the cloud shape (the organization in each path) and the collection's name
// for the server shape. It returns the function that restores the public
// service. For tests only: nothing in the product calls it.
func UseTestAPI(api, collection string) (restore func()) {
	oa, oc := testAPI, testCollection
	testAPI, testCollection = api, collection
	return func() { testAPI, testCollection = oa, oc }
}

// Provider is the provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	ep   Endpoints
	c    *restkit.Client
}

// Register adds the kind to a registry.
func Register(r *forge.Registry, g *egress.Guard) error {
	return r.Register(forge.KindAzureDevOps, func(c forge.Connection) (forge.Provider, error) { return New(c, g) })
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
	cl := restkit.NewClient(ep.Endpoints, g, func() (string, error) {
		if c.Credential.IsZero() {
			return "", fmt.Errorf("the connection %s has no credential", c.Name)
		}
		return c.Credential.Resolve()
	}, func(h http.Header, tok string) {
		// "Azure DevOps accepts a PAT through an HTTP Basic authentication header. The
		// username can be empty. Base64-encode the string :{PAT}"
		// (https://learn.microsoft.com/en-us/azure/devops/organizations/accounts/use-personal-access-tokens-to-authenticate).
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+tok)))
	})
	cl.Headers = map[string]string{"Accept": "application/json"}
	cl.Message = message
	return &Provider{Base: forge.Base{K: forge.KindAzureDevOps, Caps: Capabilities(), Mat: forge.Experimental, Adds: Additions()}, conn: c, ep: ep, c: cl}
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
func (p *Provider) Land() forge.Land                 { return land{baseLand: p.Base.Land(), p: p} }

// message reads the host's words from an error body: an object with a message.
func message(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	return m.Message
}

// orgAllowed applies the connection's owners list to an organization (on the
// server product, the collection).
func (p *Provider) orgAllowed(org string) bool { return restkit.OwnerAllowed(p.conn, org) }

// split names organization, project and repository, and enforces the owners
// list and, on the server product, the collection of the base address.
func (p *Provider) split(path string) (org, project, repo string, err error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("%w: a repository is named organization/project/repository, not %q", forge.ErrInvalid, path)
	}
	org, project, repo = parts[0], parts[1], parts[2]
	if p.ep.Collection != "" && !strings.EqualFold(org, p.ep.Collection) {
		return "", "", "", fmt.Errorf("%w: this connection is for the collection %q, not %q", forge.ErrInvalid, p.ep.Collection, org)
	}
	if !p.orgAllowed(org) {
		return "", "", "", fmt.Errorf("%w: the organization %q is not on this connection's allow-list", forge.ErrInvalid, org)
	}
	return org, project, repo, nil
}

// orgPath is the path of an organization's own calls: /{organization} on the
// cloud service, nothing on the server product (the base address has it).
func (p *Provider) orgPath(org string) string {
	if p.ep.Collection != "" {
		return ""
	}
	return "/" + restkit.Esc(org)
}

// projectPath is the path in front of every project call.
func (p *Provider) projectPath(org, project string) string {
	return p.orgPath(org) + "/" + restkit.Esc(project)
}

// repoPath is /{organization}/{project}/_apis/git/repositories/{repository}.
func (p *Provider) repoPath(r forge.Repository) (string, error) {
	o, pr, name, err := p.split(r.Path)
	if err != nil {
		return "", err
	}
	return p.projectPath(o, pr) + "/_apis/git/repositories/" + restkit.Esc(name), nil
}

// page is the wrapper of a collection: count and value.
type page[T any] struct {
	Count int `json:"count"`
	Value []T `json:"value"`
}

// owners are the organizations a connection lists: its allow-list, or on the
// server product its collection. The cloud service has no call for "my
// organizations" among the calls Launchway uses, so the allow-list is needed.
func (p *Provider) owners() ([]string, error) {
	if p.ep.Collection != "" {
		if len(p.conn.Owners) > 0 && !p.orgAllowed(p.ep.Collection) {
			return nil, nil
		}
		return []string{p.ep.Collection}, nil
	}
	if len(p.conn.Owners) == 0 {
		return nil, fmt.Errorf("%w: on the cloud service a connection needs its owners list (the organizations it may use): the host's calls Launchway uses name no organization of a token", forge.ErrInvalid)
	}
	return p.conn.Owners, nil
}
