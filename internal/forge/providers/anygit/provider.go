package anygit

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
)

const (
	noAPI    = "a plain Git server has no API Launchway can call, only Git itself"
	noAPIWay = "Do this on the Git server itself, or connect a host Launchway has a provider for"
)

// Capabilities declares what this provider does: what Git alone gives (refs,
// a checkout, a tag and a file change by push) and, for everything else, why
// not and what a person does instead.
func Capabilities() forge.Capabilities {
	c := forge.NoCapabilities(noAPI, noAPIWay)
	for _, f := range []forge.Capability{forge.CapAccess, forge.CapTags, forge.CapCommitFile} {
		c[f] = forge.Declaration{Support: forge.Native}
	}
	c[forge.CapIdentity] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Git has no call that says who a credential is, so the account is the user name the connection sends; its use is proved against a repository (git ls-remote) when one is chosen",
		Workaround: "Nothing to do: choose a repository, and Launchway proves the credential by reading it"}
	c[forge.CapRepositories] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Git cannot list the repositories of a server, so there is nothing to search or choose from",
		Workaround: "Enter the repository's path (owner/name, as it is in the server's clone address); Launchway proves it with git ls-remote and reads its default branch"}
	c[forge.CapContent] = forge.Declaration{Support: forge.Emulated,
		Reason:     "Git lists a repository's branches and tags (ls-remote), but a commit, a file or a diff is read from Launchway's own clone",
		Workaround: "Nothing to do: files and history come from the clone Launchway keeps"}
	c[forge.CapEvents] = forge.Declaration{Support: forge.Emulated,
		Reason:     "a plain Git server sends no webhook of a format Launchway can check, and has no pull requests to follow",
		Workaround: "Nothing to do: Launchway polls the repository on its schedule and starts a Sortie on a push. To react at once, call the Flight's git-hook address from the server's post-receive hook (see the guide)"}
	c[forge.CapRepoCreate] = forge.Declaration{Support: forge.None,
		Reason:     "a connection reads and writes repositories that exist; Git cannot create or delete one on a server",
		Workaround: "Create the repository on the Git server, then enter its path in Launchway"}
	c[forge.CapProposals] = forge.Declaration{Support: forge.None,
		Reason:     "a plain Git server has no pull requests",
		Workaround: "For write-back, push to a branch (write_back mode direct to a branch the credential may push to) and ask a person to merge it on the server; or connect a host that has pull requests"}
	c[forge.CapProposalRef] = forge.Declaration{Support: forge.None,
		Reason:     "a plain Git server publishes no ref for a pull request, because it has none",
		Workaround: "Nothing to do: Sorties start from pushes to branches and tags"}
	c[forge.CapResults] = forge.Declaration{Support: forge.None,
		Reason:     "a plain Git server has no commit status or check, and no pull request to carry a comment",
		Workaround: "Results are shown in Launchway only (the Flight's Sorties); nothing is written back to the server"}
	c[forge.CapProposalNote] = forge.Declaration{Support: forge.None,
		Reason:     "a plain Git server has no pull request to hold a summary comment",
		Workaround: "Read each Sortie's summary in Launchway"}
	c[forge.CapReleases] = forge.Declaration{Support: forge.None,
		Reason:     "a plain Git server has tags but no release object",
		Workaround: "Cut the release in Launchway: the annotated tag is pushed to the server, and the release page with its notes and files stays in Launchway, which serves the files"}
	c[forge.CapReviews] = forge.Declaration{Support: forge.None,
		Reason:     "a plain Git server has no reviews",
		Workaround: "Review in whatever process your team uses; Launchway does not hold reviews for this server"}
	c[forge.CapLand] = forge.Declaration{Support: forge.None,
		Reason:     "there are no pull requests to merge",
		Workaround: "Merge on the server with Git, or in Launchway's own forge if the code is hosted there"}
	c[forge.CapRules] = forge.Declaration{Support: forge.None,
		Reason:     "Git cannot tell Launchway which branches the server protects, so a refusal can only come from the push itself",
		Workaround: "Nothing to do: a push the server's hook refuses fails with the server's own message, which Launchway returns"}
	c[forge.CapForks] = forge.Declaration{Support: forge.None,
		Reason:     "a plain Git server has no forks or pull requests from them",
		Workaround: "Nothing to do"}
	return c
}

// Additions is what this host offers beyond the built-in forge: nothing is
// known about a server that is only Git.
func Additions() []forge.Addition {
	return []forge.Addition{
		{Key: "any_git_server", Title: "Any Git server", What: "Any server that speaks Git over HTTPS, with a token or a user name and password; Launchway checks out, follows pushes by polling, pushes tags and commits files through Git itself"},
	}
}

var (
	segRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.~-]{0,99}$`)
	// ErrSSH is the refusal of an ssh address.
	ErrSSH = errors.New("SSH addresses are not offered through a connection; use the server's https address (a Flight that watches a bare ssh address uses the Git credentials of the account the server runs as)")
)

// EndpointsOf checks the connection's base address and returns it: https, no
// user name or query, a host, and an optional path prefix. http is accepted for
// a loopback address only (never for a credential that would cross a network).
func EndpointsOf(base string) (*url.URL, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return nil, errors.New("the base address is the Git server's https address, for example https://git.example.com")
	}
	if strings.HasPrefix(base, "ssh:") || strings.HasPrefix(base, "git@") || strings.HasPrefix(base, "git:") {
		return nil, ErrSSH
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("the base address must be an https address without a user name, query or fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname())) {
		return nil, errors.New("the base address must be https: a credential is not sent over plain http")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Provider is the provider of one connection.
type Provider struct {
	forge.Base
	conn forge.Connection
	base *url.URL
	g    *egress.Guard
}

// New makes the provider of a connection.
func New(c forge.Connection, g *egress.Guard) (*Provider, error) {
	base, err := EndpointsOf(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
	}
	return &Provider{Base: forge.Base{K: forge.KindPlain, Caps: Capabilities(), Mat: forge.Experimental, Adds: Additions()}, conn: c, base: base, g: g}, nil
}

// BaseURL is the connection's base address, checked.
func (p *Provider) BaseURL() string { return p.base.String() }

func (p *Provider) Identity() forge.Identity         { return identity{p} }
func (p *Provider) Repositories() forge.Repositories { return repos{p} }
func (p *Provider) Content() forge.Content           { return content{p} }
func (p *Provider) Events() forge.Events             { return events{p} }
func (p *Provider) Access() forge.Access             { return access{p} }
func (p *Provider) Releases() forge.Releases         { return releases{p} }

// split checks a repository path (owner/name) and the owners list.
func (p *Provider) split(path string) (owner, name string, err error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || !segRE.MatchString(parts[0]) || !segRE.MatchString(parts[1]) || strings.Contains(path, "..") || strings.HasSuffix(parts[1], ".lock") {
		return "", "", fmt.Errorf("%w: a repository is two names, owner/name, as the server's clone address has them (add the base address's own prefix to the base address, not here), not %q", forge.ErrInvalid, path)
	}
	if len(p.conn.Owners) > 0 {
		ok := false
		for _, o := range p.conn.Owners {
			ok = ok || strings.EqualFold(o, parts[0])
		}
		if !ok {
			return "", "", fmt.Errorf("%w: the owner %q is not on this connection's allow-list", forge.ErrInvalid, parts[0])
		}
	}
	return parts[0], parts[1], nil
}

// gitURL is the clone address of a repository: the base address and the path
// as given. The credential is sent to this origin only.
func (p *Provider) gitURL(path string) (*url.URL, error) {
	owner, name, err := p.split(path)
	if err != nil {
		return nil, err
	}
	u := *p.base
	u.Path = p.base.Path + "/" + owner + "/" + name
	return &u, nil
}
