package pipeline

// defaults: what every step of the pipeline takes unless it says otherwise.
//
//	defaults:
//	  image: docker.io/library/golang:1.27
//	  env: { TZ: UTC }
//	  timeout: 20m
//	  retry: 1
//	  cache:
//	    - { name: go, paths: [.cache/go], key: { files: [go.sum] } }
//	steps:
//	  test: { run: go test ./... }                # takes all of them
//	  lint: { image: golangci/golangci-lint, run: golangci-lint run }  # its own image
//
// A step's own value always wins; env is merged key by key, so a step adds
// to the defaults and may change one of them. Only steps that run a script
// take them: approvals, images, deploys, verifications, rollbacks and steps
// that use a template have their own settings (a template is an interface,
// and what it runs is its own). The values are read by the same code as a
// step's own, so a mistake is reported in the same words, at the line of the
// default.

import (
	"gopkg.in/yaml.v3"
)

var defaultsKeys = []string{"image", "container", "env", "timeout", "retry", "cache"}

// applyDefaults copies the defaults into the steps' nodes that do not set
// their own, before the steps are read.
func (ps *parser) applyDefaults(d, steps *yaml.Node) {
	if d.Kind != yaml.MappingNode {
		ps.add(d, "defaults", "defaults is a mapping, for example: defaults: { image: alpine:3.22, env: { TZ: UTC } }")
		return
	}
	given := map[string]*yaml.Node{}
	ps.eachKey(d, "defaults", defaultsKeys, func(k, v *yaml.Node) { given[k.Value] = v })
	if given["image"] != nil && given["container"] != nil {
		ps.add(d, "defaults", "write image or container in defaults, not both: a container is a named image")
		return
	}
	if steps.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(steps.Content); i += 2 {
		st := steps.Content[i+1]
		if st.Kind != yaml.MappingNode || !takesDefaults(st) {
			continue
		}
		has := map[string]*yaml.Node{}
		for j := 0; j+1 < len(st.Content); j += 2 {
			has[st.Content[j].Value] = st.Content[j+1]
		}
		add := func(key string, v *yaml.Node) {
			st.Content = append(st.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key, Line: v.Line, Column: v.Column}, v)
		}
		for _, key := range []string{"image", "container", "timeout", "retry", "cache"} {
			v := given[key]
			if v == nil || has[key] != nil {
				continue
			}
			if (key == "image" && has["container"] != nil) || (key == "container" && has["image"] != nil) {
				continue
			}
			add(key, v)
		}
		if v := given["env"]; v != nil && v.Kind == yaml.MappingNode {
			if own := has["env"]; own == nil {
				add("env", v)
			} else if own.Kind == yaml.MappingNode {
				merged := &yaml.Node{Kind: yaml.MappingNode, Tag: own.Tag, Line: own.Line, Column: own.Column}
				merged.Content = append(merged.Content, own.Content...)
				ownKeys := map[string]bool{}
				for j := 0; j+1 < len(own.Content); j += 2 {
					ownKeys[own.Content[j].Value] = true
				}
				for j := 0; j+1 < len(v.Content); j += 2 {
					if !ownKeys[v.Content[j].Value] {
						merged.Content = append(merged.Content, v.Content[j], v.Content[j+1])
					}
				}
				for j := 0; j+1 < len(st.Content); j += 2 {
					if st.Content[j].Value == "env" {
						st.Content[j+1] = merged
					}
				}
			}
		} else if v != nil {
			ps.add(v, "defaults.env", "env is a mapping of variable names to values, for example: env: { TZ: UTC }")
		}
	}
}

// takesDefaults says a step node is a plain script step: no kind of its own
// and no template.
func takesDefaults(st *yaml.Node) bool {
	for j := 0; j+1 < len(st.Content); j += 2 {
		switch st.Content[j].Value {
		case "kind", "uses":
			return false
		}
	}
	return true
}
