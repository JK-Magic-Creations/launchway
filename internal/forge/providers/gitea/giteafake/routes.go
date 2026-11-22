package giteafake

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (h *Host) routeRepo(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && sub == "":
		return 200, h.repo(), ""
	case m == "GET" && sub == "/branches":
		out := []any{}
		for n, c := range h.branches {
			out = append(out, map[string]any{"name": n, "commit": map[string]any{"id": c}, "protected": n == "main" && h.Protect})
		}
		return 200, out, ""
	case m == "GET" && strings.HasPrefix(sub, "/branches/"):
		n := unesc(strings.TrimPrefix(sub, "/branches/"))
		c, ok := h.branches[n]
		if !ok {
			return 404, nil, ""
		}
		return 200, map[string]any{"name": n, "commit": map[string]any{"id": c}}, ""
	case m == "GET" && sub == "/tags":
		out := []any{}
		for n, t := range h.tags {
			out = append(out, map[string]any{"name": n, "id": t.ID, "commit": map[string]any{"sha": t.Commit}})
		}
		return 200, out, ""
	case m == "GET" && strings.HasPrefix(sub, "/tags/"):
		n := unesc(strings.TrimPrefix(sub, "/tags/"))
		t, ok := h.tags[n]
		if !ok {
			return 404, nil, ""
		}
		return 200, map[string]any{"name": n, "id": t.ID, "message": t.Message, "commit": map[string]any{"sha": t.Commit}}, ""
	case m == "POST" && sub == "/tags":
		if bad := params(in, []string{"tag_name"}, "message", "target"); bad != "" {
			return 0, nil, bad
		}
		n := str(in, "tag_name")
		if _, ok := h.tags[n]; ok {
			return 409, map[string]any{"message": "tag exists already"}, ""
		}
		t := tagObj{Commit: str(in, "target"), ID: h.sha(), Message: str(in, "message")}
		h.tags[n] = t
		code := 200
		if h.Forgejo {
			code = 201
		}
		return code, map[string]any{"name": n, "id": t.ID, "commit": map[string]any{"sha": t.Commit}}, ""
	case m == "GET" && strings.HasPrefix(sub, "/git/commits/"):
		sha := unesc(strings.TrimPrefix(sub, "/git/commits/"))
		return 200, map[string]any{"sha": sha, "created": "2026-01-02T03:04:05Z", "parents": []any{map[string]any{"sha": strings.Repeat("0", 40)}},
			"commit": map[string]any{"message": "a commit", "author": map[string]any{"name": "Dev", "email": "dev@example.com"}}}, ""
	}
	return 0, nil, ""
}

func (h *Host) routeStatus(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	if !strings.HasPrefix(sub, "/statuses/") {
		return 0, nil, ""
	}
	sha := unesc(strings.TrimPrefix(sub, "/statuses/"))
	switch r.Method {
	case "POST":
		if len(sha) != 40 {
			return 0, nil, "a status is posted to a 40 character sha"
		}
		if bad := params(in, []string{"state"}, "context", "description", "target_url"); bad != "" {
			return 0, nil, bad
		}
		if !map[string]bool{"pending": true, "success": true, "error": true, "failure": true}[str(in, "state")] {
			return 0, nil, "state is not one of pending, success, error, failure"
		}
		h.nextID++
		h.Statuses = append(h.Statuses, Status{h.nextID, sha, str(in, "state"), str(in, "context"), str(in, "description"), str(in, "target_url")})
		return 201, map[string]any{"id": h.nextID, "status": str(in, "state"), "context": str(in, "context")}, ""
	case "GET":
		out := []any{}
		for i := len(h.Statuses) - 1; i >= 0; i-- { // newest first, and the older ones of a context too
			if s := h.Statuses[i]; s.SHA == sha {
				out = append(out, map[string]any{"id": s.ID, "status": s.State, "context": s.Context, "description": s.Description, "target_url": s.TargetURL})
			}
		}
		return 200, out, ""
	}
	return 0, nil, ""
}

func (h *Host) prJSON(p *PR) map[string]any {
	side := func(ref, sha, repo string) map[string]any {
		return map[string]any{"ref": ref, "sha": sha, "repo": map[string]any{"full_name": repo}}
	}
	return map[string]any{"number": p.Number, "title": p.Title, "body": p.Body, "state": p.State, "draft": p.Draft, "merged": p.Merged,
		"html_url": fmt.Sprintf("https://git.example.com/acme/app/pulls/%d", p.Number), "user": map[string]any{"login": "svc-launchway"},
		"head": side(p.Head, p.HeadSHA, p.HeadRepo), "base": side(p.Base, h.branches[p.Base], "acme/app")}
}

func (h *Host) findPR(n string) *PR {
	i, _ := strconv.Atoi(n)
	for _, p := range h.PRs {
		if p.Number == i {
			return p
		}
	}
	return nil
}

func (h *Host) routePulls(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "GET" && sub == "/pulls":
		st := q.Get("state")
		if st != "open" && st != "closed" && st != "all" {
			return 0, nil, "state is open, closed or all"
		}
		out := []any{}
		for _, p := range h.PRs {
			if st == "all" || p.State == st {
				out = append(out, h.prJSON(p))
			}
		}
		return 200, out, ""
	case m == "POST" && sub == "/pulls":
		if bad := params(in, nil, "title", "body", "head", "base", "assignee", "assignees", "labels", "milestone", "due_date", "reviewers", "team_reviewers"); bad != "" {
			return 0, nil, bad
		}
		if _, ok := h.branches[str(in, "head")]; !ok {
			return 404, nil, ""
		}
		p := &PR{Number: len(h.PRs) + 1, Title: str(in, "title"), Body: str(in, "body"), State: "open", Head: str(in, "head"), Base: str(in, "base"),
			HeadSHA: h.branches[str(in, "head")], HeadRepo: "acme/app"}
		h.PRs = append(h.PRs, p)
		return 201, h.prJSON(p), ""
	case strings.HasPrefix(sub, "/pulls/"):
		return h.routePull(r, sub, q, in, raw)
	}
	return 0, nil, ""
}

func (h *Host) routePull(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	m := r.Method
	switch {
	case strings.HasPrefix(sub, "/pulls/"):
		idx, tail, _ := strings.Cut(strings.TrimPrefix(sub, "/pulls/"), "/")
		p := h.findPR(idx)
		switch {
		case m == "GET" && tail == "":
			if p == nil {
				return 404, nil, ""
			}
			return 200, h.prJSON(p), ""
		case m == "PATCH" && tail == "":
			if bad := params(in, nil, "title", "body", "state"); bad != "" || len(in) == 0 {
				return 0, nil, "an edit names title, body or state: " + bad
			}
			if p == nil {
				return 404, nil, ""
			}
			if v, ok := in["title"]; ok {
				p.Title = v.(string)
			}
			if v, ok := in["body"]; ok {
				p.Body = v.(string)
			}
			if v := str(in, "state"); v != "" {
				if v != "open" && v != "closed" {
					return 0, nil, "state is open or closed"
				}
				p.State = v
			}
			return 201, h.prJSON(p), ""
		case m == "POST" && tail == "merge":
			if bad := params(in, []string{"Do"}, "merge_when_checks_succeed", "MergeCommitID", "MergeMessageField", "MergeTitleField", "delete_branch_after_merge", "force_merge", "head_commit_id"); bad != "" {
				return 0, nil, bad
			}
			if in["merge_when_checks_succeed"] != true {
				return 0, nil, "Launchway never merges directly: merge_when_checks_succeed must be true"
			}
			if !map[string]bool{"merge": true, "rebase": true, "rebase-merge": true, "squash": true, "fast-forward-only": true, "manually-merged": true}[str(in, "Do")] {
				return 0, nil, "Do is not one of the documented values"
			}
			if p == nil {
				return 404, nil, ""
			}
			h.AutoMerge[p.Number] = str(in, "Do")
			return 200, nil, ""
		case m == "DELETE" && tail == "merge":
			if p == nil || h.AutoMerge[p.Number] == "" {
				return 404, nil, ""
			}
			delete(h.AutoMerge, p.Number)
			return 204, nil, ""
		}
	}
	return 0, nil, ""
}

func (h *Host) routeComments(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	m := r.Method
	switch {
	case m == "POST" && strings.HasPrefix(sub, "/issues/") && strings.HasSuffix(sub, "/comments"):
		if bad := params(in, []string{"body"}); bad != "" {
			return 0, nil, bad
		}
		if h.findPR(strings.TrimSuffix(strings.TrimPrefix(sub, "/issues/"), "/comments")) == nil {
			return 404, nil, ""
		}
		h.nextID++
		h.Comments[h.nextID] = str(in, "body")
		return 201, map[string]any{"id": h.nextID, "body": str(in, "body")}, ""
	case m == "PATCH" && strings.HasPrefix(sub, "/issues/comments/"):
		if bad := params(in, []string{"body"}); bad != "" {
			return 0, nil, bad
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(sub, "/issues/comments/"))
		if _, ok := h.Comments[id]; !ok {
			return 404, nil, ""
		}
		h.Comments[id] = str(in, "body")
		return 200, map[string]any{"id": id, "body": str(in, "body")}, ""
	}
	return 0, nil, ""
}

func (h *Host) relJSON(rl *release) map[string]any {
	return map[string]any{"id": rl.ID, "tag_name": rl.Tag, "name": rl.Name, "body": rl.Body, "assets": rl.Assets,
		"html_url": "https://git.example.com/acme/app/releases/tag/" + rl.Tag}
}

func (h *Host) routeReleases(r *http.Request, sub string, q url.Values, in map[string]any, raw []byte) (int, any, string) {
	m := r.Method
	opt := []string{"name", "body", "draft", "prerelease", "target_commitish"}
	if h.Forgejo {
		opt = append(opt, "hide_archive_links")
	}
	switch {
	case m == "GET" && strings.HasPrefix(sub, "/releases/tags/"):
		rl := h.releases[unesc(strings.TrimPrefix(sub, "/releases/tags/"))]
		if rl == nil {
			return 404, nil, ""
		}
		return 200, h.relJSON(rl), ""
	case m == "POST" && sub == "/releases":
		if bad := params(in, []string{"tag_name"}, opt...); bad != "" {
			return 0, nil, bad
		}
		t := str(in, "tag_name")
		if _, ok := h.tags[t]; !ok {
			return 0, nil, "a release is made for a tag that exists"
		}
		if h.releases[t] != nil {
			return 409, map[string]any{"message": "release exists"}, ""
		}
		h.nextID++
		rl := &release{ID: h.nextID, Tag: t, Name: str(in, "name"), Body: str(in, "body"), Prerelease: in["prerelease"] == true, Assets: []map[string]any{}}
		h.releases[t] = rl
		return 201, h.relJSON(rl), ""
	case strings.HasPrefix(sub, "/releases/"):
		idStr, tail, _ := strings.Cut(strings.TrimPrefix(sub, "/releases/"), "/")
		id, _ := strconv.Atoi(idStr)
		var rl *release
		for _, x := range h.releases {
			if x.ID == id {
				rl = x
			}
		}
		if rl == nil {
			return 404, nil, ""
		}
		switch {
		case m == "PATCH" && tail == "":
			if bad := params(in, nil, append(opt, "tag_name")...); bad != "" {
				return 0, nil, bad
			}
			rl.Name, rl.Body = str(in, "name"), str(in, "body")
			return 200, h.relJSON(rl), ""
		case m == "POST" && tail == "assets":
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				return 0, nil, "an attachment is sent as application/octet-stream or multipart/form-data"
			}
			name := q.Get("name")
			if name == "" {
				return 0, nil, "name is required"
			}
			h.nextID++
			rl.Assets = append(rl.Assets, map[string]any{"id": h.nextID, "name": name, "size": len(raw)})
			h.Assets[h.nextID] = raw
			return 201, map[string]any{"id": h.nextID, "name": name, "size": len(raw)}, ""
		case m == "DELETE" && strings.HasPrefix(tail, "assets/"):
			aid, _ := strconv.Atoi(strings.TrimPrefix(tail, "assets/"))
			for i, a := range rl.Assets {
				if a["id"] == aid {
					rl.Assets = append(rl.Assets[:i], rl.Assets[i+1:]...)
					delete(h.Assets, aid)
					return 204, nil, ""
				}
			}
			return 404, nil, ""
		}
	}
	return 0, nil, ""
}

func (h *Host) routeFiles(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	if !strings.HasPrefix(sub, "/contents/") {
		return 0, nil, ""
	}
	path := unesc(strings.TrimPrefix(sub, "/contents/"))
	switch r.Method {
	case "GET":
		ref := q.Get("ref")
		b, ok := h.files[ref][path]
		if !ok {
			return 404, nil, ""
		}
		return 200, map[string]any{"path": path, "sha": fmt.Sprintf("blob%d", len(b))}, ""
	case "POST", "PUT":
		opt := []string{"new_branch", "author", "committer", "dates", "signoff"}
		req := []string{"content"}
		if r.Method == "PUT" {
			req = append(req, "sha")
			opt = append(opt, "from_path")
		}
		if h.Forgejo {
			opt = append(opt, "force_overwrite_new_branch")
		}
		if bad := params(in, req, append(opt, "branch", "message")...); bad != "" {
			return 0, nil, bad
		}
		b, err := base64.StdEncoding.DecodeString(str(in, "content"))
		if err != nil {
			return 0, nil, "content is not base64"
		}
		br := str(in, "branch")
		if nb := str(in, "new_branch"); nb != "" {
			if _, ok := h.branches[nb]; ok {
				return 409, map[string]any{"message": "branch already exists"}, ""
			}
			base, ok := h.branches[br]
			if !ok {
				return 404, nil, ""
			}
			h.branches[nb] = base
			h.files[nb] = map[string][]byte{}
			for k, v := range h.files[br] {
				h.files[nb][k] = v
			}
			br = nb
		}
		if _, ok := h.branches[br]; !ok {
			return 404, nil, ""
		}
		old, exists := h.files[br][path]
		if (r.Method == "POST") == exists {
			return 422, map[string]any{"message": "file exists / does not exist"}, ""
		}
		if exists && str(in, "sha") != fmt.Sprintf("blob%d", len(old)) {
			return 422, map[string]any{"message": "sha does not match"}, ""
		}
		h.files[br][path] = b
		h.branches[br] = h.sha()
		code := 201
		if r.Method == "PUT" {
			code = 200
		}
		return code, map[string]any{"commit": map[string]any{"sha": h.branches[br]}}, ""
	}
	return 0, nil, ""
}

func (h *Host) routeProtection(r *http.Request, sub string, q url.Values, in map[string]any, _ []byte) (int, any, string) {
	if r.Method != "GET" || sub != "/branch_protections" {
		return 0, nil, ""
	}
	if h.NoProtectionAccess {
		return 403, map[string]any{"message": "forbidden"}, ""
	}
	out := []any{}
	if h.Protect {
		p := map[string]any{"rule_name": "main", "branch_name": "main", "enable_push": false, "enable_push_whitelist": false, "enable_status_check": true,
			"status_check_contexts": []string{"launchway/ci"}, "required_approvals": 1}
		if !h.Forgejo {
			p["enable_force_push"] = false
		}
		out = append(out, p)
	}
	return 200, out, ""
}
