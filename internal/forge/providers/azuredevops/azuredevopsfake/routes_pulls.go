package azuredevopsfake

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *Host) prJSON(p *PR) map[string]any {
	web := strings.Replace(h.remoteURL(), "https://acme@", "https://", 1)
	out := map[string]any{"pullRequestId": p.ID, "title": p.Title, "description": p.Desc, "status": p.Status, "isDraft": p.Draft,
		"createdBy":     map[string]any{"id": userID, "displayName": "Launchway Service", "uniqueName": "svc-launchway"},
		"sourceRefName": p.Src, "targetRefName": p.Dst,
		"lastMergeSourceCommit": map[string]any{"commitId": p.SHA},
		"repository":            map[string]any{"id": repoID, "name": Repo, "webUrl": web, "remoteUrl": h.remoteURL(), "project": map[string]any{"name": Project}}}
	if p.Fork {
		out["forkSource"] = map[string]any{"repository": map[string]any{"name": "app", "project": map[string]any{"name": "eve"}}}
	}
	if p.Status == "completed" {
		out["lastMergeCommit"] = map[string]any{"commitId": strings.Repeat("c", 40)}
	}
	if p.Auto {
		out["autoCompleteSetBy"] = map[string]any{"id": userID}
		out["completionOptions"] = map[string]any{"mergeStrategy": p.Strategy}
	}
	return out
}

func (h *Host) findPR(n string) *PR {
	for _, p := range h.PRs {
		if strconv.Itoa(p.ID) == n {
			return p
		}
	}
	return nil
}

func (h *Host) routePulls(c *call) result {
	m := c.r.Method
	switch {
	case m == http.MethodGet && strings.EqualFold(c.sub, "/pullrequests"):
		if b := params(c.q, "searchCriteria.status", "$top", "$skip"); b != "" {
			return bad("%s", b)
		}
		st := c.q.Get("searchCriteria.status")
		if st != "active" && st != "abandoned" && st != "completed" && st != "all" {
			return bad("searchCriteria.status is active, abandoned, completed or all")
		}
		vals := []any{}
		for _, p := range h.PRs {
			if st == "all" || p.Status == st {
				vals = append(vals, h.prJSON(p))
			}
		}
		return ok(list(vals...))
	case m == http.MethodPost && strings.EqualFold(c.sub, "/pullrequests"):
		if b := params(c.q); b != "" {
			return bad("%s", b)
		}
		if b := fields(c.in, []string{"sourceRefName", "targetRefName", "title"}, "description", "isDraft"); b != "" {
			return bad("%s", b)
		}
		src := strings.TrimPrefix(str(c.in, "sourceRefName"), "refs/heads/")
		if !strings.HasPrefix(str(c.in, "sourceRefName"), "refs/heads/") || !strings.HasPrefix(str(c.in, "targetRefName"), "refs/heads/") {
			return bad("ref names are full (refs/heads/...)")
		}
		sha, found := h.branches[src]
		if !found {
			return result{code: 400, out: map[string]any{"message": "source branch not found"}}
		}
		p := &PR{ID: len(h.PRs) + 1, Title: str(c.in, "title"), Desc: str(c.in, "description"), Status: "active", Draft: c.in["isDraft"] == true,
			Src: str(c.in, "sourceRefName"), Dst: str(c.in, "targetRefName"), SHA: sha}
		h.PRs = append(h.PRs, p)
		return result{code: 201, out: h.prJSON(p)}
	}
	return result{}
}

func (h *Host) routePull(c *call) result {
	low := strings.ToLower(c.sub)
	if !strings.HasPrefix(low, "/pullrequests/") {
		return result{}
	}
	id, tail, _ := strings.Cut(c.sub[len("/pullrequests/"):], "/")
	p := h.findPR(id)
	if p == nil {
		return notFound()
	}
	switch {
	case c.r.Method == http.MethodGet && tail == "":
		if b := params(c.q); b != "" {
			return bad("%s", b)
		}
		return ok(h.prJSON(p))
	case c.r.Method == http.MethodPatch && tail == "":
		return h.patchPR(c, p)
	}
	return h.routePRExtras(c, p, tail)
}

var strategies = map[string]bool{"noFastForward": true, "squash": true, "rebase": true, "rebaseMerge": true}

// patchPR: the properties the reference says an update may change; the draft flag is not one.
func (h *Host) patchPR(c *call, p *PR) result {
	if b := params(c.q); b != "" {
		return bad("%s", b)
	}
	if b := fields(c.in, nil, "title", "description", "status", "completionOptions", "mergeOptions", "autoCompleteSetBy"); b != "" || len(c.in) == 0 {
		return bad("an update names title, description, status, completionOptions, mergeOptions or autoCompleteSetBy: %s", b)
	}
	if st, set := c.in["status"]; set && st != "active" && st != "abandoned" {
		return bad("status is changed to active or abandoned (a completion is the host's)")
	}
	if ac := sub(c.in, "autoCompleteSetBy"); ac != nil {
		if b := fields(ac, []string{"id"}); b != "" || str(ac, "id") != userID {
			return bad("autoCompleteSetBy names the user's id %s", b)
		}
		opts := sub(c.in, "completionOptions")
		if b := fields(opts, nil, "mergeStrategy", "deleteSourceBranch", "mergeCommitMessage", "bypassPolicy", "bypassReason", "transitionWorkItems", "squashMerge"); b != "" || !strategies[str(opts, "mergeStrategy")] {
			return bad("completion options name a merge strategy of the documented set %s", b)
		}
		if _, isBool := opts["deleteSourceBranch"].(bool); !isBool {
			return bad("deleteSourceBranch is a boolean")
		}
		p.Auto, p.Strategy = true, str(opts, "mergeStrategy")
	}
	if v, set := c.in["title"]; set {
		p.Title, _ = v.(string)
	}
	if v, set := c.in["description"]; set {
		p.Desc, _ = v.(string)
	}
	if v, set := c.in["status"]; set {
		p.Status, _ = v.(string)
	}
	return ok(h.prJSON(p))
}

// routePush: POST .../pushes (the reference's examples: add, edit, and a new branch named with the commit to build it on).
func (h *Host) routePush(c *call) result {
	if c.r.Method != http.MethodPost || c.sub != "/pushes" {
		return result{}
	}
	if b := params(c.q); b != "" {
		return bad("%s", b)
	}
	if b := fields(c.in, []string{"refUpdates", "commits"}); b != "" {
		return bad("%s", b)
	}
	ru, _ := c.in["refUpdates"].([]any)
	cs, _ := c.in["commits"].([]any)
	if len(ru) != 1 || len(cs) != 1 {
		return bad("the fake takes one ref update and one commit")
	}
	u, _ := ru[0].(map[string]any)
	cm, _ := cs[0].(map[string]any)
	if b := fields(u, []string{"name", "oldObjectId"}); b != "" {
		return bad("refUpdate: %s", b)
	}
	if b := fields(cm, []string{"comment", "changes"}); b != "" {
		return bad("commit: %s", b)
	}
	chs, _ := cm["changes"].([]any)
	if len(chs) != 1 {
		return bad("the fake takes one change")
	}
	ch, _ := chs[0].(map[string]any)
	return h.applyPush(str(u, "name"), str(u, "oldObjectId"), ch)
}

func (h *Host) applyPush(name, old string, ch map[string]any) result {
	if b := fields(ch, []string{"changeType", "item", "newContent"}); b != "" {
		return bad("change: %s", b)
	}
	item, nc := sub(ch, "item"), sub(ch, "newContent")
	if b := fields(item, []string{"path"}); b != "" {
		return bad("item: %s", b)
	}
	if b := fields(nc, []string{"content", "contentType"}); b != "" || str(nc, "contentType") != "base64Encoded" {
		return bad("newContent: base64Encoded content %s", b)
	}
	br, isHead := strings.CutPrefix(name, "refs/heads/")
	path, slash := strings.CutPrefix(str(item, "path"), "/")
	if !isHead || !slash {
		return bad("a push names refs/heads/... and a path with a leading slash")
	}
	data, err := b64(str(nc, "content"))
	if err != nil {
		return bad("content is not base64")
	}
	base := br
	if tip, found := h.branches[br]; found {
		if tip != old {
			return result{code: 409, out: map[string]any{"message": "oldObjectId is not the tip of the branch"}}
		}
	} else if base = h.branchAt(old); base == "" {
		return bad("a new branch is cut from a commit that is the tip of a branch")
	}
	_, has := h.files[base][path]
	if kind := str(ch, "changeType"); kind != map[bool]string{true: "edit", false: "add"}[has] {
		return bad("changeType %s for a file that exists=%v", kind, has)
	}
	if base != br {
		h.files[br] = map[string][]byte{}
		for k, v := range h.files[base] {
			h.files[br][k] = v
		}
	}
	h.files[br][path] = data
	h.branches[br] = h.sha()
	return result{code: 201, out: map[string]any{"pushId": 1, "commits": []any{map[string]any{"commitId": h.branches[br]}},
		"refUpdates": []any{map[string]any{"name": name, "oldObjectId": old, "newObjectId": h.branches[br]}}}}
}

func (h *Host) branchAt(sha string) string {
	for n, c := range h.branches {
		if c == sha {
			return n
		}
	}
	return ""
}
