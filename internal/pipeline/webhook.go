package pipeline

// Webhook triggers: on: { webhook: { name: deploy-request, ref: main,
// inputs: { version: body.version } } }, or a list of them.
//
// A webhook trigger is a URL other systems call to start a run. What the
// caller must prove (a token or a signature) is set on the server, never in
// the repository. Its inputs map fields of the request's JSON body to the
// pipeline's inputs, as paths written body.field.field: a closed mapping,
// not an expression. The values are then checked exactly like values a
// person gives.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits on webhook triggers.
const (
	MaxWebhooks    = 20
	maxBodyPathLen = 10 // fields in one path
)

var (
	webhookNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	bodyFieldRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidWebhookName reports whether name can name a webhook trigger.
func ValidWebhookName(name string) bool { return webhookNameRe.MatchString(name) }

// WebhookTrigger is one entry of on.webhook.
type WebhookTrigger struct {
	Name string `json:"name"`
	// Ref is the branch or tag whose pipeline runs; empty is the project's
	// default branch.
	Ref string `json:"ref,omitempty"`
	// Inputs maps input names to fields of the body: body.a.b.
	Inputs map[string]string `json:"inputs,omitempty"`
	Line   int               `json:"-"`

	inputsLine int
}

// Webhook returns the webhook trigger with the given name, or nil.
func (t Triggers) WebhookNamed(name string) *WebhookTrigger {
	for _, w := range t.Webhook {
		if w.Name == name {
			return w
		}
	}
	return nil
}

// Values takes the inputs the trigger maps from a JSON body, as raw values
// to be checked with ResolveInputs: a field that is missing or null gives
// no value, so the input takes its default. A string is taken as it is, a
// number or true or false as written, and a list of those as a list.
// Objects, and lists of anything else, are refused.
func (w *WebhookTrigger) Values(body []byte) (map[string]string, error) {
	errs := InputErrors{}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("the body is not JSON: %v", err)
	}
	out := map[string]string{}
	for name, path := range w.Inputs {
		v, ok := lookupBody(doc, strings.Split(strings.TrimPrefix(path, "body."), "."))
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case []any:
			items := make([]string, 0, len(x))
			for _, it := range x {
				s, ok := scalarText(it)
				if !ok {
					errs[name] = path + " is a list of something other than single values"
					break
				}
				items = append(items, s)
			}
			if errs[name] == "" {
				b, _ := json.Marshal(items)
				out[name] = string(b)
			}
		default:
			s, ok := scalarText(x)
			if !ok {
				errs[name] = path + " is an object, not a value"
				continue
			}
			out[name] = s
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

func lookupBody(doc any, path []string) (any, bool) {
	for _, f := range path {
		m, ok := doc.(map[string]any)
		if !ok {
			return nil, false
		}
		if doc, ok = m[f]; !ok {
			return nil, false
		}
	}
	return doc, true
}

func scalarText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// ---- reading webhook triggers ----

func (ps *parser) parseWebhooks(n *yaml.Node, p *Pipeline) {
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
		if len(items) == 0 {
			ps.add(n, "on.webhook", "list at least one webhook")
		}
	}
	if len(items) > MaxWebhooks {
		ps.add(n, "on.webhook", "a pipeline may have at most %d webhooks", MaxWebhooks)
		return
	}
	seen := map[string]bool{}
	for i, v := range items {
		at := "on.webhook"
		if n.Kind == yaml.SequenceNode {
			at = fmt.Sprintf("on.webhook[%d]", i)
		}
		w := &WebhookTrigger{Line: v.Line}
		var inputsNode *yaml.Node
		ps.eachKey(v, at, []string{"name", "ref", "inputs"}, func(k, v2 *yaml.Node) {
			where := at + "." + k.Value
			switch k.Value {
			case "name":
				w.Name = ps.str(v2, where)
				switch {
				case !ValidWebhookName(w.Name):
					ps.add(v2, where, "%q is not a valid webhook name: use lower-case letters, digits and hyphens", clipText(w.Name, 60))
				case seen[w.Name]:
					ps.add(v2, where, "there is already a webhook called %s", w.Name)
				}
				seen[w.Name] = true
			case "ref":
				w.Ref = ps.str(v2, where)
				if !ValidVersion(w.Ref) {
					ps.add(v2, where, "name a branch or tag, such as main")
				}
			case "inputs":
				inputsNode = v2
				if v2.Kind != yaml.MappingNode {
					ps.add(v2, where, "inputs maps input names to body fields, for example: inputs: { version: body.version }")
					return
				}
				w.Inputs = map[string]string{}
				for j := 0; j+1 < len(v2.Content); j += 2 {
					name, path := v2.Content[j], v2.Content[j+1]
					if _, dup := w.Inputs[name.Value]; dup {
						ps.add(name, where+"."+name.Value, "%q is written twice", name.Value)
						continue
					}
					text := ps.str(path, where+"."+name.Value)
					if !validBodyPath(text) {
						ps.add(path, where+"."+name.Value, "write a field of the request's JSON body, such as body.version or body.release.tag")
					}
					w.Inputs[name.Value] = text
				}
			}
		})
		if v.Kind == yaml.MappingNode && w.Name == "" {
			ps.add(v, at+".name", "name the webhook; its address ends with the name")
		}
		w.inputsLine = 0
		if inputsNode != nil {
			w.inputsLine = inputsNode.Line
		}
		p.On.Webhook = append(p.On.Webhook, w)
	}
}

func validBodyPath(s string) bool {
	rest, ok := strings.CutPrefix(s, "body.")
	if !ok {
		return false
	}
	parts := strings.Split(rest, ".")
	if len(parts) > maxBodyPathLen {
		return false
	}
	for _, f := range parts {
		if !bodyFieldRe.MatchString(f) {
			return false
		}
	}
	return true
}

// checkWebhookInputs checks that each webhook maps to inputs that exist.
func (ps *parser) checkWebhookInputs(p *Pipeline) {
	for _, w := range p.On.Webhook {
		for name := range w.Inputs {
			if p.Input(name) == nil {
				ps.problems = append(ps.problems, Problem{Line: w.inputsLine, Path: "on.webhook." + w.Name + ".inputs." + name,
					Message: fmt.Sprintf("there is no input called %q to map a body field to", clipText(name, 60))})
			}
		}
	}
}
