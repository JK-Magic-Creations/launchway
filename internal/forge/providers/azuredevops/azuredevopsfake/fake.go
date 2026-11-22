// Package azuredevopsfake is a fake of the Azure DevOps REST API for tests: an
// httptest server that implements exactly the documented calls the azuredevops
// provider uses (the reference pages under
// https://learn.microsoft.com/en-us/rest/api/azure/devops/, version 7.1) and is
// strict. An unknown path, a wrong method, a call without api-version=7.1, a
// missing or wrong credential (HTTP Basic with an empty user name and the token
// as the password), a missing required field, a field or query parameter the
// documentation does not list for the call, or a body of the wrong type all fail
// the request with a status the host would give and are recorded in Violations
// so a test can assert there were none.
//
// New serves the cloud shape (/{organization}/{project}/_apis/...); NewServer
// serves the server product's shape (/tfs/DefaultCollection/{project}/_apis/...).
package azuredevopsfake

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

// Token is the only credential the fake accepts.
const Token = "ado-fake-personal-access-token"

// WebhookSecret is the secret a test configures on the connection.
const WebhookSecret = "hook-secret"

// Names of the one repository the fake holds.
const (
	Project = "proj"
	Repo    = "app"
	repoID  = "11111111-2222-3333-4444-555555555555"
	userID  = "d6245f20-2af8-44f4-9451-8107cb2767db"
)

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	Requests   []string
	Writes     int
	org        string
	prefix     string
	// Protect: the default branch has a minimum approval count, a build policy and a required reviewers policy.
	Protect bool
	// TagWithoutRef: the annotated tag call makes the tag object but not the ref (the reference does not say it makes the ref).
	TagWithoutRef bool
	branches      map[string]string
	tags          map[string]tag
	orphan        map[string]tag
	files         map[string]map[string][]byte
	statuses      map[string]map[string]map[string]any // sha -> genre/name -> status
	PRStatuses    map[int]map[string]map[string]any
	Comments      map[string]string // "thread:comment" -> content
	PRs           []*PR
	nextID        int
	nextSHA       int
}

type tag struct{ obj, commit string }

// PR is a pull request of the fake; tests may change it under Lock.
type PR struct {
	ID                  int
	Title, Desc, Status string
	Draft               bool
	Src, Dst, SHA       string // Src and Dst are full ref names
	Fork                bool
	Auto                bool
	Strategy            string
}

func (h *Host) Lock()   { h.mu.Lock() }
func (h *Host) Unlock() { h.mu.Unlock() }

func newHost(org, prefix string) *Host {
	h := &Host{org: org, prefix: prefix, branches: map[string]string{"main": strings.Repeat("a", 40)}, tags: map[string]tag{},
		files: map[string]map[string][]byte{"main": {"README.md": []byte("hi")}}, statuses: map[string]map[string]map[string]any{},
		PRStatuses: map[int]map[string]map[string]any{}, Comments: map[string]string{}, nextID: 100, nextSHA: 1}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// New starts a cloud-shaped fake with the organization acme, the project proj and the repository app (default branch main).
func New() *Host { return newHost("acme", "/acme") }

// NewServer starts a server-shaped fake whose collection is DefaultCollection.
func NewServer() *Host { return newHost("DefaultCollection", "/tfs/DefaultCollection") }

// API is the address a provider's endpoints are set to.
func (h *Host) API() string {
	if h.org == "acme" {
		return h.URL
	}
	return h.URL + h.prefix
}

// Collection is "" for the cloud shape and the collection's name for the server shape.
func (h *Host) Collection() string {
	if h.org == "acme" {
		return ""
	}
	return h.org
}

// RepoPath is the repository's name as a connection knows it: organization/project/repository.
func (h *Host) RepoPath() string { return h.org + "/" + Project + "/" + Repo }

// Org is the organization (or collection) of the repository.
func (h *Host) Org() string { return h.org }

func (h *Host) violate(w http.ResponseWriter, code int, f string, a ...any) {
	h.mu.Lock()
	h.Violations = append(h.Violations, fmt.Sprintf(f, a...))
	h.mu.Unlock()
	writeJSON(w, code, map[string]any{"message": fmt.Sprintf(f, a...)})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (h *Host) sha() string {
	h.nextSHA++
	return fmt.Sprintf("%040x", h.nextSHA)
}

func (h *Host) remoteURL() string {
	if h.org == "acme" { // the cloud service puts the organization in front of the address as a user name
		return "https://acme@dev.azure.com/acme/" + Project + "/_git/" + Repo
	}
	return "https://devops.example.com/tfs/DefaultCollection/" + Project + "/_git/" + Repo
}

func (h *Host) repo() map[string]any {
	return map[string]any{"id": repoID, "name": Repo, "defaultBranch": "refs/heads/main", "isFork": false, "isDisabled": false,
		"remoteUrl": h.remoteURL(), "sshUrl": "git@ssh.dev.azure.com:v3/" + h.org + "/" + Project + "/" + Repo,
		"webUrl":  strings.Replace(h.remoteURL(), "https://acme@", "https://", 1),
		"project": map[string]any{"id": "99999999-2222-3333-4444-555555555555", "name": Project}}
}

func list(values ...any) map[string]any {
	if values == nil {
		values = []any{}
	}
	return map[string]any{"count": len(values), "value": values}
}

// call is one request as the routes see it.
type call struct {
	r     *http.Request
	sub   string // the path below the repository: "/pullrequests/1"
	q     url.Values
	in    map[string]any
	inArr []any
}

// route results: code 0 and bad "" mean "not mine".
type result struct {
	code int
	out  any
	bad  string
}

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+Token))
	if r.Header.Get("Authorization") != want {
		h.violate(w, 401, "missing or wrong credential on %s %s (HTTP Basic, empty user name, the token as the password)", r.Method, r.URL.Path)
		return
	}
	key := r.Method + " " + r.URL.Path
	if r.URL.Query().Get("api-version") != "7.1" {
		h.violate(w, 400, "%s does not name api-version=7.1", key)
		return
	}
	c := &call{r: r, q: r.URL.Query()}
	if len(raw) > 0 {
		if r.Header.Get("Content-Type") != "application/json" {
			h.violate(w, 400, "body of %s is not sent as application/json", key)
			return
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			h.violate(w, 400, "body of %s is not JSON", key)
			return
		}
		c.in, _ = v.(map[string]any)
		c.inArr, _ = v.([]any)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Method != http.MethodGet {
		h.Writes++
	}
	res := h.route(c)
	switch {
	case res.bad != "":
		h.Violations = append(h.Violations, res.bad+" ("+key+")")
		writeJSON(w, 400, map[string]any{"message": "fake: " + res.bad})
	case res.code == 0:
		h.Violations = append(h.Violations, "unknown call "+key)
		writeJSON(w, 404, map[string]any{"message": "fake: unknown call " + key})
	default:
		h.Requests = append(h.Requests, key)
		writeJSON(w, res.code, res.out)
	}
}

func bad(f string, a ...any) result { return result{bad: fmt.Sprintf(f, a...)} }
func ok(out any) result             { return result{code: 200, out: out} }
func notFound() result              { return result{code: 404, out: map[string]any{"message": "not found"}} }

// fields checks a body: every required field is there and nothing else but the optional ones.
func fields(in map[string]any, required []string, optional ...string) string {
	allowed := map[string]bool{}
	for _, k := range required {
		if _, ok := in[k]; !ok {
			return "missing required field " + k
		}
		allowed[k] = true
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range in {
		if !allowed[k] {
			return "field " + k + " is not documented for this call"
		}
	}
	return ""
}

// params checks the query: only the listed parameters (and api-version).
func params(q url.Values, allowed ...string) string {
	ok := map[string]bool{"api-version": true}
	for _, k := range allowed {
		ok[k] = true
	}
	for k := range q {
		if !ok[k] {
			return "query parameter " + k + " is not documented for this call"
		}
	}
	return ""
}

func str(in map[string]any, k string) string { s, _ := in[k].(string); return s }
func sub(in map[string]any, k string) map[string]any {
	m, _ := in[k].(map[string]any)
	return m
}

// Basic builds the Authorization header a service hook sends with its basic
// authentication username and password (the web hooks consumer's inputs).
func Basic(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

// File returns a file on a branch.
func (h *Host) File(branch, path string) ([]byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.files[branch][path]
	return b, ok
}

// Branch returns a branch's tip.
func (h *Host) Branch(name string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.branches[name]
	return c, ok
}
