package azuredevopsfake

import (
	"net/http"
	"net/url"
	"strings"
)

// route finds the call below the organization (or collection).
func (h *Host) route(c *call) result {
	rest := strings.TrimPrefix(c.r.URL.Path, h.prefix)
	if rest == c.r.URL.Path && h.prefix != "" {
		return result{}
	}
	if rest == "/_apis/projects" && c.r.Method == http.MethodGet {
		if b := params(c.q, "$top"); b != "" {
			return bad("%s", b)
		}
		return ok(list(map[string]any{"id": "99999999-2222-3333-4444-555555555555", "name": Project, "state": "wellFormed"}))
	}
	proj, below, found := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
	if !found || !strings.EqualFold(unesc(proj), Project) {
		return result{}
	}
	switch {
	case below == "_apis/git/repositories" && c.r.Method == http.MethodGet:
		if b := params(c.q); b != "" {
			return bad("%s", b)
		}
		return ok(list(h.repo()))
	case below == "_apis/git/policy/configurations" && c.r.Method == http.MethodGet:
		return h.routePolicies(c)
	case strings.HasPrefix(below, "_apis/git/repositories/"):
		name, tail, _ := strings.Cut(strings.TrimPrefix(below, "_apis/git/repositories/"), "/")
		if n := unesc(name); !strings.EqualFold(n, Repo) && n != repoID {
			return notFound()
		}
		c.sub = "/" + tail
		if tail == "" {
			c.sub = ""
		}
		for _, f := range []func(*call) result{h.routeRepo, h.routeRefs, h.routeStatus, h.routePulls, h.routePull, h.routePush} {
			if res := f(c); res.code != 0 || res.bad != "" {
				return res
			}
		}
	}
	return result{}
}

func unesc(s string) string { u, _ := url.PathUnescape(s); return u }

func (h *Host) routePolicies(c *call) result {
	if b := params(c.q, "repositoryId", "refName"); b != "" {
		return bad("%s", b)
	}
	if c.q.Get("repositoryId") != repoID || c.q.Get("refName") != "refs/heads/main" {
		return bad("policies are asked for a repository id and a full ref name")
	}
	if !h.Protect {
		return ok(list())
	}
	pol := func(typ string, blocking bool, settings map[string]any) any {
		return map[string]any{"isEnabled": true, "isBlocking": blocking, "isDeleted": false, "id": 1, "type": map[string]any{"id": typ}, "settings": settings}
	}
	return ok(list(
		pol("fa4e907d-c16b-4a4c-9dfa-4906e5d171dd", true, map[string]any{"minimumApproverCount": 2}),
		pol("0609b952-1397-4640-95ec-e00a01b2c241", true, map[string]any{"buildDefinitionId": 5}),
		pol("fd2167ab-b0be-447a-8ec8-39368250530e", false, map[string]any{})))
}

func (h *Host) routeRepo(c *call) result {
	m := c.r.Method
	switch {
	case m == http.MethodGet && c.sub == "":
		if b := params(c.q); b != "" {
			return bad("%s", b)
		}
		return ok(h.repo())
	case m == http.MethodGet && strings.HasPrefix(c.sub, "/commits/") && !strings.Contains(strings.TrimPrefix(c.sub, "/commits/"), "/"):
		if b := params(c.q, "changeCount"); b != "" {
			return bad("%s", b)
		}
		id := unesc(strings.TrimPrefix(c.sub, "/commits/"))
		return ok(map[string]any{"commitId": id, "comment": "a commit", "parents": []any{strings.Repeat("0", 40)},
			"author": map[string]any{"name": "Dev", "email": "dev@example.com", "date": "2026-01-02T03:04:05Z"}})
	case m == http.MethodGet && c.sub == "/items":
		return h.routeItem(c)
	}
	return result{}
}

func (h *Host) routeItem(c *call) result {
	if b := params(c.q, "path", "versionDescriptor.version", "versionDescriptor.versionType"); b != "" {
		return bad("%s", b)
	}
	p, br := strings.TrimPrefix(c.q.Get("path"), "/"), c.q.Get("versionDescriptor.version")
	if c.q.Get("path") == "" || !strings.HasPrefix(c.q.Get("path"), "/") || c.q.Get("versionDescriptor.versionType") != "branch" {
		return bad("an item is asked by path (with a leading slash) and a branch version")
	}
	if _, found := h.files[br][p]; !found {
		return notFound()
	}
	return ok(map[string]any{"path": "/" + p, "objectId": strings.Repeat("e", 40)})
}
