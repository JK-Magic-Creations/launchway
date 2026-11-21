// Package tfplan reads the JSON form of a Terraform or OpenTofu plan
// (`tofu show -json planfile`) into what a person approves: what will be
// added, changed, destroyed and replaced, the attribute differences with
// sensitive values hidden, and a risk summary that puts destroys and
// replacements first. Cost is deliberately not estimated.
//
// The plan is data a repository's code produced, so it is read with limits;
// values are shortened, unknown values say so, and anything the plan marks
// sensitive (or that looks like a credential) is never shown.
package tfplan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/cac/ansible"
)

// Limits.
const (
	MaxPlanBytes   = 64 << 20
	MaxResources   = 20000
	MaxAttrs       = 60
	MaxValueRunes  = 160
	Sensitive      = "(sensitive)"
	KnownAfterWork = "(known after apply)"
)

// Actions, as a person reads them.
const (
	Create  = "create"
	Update  = "update"
	Delete  = "delete"
	Replace = "replace"
	Read    = "read"
	NoOp    = "no-op"
)

// Plan is a reviewed-able plan.
type Plan struct {
	FormatVersion string           `json:"format_version"`
	ToolVersion   string           `json:"tool_version"`
	Counts        Counts           `json:"counts"`
	Resources     []ResourceChange `json:"resources"`
	Outputs       []OutputChange   `json:"outputs"`
	Risk          Risk             `json:"risk"`
	Summary       string           `json:"summary"`
}

// Counts is the headline.
type Counts struct {
	Add     int `json:"add"`
	Change  int `json:"change"`
	Destroy int `json:"destroy"`
	Replace int `json:"replace"`
	Read    int `json:"read"`
	NoOp    int `json:"no_op"`
}

// ResourceChange is one resource instance's change.
type ResourceChange struct {
	Address  string     `json:"address"`
	Type     string     `json:"type"`
	Provider string     `json:"provider,omitempty"`
	Action   string     `json:"action"`
	Reason   string     `json:"reason,omitempty"`
	Attrs    []AttrDiff `json:"attrs,omitempty"`
	// AttrsCut says more attributes differ than are listed.
	AttrsCut bool `json:"attrs_cut,omitempty"`
}

// AttrDiff is one attribute's difference.
type AttrDiff struct {
	Path   string `json:"path"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Sensitive: the values are hidden because the plan marks them so.
	Sensitive bool `json:"sensitive,omitempty"`
	// ForcesReplacement: this attribute is why the resource is replaced.
	ForcesReplacement bool `json:"forces_replacement,omitempty"`
}

// OutputChange is an output that changes; sensitive values are hidden.
type OutputChange struct {
	Name      string `json:"name"`
	Action    string `json:"action"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// Risk puts what cannot be undone first.
type Risk struct {
	Level     string   `json:"level"` // none, low, high
	Destroyed []string `json:"destroyed,omitempty"`
	Replaced  []string `json:"replaced,omitempty"`
	Notes     []string `json:"notes,omitempty"`
}

type rawPlan struct {
	FormatVersion    string `json:"format_version"`
	TerraformVersion string `json:"terraform_version"`
	ResourceChanges  []struct {
		Address      string `json:"address"`
		Mode         string `json:"mode"`
		Type         string `json:"type"`
		ProviderName string `json:"provider_name"`
		ActionReason string `json:"action_reason"`
		Change       struct {
			Actions         []string        `json:"actions"`
			Before          json.RawMessage `json:"before"`
			After           json.RawMessage `json:"after"`
			AfterUnknown    json.RawMessage `json:"after_unknown"`
			BeforeSensitive json.RawMessage `json:"before_sensitive"`
			AfterSensitive  json.RawMessage `json:"after_sensitive"`
			ReplacePaths    [][]any         `json:"replace_paths"`
		} `json:"change"`
	} `json:"resource_changes"`
	OutputChanges map[string]struct {
		Actions        []string        `json:"actions"`
		AfterSensitive json.RawMessage `json:"after_sensitive"`
	} `json:"output_changes"`
}

// Parse reads `show -json` output.
func Parse(data []byte, secrets ...string) (*Plan, error) {
	if len(data) > MaxPlanBytes {
		return nil, fmt.Errorf("the plan is %d bytes; at most %d are read", len(data), MaxPlanBytes)
	}
	var raw rawPlan
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("the plan is not valid JSON: %w", err)
	}
	if raw.FormatVersion == "" {
		return nil, fmt.Errorf("this is not a plan in JSON form: it has no format_version (use show -json on the plan file)")
	}
	if len(raw.ResourceChanges) > MaxResources {
		return nil, fmt.Errorf("the plan changes %d resources; at most %d are read", len(raw.ResourceChanges), MaxResources)
	}
	p := &Plan{FormatVersion: raw.FormatVersion, ToolVersion: raw.TerraformVersion, Resources: []ResourceChange{}, Outputs: []OutputChange{}}
	for _, rc := range raw.ResourceChanges {
		action := actionOf(rc.Change.Actions)
		res := ResourceChange{Address: rc.Address, Type: rc.Type, Provider: rc.ProviderName, Action: action, Reason: rc.ActionReason}
		switch action {
		case Create:
			p.Counts.Add++
		case Update:
			p.Counts.Change++
		case Delete:
			p.Counts.Destroy++
		case Replace:
			p.Counts.Replace++
			p.Counts.Add++
			p.Counts.Destroy++
		case Read:
			p.Counts.Read++
		default:
			p.Counts.NoOp++
			continue // unchanged resources are not listed
		}
		forces := map[string]bool{}
		for _, rp := range rc.Change.ReplacePaths {
			forces[pathOf(rp)] = true
		}
		res.Attrs, res.AttrsCut = diffAttrs(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, rc.Change.BeforeSensitive, rc.Change.AfterSensitive, forces, secrets)
		p.Resources = append(p.Resources, res)
	}
	for name, oc := range raw.OutputChanges {
		a := actionOf(oc.Actions)
		if a == NoOp {
			continue
		}
		p.Outputs = append(p.Outputs, OutputChange{Name: name, Action: a, Sensitive: string(oc.AfterSensitive) == "true"})
	}
	sort.Slice(p.Outputs, func(i, j int) bool { return p.Outputs[i].Name < p.Outputs[j].Name })
	// Destroys and replacements first, then updates, creates, reads.
	rank := map[string]int{Delete: 0, Replace: 1, Update: 2, Create: 3, Read: 4}
	sort.SliceStable(p.Resources, func(i, j int) bool { return rank[p.Resources[i].Action] < rank[p.Resources[j].Action] })
	for _, r := range p.Resources {
		switch r.Action {
		case Delete:
			p.Risk.Destroyed = append(p.Risk.Destroyed, r.Address)
		case Replace:
			p.Risk.Replaced = append(p.Risk.Replaced, r.Address)
		}
	}
	switch {
	case len(p.Risk.Destroyed)+len(p.Risk.Replaced) > 0:
		p.Risk.Level = "high"
		if len(p.Risk.Destroyed) > 0 {
			p.Risk.Notes = append(p.Risk.Notes, fmt.Sprintf("%d resource(s) will be destroyed: the data in them is gone", len(p.Risk.Destroyed)))
		}
		if len(p.Risk.Replaced) > 0 {
			p.Risk.Notes = append(p.Risk.Notes, fmt.Sprintf("%d resource(s) will be replaced: destroyed and created again, with a new identity", len(p.Risk.Replaced)))
		}
	case p.Counts.Change > 0:
		p.Risk.Level = "low"
	default:
		p.Risk.Level = "none"
	}
	p.Summary = fmt.Sprintf("%d to add, %d to change, %d to destroy", p.Counts.Add, p.Counts.Change, p.Counts.Destroy)
	if p.Counts.Replace > 0 {
		p.Summary += fmt.Sprintf(" (%d of them replacements)", p.Counts.Replace)
	}
	if len(p.Resources) == 0 && len(p.Outputs) == 0 {
		p.Summary = "no changes: the infrastructure matches the code"
	}
	return p, nil
}

func actionOf(a []string) string {
	switch {
	case len(a) == 2 && ((a[0] == "delete" && a[1] == "create") || (a[0] == "create" && a[1] == "delete")):
		return Replace
	case len(a) == 1:
		switch a[0] {
		case "create":
			return Create
		case "update":
			return Update
		case "delete":
			return Delete
		case "read":
			return Read
		}
	}
	return NoOp
}

func pathOf(p []any) string {
	var parts []string
	for _, x := range p {
		parts = append(parts, fmt.Sprint(x))
	}
	return strings.Join(parts, ".")
}

// diffAttrs lists the attributes that differ, flattened to dotted paths.
func diffAttrs(before, after, unknown, bSens, aSens json.RawMessage, forces map[string]bool, secrets []string) ([]AttrDiff, bool) {
	b, a, u, bs, as := flatten(before), flatten(after), flatten(unknown), flatten(bSens), flatten(aSens)
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	for k := range u {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []AttrDiff
	cut := false
	for _, k := range names {
		bv, bok := b[k]
		av, aok := a[k]
		unk := u[k] == "true"
		sens := bs[k] == "true" || as[k] == "true"
		if !unk && bok == aok && bv == av {
			continue
		}
		if unk && bok && bv == "" && !aok {
			continue
		}
		if len(out) >= MaxAttrs {
			cut = true
			break
		}
		d := AttrDiff{Path: k, ForcesReplacement: forces[k]}
		switch {
		case sens:
			d.Sensitive = true
		default:
			d.Before = show(bv, bok, secrets)
			d.After = show(av, aok, secrets)
			if unk {
				d.After = KnownAfterWork
			}
		}
		out = append(out, d)
	}
	return out, cut
}

func show(v string, present bool, secrets []string) string {
	if !present {
		return ""
	}
	v = ansible.Redact(v, secrets...)
	if r := []rune(v); len(r) > MaxValueRunes {
		return string(r[:MaxValueRunes]) + "…"
	}
	return v
}

// flatten turns a JSON value into dotted paths with scalar text values.
// Absent and null values are left out.
func flatten(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return out
	}
	var walk func(prefix string, v any, depth int)
	walk = func(prefix string, v any, depth int) {
		if depth > 8 {
			out[prefix] = "(deeply nested)"
			return
		}
		switch x := v.(type) {
		case nil:
		case map[string]any:
			for k, c := range x {
				walk(join(prefix, k), c, depth+1)
			}
		case []any:
			for i, c := range x {
				walk(join(prefix, fmt.Sprint(i)), c, depth+1)
			}
		case string:
			out[prefix] = x
		default:
			b, _ := json.Marshal(x)
			out[prefix] = string(b)
		}
	}
	walk("", v, 0)
	delete(out, "")
	if _, ok := v.(bool); ok {
		out[""] = fmt.Sprint(v)
	}
	return out
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}
