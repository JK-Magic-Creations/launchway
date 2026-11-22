package azuredevopsfake

import (
	"net/http"
	"sort"
	"strings"
)

func (h *Host) refList(filter string, peel bool) []any {
	vals := []any{}
	names := []string{}
	for n := range h.branches {
		names = append(names, "refs/heads/"+n)
	}
	for n := range h.tags {
		names = append(names, "refs/tags/"+n)
	}
	sort.Strings(names)
	for _, full := range names {
		if !strings.HasPrefix(full, "refs/"+filter) {
			continue
		}
		v := map[string]any{"name": full}
		if t, isTag := h.tags[strings.TrimPrefix(full, "refs/tags/")]; isTag && strings.HasPrefix(full, "refs/tags/") {
			v["objectId"] = t.obj
			if peel {
				v["peeledObjectId"] = t.commit
			}
		} else {
			v["objectId"] = h.branches[strings.TrimPrefix(full, "refs/heads/")]
		}
		vals = append(vals, v)
	}
	return vals
}

func (h *Host) routeRefs(c *call) result {
	m := c.r.Method
	switch {
	case m == http.MethodGet && c.sub == "/refs":
		if b := params(c.q, "filter", "peelTags", "$top"); b != "" {
			return bad("%s", b)
		}
		return ok(list(h.refList(c.q.Get("filter"), c.q.Get("peelTags") == "true")...))
	case m == http.MethodPost && c.sub == "/refs":
		return h.updateRefs(c)
	case m == http.MethodPost && c.sub == "/annotatedtags":
		return h.annotatedTag(c)
	}
	return result{}
}

// updateRefs: POST .../refs with an array of {name, oldObjectId, newObjectId}.
func (h *Host) updateRefs(c *call) result {
	if b := params(c.q); b != "" || len(c.inArr) == 0 {
		return bad("refs are updated with an array of updates %s", b)
	}
	out := []any{}
	for _, e := range c.inArr {
		u, _ := e.(map[string]any)
		if b := fields(u, []string{"name", "oldObjectId", "newObjectId"}); b != "" {
			return bad("%s", b)
		}
		name := str(u, "name")
		tagName, isTag := strings.CutPrefix(name, "refs/tags/")
		if !isTag || str(u, "oldObjectId") != strings.Repeat("0", 40) {
			return bad("the fake creates tag refs only (refs/tags/..., old object id all zeros)")
		}
		var commit string
		for _, t := range h.orphan {
			if t.obj == str(u, "newObjectId") {
				commit = t.commit
			}
		}
		if commit == "" {
			return bad("the ref points at no tag object the fake made")
		}
		h.tags[tagName] = tag{obj: str(u, "newObjectId"), commit: commit}
		out = append(out, map[string]any{"name": name, "success": true, "updateStatus": "succeeded"})
	}
	return ok(list(out...))
}

func (h *Host) annotatedTag(c *call) result {
	if b := params(c.q); b != "" {
		return bad("%s", b)
	}
	if b := fields(c.in, []string{"name", "taggedObject", "message"}); b != "" {
		return bad("%s", b)
	}
	to := sub(c.in, "taggedObject")
	if b := fields(to, []string{"objectId"}, "objectType"); b != "" {
		return bad("taggedObject: %s", b)
	}
	name := str(c.in, "name")
	if strings.HasPrefix(name, "refs/") || name == "" {
		return bad("the name of an annotated tag is the short name")
	}
	if _, dup := h.tagObjOf(name); dup {
		return result{code: 409, out: map[string]any{"message": "tag already exists"}}
	}
	obj := h.sha()
	if h.TagWithoutRef {
		h.orphans(name, obj, str(to, "objectId"))
	} else {
		h.tags[name] = tag{obj: obj, commit: str(to, "objectId")}
	}
	return result{code: 201, out: map[string]any{"name": "refs/tags/" + name, "objectId": obj, "message": str(c.in, "message"),
		"taggedObject": map[string]any{"objectId": str(to, "objectId"), "objectType": "commit"}}}
}

func (h *Host) tagObjOf(name string) (tag, bool) { t, ok := h.tags[name]; return t, ok }

// orphans keeps a tag object that has no ref yet, so a ref update can find it.
func (h *Host) orphans(name, obj, commit string) {
	if h.orphan == nil {
		h.orphan = map[string]tag{}
	}
	h.orphan[name] = tag{obj: obj, commit: commit}
}
