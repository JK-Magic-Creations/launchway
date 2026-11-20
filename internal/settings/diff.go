package settings

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/ai"
)

// FieldChange is one difference between two versions of an object.
type FieldChange struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// Diff lists what differs between two specs, by path (pipeline.on.push),
// in order. Lists are compared whole.
func Diff(before, after json.RawMessage) []FieldChange {
	var a, b any
	if len(before) > 0 {
		_ = json.Unmarshal(before, &a)
	}
	if len(after) > 0 {
		_ = json.Unmarshal(after, &b)
	}
	fa, fb := map[string]any{}, map[string]any{}
	flatten("", a, fa)
	flatten("", b, fb)
	keys := map[string]bool{}
	for k := range fa {
		keys[k] = true
	}
	for k := range fb {
		keys[k] = true
	}
	var out []FieldChange
	for k := range keys {
		x, y := fa[k], fb[k]
		if jsonEqual(x, y) {
			continue
		}
		out = append(out, FieldChange{Path: k, Before: x, After: y})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if out == nil {
		out = []FieldChange{}
	}
	return out
}

func flatten(prefix string, v any, out map[string]any) {
	m, ok := v.(map[string]any)
	if !ok || (len(m) == 0 && prefix != "") {
		if prefix != "" {
			out[prefix] = v
		}
		return
	}
	for k, x := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		flatten(p, x, out)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func pathMatch(pattern, name string) (bool, error) { return path.Match(pattern, name) }

// policyProjects reads the projects a policy or a model applies to.
func policyProjects(v any) ([]string, bool) {
	switch t := v.(type) {
	case *ai.Model:
		return t.Projects, false
	case *yaml.Node:
		var scope *yaml.Node
		for i := 0; i+1 < len(t.Content); i += 2 {
			if t.Content[i].Value == "scope" {
				scope = t.Content[i+1]
			}
		}
		if scope == nil {
			return nil, true
		}
		var ps []string
		for i := 0; i+1 < len(scope.Content); i += 2 {
			if scope.Content[i].Value == "projects" {
				for _, x := range scope.Content[i+1].Content {
					ps = append(ps, x.Value)
				}
			}
		}
		return ps, len(ps) == 0
	}
	return nil, false
}

// describe names an object for messages.
func describe(kind, name string) string {
	if k := kindByName(kind); k != nil {
		return fmt.Sprintf("%s %s", k.Title, name)
	}
	return kind + " " + name
}
