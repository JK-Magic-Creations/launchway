package pipeline

// What a template may take as parameters beyond text: the store, key and
// field of a secret reference, an ai step's model and prompt. The expanded
// graph holds plain values, so runners and servers see what they always saw.

import (
	"gopkg.in/yaml.v3"
)

// secretText reads the store, key or field of a secret reference. In a
// template it may be { param: name } (with format:) of a parameter of type
// string: a list or a number cannot name a secret.
func (ps *parser) secretText(n *yaml.Node, at string) string {
	if n.Kind == yaml.MappingNode && hasKey(n, "each") {
		ps.add(n, at, "a secret reference is not repeated with the loop's values: write it, or in a template take it from a string parameter")
		return ""
	}
	if n.Kind == yaml.MappingNode && hasKey(n, "param") && ps.tmpl != nil {
		if p := ps.tmpl.params[mappingValue(n, "param")]; p != nil && p.Type != paramTypeString {
			ps.add(n, at, "%s is a %s; a secret reference takes a string parameter", p.Name, p.Type)
			return ""
		}
	}
	return ps.scalar(n, at)
}

// inScope gives an ai step's references to other steps the IDs they have in
// the run, for a template's step whose steps are prefixed.
func (a *AIStep) inScope(prefix string) *AIStep {
	if a == nil || prefix == "" {
		return a
	}
	c := *a
	c.Inputs = make([]AIInput, len(a.Inputs))
	for i, in := range a.Inputs {
		if in.Output != nil {
			o := *in.Output
			o.Step = prefix + o.Step
			in.Output = &o
		}
		if in.From != "" {
			in.From = prefix + in.From
		}
		c.Inputs[i] = in
	}
	return &c
}

// secretParamNames are the parameters a template's secret references take
// their store, key or field from, for the list of templates.
func secretParamNames(steps *yaml.Node) map[string]bool {
	out := map[string]bool{}
	if steps == nil || steps.Kind != yaml.MappingNode {
		return out
	}
	for i := 1; i < len(steps.Content); i += 2 {
		secrets := mappingNode(steps.Content[i], "secrets")
		if secrets == nil || secrets.Kind != yaml.MappingNode {
			continue
		}
		for j := 1; j < len(secrets.Content); j += 2 {
			ref := secrets.Content[j]
			if ref.Kind != yaml.MappingNode {
				continue
			}
			for k := 1; k < len(ref.Content); k += 2 {
				if v := ref.Content[k]; v.Kind == yaml.MappingNode && hasKey(v, "param") {
					out[mappingValue(v, "param")] = true
				}
			}
		}
	}
	return out
}

func mappingNode(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// markSecretParams marks the parameters that some step uses as the store,
// key or field of a secret reference, so authors can see it.
func (info *TemplateInfo) markSecretParams(doc *yaml.Node) {
	used := secretParamNames(mappingNode(doc, "steps"))
	for _, p := range info.Params {
		p.SecretReference = used[p.Name]
	}
}
