package outside

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/github"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// REST speaks the REST API of the most common public Git host through the
// github provider: it looks the tag up, creates (or finds) the release for
// it, and uploads each file to the upload address the host's answer names.
// Every request goes through the egress guard the mirrors use, at the address
// check and again when connecting; no redirect is followed, and the
// credential is sent only to the API's host and the upload host of the same
// mirror.
type REST struct {
	guard *egress.Guard
	http  *http.Client
}

// NewREST makes the implementation; the guard is the one the mirrors use.
func NewREST(g *egress.Guard) *REST {
	if g == nil {
		g, _ = egress.New(nil)
	}
	return &REST{guard: g, http: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DialContext: (&net.Dialer{Timeout: 15 * time.Second, Control: g.Control}).DialContext},
	}}
}

// repoOf names owner and repository from a mirror's address.
func repoOf(u *url.URL) (string, string, error) {
	segs := strings.Split(strings.Trim(strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git"), "/"), "/")
	if len(segs) < 2 || segs[len(segs)-1] == "" {
		return "", "", fmt.Errorf("the mirror's address %s has no owner and repository in its path", redact.URLs(u.String()))
	}
	return segs[len(segs)-2], segs[len(segs)-1], nil
}

// apiOf is the address of the API: the override, or https://api.<host>.
func apiOf(t Target, mirror *url.URL) (*url.URL, error) {
	if t.API != "" {
		u, err := url.Parse(strings.TrimSpace(t.API))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return nil, errors.New("the outside API address must be an https or http address with no user name or password")
		}
		return u, nil
	}
	return &url.URL{Scheme: mirror.Scheme, Host: "api." + mirror.Host}, nil
}

// Publish implements Publisher. The calls are the github provider's
// (internal/forge/providers/github), the same code a connection's "Cut a
// release" uses; only the address and the credential come from the mirror.
func (r *REST) Publish(ctx context.Context, t Target, rel Release) (Result, error) {
	mu, err := url.Parse(t.URL)
	if err != nil || mu.Host == "" {
		return Result{}, errors.New("the mirror's address is not a Git repository address")
	}
	owner, repo, err := repoOf(mu)
	if err != nil {
		return Result{}, err
	}
	api, err := apiOf(t, mu)
	if err != nil {
		return Result{}, err
	}
	if t.Credential.IsZero() {
		return Result{}, errors.New("the mirror has no credential, and the outside host needs one to create a release")
	}
	up := &url.URL{Scheme: api.Scheme, Host: "uploads." + mu.Host}
	ep := github.Endpoints{API: api, Upload: up, Hosts: map[string]bool{api.Host: true, mu.Host: true, up.Host: true}}
	p := github.NewWith(forge.Connection{Name: "mirror", Kind: forge.KindGitHub}, ep, r.guard, func() (string, error) { return t.Credential.Resolve() })
	files := make([]forge.ReleaseFile, 0, len(rel.Files))
	for _, f := range rel.Files {
		files = append(files, forge.ReleaseFile{Name: f.Name, Size: f.Size, Open: f.Open})
	}
	link, err := p.Releases().PublishRelease(ctx, forge.Repository{Path: owner + "/" + repo},
		forge.ReleaseSpec{Tag: rel.Tag, Title: rel.Title, Notes: rel.Notes, Prerelease: rel.Prerelease, Files: files})
	if errors.Is(err, github.ErrTagMissing) {
		return Result{}, fmt.Errorf("%w: %v", ErrTagMissing, err)
	}
	if err != nil {
		return Result{}, errors.New(redact.URLs(err.Error()))
	}
	return Result{URL: link}, nil
}
