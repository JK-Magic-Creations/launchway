package outside

import (
	"bytes"
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

	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// REST speaks the REST API of the most common public Git host: it looks the
// tag up, creates (or finds) the release for it, and uploads each file to the
// upload address the host's answer names. Every request goes through the
// egress guard the mirrors use, at the address check and again when
// connecting; no redirect is followed, and the credential is sent only to the
// API's host and the upload host of the same mirror.
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
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, Control: g.Control}).DialContext,
			ResponseHeaderTimeout: 2 * time.Minute,
			DisableKeepAlives:     true,
		},
	}}
}

const maxAnswer = 4 << 20

// call is one authenticated request.
type call struct {
	r      *REST
	token  string
	api    *url.URL
	hosts  map[string]bool // hosts the credential may be sent to
	ctx    context.Context
	secret []string
}

// scrub removes the credential and any address credentials from a message.
func (c *call) scrub(msg string) string {
	for _, s := range c.secret {
		if len(s) > 3 {
			msg = strings.ReplaceAll(msg, s, "***")
		}
	}
	msg = redact.URLs(msg)
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

func (c *call) errf(format string, a ...any) error {
	return errors.New(c.scrub(fmt.Sprintf(format, a...)))
}

type answer struct {
	status int
	body   []byte
}

// message is the host's own words for a refusal.
func (a answer) message() string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(a.body, &m) == nil && m.Message != "" {
		return m.Message
	}
	return ""
}

func (c *call) do(method, raw string, body io.Reader, size int64, ctype string) (answer, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return answer{}, c.errf("the outside host's address %q is not valid", raw)
	}
	if !c.hosts[u.Host] {
		return answer{}, c.errf("the outside host named %s, which is not the mirror's host; the credential is not sent there", u.Host)
	}
	bare := *u
	bare.RawQuery = ""
	if err := c.r.guard.Check(c.ctx, bare.String()); err != nil {
		return answer{}, c.errf("%v", err)
	}
	req, err := http.NewRequestWithContext(c.ctx, method, raw, body)
	if err != nil {
		return answer{}, c.errf("%v", err)
	}
	if body != nil {
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "launchway")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.r.http.Do(req)
	if err != nil {
		return answer{}, c.errf("the outside host cannot be reached: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return answer{}, c.errf("reading the outside host's answer: %v", err)
	}
	a := answer{status: resp.StatusCode, body: b}
	switch {
	case a.status == http.StatusUnauthorized || a.status == http.StatusForbidden:
		return a, c.errf("the outside host refused the credential (%d%s). A token that may push may not be allowed to make releases: it needs the permission to write releases (contents) on this repository", a.status, withMsg(a))
	case a.status >= 300 && a.status < 400:
		return a, c.errf("the outside host redirected the request (%d); Launchway does not follow redirects: set the mirror's outside API address to the address it leads to", a.status)
	}
	return a, nil
}

func withMsg(a answer) string {
	if m := a.message(); m != "" {
		return ": " + m
	}
	return ""
}

func (c *call) json(method, path string, in any, out any) (int, error) {
	var body io.Reader
	var size int64
	if in != nil {
		b, _ := json.Marshal(in)
		body, size = bytes.NewReader(b), int64(len(b))
	}
	a, err := c.do(method, strings.TrimSuffix(c.api.String(), "/")+path, body, size, map[bool]string{true: "application/json"}[in != nil])
	if err != nil {
		return a.status, err
	}
	if a.status >= 400 {
		return a.status, c.errf("the outside host answered %d%s", a.status, withMsg(a))
	}
	if out != nil {
		if err := json.Unmarshal(a.body, out); err != nil {
			return a.status, c.errf("the outside host's answer is not what its API sends")
		}
	}
	return a.status, nil
}

type remoteAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type remoteRelease struct {
	ID        int64         `json:"id"`
	HTMLURL   string        `json:"html_url"`
	UploadURL string        `json:"upload_url"`
	Assets    []remoteAsset `json:"assets"`
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

// Publish implements Publisher.
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
	tok, err := t.Credential.Resolve() // at the moment of the call; never kept
	if err != nil {
		return Result{}, errors.New(redact.URLs(err.Error()))
	}
	c := &call{r: r, token: tok, api: api, ctx: ctx, secret: []string{tok},
		hosts: map[string]bool{api.Host: true, mu.Host: true, "uploads." + mu.Host: true}}
	repoPath := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)

	// The tag must be there already: asking for a release of a missing tag
	// would make the host create it from its default branch.
	status, err := c.json(http.MethodGet, repoPath+"/git/ref/tags/"+escapeRef(rel.Tag), nil, nil)
	if status == http.StatusNotFound {
		return Result{}, fmt.Errorf("%w: %s is not there yet, or the credential cannot see the repository", ErrTagMissing, rel.Tag)
	}
	if err != nil {
		return Result{}, err
	}

	var made remoteRelease
	status, err = c.json(http.MethodGet, repoPath+"/releases/tags/"+escapeRef(rel.Tag), nil, &made)
	switch {
	case status == http.StatusNotFound:
		_, err = c.json(http.MethodPost, repoPath+"/releases", map[string]any{
			"tag_name": rel.Tag, "name": rel.Title, "body": rel.Notes, "draft": false, "prerelease": rel.Prerelease,
		}, &made)
		if err != nil {
			return Result{}, err
		}
	case err != nil:
		return Result{}, err
	}
	have := map[string]remoteAsset{}
	for _, a := range made.Assets {
		have[a.Name] = a
	}
	for _, f := range rel.Files {
		if a, ok := have[f.Name]; ok {
			if a.Size == f.Size {
				continue // uploaded by an earlier try
			}
			if _, err := c.json(http.MethodDelete, repoPath+"/releases/assets/"+fmt.Sprint(a.ID), nil, nil); err != nil {
				return Result{}, err
			}
		}
		if err := c.upload(made, f); err != nil {
			return Result{}, err
		}
	}
	return Result{URL: made.HTMLURL}, nil
}

func escapeRef(tag string) string {
	parts := strings.Split(tag, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// upload streams one file to the upload address of the release.
func (c *call) upload(rel remoteRelease, f File) error {
	base, _, _ := strings.Cut(rel.UploadURL, "{")
	if base == "" {
		return c.errf("the outside host's answer names no address to upload %s to", f.Name)
	}
	u, err := url.Parse(base)
	if err != nil {
		return c.errf("the outside host's upload address is not valid")
	}
	q := u.Query()
	q.Set("name", f.Name)
	u.RawQuery = q.Encode()
	rc, err := f.Open(c.ctx)
	if err != nil {
		return c.errf("opening %s from the release: %v", f.Name, err)
	}
	defer rc.Close()
	a, err := c.do(http.MethodPost, u.String(), rc, f.Size, "application/octet-stream")
	if err != nil {
		return c.errf("uploading %s: %v", f.Name, err)
	}
	if a.status >= 400 {
		return c.errf("uploading %s: the outside host answered %d%s", f.Name, a.status, withMsg(a))
	}
	var got remoteAsset
	if json.Unmarshal(a.body, &got) == nil && got.Size != 0 && got.Size != f.Size {
		return c.errf("uploading %s: the outside host kept %d bytes, not %d", f.Name, got.Size, f.Size)
	}
	return nil
}
