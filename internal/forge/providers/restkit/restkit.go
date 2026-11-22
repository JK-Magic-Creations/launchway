// Package restkit is the HTTP access every outside-host provider shares: one
// connection's client, which sends the credential only to the connection's own
// hosts, checks each address with the egress guard (at the request and again
// when connecting), follows no redirect, caps answers, reads the credential at
// the moment of a call, and never returns or logs it. A provider supplies how
// the credential is presented (its header) and the host's own error words.
package restkit

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

// MaxAnswer caps what is read of one answer.
const MaxAnswer = 8 << 20

// Endpoints are the addresses a connection reaches and the only hosts the
// credential is ever sent to.
type Endpoints struct {
	API *url.URL
	// Hosts are the host names (with port) the credential may be sent to.
	Hosts map[string]bool
}

// ParseBase reads a connection's base address: http or https, no user name.
func ParseBase(base string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, errors.New("the base address must be an http or https address without a user name")
	}
	return u, nil
}

// Client is one connection's HTTP access.
type Client struct {
	EP    Endpoints
	guard *egress.Guard
	http  *http.Client
	// Token reads the credential; called for every request.
	Token func() (string, error)
	// Auth sets the credential header on a request.
	Auth func(h http.Header, token string)
	// Headers are fixed headers sent with every request (Accept, version).
	Headers map[string]string
	// Message extracts the host's own words from an error body.
	Message func(body []byte) string
	Now     func() time.Time
	// Sign, when set, signs the request after its headers are set, given the
	// whole body (a host that signs requests, as AWS does, hashes it). It runs
	// for every request, so credentials that rotate are read at that moment.
	Sign func(req *http.Request, body []byte) error
}

// NewClient makes a client. The guard is the product's egress allow-list.
func NewClient(ep Endpoints, g *egress.Guard, token func() (string, error), auth func(http.Header, string)) *Client {
	if g == nil {
		g, _ = egress.New(nil)
	}
	return &Client{EP: ep, guard: g, Token: token, Auth: auth, Now: time.Now, http: &http.Client{
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
	secret string
	msg    func([]byte) string
}

// Message is the host's own words for a refusal.
func (a Answer) Message() string {
	if a.msg != nil {
		return a.msg(a.Body)
	}
	return ""
}

type call struct{ secret string }

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

// Do sends one request to raw, which must be at an allowed host. A body of
// size 0 with a non-nil reader is sent empty.
func (c *Client) Do(ctx context.Context, method, raw string, body io.Reader, size int64, ctype string) (Answer, error) {
	return c.DoWith(ctx, method, raw, body, size, ctype, nil)
}

// DoWith is Do with more headers for this one request (the host's per-call
// header, such as an action name).
func (c *Client) DoWith(ctx context.Context, method, raw string, body io.Reader, size int64, ctype string, extra http.Header) (Answer, error) {
	tok, err := c.Token()
	if err != nil {
		return Answer{}, errors.New(redact.URLs(err.Error()))
	}
	k := &call{secret: tok}
	u, err := url.Parse(raw)
	if err != nil {
		return Answer{}, k.errf("the host's address %q is not valid", raw)
	}
	if !c.EP.Hosts[u.Host] {
		return Answer{}, k.errf("the address names %s, which is not this connection's host; the credential is not sent there", u.Host)
	}
	bare := *u
	bare.RawQuery = ""
	if err := c.guard.Check(ctx, bare.String()); err != nil {
		return Answer{}, k.errf("%v", err)
	}
	var whole []byte
	if c.Sign != nil && body != nil {
		if whole, err = io.ReadAll(body); err != nil {
			return Answer{}, k.errf("reading the request body: %v", err)
		}
		body, size = bytes.NewReader(whole), int64(len(whole))
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
	for h, v := range c.Headers {
		req.Header.Set(h, v)
	}
	req.Header.Set("User-Agent", "launchway")
	for h, vs := range extra {
		req.Header[http.CanonicalHeaderKey(h)] = vs
	}
	if c.Auth != nil {
		c.Auth(req.Header, tok)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.Sign != nil {
		if err := c.Sign(req, whole); err != nil {
			return Answer{}, k.errf("signing the request: %v", err)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Answer{}, k.errf("the host cannot be reached: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswer+1))
	if err != nil {
		return Answer{}, k.errf("reading the host's answer: %v", err)
	}
	if len(b) > MaxAnswer {
		return Answer{}, k.errf("the host's answer is larger than the %d MiB Launchway reads; it is refused, not cut", MaxAnswer>>20)
	}
	a := Answer{Status: resp.StatusCode, Body: b, Header: resp.Header, secret: tok, msg: c.Message}
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

// URL joins a path (already escaped) to the API address.
func (c *Client) URL(path string) string { return strings.TrimSuffix(c.EP.API.String(), "/") + path }

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
	a, err := c.Do(ctx, method, c.URL(path), body, size, ctype)
	if err != nil {
		return a.Status, err
	}
	return a.Status, c.Finish(a, out, tolerate)
}

// Finish turns an answer into an error or decodes it.
func (c *Client) Finish(a Answer, out any, tolerate []int) error {
	if a.Status >= 400 {
		for _, t := range tolerate {
			if t == a.Status {
				return nil
			}
		}
		k := &call{secret: a.secret}
		if a.Status == http.StatusForbidden || a.Status == http.StatusNotFound {
			return k.errf("the host answered %d%s. Either the token lacks the permission this needs on the repository (see the connection test for what each feature needs), or the repository is not visible to it", a.Status, withMsg(a))
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

// Get makes a GET and returns the answer whole (for headers and paging).
func (c *Client) Get(ctx context.Context, path string, out any, tolerate ...int) (Answer, error) {
	a, err := c.Do(ctx, http.MethodGet, c.URL(path), nil, 0, "")
	if err != nil {
		return a, err
	}
	return a, c.Finish(a, out, tolerate)
}

// Esc escapes one path element; EscPath escapes a slash-separated path keeping the slashes.
func Esc(s string) string { return url.PathEscape(s) }

// EscPath escapes each element of a slash-separated path.
func EscPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Owners is the allow-list of owner or group names of a connection.
func OwnerAllowed(c forge.Connection, owner string) bool {
	if len(c.Owners) == 0 {
		return true
	}
	for _, o := range c.Owners {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	return false
}

// TrimHeads removes refs/heads/ from a ref name.
func TrimHeads(s string) string { return strings.TrimPrefix(s, "refs/heads/") }
