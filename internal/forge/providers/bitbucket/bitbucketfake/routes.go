package bitbucketfake

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (h *Host) routeRepo(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && sub == "":
		return 200, h.repo(), ""
	case m == "GET" && sub == "/refs/branches":
		vals := []any{}
		for n, c := range h.branches {
			vals = append(vals, map[string]any{"name": n, "target": map[string]any{"hash": c}})
		}
		return 200, wrap(vals...), ""
	case m == "GET" && strings.HasPrefix(sub, "/refs/branches/"):
		n := unesc(strings.TrimPrefix(sub, "/refs/branches/"))
		c, ok := h.branches[n]
		if !ok {
			return 404, nil, ""
		}
		return 200, map[string]any{"name": n, "target": map[string]any{"hash": c}}, ""
	case m == "GET" && sub == "/refs/tags":
		vals := []any{}
		for n, c := range h.tags {
			vals = append(vals, map[string]any{"name": n, "target": map[string]any{"hash": c}})
		}
		return 200, wrap(vals...), ""
	case m == "GET" && strings.HasPrefix(sub, "/refs/tags/"):
		n := unesc(strings.TrimPrefix(sub, "/refs/tags/"))
		c, ok := h.tags[n]
		if !ok {
			return 404, nil, ""
		}
		return 200, map[string]any{"name": n, "target": map[string]any{"hash": c}}, ""
	case m == "POST" && sub == "/refs/tags":
		if bad := params(in, []string{"name", "target"}, "message", "type"); bad != "" {
			return 0, nil, bad
		}
		t := sub0(in, "target")
		if _, ok := t["hash"].(string); !ok {
			return 0, nil, "target.hash is required"
		}
		n := str(in, "name")
		if _, ok := h.tags[n]; ok {
			return 400, map[string]any{"type": "error", "error": map[string]any{"message": "tag already exists"}}, ""
		}
		h.tags[n] = str(t, "hash")
		return 201, map[string]any{"name": n, "target": t}, ""
	case m == "GET" && strings.HasPrefix(sub, "/commit/") && !strings.Contains(strings.TrimPrefix(sub, "/commit/"), "/"):
		return 200, map[string]any{"hash": unesc(strings.TrimPrefix(sub, "/commit/")), "date": "2026-01-02T03:04:05+00:00", "message": "a commit",
			"author": map[string]any{"raw": "Dev <dev@example.com>"}, "parents": []any{map[string]any{"hash": strings.Repeat("0", 40)}}}, ""
	case m == "GET" && sub == "/branch-restrictions":
		if !h.Write && h.NoAccount {
			return 403, nil, ""
		}
		vals := []any{}
		if h.Protect {
			vals = append(vals, map[string]any{"kind": "push", "pattern": "main", "branch_match_kind": "glob"},
				map[string]any{"kind": "require_approvals_to_merge", "pattern": "main", "branch_match_kind": "glob", "value": 2},
				map[string]any{"kind": "require_passing_builds_to_merge", "pattern": "main", "branch_match_kind": "glob", "value": 1})
		}
		return 200, wrap(vals...), ""
	}
	return 0, nil, ""
}

func sub0(in map[string]any, k string) map[string]any { return sub(in, k) }

func (h *Host) routeStatus(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	if !strings.HasPrefix(sub, "/commit/") || !strings.Contains(sub, "/statuses") {
		return 0, nil, ""
	}
	rest := strings.TrimPrefix(sub, "/commit/")
	sha, tail, _ := strings.Cut(rest, "/")
	m := r.Method
	switch {
	case m == "GET" && tail == "statuses":
		vals := []any{}
		for _, s := range h.statuses[sha] {
			vals = append(vals, s)
		}
		return 200, wrap(vals...), ""
	case (m == "POST" && tail == "statuses/build") || (m == "PUT" && strings.HasPrefix(tail, "statuses/build/")):
		if len(sha) != 40 {
			return 0, nil, "a status is posted to a 40 character commit"
		}
		if bad := params(in, nil, "key", "state", "name", "url", "description", "refname", "type"); bad != "" {
			return 0, nil, bad
		}
		if !map[string]bool{"FAILED": true, "INPROGRESS": true, "STOPPED": true, "SUCCESSFUL": true}[str(in, "state")] || str(in, "key") == "" {
			return 0, nil, "state is one of FAILED, INPROGRESS, STOPPED, SUCCESSFUL and a key is given"
		}
		k := str(in, "key")
		if m == "PUT" && strings.TrimPrefix(tail, "statuses/build/") != k {
			return 0, nil, "the key of the address and of the body are the same"
		}
		if h.statuses[sha] == nil {
			h.statuses[sha] = map[string]map[string]any{}
		}
		_, exists := h.statuses[sha][k]
		if m == "PUT" && !exists {
			return 404, map[string]any{"type": "error", "error": map[string]any{"message": "no such build status"}}, ""
		}
		if m == "POST" && exists {
			return 0, nil, "a build status that exists is updated with PUT"
		}
		h.statuses[sha][k] = in
		if m == "PUT" {
			return 200, in, ""
		}
		return 201, in, ""
	}
	return 0, nil, ""
}

func (h *Host) side(p *PR, dst bool) map[string]any {
	if dst {
		return map[string]any{"branch": map[string]any{"name": p.Dst}, "repository": map[string]any{"full_name": "acme/app"}, "commit": map[string]any{"hash": h.branches[p.Dst]}}
	}
	return map[string]any{"branch": map[string]any{"name": p.Src}, "repository": map[string]any{"full_name": p.SrcRepo}, "commit": map[string]any{"hash": p.SHA}}
}

func (h *Host) prJSON(p *PR) map[string]any {
	return map[string]any{"type": "pullrequest", "id": p.ID, "title": p.Title, "description": p.Desc, "state": p.State, "draft": p.Draft,
		"author": map[string]any{"nickname": "svc-launchway"}, "source": h.side(p, false), "destination": h.side(p, true),
		"links": map[string]any{"html": map[string]any{"href": fmt.Sprintf("https://bitbucket.org/acme/app/pull-requests/%d", p.ID)}}}
}

func (h *Host) findPR(n string) *PR {
	for _, p := range h.PRs {
		if fmt.Sprint(p.ID) == n {
			return p
		}
	}
	return nil
}

func (h *Host) routePulls(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && sub == "/pullrequests":
		st := q.Get("state")
		if st != "OPEN" && st != "MERGED" && st != "DECLINED" && st != "SUPERSEDED" {
			return 0, nil, "state is OPEN, MERGED, DECLINED or SUPERSEDED"
		}
		vals := []any{}
		for _, p := range h.PRs {
			if p.State == st {
				vals = append(vals, h.prJSON(p))
			}
		}
		return 200, wrap(vals...), ""
	case m == "POST" && sub == "/pullrequests":
		if bad := params(in, []string{"title", "source"}, "description", "draft", "destination", "close_source_branch", "reviewers", "type"); bad != "" {
			return 0, nil, bad
		}
		src := str(sub0(sub0(in, "source"), "branch"), "name")
		if _, ok := h.branches[src]; !ok {
			return 400, map[string]any{"type": "error", "error": map[string]any{"message": "source branch not found"}}, ""
		}
		dst := str(sub0(sub0(in, "destination"), "branch"), "name")
		p := &PR{ID: len(h.PRs) + 1, Title: str(in, "title"), Desc: str(in, "description"), State: "OPEN", Draft: in["draft"] == true, Src: src, Dst: dst, SHA: h.branches[src], SrcRepo: "acme/app"}
		h.PRs = append(h.PRs, p)
		return 201, h.prJSON(p), ""
	case strings.HasPrefix(sub, "/pullrequests/"):
		id, tail, _ := strings.Cut(strings.TrimPrefix(sub, "/pullrequests/"), "/")
		p := h.findPR(id)
		switch {
		case m == "GET" && tail == "":
			if p == nil {
				return 404, nil, ""
			}
			return 200, h.prJSON(p), ""
		case m == "PUT" && tail == "":
			if bad := params(in, nil, "title", "description", "draft"); bad != "" || len(in) == 0 {
				return 0, nil, "an update names title, description or draft: " + bad
			}
			if p == nil {
				return 404, nil, ""
			}
			if v, ok := in["title"]; ok {
				p.Title = v.(string)
			}
			if v, ok := in["description"]; ok {
				p.Desc = v.(string)
			}
			if v, ok := in["draft"]; ok {
				p.Draft = v.(bool)
			}
			return 200, h.prJSON(p), ""
		case m == "POST" && tail == "decline":
			if p == nil {
				return 404, nil, ""
			}
			p.State = "DECLINED"
			return 200, h.prJSON(p), ""
		case m == "POST" && tail == "comments":
			if bad := params(in, []string{"content"}, "parent", "inline", "pending"); bad != "" || str(sub0(in, "content"), "raw") == "" {
				return 0, nil, "a comment has content.raw " + bad
			}
			if p == nil {
				return 404, nil, ""
			}
			h.nextID++
			h.Comments[h.nextID] = str(sub0(in, "content"), "raw")
			return 201, map[string]any{"id": h.nextID, "content": sub0(in, "content")}, ""
		case m == "PUT" && strings.HasPrefix(tail, "comments/"):
			if bad := params(in, []string{"content"}); bad != "" || str(sub0(in, "content"), "raw") == "" {
				return 0, nil, "a comment has content.raw " + bad
			}
			id := atoi(strings.TrimPrefix(tail, "comments/"))
			if _, ok := h.Comments[id]; !ok || p == nil {
				return 404, nil, ""
			}
			h.Comments[id] = str(sub0(in, "content"), "raw")
			return 200, map[string]any{"id": id}, ""
		}
	}
	return 0, nil, ""
}

func (h *Host) routeSrc(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	if r.Method != "POST" || sub != "/src" {
		return 0, nil, ""
	}
	fields, files, bad := parseMultipart(r, raw)
	if bad != "" {
		return 0, nil, bad
	}
	for k := range fields {
		if k != "message" && k != "branch" && k != "parents" && k != "author" && k != "files" {
			return 0, nil, "form field " + k + " is not documented for this call"
		}
	}
	if len(files) != 1 || fields["message"] == "" || fields["branch"] == "" {
		return 0, nil, "a commit has a message, a branch and one file"
	}
	br := fields["branch"]
	if par := fields["parents"]; par != "" {
		if _, ok := h.branches[br]; ok {
			return 409, map[string]any{"type": "error", "error": map[string]any{"message": "parent is not the tip of the branch"}}, ""
		}
		h.branches[br] = par
		h.files[br] = map[string][]byte{}
		for k, v := range h.files[h.branchOf(par)] {
			h.files[br][k] = v
		}
	}
	if _, ok := h.branches[br]; !ok {
		h.branches[br] = h.branches["main"] // a new branch with no parent inherits the main branch's tip
		h.files[br] = map[string][]byte{}
	}
	for p, b := range files {
		h.files[br][p] = b
	}
	h.branches[br] = h.sha()
	return 201, nil, ""
}

func (h *Host) branchOf(sha string) string {
	for n, c := range h.branches {
		if c == sha {
			return n
		}
	}
	return "main"
}
