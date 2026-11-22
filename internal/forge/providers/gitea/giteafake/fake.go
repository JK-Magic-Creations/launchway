// Package giteafake is a fake of the Gitea and Forgejo API for tests: an
// httptest server that implements exactly the documented calls the gitea
// provider uses (the operations of the hosts' OpenAPI files) and is strict. An
// unknown path, a wrong method, a missing or wrong Authorization header, a
// missing required parameter, a parameter the documentation does not list for
// the call, or a body that is not JSON all fail the request with a status the
// host would give and are recorded in Violations so a test can assert there
// were none. New(true) behaves as Forgejo does where the two differ.
package giteafake

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

// Token is the only credential the fake accepts.
const Token = "gta_faketoken1234567890"

// WebhookSecret is the secret a test configures on the connection.
const WebhookSecret = "hook-secret"

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	Requests   []string
	Writes     int
	Forgejo    bool
	// Push: the account may push to the repository.
	Push bool
	// Protect: the default branch has a protection rule that disables direct pushes.
	Protect bool
	// NoProtectionAccess: the branch protection list answers 403.
	NoProtectionAccess bool
	branches           map[string]string
	tags               map[string]tagObj
	files              map[string]map[string][]byte
	releases           map[string]*release
	nextID             int
	nextSHA            int
	Statuses           []Status
	Comments           map[int]string
	PRs                []*PR
	// AutoMerge is the pull requests with a scheduled merge, by number.
	AutoMerge map[int]string
	Assets    map[int][]byte
}

// Status is one commit status the fake received.
type Status struct {
	ID                                          int
	SHA, State, Context, Description, TargetURL string
}

type tagObj struct{ Commit, ID, Message string }

type release struct {
	ID         int
	Tag, Name  string
	Body       string
	Prerelease bool
	Assets     []map[string]any
}

// PR is a pull request of the fake; tests may change it under Lock.
type PR struct {
	Number             int
	Title, Body, State string
	Draft, Merged      bool
	Head, Base         string
	HeadSHA            string
	HeadRepo           string
}

func (h *Host) Lock()   { h.mu.Lock() }
func (h *Host) Unlock() { h.mu.Unlock() }

// New starts a fake with the repository acme/app (default branch main).
func New(forgejo bool) *Host {
	h := &Host{Forgejo: forgejo, Push: true, branches: map[string]string{"main": strings.Repeat("a", 40)}, tags: map[string]tagObj{},
		files: map[string]map[string][]byte{"main": {}}, releases: map[string]*release{}, Comments: map[int]string{}, Assets: map[int][]byte{},
		AutoMerge: map[int]string{}, nextID: 100, nextSHA: 1}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// BaseURL is the install's address: the API is at /api/v1.
func (h *Host) BaseURL() string { return h.URL }

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

func (h *Host) user() map[string]any {
	return map[string]any{"id": 3, "login": "svc-launchway", "full_name": "Launchway Service"}
}

func (h *Host) repo() map[string]any {
	return map[string]any{"id": 7, "full_name": "acme/app", "name": "app", "private": true, "archived": false, "default_branch": "main",
		"clone_url": "https://git.example.com/acme/app.git", "ssh_url": "git@git.example.com:acme/app.git", "html_url": "https://git.example.com/acme/app",
		"owner": map[string]any{"login": "acme"}, "permissions": map[string]any{"admin": false, "push": h.Push, "pull": true}}
}

var reRepo = regexp.MustCompile(`^/api/v1/repos/acme/app(/.*)?$`)

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if r.Header.Get("Authorization") != "token "+Token {
		h.violate(w, 401, "missing or wrong Authorization on %s %s", r.Method, r.URL.Path)
		return
	}
	path := r.URL.Path
	key := r.Method + " " + path
	var in map[string]any
	if len(body) > 0 && !strings.HasSuffix(path, "/assets") {
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
	code, out, bad := h.route(r, path, r.URL.Query(), in, body)
	if bad != "" {
		h.Violations = append(h.Violations, bad+" ("+key+")")
		writeJSON(w, 400, map[string]any{"message": "fake: " + bad})
		return
	}
	if code == 0 {
		h.Violations = append(h.Violations, "unknown call "+key)
		writeJSON(w, 404, map[string]any{"message": "not found (fake: unknown call " + key + ")"})
		return
	}
	h.Requests = append(h.Requests, key)
	writeJSON(w, code, out)
}

// params checks a body against the documentation: every required name is
// present and no name is outside the documented ones.
func params(in map[string]any, required []string, optional ...string) string {
	for _, k := range required {
		if _, ok := in[k]; !ok {
			return "missing required parameter " + k
		}
	}
	ok := map[string]bool{}
	for _, k := range append(required, optional...) {
		ok[k] = true
	}
	for k := range in {
		if !ok[k] {
			return "parameter " + k + " is not documented for this call"
		}
	}
	return ""
}

func str(in map[string]any, k string) string { s, _ := in[k].(string); return s }
func unesc(s string) string                  { u, _ := url.PathUnescape(s); return u }

type routeFn func(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string)

func (h *Host) route(r *http.Request, path string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && path == "/api/v1/user":
		return 200, h.user(), ""
	case m == "GET" && path == "/api/v1/repos/search":
		if q.Get("uid") != "3" || q.Get("limit") == "" || func() bool { n, _ := strconv.Atoi(q.Get("limit")); return n > 50 }() {
			return 0, nil, "a search is for the account (uid) with a limit of at most 50"
		}
		return 200, map[string]any{"ok": true, "data": []any{h.repo()}}, ""
	}
	sm := reRepo.FindStringSubmatch(path)
	if sm == nil {
		return 0, nil, ""
	}
	for _, f := range []routeFn{h.routeRepo, h.routeStatus, h.routePulls, h.routeComments, h.routeReleases, h.routeFiles, h.routeProtection} {
		if code, out, bad := f(r, sm[1], q, in, raw); code != 0 || bad != "" {
			return code, out, bad
		}
	}
	return 0, nil, ""
}

// Sign signs a delivery body the way the host does: the hex HMAC-SHA256 of the
// raw body with no prefix.
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Delivery builds a signed delivery's headers for the host's kind.
func (h *Host) Delivery(event, id string, body []byte) http.Header {
	name := "Gitea"
	if h.Forgejo {
		name = "Forgejo"
	}
	hd := http.Header{}
	hd.Set("X-"+name+"-Event", event)
	hd.Set("X-"+name+"-Delivery", id)
	hd.Set("X-"+name+"-Signature", Sign([]byte(WebhookSecret), body))
	return hd
}

// File returns a file on a branch.
func (h *Host) File(branch, path string) ([]byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.files[branch][path]
	return b, ok
}
