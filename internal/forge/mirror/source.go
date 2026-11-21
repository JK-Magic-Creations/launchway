package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// SourceRepo is one repository on the server an import reads from.
type SourceRepo struct {
	Owner, Name string
	// URL is where it is cloned from (https or http, no credentials in it).
	URL           string
	Visibility    forge.Visibility
	DefaultBranch string
	Archived      bool
	// Empty is true when the source says it has no commits.
	Empty bool
}

// Path is "owner/name".
func (r SourceRepo) Path() string { return r.Owner + "/" + r.Name }

// Source lists the repositories an import may bring in. Providers of it are
// small: a list of addresses, and the API of a Git host.
type Source interface {
	// List returns the repositories of an owner (a user or an organization),
	// or every repository the credential can see when owner is empty.
	List(ctx context.Context, owner string) ([]SourceRepo, error)
	// Host is the host (and port) whose repositories these are: the only one
	// the connection's credential is ever sent to.
	Host() string
}

// maxSourceRepos bounds one listing.
const maxSourceRepos = 5000

// ---------------------------------------------------------------- a list of addresses

// URLList is the plain-Git source: repositories given as a list of addresses.
// Each is named by the last two parts of its path (owner/name, a trailing .git
// removed), and is private unless said otherwise.
type URLList struct {
	URLs []string
	// Visibility of everything listed (default private).
	Visibility forge.Visibility
	// Credentialed is the host the connection's credential belongs to.
	Credentialed string
}

// Host implements Source.
func (u URLList) Host() string { return u.Credentialed }

// List implements Source. A listing for an owner returns that owner's.
func (u URLList) List(_ context.Context, owner string) ([]SourceRepo, error) {
	vis := u.Visibility
	if vis == "" {
		vis = forge.Private
	}
	var out []SourceRepo
	for _, raw := range u.URLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p, err := url.Parse(raw)
		if err != nil || p.Host == "" {
			return nil, fmt.Errorf("%w: %q is not an address", forge.ErrInvalid, redact.URLs(raw))
		}
		if p.User != nil {
			return nil, fmt.Errorf("%w: an address must not contain a user name or password (give the credential as a reference): %s", forge.ErrInvalid, redact.URLs(raw))
		}
		segs := strings.Split(strings.Trim(strings.TrimSuffix(strings.TrimSuffix(p.Path, "/"), ".git"), "/"), "/")
		if len(segs) < 2 {
			return nil, fmt.Errorf("%w: %q has no owner and name in its path", forge.ErrInvalid, raw)
		}
		r := SourceRepo{Owner: segs[len(segs)-2], Name: segs[len(segs)-1], URL: raw, Visibility: vis}
		if owner == "" || owner == r.Owner {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- a Gitea-compatible API

// GiteaAPI lists repositories through the REST API of a Gitea-compatible Git
// server (kinds gitea and forgejo). It makes only GET requests, only to the
// connection's own host, never follows a redirect, and reads at most a bounded
// amount.
type GiteaAPI struct {
	Base  string // https://git.example.com
	Token string // may be empty for a server that lists public repositories
	Guard *egress.Guard
	// HTTP may be replaced in tests.
	HTTP *http.Client
}

// NewGiteaAPI makes a client for a connection; the credential is resolved now.
func NewGiteaAPI(c forge.Connection, g *egress.Guard) (*GiteaAPI, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("%w: the connection %s has no base_url", forge.ErrInvalid, c.Name)
	}
	a := &GiteaAPI{Base: strings.TrimSuffix(c.BaseURL, "/"), Guard: g}
	if !c.Credential.IsZero() {
		tok, err := c.Credential.Resolve()
		if err != nil {
			return nil, err
		}
		a.Token = tok
	}
	a.HTTP = &http.Client{Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second, Control: g.Control}).DialContext, DisableKeepAlives: true}}
	return a, nil
}

// Host implements Source.
func (a *GiteaAPI) Host() string {
	u, err := url.Parse(a.Base)
	if err != nil {
		return ""
	}
	return u.Host
}

type giteaRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Empty         bool   `json:"empty"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
}

func (a *GiteaAPI) get(ctx context.Context, path string, q url.Values, out any) (int, error) {
	u := a.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	if err := a.Guard.Check(ctx, a.Base+path); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if a.Token != "" {
		req.Header.Set("Authorization", "token "+a.Token)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the source server cannot be reached: %s", strings.ReplaceAll(redact.URLs(err.Error()), a.Token, "***"))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return resp.StatusCode, fmt.Errorf("the source server refused the credential (%d): check the connection's credential and that its token may list repositories", resp.StatusCode)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return resp.StatusCode, fmt.Errorf("the source server redirected the request (%d); Launchway does not follow redirects: use the address the redirect leads to as the connection's base_url", resp.StatusCode)
	case resp.StatusCode >= 400:
		return resp.StatusCode, fmt.Errorf("the source server answered %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, errors.New("the source server's answer is not what a Gitea-compatible API sends")
	}
	return resp.StatusCode, nil
}

// List implements Source.
func (a *GiteaAPI) List(ctx context.Context, owner string) ([]SourceRepo, error) {
	var all []giteaRepo
	paths := []string{"/api/v1/repos/search"}
	if owner != "" {
		paths = []string{"/api/v1/orgs/" + url.PathEscape(owner) + "/repos", "/api/v1/users/" + url.PathEscape(owner) + "/repos"}
	}
	var lastErr error
	for i, path := range paths {
		all, lastErr = nil, nil
		for page := 1; page <= 200; page++ {
			q := url.Values{"limit": {"50"}, "page": {fmt.Sprint(page)}}
			var items []giteaRepo
			var status int
			var err error
			if owner == "" {
				var wrap struct {
					Data []giteaRepo `json:"data"`
				}
				status, err = a.get(ctx, path, q, &wrap)
				items = wrap.Data
			} else {
				status, err = a.get(ctx, path, q, &items)
			}
			if err != nil {
				if status == http.StatusNotFound && i+1 < len(paths) {
					lastErr = err
					break // not an organization: try a user
				}
				return nil, err
			}
			if len(items) == 0 {
				break
			}
			all = append(all, items...)
			if len(all) > maxSourceRepos {
				return nil, fmt.Errorf("the source lists more than %d repositories; import an owner at a time", maxSourceRepos)
			}
		}
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("the source has no organization or user %q (or the credential cannot see it)", owner)
	}
	var out []SourceRepo
	for _, g := range all {
		o := g.Owner.Login
		if o == "" {
			o, _, _ = strings.Cut(g.FullName, "/")
		}
		r := SourceRepo{Owner: o, Name: g.Name, URL: g.CloneURL, DefaultBranch: g.DefaultBranch, Archived: g.Archived, Empty: g.Empty, Visibility: forge.Public}
		if g.Private {
			r.Visibility = forge.Private
		}
		out = append(out, r)
	}
	return out, nil
}
