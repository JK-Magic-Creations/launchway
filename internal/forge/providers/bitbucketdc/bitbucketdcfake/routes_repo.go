package bitbucketdcfake

import (
	"net/http"
	"net/url"
	"strings"
)

func refJSON(prefix, name, commit string) map[string]any {
	return map[string]any{"id": prefix + name, "displayId": name, "type": map[bool]string{true: "BRANCH", false: "TAG"}[prefix == "refs/heads/"], "latestCommit": commit, "latestChangeset": commit}
}

func (h *Host) routeRepo(r *http.Request, sub string, in map[string]any, _ []byte) (int, any, string) {
	m, q := r.Method, r.URL.Query()
	switch {
	case m == "GET" && sub == "":
		if bad := queryOK(q); bad != "" {
			return 0, nil, bad
		}
		return 200, h.repo(), ""
	case m == "GET" && sub == "/default-branch":
		return 200, map[string]any{"id": "refs/heads/main", "displayId": "main", "type": "BRANCH"}, ""
	case m == "GET" && (sub == "/branches" || sub == "/tags"):
		if bad := queryOK(q, "limit", "start", "filterText", "orderBy", "details", "base", "context", "boostMatches"); bad != "" {
			return 0, nil, bad
		}
		vals := []any{}
		src, prefix := h.branches, "refs/heads/"
		if sub == "/tags" {
			src, prefix = h.tags, "refs/tags/"
		}
		for n, c := range src {
			if f := q.Get("filterText"); f == "" || strings.Contains(n, f) {
				vals = append(vals, refJSON(prefix, n, c))
			}
		}
		return 200, wrap(vals...), ""
	case m == "GET" && strings.HasPrefix(sub, "/tags/"):
		n := unesc(strings.TrimPrefix(sub, "/tags/"))
		c, ok := h.tags[n]
		if !ok {
			return 404, errBody("no such tag"), ""
		}
		return 200, refJSON("refs/tags/", n, c), ""
	}
	return 0, nil, ""
}

func (h *Host) routeCommits(r *http.Request, sub string, in map[string]any, _ []byte) (int, any, string) {
	m, q := r.Method, r.URL.Query()
	switch {
	case m == "GET" && sub == "/commits":
		if bad := queryOK(q, "path", "until", "since", "limit", "start", "merges", "followRenames", "ignoreMissing", "withCounts", "avatarSize", "avatarScheme"); bad != "" {
			return 0, nil, bad
		}
		vals := []any{}
		if c, ok := h.fileCommit[q.Get("until")][q.Get("path")]; ok {
			vals = append(vals, map[string]any{"id": c, "displayId": c[:11]})
		}
		return 200, wrap(vals...), ""
	case m == "POST" && strings.HasSuffix(sub, "/builds") && strings.HasPrefix(sub, "/commits/"):
		return h.storeBuild(strings.TrimSuffix(strings.TrimPrefix(sub, "/commits/"), "/builds"), in)
	case m == "GET" && strings.HasPrefix(sub, "/commits/") && !strings.Contains(strings.TrimPrefix(sub, "/commits/"), "/"):
		if bad := queryOK(q, "path"); bad != "" {
			return 0, nil, bad
		}
		id := unesc(strings.TrimPrefix(sub, "/commits/"))
		return 200, map[string]any{"id": id, "displayId": id[:7], "message": "a commit", "authorTimestamp": 1767323045000,
			"author": map[string]any{"name": "Dev", "emailAddress": "dev@example.com"}, "parents": []any{map[string]any{"id": strings.Repeat("0", 40)}}}, ""
	}
	return 0, nil, ""
}

// storeBuild: POST .../commits/{commitId}/builds, "Store a build status": key, state and url are required.
func (h *Host) storeBuild(sha string, in map[string]any) (int, any, string) {
	if bad := params(in, []string{"key", "state", "url"}, "name", "description", "buildNumber", "duration", "lastUpdated", "parent", "ref", "testResults"); bad != "" {
		return 0, nil, bad
	}
	if len(sha) != 40 {
		return 0, nil, "a status is posted to a 40 character commit"
	}
	if !map[string]bool{"CANCELLED": true, "FAILED": true, "INPROGRESS": true, "SUCCESSFUL": true, "UNKNOWN": true}[str(in, "state")] {
		return 0, nil, "state is one of CANCELLED, FAILED, INPROGRESS, SUCCESSFUL, UNKNOWN"
	}
	if h.builds[sha] == nil {
		h.builds[sha] = map[string]map[string]any{}
	}
	h.builds[sha][str(in, "key")] = in
	return 204, nil, ""
}

func (h *Host) routeGit(r *http.Request, sub string, in map[string]any) (int, any, string) {
	if r.Method != "POST" || sub != "/tags" {
		return 0, nil, ""
	}
	if bad := params(in, []string{"name", "startPoint"}, "message", "type", "force"); bad != "" {
		return 0, nil, bad
	}
	if t := str(in, "type"); t != "" && t != "ANNOTATED" && t != "LIGHTWEIGHT" {
		return 0, nil, "type is ANNOTATED or LIGHTWEIGHT"
	}
	n := str(in, "name")
	if _, ok := h.tags[n]; ok {
		return 409, errBody("tag already exists"), ""
	}
	h.tags[n] = str(in, "startPoint")
	return 201, refJSON("refs/tags/", n, h.tags[n]), ""
}

// routeRestrictions: GET /branch-permissions/latest/.../restrictions needs REPO_ADMIN.
func (h *Host) routeRestrictions(r *http.Request, sub string, q url.Values) (int, any, string) {
	if r.Method != "GET" || sub != "/restrictions" {
		return 0, nil, ""
	}
	if bad := queryOK(q, "limit", "start", "type", "matcherType", "matcherId"); bad != "" {
		return 0, nil, bad
	}
	if !h.Admin {
		return 403, errBody("REPO_ADMIN permission is required"), ""
	}
	vals := []any{}
	if h.Protect {
		vals = append(vals,
			map[string]any{"id": 1, "type": "pull-request-only", "matcher": map[string]any{"id": "refs/heads/main", "displayId": "main", "type": map[string]any{"id": "BRANCH", "name": "Branch"}}},
			map[string]any{"id": 2, "type": "fast-forward-only", "matcher": map[string]any{"id": "ma*", "displayId": "ma*", "type": map[string]any{"id": "PATTERN", "name": "Pattern"}}},
			map[string]any{"id": 3, "type": "no-deletes", "matcher": map[string]any{"id": "refs/heads/dev", "displayId": "dev", "type": map[string]any{"id": "BRANCH", "name": "Branch"}}})
	}
	return 200, wrap(vals...), ""
}

func (h *Host) routeSettings(r *http.Request, sub string, in map[string]any, _ []byte) (int, any, string) {
	if r.Method != "GET" {
		return 0, nil, ""
	}
	switch sub {
	case "/settings/auto-merge":
		return 200, map[string]any{"enabled": h.AutoMerge, "restrictionState": "NONE", "scope": map[string]any{"type": "REPOSITORY", "resourceId": 1}}, ""
	case "/settings/pull-requests":
		a, b := 0, 0
		if h.Protect {
			a, b = 2, 1
		}
		return 200, map[string]any{"requiredApprovers": a, "requiredSuccessfulBuilds": b, "requiredAllApprovers": false, "requiredAllTasksComplete": false, "mergeConfig": map[string]any{"type": "REPOSITORY"}}, ""
	}
	return 0, nil, ""
}
