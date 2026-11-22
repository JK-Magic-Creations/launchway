// Package githubfake is a fake of the outside host for tests: an httptest
// server that implements exactly the documented calls the github provider
// uses, and is strict. An unknown path, a wrong method, a missing or wrong
// credential, a missing API version header or a body that is not what the
// documentation says all fail the request with a status the host would give
// and are recorded in Violations so a test can assert there were none.
package githubfake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Token is the only credential the fake accepts.
const Token = "ghp_faketoken1234567890"

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	// Requests is every accepted request as "METHOD path".
	Requests []string
	// Writes counts the accepted mutating requests.
	Writes   int
	Expiry   string
	Protect  bool // the default branch is protected
	Push     bool // the token may push
	refs     map[string]string
	tagObjs  map[string]string // tag object sha -> commit
	files    map[string]file
	releases map[string]*release
	nextID   int64
	Statuses []Status
	Comments map[int64]string
	Pulls    []map[string]any
	Assets   map[int64][]byte
}

// Status is one commit status the fake received.
type Status struct{ SHA, State, Context, Description, TargetURL string }

type file struct {
	sha     string
	content []byte
}

type release struct {
	ID     int64
	Tag    string
	Body   string
	Assets []map[string]any
}

// New starts a fake with the repository acme/app (default branch main).
func New() *Host {
	h := &Host{refs: map[string]string{"heads/main": strings.Repeat("a", 40)}, tagObjs: map[string]string{}, files: map[string]file{},
		releases: map[string]*release{}, Comments: map[int64]string{}, Assets: map[int64][]byte{}, Push: true, Expiry: "2030-01-02 03:04:05 UTC"}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

var (
	reRepo   = regexp.MustCompile(`^/api/v3/repos/acme/app(/.*)?$`)
	reUpload = regexp.MustCompile(`^/api/uploads/repos/acme/app/releases/(\d+)/assets$`)
	reSHA    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// BaseURL is what a connection's base address is: the fake acts as a
// self-managed install, whose API is at /api/v3 and uploads at /api/uploads.
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
	_ = json.NewEncoder(w).Encode(v)
}

func repoJSON(push bool) map[string]any {
	return map[string]any{"full_name": "acme/app", "name": "app", "private": true, "visibility": "private", "archived": false, "default_branch": "main",
		"clone_url": "https://git.example.com/acme/app.git", "ssh_url": "git@git.example.com:acme/app.git",
		"owner": map[string]any{"login": "acme"}, "permissions": map[string]any{"admin": false, "push": push, "pull": true}}
}

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if r.Header.Get("Authorization") != "Bearer "+Token {
		h.violate(w, 401, "missing or wrong credential on %s %s", r.Method, r.URL.Path)
		return
	}
	if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" || r.Header.Get("Accept") != "application/vnd.github+json" {
		h.violate(w, 400, "missing API version or accept header on %s %s", r.Method, r.URL.Path)
		return
	}
	path := r.URL.Path
	key := r.Method + " " + path
	var in map[string]any
	if len(body) > 0 && !strings.Contains(path, "/api/uploads/") {
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
	code, out, ok := h.route(r, path, in, body)
	if !ok {
		h.Violations = append(h.Violations, "unknown call "+key)
		writeJSON(w, 404, map[string]any{"message": "Not Found (fake: unknown call " + key + ")"})
		return
	}
	h.Requests = append(h.Requests, key)
	w.Header().Set("X-RateLimit-Remaining", "4999")
	w.Header().Set("GitHub-Authentication-Token-Expiration", h.Expiry)
	writeJSON(w, code, out)
}

func (h *Host) bad(f string, a ...any) (int, any, bool) {
	h.Violations = append(h.Violations, fmt.Sprintf(f, a...))
	return 422, map[string]any{"message": fmt.Sprintf(f, a...)}, true
}

func (h *Host) id() int64 { h.nextID++; return h.nextID }

func (h *Host) route(r *http.Request, path string, in map[string]any, raw []byte) (int, any, bool) {
	m := r.Method
	if m == "GET" && path == "/api/v3/user" {
		return 200, map[string]any{"login": "svc-launchway", "name": "Launchway service"}, true
	}
	if m == "GET" && path == "/api/v3/user/repos" {
		return 200, []any{repoJSON(h.Push)}, true
	}
	if mm := reUpload.FindStringSubmatch(path); mm != nil {
		if m != "POST" || r.Header.Get("Content-Type") != "application/octet-stream" || r.URL.Query().Get("name") == "" {
			return h.bad("an upload is a POST of octet-stream with ?name=")
		}
		id, _ := strconv.ParseInt(mm[1], 10, 64)
		for _, rel := range h.releases {
			if rel.ID == id {
				aid := h.id()
				h.Assets[aid] = raw
				rel.Assets = append(rel.Assets, map[string]any{"id": aid, "name": r.URL.Query().Get("name"), "size": len(raw)})
				return 201, map[string]any{"id": aid, "name": r.URL.Query().Get("name"), "size": len(raw)}, true
			}
		}
		return 404, map[string]any{"message": "Not Found"}, true
	}
	mm := reRepo.FindStringSubmatch(path)
	if mm == nil {
		return 0, nil, false
	}
	sub := mm[1]
	switch {
	case m == "GET" && sub == "":
		return 200, repoJSON(h.Push), true
	}
	return h.routeGit(r, m, sub, in, raw)
}

// routeGit is the routes of refs and tags.
func (h *Host) routeGit(r *http.Request, m, sub string, in map[string]any, raw []byte) (int, any, bool) {
	switch {
	case m == "GET" && strings.HasPrefix(sub, "/git/matching-refs/"):
		prefix := strings.TrimPrefix(sub, "/git/matching-refs/")
		var out []any
		for k, v := range h.refs {
			if strings.HasPrefix(k, prefix+"/") {
				out = append(out, map[string]any{"ref": "refs/" + k, "object": map[string]any{"sha": v, "type": "commit"}})
			}
		}
		return 200, out, true
	case m == "GET" && strings.HasPrefix(sub, "/git/ref/"):
		name := strings.TrimPrefix(sub, "/git/ref/")
		sha, ok := h.refs[name]
		if !ok {
			return 404, map[string]any{"message": "Not Found"}, true
		}
		typ := "commit"
		if _, isTag := h.tagObjs[sha]; isTag {
			typ = "tag"
		}
		return 200, map[string]any{"ref": "refs/" + name, "object": map[string]any{"sha": sha, "type": typ}}, true
	case m == "GET" && strings.HasPrefix(sub, "/git/tags/"):
		sha := strings.TrimPrefix(sub, "/git/tags/")
		c, ok := h.tagObjs[sha]
		if !ok {
			return 404, map[string]any{"message": "Not Found"}, true
		}
		return 200, map[string]any{"sha": sha, "object": map[string]any{"sha": c, "type": "commit"}}, true
	case m == "POST" && sub == "/git/tags":
		for _, f := range []string{"tag", "message", "object", "type"} {
			if s, _ := in[f].(string); s == "" {
				return h.bad("create a tag object requires %s", f)
			}
		}
		if !reSHA.MatchString(in["object"].(string)) {
			return h.bad("object must be a commit sha")
		}
		sha := fmt.Sprintf("%040x", h.id()+1000)
		h.tagObjs[sha] = in["object"].(string)
		return 201, map[string]any{"sha": sha, "tag": in["tag"]}, true
	case m == "POST" && sub == "/git/refs":
		ref, _ := in["ref"].(string)
		sha, _ := in["sha"].(string)
		if !strings.HasPrefix(ref, "refs/") || !reSHA.MatchString(sha) {
			return h.bad("create a reference requires a full ref and a sha")
		}
		if _, dup := h.refs[strings.TrimPrefix(ref, "refs/")]; dup {
			return 422, map[string]any{"message": "Reference already exists"}, true
		}
		h.refs[strings.TrimPrefix(ref, "refs/")] = sha
		return 201, map[string]any{"ref": ref}, true
	}
	return h.routeStatuses(r, m, sub, in, raw)
}

// routeStatuses is the middle of the routes: statuses, commits, branches, pulls, comments.
func (h *Host) routeStatuses(r *http.Request, m, sub string, in map[string]any, raw []byte) (int, any, bool) {
	switch {
	case m == "POST" && strings.HasPrefix(sub, "/statuses/"):
		sha := strings.TrimPrefix(sub, "/statuses/")
		st, _ := in["state"].(string)
		if !reSHA.MatchString(sha) && len(sha) != 64 {
			return h.bad("a status is for a full sha")
		}
		switch st {
		case "error", "failure", "pending", "success":
		default:
			return h.bad("state must be error, failure, pending or success")
		}
		ctx, _ := in["context"].(string)
		d, _ := in["description"].(string)
		tu, _ := in["target_url"].(string)
		if len([]rune(d)) > 140 {
			return h.bad("description is over 140 characters")
		}
		if !h.Push {
			return 403, map[string]any{"message": "Resource not accessible by personal access token"}, true
		}
		h.Statuses = append(h.Statuses, Status{sha, st, ctx, d, tu})
		return 201, map[string]any{"state": st}, true
	case m == "GET" && strings.HasPrefix(sub, "/commits/") && strings.HasSuffix(sub, "/statuses"):
		sha := strings.TrimSuffix(strings.TrimPrefix(sub, "/commits/"), "/statuses")
		out := []any{}
		for i := len(h.Statuses) - 1; i >= 0; i-- {
			if s := h.Statuses[i]; s.SHA == sha {
				out = append(out, map[string]any{"state": s.State, "context": s.Context, "description": s.Description, "target_url": s.TargetURL})
			}
		}
		return 200, out, true
	case m == "GET" && strings.HasPrefix(sub, "/commits/"):
		return 200, map[string]any{"sha": strings.TrimPrefix(sub, "/commits/"), "parents": []any{},
			"commit": map[string]any{"message": "m", "author": map[string]any{"name": "A", "email": "a@example.com", "date": "2026-01-02T03:04:05Z"}}}, true
	case m == "GET" && sub == "/branches/main/protection":
		if !h.Protect {
			return 404, map[string]any{"message": "Branch not protected"}, true
		}
		return 200, map[string]any{"required_status_checks": map[string]any{"contexts": []string{"launchway/ci"}},
			"required_pull_request_reviews": map[string]any{"required_approving_review_count": 1}, "allow_force_pushes": map[string]any{"enabled": false}}, true
	}
	return h.routePulls(r, m, sub, in, raw)
}

// routePulls is the routes of pull requests and their comments.
func (h *Host) routePulls(r *http.Request, m, sub string, in map[string]any, raw []byte) (int, any, bool) {
	switch {
	case m == "GET" && sub == "/pulls":
		return 200, h.Pulls, true
	case m == "GET" && strings.HasPrefix(sub, "/pulls/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(sub, "/pulls/"))
		for _, p := range h.Pulls {
			if p["number"] == n {
				return 200, p, true
			}
		}
		return 404, map[string]any{"message": "Not Found"}, true
	case m == "POST" && sub == "/pulls":
		for _, f := range []string{"title", "head", "base"} {
			if s, _ := in[f].(string); s == "" {
				return h.bad("create a pull request requires %s", f)
			}
		}
		n := len(h.Pulls) + 1
		p := map[string]any{"number": n, "title": in["title"], "body": in["body"], "state": "open", "draft": in["draft"],
			"user": map[string]any{"login": "svc-launchway"},
			"head": map[string]any{"ref": in["head"], "sha": strings.Repeat("b", 40), "repo": map[string]any{"full_name": "acme/app"}},
			"base": map[string]any{"ref": in["base"], "sha": strings.Repeat("a", 40), "repo": map[string]any{"full_name": "acme/app"}}}
		h.Pulls = append(h.Pulls, p)
		return 201, p, true
	case m == "POST" && regexp.MustCompile(`^/issues/\d+/comments$`).MatchString(sub):
		b, _ := in["body"].(string)
		if b == "" {
			return h.bad("a comment has a body")
		}
		id := h.id()
		h.Comments[id] = b
		return 201, map[string]any{"id": id}, true
	case m == "PATCH" && strings.HasPrefix(sub, "/issues/comments/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(sub, "/issues/comments/"), 10, 64)
		if _, ok := h.Comments[id]; !ok {
			return 404, map[string]any{"message": "Not Found"}, true
		}
		b, _ := in["body"].(string)
		h.Comments[id] = b
		return 200, map[string]any{"id": id}, true
	}
	return h.routeReleases(r, m, sub, in, raw)
}

// routeReleases is the rest of the routes: releases, assets and file contents.
func (h *Host) routeReleases(r *http.Request, m, sub string, in map[string]any, raw []byte) (int, any, bool) {
	switch {
	case m == "GET" && strings.HasPrefix(sub, "/releases/tags/"):
		rel, ok := h.releases[strings.TrimPrefix(sub, "/releases/tags/")]
		if !ok {
			return 404, map[string]any{"message": "Not Found"}, true
		}
		return 200, h.releaseJSON(rel), true
	case m == "POST" && sub == "/releases":
		tag, _ := in["tag_name"].(string)
		if tag == "" {
			return h.bad("create a release requires tag_name")
		}
		if _, ok := h.refs["tags/"+tag]; !ok {
			return h.bad("the fake does not create a tag behind a release; the provider must have made it")
		}
		rel := &release{ID: h.id(), Tag: tag}
		rel.Body, _ = in["body"].(string)
		h.releases[tag] = rel
		return 201, h.releaseJSON(rel), true
	case m == "PATCH" && strings.HasPrefix(sub, "/releases/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(sub, "/releases/"), 10, 64)
		for _, rel := range h.releases {
			if rel.ID == id {
				rel.Body, _ = in["body"].(string)
				return 200, h.releaseJSON(rel), true
			}
		}
		return 404, map[string]any{"message": "Not Found"}, true
	case m == "DELETE" && strings.HasPrefix(sub, "/releases/assets/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(sub, "/releases/assets/"), 10, 64)
		for _, rel := range h.releases {
			for i, a := range rel.Assets {
				if a["id"] == id {
					rel.Assets = append(rel.Assets[:i], rel.Assets[i+1:]...)
					return 204, nil, true
				}
			}
		}
		return 404, map[string]any{"message": "Not Found"}, true
	case m == "GET" && strings.HasPrefix(sub, "/contents/"):
		f, ok := h.files[r.URL.Query().Get("ref")+":"+strings.TrimPrefix(sub, "/contents/")]
		if !ok {
			return 404, map[string]any{"message": "Not Found"}, true
		}
		return 200, map[string]any{"sha": f.sha}, true
	case m == "PUT" && strings.HasPrefix(sub, "/contents/"):
		p := strings.TrimPrefix(sub, "/contents/")
		br, _ := in["branch"].(string)
		msg, _ := in["message"].(string)
		c, err := base64.StdEncoding.DecodeString(fmt.Sprint(in["content"]))
		if msg == "" || err != nil || br == "" {
			return h.bad("a file write has a message, base64 content and a branch")
		}
		if h.Protect && br == "main" {
			return 409, map[string]any{"message": "Branch is protected"}, true
		}
		old, exists := h.files[br+":"+p]
		if exists && in["sha"] != old.sha {
			return 409, map[string]any{"message": p + " does not match " + old.sha}, true
		}
		if _, ok := h.refs["heads/"+br]; !ok {
			return 404, map[string]any{"message": "Branch not found"}, true
		}
		sum := sha256.Sum256(c)
		h.files[br+":"+p] = file{sha: hex.EncodeToString(sum[:20]), content: c}
		cs := fmt.Sprintf("%040x", h.id()+5000)
		h.refs["heads/"+br] = cs
		return 201, map[string]any{"commit": map[string]any{"sha": cs}}, true
	}
	return 0, nil, false
}

func (h *Host) releaseJSON(r *release) map[string]any {
	return map[string]any{"id": r.ID, "html_url": h.URL + "/acme/app/releases/tag/" + r.Tag,
		"upload_url": h.URL + "/api/uploads/repos/acme/app/releases/" + strconv.FormatInt(r.ID, 10) + "/assets{?name,label}", "assets": r.Assets}
}

// File returns what was committed to a branch.
func (h *Host) File(branch, path string) ([]byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.files[branch+":"+path]
	return f.content, ok
}

// Ref returns a ref's sha ("tags/v1", "heads/main").
func (h *Host) Ref(name string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.refs[name]
	return s, ok
}

// Sign is the signature header a delivery of body carries.
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Lock and Unlock let a test read Statuses, Comments and the rest while the
// server may still be serving.
func (h *Host) Lock()   { h.mu.Lock() }
func (h *Host) Unlock() { h.mu.Unlock() }
