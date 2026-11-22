package bitbucketdcfake

import (
	"fmt"
	"net/http"
	"strings"
)

func (h *Host) side(p *PR, dst bool) map[string]any {
	key, slug, _ := strings.Cut(p.SrcRepo, "/")
	name, commit := p.Src, p.SHA
	if dst {
		key, slug, name, commit = h.Key, h.Slug, p.Dst, h.branches[p.Dst]
	}
	return map[string]any{"id": "refs/heads/" + name, "displayId": name, "type": "BRANCH", "latestCommit": commit,
		"repository": map[string]any{"slug": slug, "project": map[string]any{"key": key}}}
}

func (h *Host) prJSON(p *PR) map[string]any {
	rev := []any{}
	for _, n := range p.Reviewers {
		rev = append(rev, map[string]any{"user": map[string]any{"name": n}, "role": "REVIEWER"})
	}
	return map[string]any{"id": p.ID, "version": p.Version, "title": p.Title, "description": p.Desc, "state": p.State, "draft": p.Draft, "open": p.State == "OPEN", "closed": p.State != "OPEN",
		"author": map[string]any{"user": map[string]any{"name": "svc-launchway", "displayName": "Launchway Service"}, "role": "AUTHOR"}, "reviewers": rev,
		"fromRef": h.side(p, false), "toRef": h.side(p, true),
		"links": map[string]any{"self": []any{map[string]any{"href": fmt.Sprintf("%s/projects/%s/repos/%s/pull-requests/%d", h.BaseURL(), h.Key, h.Slug, p.ID)}}}}
}

func (h *Host) findPR(n string) *PR {
	for _, p := range h.PRs {
		if fmt.Sprint(p.ID) == n {
			return p
		}
	}
	return nil
}

// checkRef reads a ref of a pull request body: a full ref name and the repository by slug and project key.
func (h *Host) checkRef(in map[string]any, k string, mustExist bool) (name, repo, bad string) {
	m := sub(in, k)
	if m == nil {
		return "", "", "missing required field " + k
	}
	id := str(m, "id")
	rp := sub(m, "repository")
	slug, key := str(rp, "slug"), str(sub(rp, "project"), "key")
	if !strings.HasPrefix(id, "refs/heads/") || slug == "" || key == "" {
		return "", "", k + " names a full ref and its repository by slug and project key"
	}
	name = strings.TrimPrefix(id, "refs/heads/")
	if _, ok := h.branches[name]; mustExist && !ok {
		return "", "", ""
	}
	return name, key + "/" + slug, ""
}

func (h *Host) routePulls(r *http.Request, sub string, in map[string]any, _ []byte) (int, any, string) {
	m, q := r.Method, r.URL.Query()
	switch {
	case m == "GET" && sub == "/pull-requests":
		if bad := queryOK(q, "state", "limit", "start", "order", "at", "direction", "filterText", "draft", "withAttributes", "withProperties"); bad != "" {
			return 0, nil, bad
		}
		st := q.Get("state")
		if st != "OPEN" && st != "MERGED" && st != "DECLINED" {
			return 0, nil, "state is OPEN, MERGED or DECLINED"
		}
		vals := []any{}
		for _, p := range h.PRs {
			if p.State == st {
				vals = append(vals, h.prJSON(p))
			}
		}
		return 200, wrap(vals...), ""
	case m == "POST" && sub == "/pull-requests":
		if bad := params(in, []string{"title", "fromRef", "toRef"}, "description", "draft", "reviewers", "state", "locked"); bad != "" {
			return 0, nil, bad
		}
		src, srcRepo, bad := h.checkRef(in, "fromRef", false)
		dst, _, bad2 := h.checkRef(in, "toRef", false)
		if bad != "" || bad2 != "" {
			return 0, nil, bad + bad2
		}
		if _, ok := h.branches[src]; !ok {
			return 409, errBody("source branch not found"), ""
		}
		p := &PR{ID: len(h.PRs) + 1, Version: 0, Title: str(in, "title"), Desc: str(in, "description"), State: "OPEN", Draft: in["draft"] == true, Src: src, Dst: dst, SHA: h.branches[src], SrcRepo: srcRepo}
		h.PRs = append(h.PRs, p)
		return 201, h.prJSON(p), ""
	case strings.HasPrefix(sub, "/pull-requests/"):
		return h.routePull(r, strings.TrimPrefix(sub, "/pull-requests/"), in)
	}
	return 0, nil, ""
}

// versioned checks the pull request's version (a query parameter or the body's) and moves it on.
func versioned(p *PR, got any) (int, any) {
	v, ok := got.(float64)
	if !ok || int(v) != p.Version {
		return 409, errBody("the pull request has been modified since the version given")
	}
	p.Version++
	return 0, nil
}

func (h *Host) routePull(r *http.Request, rest string, in map[string]any) (int, any, string) {
	m, q := r.Method, r.URL.Query()
	id, tail, _ := strings.Cut(rest, "/")
	p := h.findPR(id)
	if p == nil {
		return 404, errBody("no such pull request"), ""
	}
	switch {
	case m == "GET" && tail == "":
		if bad := queryOK(q, "withProperties"); bad != "" {
			return 0, nil, bad
		}
		return 200, h.prJSON(p), ""
	case m == "PUT" && tail == "":
		if bad := params(in, []string{"version"}, "title", "description", "draft", "reviewers", "toRef"); bad != "" {
			return 0, nil, bad
		}
		if code, out := versioned(p, in["version"]); code != 0 {
			return code, out, ""
		}
		p.Title, p.Desc = str(in, "title"), str(in, "description")
		if d, ok := in["draft"].(bool); ok {
			p.Draft = d
		}
		if rs, ok := in["reviewers"].([]any); ok {
			p.Reviewers = nil
			for _, x := range rs {
				p.Reviewers = append(p.Reviewers, str(sub(x.(map[string]any), "user"), "name"))
			}
		}
		return 200, h.prJSON(p), ""
	case m == "POST" && (tail == "decline" || tail == "reopen"):
		if bad := params(in, []string{"version"}, "comment"); bad != "" {
			return 0, nil, bad
		}
		if code, out := versioned(p, in["version"]); code != 0 {
			return code, out, ""
		}
		p.State = map[string]string{"decline": "DECLINED", "reopen": "OPEN"}[tail]
		return 200, h.prJSON(p), ""
	case m == "POST" && tail == "merge":
		return h.merge(p, q.Get("version"), in)
	case m == "DELETE" && tail == "auto-merge":
		if _, ok := h.AutoMergeRequested[p.ID]; !ok {
			return 404, errBody("no auto-merge request"), ""
		}
		delete(h.AutoMergeRequested, p.ID)
		return 204, nil, ""
	case tail == "comments" || strings.HasPrefix(tail, "comments/"):
		return h.routeComment(m, tail, in)
	}
	return 0, nil, ""
}

// merge: POST .../merge. A connection never merges at once, so a body that does
// not set autoMerge is a violation.
func (h *Host) merge(p *PR, version string, in map[string]any) (int, any, string) {
	if in["autoMerge"] != true {
		return 0, nil, "the connection asks for auto-merge only (autoMerge true), never an immediate merge"
	}
	if bad := params(in, []string{"autoMerge"}, "strategyId", "message", "autoSubject", "bypassMergeQueue", "version"); bad != "" {
		return 0, nil, bad
	}
	if version != fmt.Sprint(p.Version) {
		return 409, errBody("the pull request has been modified since the version given"), ""
	}
	if !h.AutoMerge {
		return 409, errBody("auto-merge is not enabled for this repository"), ""
	}
	if s := str(in, "strategyId"); s != "no-ff" && s != "squash" {
		return 0, nil, "strategyId is one the host lists: " + s
	}
	h.AutoMergeRequested[p.ID] = str(in, "strategyId")
	return 200, h.prJSON(p), ""
}

func (h *Host) routeComment(m, tail string, in map[string]any) (int, any, string) {
	switch {
	case m == "POST" && tail == "comments":
		if bad := params(in, []string{"text"}, "parent", "anchor", "severity", "state", "properties"); bad != "" || str(in, "text") == "" {
			return 0, nil, "a comment has text " + bad
		}
		h.nextID++
		h.Comments[h.nextID] = str(in, "text")
		h.commentVer[h.nextID] = 0
		return 201, map[string]any{"id": h.nextID, "version": 0, "text": str(in, "text")}, ""
	case (m == "GET" || m == "PUT") && strings.HasPrefix(tail, "comments/"):
		id := atoi(strings.TrimPrefix(tail, "comments/"))
		if _, ok := h.Comments[id]; !ok {
			return 404, errBody("no such comment"), ""
		}
		if m == "GET" {
			return 200, map[string]any{"id": id, "version": h.commentVer[id], "text": h.Comments[id]}, ""
		}
		if bad := params(in, []string{"text", "version"}, "severity", "state", "anchor", "properties"); bad != "" {
			return 0, nil, "an edit has text and the comment's version " + bad
		}
		if v, _ := in["version"].(float64); int(v) != h.commentVer[id] {
			return 409, errBody("the comment has been modified since the version given"), ""
		}
		h.Comments[id] = str(in, "text")
		h.commentVer[id]++
		return 200, map[string]any{"id": id, "version": h.commentVer[id], "text": h.Comments[id]}, ""
	}
	return 0, nil, ""
}
