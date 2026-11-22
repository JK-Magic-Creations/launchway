package codecommit

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

// Capabilities declares what this provider does.
func Capabilities() forge.Capabilities {
	c := forge.NoCapabilities("this is not built for this host yet", "Do this on the Git host itself")
	for _, f := range []forge.Capability{forge.CapRepositories, forge.CapAccess, forge.CapProposals, forge.CapCommitFile, forge.CapProposalNote} {
		c[f] = forge.Declaration{Support: forge.Native}
	}
	c[forge.CapIdentity] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway calls no service that names the owner of an access key (it needs no extra permission for it), so the account is shown as the access key (masked) or the assumed role session, and its use is proved by listing the repositories",
		Workaround: "Nothing to do: the connection test shows the key or role session and what it can read"}
	c[forge.CapContent] = forge.Declaration{Support: forge.Emulated,
		Reason:     "the API lists branches and reads a commit but has no call that lists Git tags, and Launchway reads files and diffs from its own clone",
		Workaround: "Nothing to do: tags and files come from the clone Launchway keeps, and the branches and commits from the API"}
	c[forge.CapEvents] = forge.Declaration{Support: forge.Emulated,
		Reason:     "CodeCommit has no webhook to an address of your choice: its events leave through AWS's own event and notification services (EventBridge, SNS)",
		Workaround: "Nothing to do: Launchway polls the repository and its pull requests (this is the mode of every connection of this kind). To react faster than the poll, route the host's events to a service of yours that calls Launchway's API"}
	c[forge.CapProposalRef] = forge.Declaration{Support: forge.None,
		Reason:     "CodeCommit publishes no ref for a pull request's head; the head is the source branch, read from the pull request",
		Workaround: "Nothing to do: Sorties for pull requests start from the commit the host names for the source branch"}
	c[forge.CapDrafts] = forge.Declaration{Support: forge.None,
		Reason:     "CodeCommit pull requests have no draft state",
		Workaround: "Open the pull request when it is ready, or keep the branch without a pull request until then"}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them",
		Workaround: "Create the repository in CodeCommit, then choose it in Launchway"}
	c[forge.CapResults] = forge.Declaration{Support: forge.None,
		Reason:     "CodeCommit has no commit status or check API for another system to write",
		Workaround: "Launchway reports each result in the pull request's one summary comment, which it updates in place; a commit with no pull request has nowhere to say it and its result is read in Launchway"}
	c[forge.CapResultsGate] = forge.Declaration{Support: forge.None,
		Reason:     "CodeCommit's approval rules count approvals by people or roles, not results from another system, and Launchway cannot make a result a condition of merging",
		Workaround: "Give an approval rule to a role that reviews after the result, or gate the merge in Launchway's own pipeline"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads who approved a pull request (its approval states) but does not approve or comment as a reviewer on the host's behalf",
		Workaround: "Review and approve in the CodeCommit console or with the AWS CLI"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "merging stays on the host, under its approval rules",
		Workaround: "Merge the pull request in CodeCommit"}
	c[forge.CapAutoLand] = forge.Declaration{Support: forge.None,
		Reason:     "CodeCommit has no auto-merge",
		Workaround: "Merge the pull request in CodeCommit once its approval rules are met (write_back.merge: person)"}
	c[forge.CapRules] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Launchway reads the approval rule templates associated with the repository but never changes them, and a template governs merging a pull request, not a push (a push is limited by IAM policy, which Launchway cannot read)",
		Workaround: "Change approval rule templates in CodeCommit; limit direct pushes to a branch with an IAM policy"}
	c[forge.CapForks] = forge.Declaration{Support: forge.None,
		Reason:     "a CodeCommit pull request names one repository for its source and destination, so a pull request from a fork cannot exist and fork detection has nothing to detect",
		Workaround: "Nothing to do: every pull request of a repository comes from a branch of that repository"}
	c[forge.CapTags] = forge.Declaration{Support: forge.Emulated,
		Reason:     "CodeCommit has no API call that creates a tag, so Launchway makes the annotated tag and pushes it with its own git, using the connection's credentials",
		Workaround: "Nothing to do, but the credentials need the GitPull and GitPush permissions on the repository (the API permissions alone do not tag)"}
	c[forge.CapReleases] = forge.Declaration{Support: forge.None,
		Reason:     "CodeCommit has no release object: it has branches and tags",
		Workaround: "Cut the release in Launchway: the annotated tag is made on the host, and the release page with its notes and files stays in Launchway, which serves the files"}
	return c
}

// Additions is what this host offers beyond the built-in forge.
func Additions() []forge.Addition {
	return []forge.Addition{
		{Key: "approval_rules", Title: "Approval rules", What: "A pull request can need a number of approvals from named people or roles before it may be merged; the rules come from templates set on the host, and Launchway reads them and each pull request's approval states"},
		{Key: "approval_rule_templates", Title: "Approval rule templates", What: "A template creates the same approval rule on every new pull request of the repositories it is associated with, for chosen destination branches; set up on the host"},
		{Key: "pull_request_approval_state", Title: "Approval state", What: "Who approved or revoked their approval of the current revision of a pull request, read from the host and shown on the Flight"},
		{Key: "iam_access", Title: "IAM access control", What: "Who may read, push, tag or merge is decided by IAM policy on the repository (and a role Launchway assumes for a short session), not by a token's scopes"},
	}
}

// Endpoints are the addresses a connection reaches.
type Endpoints struct {
	restkit.Endpoints
	// Region is where the repositories are, and the region requests are signed for.
	Region string
	// GitHost is the name (without port) of the Git endpoint: clone addresses must be on it.
	GitHost string
	// STS is the token service of the region.
	STS *url.URL
	// FIPS says the base address is a FIPS Git endpoint.
	FIPS bool
	// Test says the endpoints are a fake host's.
	Test bool
}

var gitHostRE = regexp.MustCompile(`^git-codecommit(-fips)?\.([a-z0-9-]+)\.(amazonaws\.com(?:\.cn)?)$`)

// EndpointsOf gives the addresses of a connection from its base address, the Git
// connection endpoint of the region: https://git-codecommit.<region>.amazonaws.com
// (also git-codecommit-fips.<region>.amazonaws.com and the .com.cn domain; the
// table at https://docs.aws.amazon.com/codecommit/latest/userguide/regions.html).
// The API endpoint is codecommit.<region>... (codecommit-fips... for a FIPS one;
// https://docs.aws.amazon.com/general/latest/gr/codecommit.html) and the token
// service sts.<region>... (https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_temp_enable-regions.html).
func EndpointsOf(base string) (Endpoints, error) {
	var e Endpoints
	if testAPI != "" { // tests of the service run against a fake host
		u, err := url.Parse(testAPI)
		if err != nil {
			return e, err
		}
		e.API, e.STS, e.Region, e.GitHost, e.Test = u, u, "us-east-1", u.Hostname(), true
		e.Hosts = map[string]bool{u.Host: true}
		return e, nil
	}
	if strings.TrimSpace(base) == "" {
		return e, errors.New("the base address is the repositories' Git endpoint in their region, for example https://git-codecommit.us-east-2.amazonaws.com")
	}
	u, err := restkit.ParseBase(base)
	if err != nil {
		return e, err
	}
	m := gitHostRE.FindStringSubmatch(u.Host)
	if m == nil || u.Scheme != "https" || strings.Trim(u.Path, "/") != "" {
		return e, errors.New("the base address must be the Git endpoint of the region, https://git-codecommit.<region>.amazonaws.com (or git-codecommit-fips.<region>.amazonaws.com), with no path")
	}
	fips, region, domain := m[1] != "", m[2], m[3]
	api := "codecommit"
	if fips {
		api = "codecommit-fips"
	}
	e.API = &url.URL{Scheme: "https", Host: api + "." + region + "." + domain}
	e.STS = &url.URL{Scheme: "https", Host: "sts." + region + "." + domain}
	e.Region, e.GitHost, e.FIPS = region, u.Host, fips
	e.Hosts = map[string]bool{e.API.Host: true, e.STS.Host: true}
	return e, nil
}

var testAPI string

// UseTestAPI points every connection at a fake host's address (the API, the
// token service and the Git endpoint are all that one server). It returns the
// function that restores the public service. For tests only: nothing in the
// product calls it.
func UseTestAPI(api string) (restore func()) {
	old := testAPI
	testAPI = api
	return func() { testAPI = old }
}

// Provider is the provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	ep   Endpoints
	c    *restkit.Client
	g    *egress.Guard
	now  func() time.Time
}

// Register adds the kind to a registry.
func Register(r *forge.Registry, g *egress.Guard) error {
	return r.Register(forge.KindCodeCommit, func(c forge.Connection) (forge.Provider, error) { return New(c, g) })
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
	p := &Provider{Base: forge.Base{K: forge.KindCodeCommit, Caps: Capabilities(), Mat: forge.Experimental, Adds: Additions()}, conn: c, ep: ep, g: g, now: time.Now}
	// The token is only what must be kept out of messages: the secret access key.
	cl := restkit.NewClient(ep.Endpoints, g, p.secretKey, nil)
	cl.Message = message
	cl.Sign = func(req *http.Request, body []byte) error {
		s, err := p.session(req.Context())
		if err != nil {
			return err
		}
		awssig.Signer{Creds: s.creds, Region: ep.Region, Service: "codecommit"}.Sign(req, awssig.HexSHA256(body), p.now())
		return nil
	}
	p.c = cl
	return p
}

// secretKey reads the secret access key of the credential document, for the
// client to keep out of any message; it fails when there is no usable credential.
func (p *Provider) secretKey() (string, error) {
	if p.conn.Credential.IsZero() {
		return "", fmt.Errorf("the connection %s has no credential", p.conn.Name)
	}
	raw, err := p.conn.Credential.Resolve()
	if err != nil {
		return "", err
	}
	d, err := parseCreds(raw)
	if err != nil {
		return "", fmt.Errorf("the connection %s: %w", p.conn.Name, err)
	}
	return d.SecretAccessKey, nil
}

// SetClock sets the time the provider signs and judges expiry by (tests).
func (p *Provider) SetClock(now func() time.Time) { p.now = now }

// Client returns the connection's client, for the tests.
func (p *Provider) Client() *restkit.Client { return p.c }

func (p *Provider) Identity() forge.Identity         { return identity{p} }
func (p *Provider) Repositories() forge.Repositories { return repos{p} }
func (p *Provider) Content() forge.Content           { return content{p} }
func (p *Provider) Proposals() forge.Proposals       { return proposals{p} }
func (p *Provider) Reviews() forge.Reviews           { return reviews{p} }
func (p *Provider) Rules() forge.Rules               { return rules{p} }
func (p *Provider) Events() forge.Events             { return events{p} }
func (p *Provider) Access() forge.Access             { return access{p} }
func (p *Provider) Releases() forge.Releases         { return releases{p} }

// split names account and repository and enforces the owners list.
func (p *Provider) split(path string) (account, name string, err error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || !accountRE.MatchString(parts[0]) || !nameRE.MatchString(parts[1]) {
		return "", "", fmt.Errorf("%w: a repository is named account/name (the 12-digit account id, then the repository name), not %q", forge.ErrInvalid, path)
	}
	if !restkit.OwnerAllowed(p.conn, parts[0]) {
		return "", "", fmt.Errorf("%w: the account %q is not on this connection's allow-list", forge.ErrInvalid, parts[0])
	}
	return parts[0], parts[1], nil
}

var (
	accountRE = regexp.MustCompile(`^[0-9]{12}$`)
	nameRE    = regexp.MustCompile(`^[\w.-]{1,100}$`)
)

// GitURL is the address of the region's Git endpoint, which clones go to.
func (e Endpoints) GitURL() string {
	if e.Test {
		return e.API.String()
	}
	return "https://" + e.GitHost
}
