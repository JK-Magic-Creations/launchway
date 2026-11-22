// Package github is the provider for the host kind "github": the public
// service and self-managed installs of it. It speaks only the REST calls
// named in the comments, with the standard library, to the connection's own
// API host and its documented upload host. Every request goes through the
// egress guard (address check, and again when connecting); no redirect is
// followed; the credential is read from its reference at the moment of a call
// and never stored, logged or returned.
//
// REST reference: https://docs.github.com/en/rest . Webhook reference:
// https://docs.github.com/en/webhooks/webhook-events-and-payloads .
package github

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

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// ErrTagMissing is wrapped when a release is asked for a tag the host lacks.
var ErrTagMissing = errors.New("the host does not have the tag")

const maxAnswer = 8 << 20

// Endpoints are the addresses a connection reaches, and the only hosts the
// credential is ever sent to.
type Endpoints struct {
	API    *url.URL // e.g. https://api.github.com or https://git.example.com/api/v3
	Upload *url.URL // e.g. https://uploads.github.com or https://git.example.com/api/uploads
	// Hosts are the host names the credential may be sent to.
	Hosts map[string]bool
}

// EndpointsOf derives the API and upload addresses from a connection's base
// address: empty or the public service's own address gives the public API
// (https://docs.github.com/en/rest/overview/resources-in-the-rest-api#root-endpoint);
// any other address is a self-managed install, whose API is at <base>/api/v3 and
// uploads at <base>/api/uploads
// (https://docs.github.com/en/enterprise-server@latest/rest/guides/getting-started-with-the-rest-api).
// uploadOverride replaces the derived upload address.
func EndpointsOf(base, uploadOverride string) (Endpoints, error) {
	var e Endpoints
	switch strings.TrimSuffix(strings.TrimSpace(base), "/") {
	case "", "https://github.com":
		e.API = &url.URL{Scheme: "https", Host: "api.github.com"}
		e.Upload = &url.URL{Scheme: "https", Host: "uploads.github.com"}
	default:
		u, err := url.Parse(strings.TrimSpace(base))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return e, errors.New("the base address must be an http or https address without a user name")
		}
		root := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api/v3"), "/")
		e.API = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: root + "/api/v3"}
		e.Upload = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: root + "/api/uploads"}
	}
	if uploadOverride != "" {
		u, err := url.Parse(uploadOverride)
		if err != nil || u.Host == "" || u.User != nil {
			return e, errors.New("the upload address is not valid")
		}
		e.Upload = u
	}
	e.Hosts = map[string]bool{e.API.Host: true, e.Upload.Host: true}
	return e, nil
}

// Client is one connection's HTTP access.
type Client struct {
	ep    Endpoints
	guard *egress.Guard
	http  *http.Client
	// Token reads the credential; called for every request.
	Token func() (string, error)
	Now   func() time.Time
}

// NewClient makes a client. The guard is the product's egress allow-list.
func NewClient(ep Endpoints, g *egress.Guard, token func() (string, error)) *Client {
	if g == nil {
		g, _ = egress.New(nil)
	}
	return &Client{ep: ep, guard: g, Token: token, Now: time.Now, http: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, Control: g.Control}).DialContext,
			ResponseHeaderTimeout: 2 * time.Minute,
			DisableKeepAlives:     true,
		},
	}}
}

// Answer is a host's reply.
type Answer struct {
	Status int
	Body   []byte
	Header http.Header
}

// Message is the host's own words for a refusal.
func (a Answer) Message() string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(a.Body, &m) == nil {
		return m.Message
	}
	return ""
}

type call struct {
	c      *Client
	ctx    context.Context
	secret string
}

func (k *call) scrub(msg string) string {
	if len(k.secret) > 3 {
		msg = strings.ReplaceAll(msg, k.secret, "***")
	}
	msg = strings.Join(strings.Fields(redact.URLs(msg)), " ")
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

func (k *call) errf(format string, a ...any) error {
	return errors.New(k.scrub(fmt.Sprintf(format, a...)))
}

// do sends one request to raw, which must be at an allowed host.
// Docs for the headers: https://docs.github.com/en/rest/using-the-rest-api/getting-started-with-the-rest-api
func (c *Client) do(ctx context.Context, method, raw string, body io.Reader, size int64, ctype string) (Answer, error) {
	tok, err := c.Token()
	if err != nil {
		return Answer{}, errors.New(redact.URLs(err.Error()))
	}
	k := &call{c: c, ctx: ctx, secret: tok}
	u, err := url.Parse(raw)
	if err != nil {
		return Answer{}, k.errf("the host's address %q is not valid", raw)
	}
	if !c.ep.Hosts[u.Host] {
		return Answer{}, k.errf("the address names %s, which is not this connection's host; the credential is not sent there", u.Host)
	}
	bare := *u
	bare.RawQuery = ""
	if err := c.guard.Check(ctx, bare.String()); err != nil {
		return Answer{}, k.errf("%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return Answer{}, k.errf("%v", err)
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
	req.Header.Set("Authorization", "Bearer "+tok)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Answer{}, k.errf("the host cannot be reached: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return Answer{}, k.errf("reading the host's answer: %v", err)
	}
	a := Answer{Status: resp.StatusCode, Body: b, Header: resp.Header}
	switch {
	case a.Status == http.StatusUnauthorized:
		return a, k.errf("the host refused the credential (401%s): it is wrong, revoked or expired", withMsg(a))
	case a.Status >= 300 && a.Status < 400:
		return a, k.errf("the host redirected the request (%d); Launchway does not follow redirects: correct the connection's base address", a.Status)
	}
	return a, nil
}

func withMsg(a Answer) string {
	if m := a.Message(); m != "" {
		return ": " + m
	}
	return ""
}

// Call sends a JSON request below the API address and decodes a JSON answer
// into out (when not nil). A status of 400 or more is an error, except those
// in tolerate, which are returned with a nil error for the caller to read.
func (c *Client) Call(ctx context.Context, method, path string, in, out any, tolerate ...int) (int, error) {
	var body io.Reader
	var size int64
	ctype := ""
	if in != nil {
		b, _ := json.Marshal(in)
		body, size, ctype = bytes.NewReader(b), int64(len(b)), "application/json"
	}
	a, err := c.do(ctx, method, strings.TrimSuffix(c.ep.API.String(), "/")+path, body, size, ctype)
	if err != nil {
		return a.Status, err
	}
	return a.Status, c.finish(a, out, tolerate)
}

func (c *Client) finish(a Answer, out any, tolerate []int) error {
	if a.Status >= 400 {
		for _, t := range tolerate {
			if t == a.Status {
				return nil
			}
		}
		k := &call{secret: ""}
		if a.Status == http.StatusForbidden || a.Status == http.StatusNotFound {
			return k.errf("the host answered %d%s. Either the token lacks the permission this needs on the repository, or the repository is not visible to it", a.Status, withMsg(a))
		}
		return k.errf("the host answered %d%s", a.Status, withMsg(a))
	}
	if out != nil && len(a.Body) > 0 {
		if err := json.Unmarshal(a.Body, out); err != nil {
			return errors.New("the host's answer is not what its API sends")
		}
	}
	return nil
}

// CallPage is Call for a listing: it also reports whether another page follows
// (the Link header's rel="next": https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api).
func (c *Client) CallPage(ctx context.Context, path string, out any) (more bool, err error) {
	a, err := c.do(ctx, http.MethodGet, strings.TrimSuffix(c.ep.API.String(), "/")+path, nil, 0, "")
	if err != nil {
		return false, err
	}
	if err := c.finish(a, out, nil); err != nil {
		return false, err
	}
	return strings.Contains(a.Header.Get("Link"), `rel="next"`), nil
}

// Header makes a GET and returns the answer whole, for the probes that read headers.
func (c *Client) Header(ctx context.Context, path string, out any) (Answer, error) {
	a, err := c.do(ctx, http.MethodGet, strings.TrimSuffix(c.ep.API.String(), "/")+path, nil, 0, "")
	if err != nil {
		return a, err
	}
	return a, c.finish(a, out, nil)
}

// Upload streams one file to an upload address the host's release names. The
// address must be at the connection's documented upload host.
func (c *Client) Upload(ctx context.Context, uploadURL, name string, f forge.ReleaseFile) (int64, error) {
	base, _, _ := strings.Cut(uploadURL, "{")
	u, err := url.Parse(base)
	if err != nil || base == "" {
		return 0, errors.New("the host's answer names no address to upload to")
	}
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()
	rc, err := f.Open(ctx)
	if err != nil {
		return 0, fmt.Errorf("opening %s from the release: %v", name, err)
	}
	defer rc.Close()
	a, err := c.do(ctx, http.MethodPost, u.String(), rc, f.Size, "application/octet-stream")
	if err != nil {
		return 0, fmt.Errorf("uploading %s: %v", name, err)
	}
	if err := c.finish(a, nil, nil); err != nil {
		return 0, fmt.Errorf("uploading %s: %v", name, err)
	}
	var got struct {
		Size int64 `json:"size"`
	}
	_ = json.Unmarshal(a.Body, &got)
	return got.Size, nil
}

func esc(s string) string { return url.PathEscape(s) }

func escPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
