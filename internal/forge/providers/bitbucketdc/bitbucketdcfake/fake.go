// Package bitbucketdcfake is a fake of the Bitbucket Data Center REST API for
// tests: an httptest server that implements exactly the documented calls the
// bitbucketdc provider uses (the operations of the product's OpenAPI file,
// https://developer.atlassian.com/server/bitbucket/10.2.swagger.v3.json) and
// is strict. An unknown path, a wrong method, a missing or wrong bearer token, a
// missing required field, a field or a query parameter the documentation does not
// list for the call, a stale version, or a body of the wrong type all fail the
// request with a status the host would give and are recorded in Violations so a
// test can assert there were none. The install lives below the context path
// /bb, as an install may.
package bitbucketdcfake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Access is the only credential the fake accepts.
const Access = "bitbucketdc-fake-token"

// WebhookSecret is the secret a test configures on the connection.
const WebhookSecret = "hook-secret"

// Context is the install's context path.
const Context = "/bb"

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	Requests   []string
	Writes     int
	// Key and Slug name the one repository (ACME/app unless made with NewFor).
	Key, Slug string
	// Write and Admin: the token holds repository write and admin permission.
	Write, Admin bool
	// Protect: the default branch has a pull-request-only restriction and merge checks.
	Protect bool
	// AutoMerge: the repository's settings allow auto-merge.
	AutoMerge bool
	Comments  map[int]string
	PRs       []*PR
	// AutoMergeRequested lists the pull request numbers an auto-merge was asked for.
	AutoMergeRequested map[int]string
	branches           map[string]string
	tags               map[string]string
	files              map[string]map[string][]byte
	fileCommit         map[string]map[string]string // branch -> path -> last commit
	builds             map[string]map[string]map[string]any
	nextID             int
	nextSHA            int
	commentVer         map[int]int
}

// PR is a pull request of the fake; tests may change it under Lock.
type PR struct {
	ID, Version            int
	Title, Desc, State     string
	Draft                  bool
	Src, Dst, SHA, SrcRepo string // SrcRepo is "KEY/slug"
	Reviewers              []string
}

func (h *Host) Lock()   { h.mu.Lock() }
func (h *Host) Unlock() { h.mu.Unlock() }

// New starts a fake with the repository ACME/app (default branch main).
func New() *Host { return NewFor("ACME", "app") }

// NewFor starts a fake with the repository key/slug (a personal repository has the key ~USER).
func NewFor(key, slug string) *Host {
	h := &Host{Key: key, Slug: slug, Write: true, Admin: true, AutoMerge: true, branches: map[string]string{"main": strings.Repeat("a", 40)}, tags: map[string]string{},
		files: map[string]map[string][]byte{"main": {}}, fileCommit: map[string]map[string]string{"main": {}}, builds: map[string]map[string]map[string]any{},
		Comments: map[int]string{}, commentVer: map[int]int{}, AutoMergeRequested: map[int]string{}, nextID: 100, nextSHA: 1}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// BaseURL is the install's address, the connection's base address.
func (h *Host) BaseURL() string { return h.URL + Context }

// Path is the repository's path on the host, KEY/slug.
func (h *Host) Path() string { return h.Key + "/" + h.Slug }

func (h *Host) violate(w http.ResponseWriter, code int, f string, a ...any) {
	h.mu.Lock()
	h.Violations = append(h.Violations, fmt.Sprintf(f, a...))
	h.mu.Unlock()
	writeErr(w, code, fmt.Sprintf(f, a...))
}

func errBody(msg string) map[string]any {
	return map[string]any{"errors": []any{map[string]any{"context": nil, "message": msg, "exceptionName": "fake"}}}
}

func writeErr(w http.ResponseWriter, code int, msg string) { writeJSON(w, code, errBody(msg)) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (h *Host) sha() string {
	h.nextSHA++
	return fmt.Sprintf("%040x", h.nextSHA)
}

func (h *Host) repo() map[string]any {
	return map[string]any{"slug": h.Slug, "id": 1, "name": h.Slug, "scmId": "git", "state": "AVAILABLE", "public": false, "archived": false,
		"project": map[string]any{"key": h.Key, "id": 1, "name": h.Key, "type": "NORMAL"},
		"links": map[string]any{"clone": []any{
			map[string]any{"name": "http", "href": h.BaseURL() + "/scm/" + strings.ToLower(h.Key) + "/" + h.Slug + ".git"},
			map[string]any{"name": "ssh", "href": "ssh://git@127.0.0.1:7999/" + strings.ToLower(h.Key) + "/" + h.Slug + ".git"}}}}
}

func wrap(values ...any) map[string]any {
	if values == nil {
		values = []any{}
	}
	return map[string]any{"size": len(values), "limit": 25, "isLastPage": true, "start": 0, "values": values}
}

func (h *Host) repoRe() *regexp.Regexp {
	return regexp.MustCompile(`^/rest/(api|git|branch-permissions)/latest/projects/` + regexp.QuoteMeta(url.PathEscape(h.Key)) + `/repos/` + regexp.QuoteMeta(h.Slug) + `(/.*)?$`)
}

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	path := strings.TrimPrefix(r.URL.Path, Context)
	if !strings.HasPrefix(r.URL.Path, Context+"/") {
		h.violate(w, 404, "the install is at %s, not %s", Context, r.URL.Path)
		return
	}
	key := r.Method + " " + path
	if path != "/rest/api/latest/application-properties" && r.Header.Get("Authorization") != "Bearer "+Access {
		h.violate(w, 401, "missing or wrong bearer token on %s", key)
		return
	}
	var in map[string]any
	if len(body) > 0 && !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		if r.Header.Get("Content-Type") != "application/json" {
			h.violate(w, 400, "body of %s is not sent as application/json", key)
			return
		}
		if err := json.Unmarshal(body, &in); err != nil {
			h.violate(w, 400, "body of %s is not JSON", key)
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Method != http.MethodGet {
		h.Writes++
	}
	code, out, bad := h.route(r, path, in, body)
	if bad != "" {
		h.Violations = append(h.Violations, bad+" ("+key+")")
		writeErr(w, 400, "fake: "+bad)
		return
	}
	if code == 0 {
		h.Violations = append(h.Violations, "unknown call "+key)
		writeErr(w, 404, "fake: unknown call "+key)
		return
	}
	h.Requests = append(h.Requests, key)
	writeJSON(w, code, out)
}

// queryOK reports a query parameter the documentation does not list for the call.
func queryOK(q url.Values, allowed ...string) string {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range q {
		if !ok[k] {
			return "query parameter " + k + " is not documented for this call"
		}
	}
	return ""
}

func params(in map[string]any, required []string, optional ...string) string {
	for _, k := range required {
		if _, ok := in[k]; !ok {
			return "missing required field " + k
		}
	}
	ok := map[string]bool{}
	for _, k := range append(required, optional...) {
		ok[k] = true
	}
	for k := range in {
		if !ok[k] {
			return "field " + k + " is not documented for this call"
		}
	}
	return ""
}

func str(in map[string]any, k string) string { s, _ := in[k].(string); return s }
func unesc(s string) string                  { u, _ := url.PathUnescape(s); return u }
func sub(in map[string]any, k string) map[string]any {
	m, _ := in[k].(map[string]any)
	return m
}

type routeFn func(r *http.Request, sub string, in map[string]any, raw []byte) (int, any, string)

func (h *Host) route(r *http.Request, path string, in map[string]any, raw []byte) (int, any, string) {
	q := r.URL.Query()
	switch {
	case r.Method == "GET" && path == "/rest/api/latest/application-properties":
		return 200, map[string]any{"version": "10.2.0", "buildNumber": "10002000", "buildDate": "1", "displayName": "Bitbucket"}, ""
	case r.Method == "GET" && path == "/rest/api/latest/repos":
		return h.search(q)
	case r.Method == "GET" && strings.HasPrefix(path, "/rest/build-status/latest/commits/"):
		if bad := queryOK(q, "limit", "start", "orderBy"); bad != "" {
			return 0, nil, bad
		}
		vals := []any{}
		for _, s := range h.builds[strings.TrimPrefix(path, "/rest/build-status/latest/commits/")] {
			vals = append(vals, s)
		}
		return 200, wrap(vals...), ""
	}
	sm := h.repoRe().FindStringSubmatch(path)
	if sm == nil {
		return 0, nil, ""
	}
	switch sm[1] {
	case "git":
		return h.routeGit(r, sm[2], in)
	case "branch-permissions":
		return h.routeRestrictions(r, sm[2], q)
	}
	for _, f := range []routeFn{h.routeRepo, h.routeCommits, h.routePulls, h.routeSettings, h.routeBrowse} {
		if code, out, bad := f(r, sm[2], in, raw); code != 0 || bad != "" {
			return code, out, bad
		}
	}
	return 0, nil, ""
}

func (h *Host) search(q url.Values) (int, any, string) {
	if bad := queryOK(q, "limit", "start", "projectkey", "permission", "name"); bad != "" {
		return 0, nil, bad
	}
	if k := q.Get("projectkey"); k != "" && !strings.EqualFold(k, h.Key) {
		return 200, wrap(), ""
	}
	switch q.Get("permission") {
	case "", "REPO_READ":
	case "REPO_WRITE":
		if !h.Write {
			return 200, wrap(), ""
		}
	case "REPO_ADMIN":
		if !h.Admin {
			return 200, wrap(), ""
		}
	default:
		return 0, nil, "permission is REPO_READ, REPO_WRITE or REPO_ADMIN"
	}
	return 200, wrap(h.repo()), ""
}

// Sign signs a delivery body the way the host does: "sha256=" and the hex HMAC of the raw body.
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Delivery builds a signed delivery's headers.
func Delivery(event, id string, body []byte) http.Header {
	h := http.Header{}
	h.Set("X-Event-Key", event)
	h.Set("X-Request-Id", id)
	h.Set("X-Hub-Signature", Sign([]byte(WebhookSecret), body))
	return h
}

// File returns a file on a branch.
func (h *Host) File(branch, path string) ([]byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.files[branch][path]
	return b, ok
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
