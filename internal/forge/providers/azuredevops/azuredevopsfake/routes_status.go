package azuredevopsfake

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

var statusStates = map[string]bool{"notSet": true, "pending": true, "succeeded": true, "failed": true, "error": true, "notApplicable": true}

// checkStatus validates a status body (commit or pull request).
func checkStatus(in map[string]any, extra ...string) string {
	if b := fields(in, []string{"state", "context"}, append([]string{"description", "targetUrl"}, extra...)...); b != "" {
		return b
	}
	if !statusStates[str(in, "state")] {
		return "state is one of notSet, pending, succeeded, failed, error, notApplicable"
	}
	ctx := sub(in, "context")
	if b := fields(ctx, []string{"name"}, "genre"); b != "" || str(ctx, "name") == "" {
		return "context: a name (and an optional genre) " + b
	}
	return ""
}

func store(m map[string]map[string]any, in map[string]any) {
	ctx := sub(in, "context")
	m[str(ctx, "genre")+"/"+str(ctx, "name")] = in
}

func (h *Host) routeStatus(c *call) result {
	if !strings.HasPrefix(c.sub, "/commits/") || !strings.HasSuffix(c.sub, "/statuses") {
		return result{}
	}
	sha := unesc(strings.TrimSuffix(strings.TrimPrefix(c.sub, "/commits/"), "/statuses"))
	switch c.r.Method {
	case http.MethodGet:
		if b := params(c.q, "top", "skip", "latestOnly"); b != "" {
			return bad("%s", b)
		}
		vals := []any{}
		for _, s := range h.statuses[sha] {
			vals = append(vals, s)
		}
		return ok(list(vals...))
	case http.MethodPost:
		if b := params(c.q); b != "" {
			return bad("%s", b)
		}
		if len(sha) != 40 {
			return bad("a status is posted to a 40 character commit")
		}
		if b := checkStatus(c.in); b != "" {
			return bad("%s", b)
		}
		if h.statuses[sha] == nil {
			h.statuses[sha] = map[string]map[string]any{}
		}
		store(h.statuses[sha], c.in)
		return result{code: 201, out: c.in}
	}
	return result{}
}

// routePRExtras: pull request statuses, threads and comments.
func (h *Host) routePRExtras(c *call, p *PR, tail string) result {
	m := c.r.Method
	switch {
	case m == http.MethodPost && tail == "statuses":
		if b := params(c.q); b != "" {
			return bad("%s", b)
		}
		if b := checkStatus(c.in, "iterationId"); b != "" {
			return bad("%s", b)
		}
		if h.PRStatuses[p.ID] == nil {
			h.PRStatuses[p.ID] = map[string]map[string]any{}
		}
		store(h.PRStatuses[p.ID], c.in)
		return result{code: 201, out: c.in}
	case m == http.MethodPost && tail == "threads":
		return h.newThread(c)
	case m == http.MethodPatch && strings.HasPrefix(tail, "threads/"):
		t, cm, _ := strings.Cut(strings.TrimPrefix(tail, "threads/"), "/comments/")
		if b := params(c.q); b != "" {
			return bad("%s", b)
		}
		if b := fields(c.in, []string{"content"}); b != "" || str(c.in, "content") == "" {
			return bad("a comment update has content %s", b)
		}
		if _, found := h.Comments[t+":"+cm]; !found {
			return notFound()
		}
		h.Comments[t+":"+cm] = str(c.in, "content")
		return ok(map[string]any{"id": atoi(cm), "content": str(c.in, "content")})
	}
	return result{}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func (h *Host) newThread(c *call) result {
	if b := params(c.q); b != "" {
		return bad("%s", b)
	}
	if b := fields(c.in, []string{"comments"}, "status"); b != "" {
		return bad("%s", b)
	}
	cs, _ := c.in["comments"].([]any)
	if len(cs) != 1 {
		return bad("a new thread has one comment")
	}
	cm, _ := cs[0].(map[string]any)
	if b := fields(cm, []string{"content"}, "parentCommentId", "commentType"); b != "" || str(cm, "content") == "" {
		return bad("a comment has content %s", b)
	}
	if ct, set := cm["commentType"]; set && ct != float64(1) {
		return bad("commentType is 1 (text)")
	}
	h.nextID++
	h.Comments[strconv.Itoa(h.nextID)+":1"] = str(cm, "content")
	return result{code: 200, out: map[string]any{"id": h.nextID, "status": "active", "comments": []any{map[string]any{"id": 1, "content": str(cm, "content"), "commentType": "text"}}}}
}

func b64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
