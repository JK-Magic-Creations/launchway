// Package bitbucketfake is a fake of the Bitbucket Cloud API for tests: an
// httptest server that implements exactly the documented calls the bitbucket
// provider uses (the operations of https://api.bitbucket.org/swagger.json) and
// is strict. An unknown path, a wrong method, a missing or wrong bearer token, a
// missing required field, a field the documentation does not list for the call, or
// a body of the wrong type all fail the request with a status the host would
// give and are recorded in Violations so a test can assert there were none.
package bitbucketfake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Token is the only credential the fake accepts.
const Token = "bbat_faketoken1234567890"

// WebhookSecret is the secret a test configures on the connection.
const WebhookSecret = "hook-secret"

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	Requests   []string
	Writes     int
	// Write: the account has write permission on the repository.
	Write bool
	// NoAccount: the token is a repository access token and has no user.
	NoAccount bool
	// Protect: the default branch has a push restriction.
	Protect  bool
	branches map[string]string
	tags     map[string]string
	files    map[string]map[string][]byte
	statuses map[string]map[string]map[string]any // sha -> key -> status
	Comments map[int]string
	PRs      []*PR
	nextID   int
	nextSHA  int
}

// PR is a pull request of the fake; tests may change it under Lock.
type PR struct {
	ID                     int
	Title, Desc, State     string
	Draft                  bool
	Src, Dst, SHA, SrcRepo string
}

func (h *Host) Lock()   { h.mu.Lock() }
func (h *Host) Unlock() { h.mu.Unlock() }

// New starts a fake with the repository acme/app (default branch main).
func New() *Host {
	h := &Host{Write: true, branches: map[string]string{"main": strings.Repeat("a", 40)}, tags: map[string]string{},
		files: map[string]map[string][]byte{"main": {}}, statuses: map[string]map[string]map[string]any{}, Comments: map[int]string{}, nextID: 100, nextSHA: 1}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// API is the address a provider's endpoints are set to.
func (h *Host) API() string { return h.URL + "/2.0" }

func (h *Host) violate(w http.ResponseWriter, code int, f string, a ...any) {
	h.mu.Lock()
	h.Violations = append(h.Violations, fmt.Sprintf(f, a...))
	h.mu.Unlock()
	writeJSON(w, code, map[string]any{"type": "error", "error": map[string]any{"message": fmt.Sprintf(f, a...)}})
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

func (h *Host) repo() map[string]any {
	return map[string]any{"type": "repository", "full_name": "acme/app", "name": "App", "slug": "app", "is_private": true, "mainbranch": map[string]any{"name": "main"},
		"links": map[string]any{"clone": []any{map[string]any{"name": "https", "href": "https://bitbucket.org/acme/app.git"}, map[string]any{"name": "ssh", "href": "git@bitbucket.org:acme/app.git"}}}}
}

func wrap(values ...any) map[string]any {
	if values == nil {
		values = []any{}
	}
	return map[string]any{"size": len(values), "page": 1, "pagelen": 50, "values": values}
}

var reRepo = regexp.MustCompile(`^/2\.0/repositories/acme/app(/.*)?$`)

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if r.Header.Get("Authorization") != "Bearer "+Token {
		h.violate(w, 401, "missing or wrong bearer token on %s %s", r.Method, r.URL.Path)
		return
	}
	path := r.URL.Path
	key := r.Method + " " + path
	var in map[string]any
	if len(body) > 0 && !strings.HasSuffix(path, "/src") {
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
		writeJSON(w, 400, map[string]any{"type": "error", "error": map[string]any{"message": "fake: " + bad}})
		return
	}
	if code == 0 {
		h.Violations = append(h.Violations, "unknown call "+key)
		writeJSON(w, 404, map[string]any{"type": "error", "error": map[string]any{"message": "fake: unknown call " + key}})
		return
	}
	h.Requests = append(h.Requests, key)
	writeJSON(w, code, out)
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

type routeFn func(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string)

func (h *Host) route(r *http.Request, path string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && path == "/2.0/user":
		if h.NoAccount {
			return 403, map[string]any{"type": "error", "error": map[string]any{"message": "no account"}}, ""
		}
		return 200, map[string]any{"type": "user", "nickname": "svc-launchway", "display_name": "Launchway Service", "account_id": "1"}, ""
	case m == "GET" && path == "/2.0/user/workspaces":
		return 200, wrap(map[string]any{"type": "workspace_access", "workspace": map[string]any{"slug": "acme"}}), ""
	case m == "GET" && path == "/2.0/user/workspaces/acme/permissions/repositories":
		if h.NoAccount {
			return 403, nil, ""
		}
		p := "read"
		if h.Write {
			p = "write"
		}
		return 200, wrap(map[string]any{"type": "repository_permission", "permission": p, "repository": map[string]any{"full_name": "acme/app"}}), ""
	case m == "GET" && path == "/2.0/repositories/acme":
		if q.Get("role") != "member" {
			return 0, nil, "a listing is of the repositories the account is a member of (role=member)"
		}
		return 200, wrap(h.repo()), ""
	}
	sm := reRepo.FindStringSubmatch(path)
	if sm == nil {
		return 0, nil, ""
	}
	for _, f := range []routeFn{h.routeRepo, h.routeStatus, h.routePulls, h.routeSrc} {
		if code, out, bad := f(r, sm[1], q, in, raw); code != 0 || bad != "" {
			return code, out, bad
		}
	}
	return 0, nil, ""
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
	h.Set("X-Request-UUID", id)
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

func parseMultipart(r *http.Request, raw []byte) (map[string]string, map[string][]byte, string) {
	mt, p, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		return nil, nil, "a commit is sent as multipart/form-data"
	}
	mr := multipart.NewReader(strings.NewReader(string(raw)), p["boundary"])
	fields, files := map[string]string{}, map[string][]byte{}
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(part)
		if part.FileName() != "" {
			files[strings.TrimPrefix(part.FormName(), "/")] = b
		} else {
			fields[part.FormName()] = string(b)
		}
	}
	return fields, files, ""
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
