package pipeline

// The JSON Schema of the pipeline file, for editors: completion of keys, and
// a red line under a key that does not exist, before the file is ever saved.
// It is built from the parser's own lists of keys (topKeys, stepKeys and the
// lists of each kind of step), so a key the parser learns is in the schema
// the next time it is generated, and a test fails if the published copy is
// out of date. It says which keys exist and the shape of the common ones; the
// parser stays the judge of values, and says exactly what is wrong.

import "slices"

// SchemaID is where the schema is published.
const SchemaID = "https://jk-magic-creations.github.io/launchway/pipeline.schema.json"

type obj = map[string]any

func prop(typ any, desc string) obj {
	o := obj{"description": desc}
	if typ != nil {
		o["type"] = typ
	}
	return o
}

var scalarOrAny = []string{"string", "number", "boolean", "object", "array"}

// keyHelp is what the schema says about the keys an editor shows most.
var keyHelp = map[string]obj{
	"pipeline":     prop("string", "The pipeline's name: lower-case letters, digits and hyphens."),
	"supersede":    prop("boolean", "A new run of a push or pull request cancels older runs of the same ref still going."),
	"description":  prop("string", "One line saying what the step is for."),
	"kind":         {"description": "What the step is: omit for a step that runs a script.", "enum": []string{"approval", "image", "deploy", "rollback", "rollout", "verify", "ai"}},
	"container":    prop("string", "A named container from containers: that the step runs in."),
	"image":        prop(scalarOrAny, "The container image the step's script runs in."),
	"run":          prop("string", "The script to run."),
	"needs":        prop([]string{"string", "array"}, "The steps that must succeed first; a step that uses for_each or a template is waited for as a whole."),
	"when":         prop("object", "Conditions for the step to run: branch, tag, input, output, upstream."),
	"env":          prop("object", "Environment variables of the step."),
	"secrets":      prop("object", "Variables whose values a runner looks up from a secret store."),
	"retry":        prop([]string{"integer", "object"}, "Extra attempts after a failure, or { times, wait, backoff, on }."),
	"timeout":      prop("string", "How long one attempt may run, such as 10m."),
	"reports":      prop("object", "Files the step writes that Launchway reads: tests, coverage, findings, sbom."),
	"resources":    prop("object", "CPU and memory of the step: { cpu, cpu_limit, memory, memory_limit }. On a step that uses a template: the default of every stage that runs a script."),
	"services":     prop([]string{"array", "string"}, "The service containers that run beside this step ([] for none, all for every one); a step that says nothing starts them all."),
	"stages":       prop("object", "On a step that uses a template: settings of single stages by name, such as { test: { resources: { memory: 6Gi }, services: [db] } }."),
	"uses":         prop("string", "A template to use in place of this step: library/template, such as launchway/go-service."),
	"with":         prop("object", "The values of the template's parameters."),
	"for_each":     prop("object", "Repeat the step once for each combination of the lists (a matrix); see exclude, add, max_parallel, fail_fast, continue_for."),
	"exclude":      prop("array", "With for_each: combinations to leave out."),
	"add":          prop("array", "With for_each: whole combinations to add outside the lists."),
	"max_parallel": prop("integer", "With for_each: how many combinations may be queued or running at once."),
	"fail_fast":    prop("boolean", "With for_each: stop the other combinations once one fails (default true)."),
	"continue_for": prop("array", "With for_each: combinations that may fail without failing the run."),
	"artifacts":    prop("array", "Sets of files the step keeps for download and for later steps."),
	"fetch":        prop("array", "Artifacts of earlier steps to put in the workspace first."),
	"cache":        prop("array", "Directories kept between runs, restored before the step and saved after it."),
	"defaults":     prop("object", "What every script step takes unless it says otherwise: image, container, env, timeout, retry, cache."),
	"containers":   prop("object", "Named images that steps run in, and services that run beside them."),
	"libraries":    prop("object", "Shared libraries a pipeline uses templates from; launchway/ needs no entry."),
	"on":           prop("object", "What starts a run: push, pull_request, schedule, webhook."),
	"inputs":       prop("object", "Values a run is started with."),
	"runner":       prop("object", "Which runners may run the steps: a pool, labels or both."),
	"workspace":    prop("object", "Paths a runner that carries the workspace from step to step leaves out of it: workspace: { exclude: [node_modules] }."),
	"lane":         prop("object", "Which runs of the project may go at the same time."),
}

func property(key string) obj {
	if h, ok := keyHelp[key]; ok {
		return h
	}
	return obj{}
}

func properties(lists ...[]string) obj {
	out := obj{}
	for _, l := range lists {
		for _, k := range l {
			out[k] = property(k)
		}
	}
	return out
}

// Schema returns the JSON Schema (draft 2020-12) of the pipeline file.
func Schema() map[string]any {
	stepKeysAll := slices.Concat(stepKeys, deployKeys, rollbackKeys, rolloutStepKeys, imageKeys, verifyKeys, aiKeys,
		[]string{"approvers", "environment", "service", "version"})
	step := obj{
		"type":                 "object",
		"description":          "One step. The keys that apply depend on kind; the parser says which do not.",
		"properties":           properties(stepKeysAll),
		"additionalProperties": false,
	}
	return obj{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         SchemaID,
		"title":       "Launchway pipeline file (.launchway/pipeline.yaml)",
		"description": "A pipeline: its steps, and what starts it. Generated from the parser; `launchway validate` is the judge.",
		"type":        "object",
		"required":    []string{"pipeline", "steps"},
		"properties": func() obj {
			p := properties(topKeys)
			p["steps"] = obj{"type": "object", "description": "The steps, by name.", "additionalProperties": step, "minProperties": 1}
			p["defaults"] = obj{"type": "object", "description": keyHelp["defaults"]["description"], "properties": properties(defaultsKeys), "additionalProperties": false}
			p["containers"] = obj{"type": "object", "description": keyHelp["containers"]["description"],
				"additionalProperties": obj{"type": "object", "properties": properties(containerKeys), "additionalProperties": false}}
			return p
		}(),
		"additionalProperties": false,
	}
}
