// Package gitlabfake is a fake of the GitLab API for tests: an httptest server
// that implements exactly the documented calls the gitlab provider uses
// (https://docs.gitlab.com/api/) and is strict. An unknown path, a wrong
// method, a missing or wrong PRIVATE-TOKEN header, a missing required
// parameter, a parameter the documentation does not list for the call, or a
// body that is not JSON all fail the request with a status the host would give
// and are recorded in Violations so a test can assert there were none.
package gitlabfake

import (
	"crypto/rand"
	"encoding/base64"
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
const Token = "glpat-faketoken1234567890"

// WebhookSecret is the secret token a test configures on the connection.
const WebhookSecret = "hook-secret-token"

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	// Requests is every accepted request as "METHOD path".
	Requests []string
	// Writes counts the accepted mutating requests.
	Writes  int
	Expires string // expires_at of the token ("" none)
	Scopes  []string
	// Level is the token's role on the project (access_level).
	Level int
	// Protect: the default branch is protected, with push access level 0.
	Protect bool
	// NoSelf makes /personal_access_tokens/self answer 404 (a project token).
	NoSelf bool
	// MaxRequests is the largest number of notes etc; unused.
	branches map[string]string
	tags     map[string]tag
	files    map[string]map[string][]byte // branch -> path -> content
	releases map[string]*release
	nextID   int
	nextSHA  int
	Statuses []Status
	Notes    map[int]string
	MRs      []*MR
	// AutoMerge is the merge requests with auto-merge on, by iid, with squash.
	AutoMerge map[int]bool
	Packages  map[string][]byte
}

// Status is one commit status the fake received.
type Status struct{ SHA, State, Name, Description, TargetURL string }

type tag struct{ Commit, Target, Message string }

type release struct {
	Tag, Name, Description string
	Links                  []map[string]any
}

// MR is a merge request of the fake; tests may change it under Lock.
type MR struct {
	IID                          int
	Title, Description, State    string
	Draft                        bool
	Source, Target               string
	SourceProject, TargetProject int
	SHA                          string
}

// Lock and Unlock guard the fake's state from a test.
func (h *Host) Lock()   { h.mu.Lock() }
func (h *Host) Unlock() { h.mu.Unlock() }

// New starts a fake with the project acme/app (id 7, default branch main).
func New() *Host {
	h := &Host{branches: map[string]string{"main": strings.Repeat("a", 40)}, tags: map[string]tag{}, files: map[string]map[string][]byte{"main": {}},
		releases: map[string]*release{}, Notes: map[int]string{}, Packages: map[string][]byte{}, AutoMerge: map[int]bool{},
		Level: 40, Scopes: []string{"api"}, Expires: "2030-01-02", nextID: 100, nextSHA: 1}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// BaseURL is what a connection's base address is: the fake acts as a
// self-managed install, whose API is at /api/v4.
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

func (h *Host) project() map[string]any {
	return map[string]any{"id": 7, "name": "app", "path": "app", "path_with_namespace": "acme/app", "visibility": "private", "archived": false,
		"default_branch": "main", "http_url_to_repo": "https://git.example.com/acme/app.git", "ssh_url_to_repo": "git@git.example.com:acme/app.git",
		"web_url": "https://git.example.com/acme/app", "namespace": map[string]any{"id": 1, "full_path": "acme", "kind": "group"},
		"permissions": map[string]any{"project_access": map[string]any{"access_level": h.Level}, "group_access": nil}}
}

var rePrefix = regexp.MustCompile(`^/api/v4/projects/acme%2Fapp(/.*)?$`)

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if r.Header.Get("PRIVATE-TOKEN") != Token {
		h.violate(w, 401, "missing or wrong PRIVATE-TOKEN on %s %s", r.Method, r.URL.Path)
		return
	}
	if r.Header.Get("Authorization") != "" {
		h.violate(w, 400, "an Authorization header was sent as well as PRIVATE-TOKEN on %s %s", r.Method, r.URL.Path)
		return
	}
	esc := r.URL.EscapedPath()
	key := r.Method + " " + esc
	var in map[string]any
	if len(body) > 0 && !strings.Contains(esc, "/packages/generic/") {
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
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Writes++
	}
	code, out, bad := h.route(r, esc, r.URL.Query(), in, body)
	if bad != "" {
		h.Violations = append(h.Violations, bad+" ("+key+")")
		writeJSON(w, 400, map[string]any{"message": "fake: " + bad})
		return
	}
	if code == 0 {
		h.Violations = append(h.Violations, "unknown call "+key)
		writeJSON(w, 404, map[string]any{"message": "404 Not Found (fake: unknown call " + key + ")"})
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

func (h *Host) mrJSON(m *MR) map[string]any {
	st := m.State
	return map[string]any{"iid": m.IID, "title": m.Title, "description": m.Description, "state": st, "draft": m.Draft,
		"source_branch": m.Source, "target_branch": m.Target, "source_project_id": m.SourceProject, "target_project_id": m.TargetProject,
		"sha": m.SHA, "merge_commit_sha": nil, "web_url": fmt.Sprintf("https://git.example.com/acme/app/-/merge_requests/%d", m.IID),
		"author": map[string]any{"username": "svc-launchway"}, "diff_refs": map[string]any{"base_sha": h.branches[m.Target]},
		"merged_at": nil, "merge_when_pipeline_succeeds": h.AutoMerge[m.IID]}
}

func (h *Host) findMR(iid string) *MR {
	n, _ := strconv.Atoi(iid)
	for _, m := range h.MRs {
		if m.IID == n {
			return m
		}
	}
	return nil
}

func unesc(s string) string { u, _ := url.PathUnescape(s); return u }

func (h *Host) release(t string) map[string]any {
	rl := h.releases[t]
	return map[string]any{"tag_name": rl.Tag, "name": rl.Name, "description": rl.Description,
		"_links": map[string]any{"self": "https://git.example.com/acme/app/-/releases/" + rl.Tag}}
}

// route answers one call; code 0 is an unknown call, bad a violation of the documented shape.
func (h *Host) route(r *http.Request, esc string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && esc == "/api/v4/user":
		return 200, map[string]any{"id": 3, "username": "svc-launchway", "name": "Launchway Service"}, ""
	case m == "GET" && esc == "/api/v4/personal_access_tokens/self":
		if h.NoSelf {
			return 404, map[string]any{"message": "404 Not Found"}, ""
		}
		var exp any
		if h.Expires != "" {
			exp = h.Expires
		}
		return 200, map[string]any{"id": 1, "name": "launchway", "revoked": false, "scopes": h.Scopes, "user_id": 3, "active": true, "expires_at": exp}, ""
	case m == "GET" && esc == "/api/v4/projects":
		if q.Get("membership") != "true" {
			return 0, nil, "the project listing is asked without membership=true"
		}
		return 200, []any{h.project()}, ""
	}
	sm := rePrefix.FindStringSubmatch(esc)
	if sm == nil {
		return 0, nil, ""
	}
	sub := sm[1]
	for _, f := range []func(*http.Request, string, url.Values, map[string]any, []byte) (int, any, string){h.routeRepo, h.routeStatus, h.routeMR, h.routeRelease, h.routePackage, h.routeFiles, h.routeProtected} {
		if code, out, bad := f(r, sub, q, in, raw); code != 0 || bad != "" {
			return code, out, bad
		}
	}
	return 0, nil, ""
}

func (h *Host) routeRepo(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && sub == "":
		return 200, h.project(), ""
	case m == "GET" && sub == "/repository/branches":
		var out []any
		for n, c := range h.branches {
			out = append(out, map[string]any{"name": n, "commit": map[string]any{"id": c}, "protected": n == "main" && h.Protect, "default": n == "main"})
		}
		return 200, out, ""
	case m == "GET" && strings.HasPrefix(sub, "/repository/branches/"):
		n := unesc(strings.TrimPrefix(sub, "/repository/branches/"))
		c, ok := h.branches[n]
		if !ok {
			return 404, map[string]any{"message": "404 Branch Not Found"}, ""
		}
		return 200, map[string]any{"name": n, "commit": map[string]any{"id": c}, "default": n == "main"}, ""
	case m == "GET" && sub == "/repository/tags":
		var out []any
		for n, t := range h.tags {
			out = append(out, map[string]any{"name": n, "target": t.Target, "commit": map[string]any{"id": t.Commit}})
		}
		return 200, out, ""
	case m == "GET" && strings.HasPrefix(sub, "/repository/tags/"):
		n := unesc(strings.TrimPrefix(sub, "/repository/tags/"))
		t, ok := h.tags[n]
		if !ok {
			return 404, map[string]any{"message": "404 Tag Not Found"}, ""
		}
		return 200, map[string]any{"name": n, "target": t.Target, "message": t.Message, "commit": map[string]any{"id": t.Commit}}, ""
	case m == "POST" && sub == "/repository/tags":
		if bad := params(in, []string{"tag_name", "ref"}, "message"); bad != "" {
			return 0, nil, bad
		}
		n := str(in, "tag_name")
		if _, ok := h.tags[n]; ok {
			return 409, map[string]any{"message": "Tag " + n + " already exists"}, ""
		}
		t := tag{Commit: str(in, "ref"), Message: str(in, "message")}
		if t.Message != "" {
			t.Target = h.sha() // an annotated tag's target is the tag object
		} else {
			t.Target = t.Commit
		}
		h.tags[n] = t
		return 201, map[string]any{"name": n, "target": t.Target, "message": t.Message, "commit": map[string]any{"id": t.Commit}}, ""
	case m == "GET" && strings.HasPrefix(sub, "/repository/commits/") && strings.HasSuffix(sub, "/statuses"):
		sha := unesc(strings.TrimSuffix(strings.TrimPrefix(sub, "/repository/commits/"), "/statuses"))
		latest := map[string]Status{}
		var order []string
		for _, s := range h.Statuses {
			if s.SHA == sha {
				if _, ok := latest[s.Name]; !ok {
					order = append(order, s.Name)
				}
				latest[s.Name] = s
			}
		}
		out := []any{}
		for _, n := range order {
			s := latest[n]
			out = append(out, map[string]any{"name": s.Name, "status": s.State, "description": s.Description, "target_url": s.TargetURL, "sha": sha})
		}
		return 200, out, ""
	case m == "GET" && strings.HasPrefix(sub, "/repository/commits/"):
		sha := unesc(strings.TrimPrefix(sub, "/repository/commits/"))
		return 200, map[string]any{"id": sha, "parent_ids": []string{strings.Repeat("0", 40)}, "message": "a commit", "author_name": "Dev", "author_email": "dev@example.com", "authored_date": "2026-01-02T03:04:05.000Z"}, ""
	}
	return 0, nil, ""
}

func (h *Host) routeStatus(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "POST" && strings.HasPrefix(sub, "/statuses/"):
		sha := unesc(strings.TrimPrefix(sub, "/statuses/"))
		if len(sha) != 40 {
			return 0, nil, "a status is posted to a 40 character sha"
		}
		if bad := params(in, []string{"state"}, "ref", "name", "context", "target_url", "description", "coverage", "pipeline_id"); bad != "" {
			return 0, nil, bad
		}
		if !map[string]bool{"pending": true, "running": true, "success": true, "failed": true, "canceled": true, "skipped": true}[str(in, "state")] {
			return 0, nil, "state is not one of the documented values"
		}
		for _, k := range []string{"description", "target_url", "ref"} {
			if len([]rune(str(in, k))) > 255 {
				return 0, nil, k + " is longer than 255 characters"
			}
		}
		h.Statuses = append(h.Statuses, Status{sha, str(in, "state"), str(in, "name"), str(in, "description"), str(in, "target_url")})
		return 201, map[string]any{"id": 1, "name": str(in, "name"), "sha": sha, "status": str(in, "state")}, ""
	}
	return 0, nil, ""
}

func (h *Host) routeMR(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && sub == "/merge_requests":
		want := q.Get("state")
		if want != "opened" && want != "closed" && want != "merged" {
			return 0, nil, "state is not one of the documented values"
		}
		out := []any{}
		for _, mr := range h.MRs {
			if mr.State == want {
				out = append(out, h.mrJSON(mr))
			}
		}
		return 200, out, ""
	case m == "POST" && sub == "/merge_requests":
		if bad := params(in, []string{"source_branch", "target_branch", "title"}, "description"); bad != "" {
			return 0, nil, bad
		}
		if _, ok := h.branches[str(in, "source_branch")]; !ok {
			return 400, map[string]any{"message": "source branch does not exist"}, ""
		}
		mr := &MR{IID: len(h.MRs) + 1, Title: str(in, "title"), Description: str(in, "description"), State: "opened", Source: str(in, "source_branch"),
			Target: str(in, "target_branch"), SourceProject: 7, TargetProject: 7, SHA: h.branches[str(in, "source_branch")]}
		h.MRs = append(h.MRs, mr)
		return 201, h.mrJSON(mr), ""
	case strings.HasPrefix(sub, "/merge_requests/"):
		return h.routeMRItem(r, sub, q, in, raw)
	}
	return 0, nil, ""
}

func (h *Host) routeMRAction(m string, mr *MR, tail string, in map[string]any) (int, any, string) {
	switch {
	case m == "PUT" && tail == "merge":
		if bad := params(in, nil, "auto_merge", "squash", "sha", "should_remove_source_branch", "merge_commit_message", "squash_commit_message"); bad != "" {
			return 0, nil, bad
		}
		if mr == nil {
			return 404, map[string]any{"message": "404 Not found"}, ""
		}
		if in["auto_merge"] != true {
			return 0, nil, "Launchway never merges directly: auto_merge must be true"
		}
		h.AutoMerge[mr.IID] = true
		return 200, h.mrJSON(mr), ""
	case m == "POST" && tail == "cancel_auto_merge":
		if len(in) > 0 {
			return 0, nil, "cancel_auto_merge takes no parameters"
		}
		if mr == nil || !h.AutoMerge[mr.IID] {
			return 409, map[string]any{"message": "Can't cancel the automatic merge"}, ""
		}
		delete(h.AutoMerge, mr.IID)
		return 201, h.mrJSON(mr), ""
	case m == "POST" && tail == "notes":
		if bad := params(in, []string{"body"}); bad != "" {
			return 0, nil, bad
		}
		if mr == nil {
			return 404, map[string]any{"message": "404 Not found"}, ""
		}
		h.nextID++
		h.Notes[h.nextID] = str(in, "body")
		return 201, map[string]any{"id": h.nextID, "body": str(in, "body"), "system": false}, ""
	case m == "PUT" && strings.HasPrefix(tail, "notes/"):
		if bad := params(in, []string{"body"}); bad != "" {
			return 0, nil, bad
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(tail, "notes/"))
		if _, ok := h.Notes[id]; !ok || mr == nil {
			return 404, map[string]any{"message": "404 Note Not Found"}, ""
		}
		h.Notes[id] = str(in, "body")
		return 200, map[string]any{"id": id, "body": str(in, "body")}, ""
	}
	return 0, nil, ""
}

func (h *Host) routeMRItem(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case strings.HasPrefix(sub, "/merge_requests/"):
		rest := strings.TrimPrefix(sub, "/merge_requests/")
		iid, tail, _ := strings.Cut(rest, "/")
		mr := h.findMR(iid)
		switch {
		case m == "GET" && tail == "":
			if mr == nil {
				return 404, map[string]any{"message": "404 Not found"}, ""
			}
			return 200, h.mrJSON(mr), ""
		case m == "PUT" && tail == "":
			if bad := params(in, nil, "title", "description", "state_event"); bad != "" || len(in) == 0 {
				return 0, nil, "an update names at least one documented parameter: " + bad
			}
			if mr == nil {
				return 404, map[string]any{"message": "404 Not found"}, ""
			}
			if v, ok := in["title"]; ok {
				mr.Title = v.(string)
			}
			if v, ok := in["description"]; ok {
				mr.Description = v.(string)
			}
			switch str(in, "state_event") {
			case "close":
				mr.State = "closed"
			case "reopen":
				mr.State = "opened"
			case "":
			default:
				return 0, nil, "state_event is close or reopen"
			}
			return 200, h.mrJSON(mr), ""
		default:
			return h.routeMRAction(m, mr, tail, in)
		}
	}
	return 0, nil, ""
}

func (h *Host) routeRelease(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "POST" && sub == "/releases":
		if bad := params(in, []string{"tag_name"}, "name", "description"); bad != "" {
			return 0, nil, bad
		}
		t := str(in, "tag_name")
		if _, ok := h.tags[t]; !ok {
			return 0, nil, "a release is made for a tag that exists (ref would be needed)"
		}
		if _, ok := h.releases[t]; ok {
			return 409, map[string]any{"message": "Release already exists"}, ""
		}
		h.releases[t] = &release{Tag: t, Name: str(in, "name"), Description: str(in, "description")}
		return 201, h.release(t), ""
	case strings.HasPrefix(sub, "/releases/"):
		rest := strings.TrimPrefix(sub, "/releases/")
		t, tail, _ := strings.Cut(rest, "/")
		t = unesc(t)
		rl := h.releases[t]
		switch {
		case m == "GET" && tail == "":
			if rl == nil {
				return 404, map[string]any{"message": "404 Not Found"}, ""
			}
			return 200, h.release(t), ""
		case m == "PUT" && tail == "":
			if bad := params(in, nil, "name", "description"); bad != "" {
				return 0, nil, bad
			}
			if rl == nil {
				return 404, map[string]any{"message": "404 Not Found"}, ""
			}
			rl.Name, rl.Description = str(in, "name"), str(in, "description")
			return 200, h.release(t), ""
		case m == "GET" && tail == "assets/links":
			if rl == nil {
				return 404, map[string]any{"message": "404 Not Found"}, ""
			}
			out := []any{}
			for _, l := range rl.Links {
				out = append(out, l)
			}
			return 200, out, ""
		case m == "POST" && tail == "assets/links":
			if bad := params(in, []string{"name", "url"}, "direct_asset_path", "link_type"); bad != "" {
				return 0, nil, bad
			}
			if rl == nil {
				return 404, map[string]any{"message": "404 Not Found"}, ""
			}
			if lt := str(in, "link_type"); lt != "" && !map[string]bool{"other": true, "runbook": true, "image": true, "package": true}[lt] {
				return 0, nil, "link_type is not one of the documented values"
			}
			if _, ok := h.Packages[strings.TrimPrefix(str(in, "url"), h.URL+"/api/v4/projects/acme%2Fapp/packages/generic/")]; !ok {
				return 0, nil, "the link names a file that was not uploaded to the package registry"
			}
			h.nextID++
			l := map[string]any{"id": h.nextID, "name": str(in, "name"), "url": str(in, "url"), "link_type": str(in, "link_type")}
			rl.Links = append(rl.Links, l)
			return 201, l, ""
		}
		return 0, nil, ""
	}
	return 0, nil, ""
}

func (h *Host) routePackage(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "PUT" && strings.HasPrefix(sub, "/packages/generic/"):
		rest := strings.TrimPrefix(sub, "/packages/generic/")
		if strings.Count(rest, "/") != 2 || strings.ContainsAny(unesc(rest), " ") {
			return 0, nil, "a package file is /:package_name/:package_version/:file_name with no spaces"
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			return 0, nil, "a package file is sent as a raw body"
		}
		h.Packages[rest] = raw
		return 201, map[string]any{"message": "201 Created"}, ""
	}
	return 0, nil, ""
}

func (h *Host) routeFiles(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case strings.HasPrefix(sub, "/repository/files/"):
		path := unesc(strings.TrimPrefix(sub, "/repository/files/"))
		switch m {
		case "HEAD":
			ref := q.Get("ref")
			if ref == "" {
				return 0, nil, "ref is required"
			}
			if _, ok := h.files[ref][path]; !ok {
				return 404, nil, ""
			}
			return 200, nil, ""
		case "POST", "PUT":
			if bad := params(in, []string{"branch", "content", "commit_message"}, "start_branch", "encoding", "author_email", "author_name", "execute_filemode", "last_commit_id"); bad != "" {
				return 0, nil, bad
			}
			if in["encoding"] != "base64" {
				return 0, nil, "the fake accepts base64 content only"
			}
			b, err := base64.StdEncoding.DecodeString(str(in, "content"))
			if err != nil {
				return 0, nil, "content is not base64"
			}
			br := str(in, "branch")
			if sb := str(in, "start_branch"); sb != "" {
				if _, ok := h.branches[br]; ok {
					return 400, map[string]any{"message": "A branch called '" + br + "' already exists"}, ""
				}
				base, ok := h.branches[sb]
				if !ok {
					return 400, map[string]any{"message": "start_branch does not exist"}, ""
				}
				h.branches[br] = base
				h.files[br] = map[string][]byte{}
				for k, v := range h.files[sb] {
					h.files[br][k] = v
				}
			}
			if _, ok := h.branches[br]; !ok {
				return 400, map[string]any{"message": "branch does not exist"}, ""
			}
			_, exists := h.files[br][path]
			if (m == "POST") == exists {
				return 400, map[string]any{"message": "A file with this name already exists / doesn't exist"}, ""
			}
			h.files[br][path] = b
			h.branches[br] = h.sha()
			code := 201
			if m == "PUT" {
				code = 200
			}
			return code, map[string]any{"file_path": path, "branch": br}, ""
		}
		return 0, nil, ""
	}
	return 0, nil, ""
}

func (h *Host) routeProtected(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && strings.HasPrefix(sub, "/protected_branches/"):
		n := unesc(strings.TrimPrefix(sub, "/protected_branches/"))
		if n != "main" || !h.Protect {
			return 404, map[string]any{"message": "404 Not found"}, ""
		}
		return 200, map[string]any{"id": 1, "name": "main", "allow_force_push": false, "code_owner_approval_required": false,
			"push_access_levels":  []any{map[string]any{"access_level": 0, "access_level_description": "No one"}},
			"merge_access_levels": []any{map[string]any{"access_level": 40, "access_level_description": "Maintainers"}}}, ""
	}
	return 0, nil, ""
}

// File returns a file on a branch.
func (h *Host) File(branch, path string) ([]byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.files[branch][path]
	return b, ok
}

// Sign is not needed: a delivery carries the secret token itself. Delivery
// builds a delivery's headers.
func Delivery(event string) http.Header {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	h := http.Header{}
	h.Set("X-Gitlab-Token", WebhookSecret)
	h.Set("X-Gitlab-Event", event)
	h.Set("webhook-id", hex.EncodeToString(b))
	return h
}
