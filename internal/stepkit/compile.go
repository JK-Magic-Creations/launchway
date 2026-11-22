package stepkit

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// TemplateKey is the key of a template that holds the step's manifest.
const TemplateKey = "step_manifest"

// StepDir is where a step's files are, in its library or repository.
const StepDir = "steps"

// ManifestPath is where a step's manifest is in a library, or a repository.
func ManifestPath(name string) string { return StepDir + "/" + name + "/step.yaml" }

// shellQuote quotes text for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Script is what the step runs in its container: it goes to the step's
// directory (in the library, or in the repository for a step of the same
// repository) and starts the command there.
func (m *Manifest) Script(self bool) string {
	base := `"$LAUNCHWAY_LIB"`
	if self {
		base = `"$PWD"`
	}
	var cmd string
	if m.Shell {
		cmd = m.Command[0]
	} else {
		q := make([]string, len(m.Command))
		for i, a := range m.Command {
			q[i] = shellQuote(a)
		}
		cmd = strings.Join(q, " ")
	}
	return fmt.Sprintf("LAUNCHWAY_WORKSPACE=\"$PWD\"; export LAUNCHWAY_WORKSPACE\n"+
		"LAUNCHWAY_STEP_DIR=%s/%s/%s; export LAUNCHWAY_STEP_DIR\n"+
		"cd \"$LAUNCHWAY_STEP_DIR\" || { echo \"the step directory %s/%s is not in the workspace\" >&2; exit 1; }\n"+
		"exec %s\n", base, StepDir, m.Name, StepDir, m.Name, cmd)
}

type tplParam struct {
	Type        string   `yaml:"type,omitempty"`
	Default     *string  `yaml:"default,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Options     []string `yaml:"options,omitempty"`
}

type tplRes struct {
	CPU    string `yaml:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

type tplStep struct {
	Image     string              `yaml:"image"`
	Run       string              `yaml:"run"`
	Env       map[string]any      `yaml:"env,omitempty"`
	Secrets   map[string]any      `yaml:"secrets,omitempty"`
	Timeout   string              `yaml:"timeout,omitempty"`
	Resources *tplRes             `yaml:"resources,omitempty"`
	Reports   map[string][]string `yaml:"reports,omitempty"`
	Artifacts []map[string]any    `yaml:"artifacts,omitempty"`
}

type tplFile struct {
	Template     string              `yaml:"template"`
	Description  string              `yaml:"description,omitempty"`
	StepManifest string              `yaml:"step_manifest"`
	Params       *yaml.Node          `yaml:"params,omitempty"`
	Steps        map[string]*tplStep `yaml:"steps"`
	Outputs      *yaml.Node          `yaml:"outputs,omitempty"`
}

// MainStep is the ID, inside the template, of the one step a custom step
// becomes.
const MainStep = "run"

// Template makes the template a custom step is expanded as: its inputs are
// the template's parameters (so `with:` is checked the way every template's
// is), its outputs the template's outputs, and the step carries the manifest so
// that the permissions and the checks it was expanded with are the ones the
// run records. self is true for a step in the repository the pipeline is in.
func (m *Manifest) Template(self bool) ([]byte, error) {
	params := &yaml.Node{Kind: yaml.MappingNode}
	secrets := map[string]any{}
	add := func(name string, p tplParam) error {
		var v yaml.Node
		if err := v.Encode(p); err != nil {
			return err
		}
		params.Content = append(params.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: name}, &v)
		return nil
	}
	for _, in := range m.Inputs {
		switch in.Type {
		case TypeSecret:
			for _, suffix := range []string{"store", "key", "field"} {
				p := tplParam{Type: "string", Description: fmt.Sprintf("%s of the secret reference for %s", suffix, in.Name)}
				if suffix == "field" {
					empty := ""
					p.Default = &empty
				}
				if suffix == "store" {
					p.Description = in.Description
					if p.Description == "" {
						p.Description = "secret " + in.Name + ": the store of its reference"
					}
				}
				if err := add(in.Name+"_"+suffix, p); err != nil {
					return nil, err
				}
			}
			secrets[EnvPart(in.Name)] = map[string]any{
				"store": map[string]string{"param": in.Name + "_store"},
				"key":   map[string]string{"param": in.Name + "_key"},
				"field": map[string]string{"param": in.Name + "_field"},
			}
		default:
			p := tplParam{Description: in.Description, Default: in.Default}
			switch in.Type {
			case TypeNumber, TypeBoolean, TypeList:
				p.Type = in.Type
			default:
				p.Type = "string"
			}
			if in.Type == TypeString || in.Type == TypeNumber {
				p.Options = in.Options
			}
			if !in.Required && in.Default == nil {
				empty := ""
				if in.Type == TypeList {
					empty = "[]"
				}
				if in.Type == TypeBoolean {
					empty = "false"
				}
				if in.Type == TypeRecord {
					empty = "{}"
				}
				if in.Type == TypeNumber {
					return nil, fmt.Errorf("input %s: an optional number needs a default", in.Name)
				}
				p.Default = &empty
			}
			if err := add(in.Name, p); err != nil {
				return nil, err
			}
		}
	}
	st := &tplStep{Image: m.Image, Run: m.Script(self), Timeout: m.Timeout,
		Reports: map[string][]string{
			"tests":    {IODir + "/" + ReportsDir + "/**/*.xml"},
			"findings": {IODir + "/" + ReportsDir + "/**/*.sarif"},
		},
		Artifacts: []map[string]any{{"name": ArtifactName, "paths": []string{IODir + "/" + ArtifactsDir + "/**/*"}, "when": "always"}},
	}
	if len(secrets) > 0 {
		st.Secrets = secrets
	}
	if m.CPU != "" || m.Memory != "" {
		st.Resources = &tplRes{CPU: m.CPU, Memory: m.Memory}
	}
	f := &tplFile{Template: m.Name, Description: m.Description, StepManifest: m.Source, Steps: map[string]*tplStep{MainStep: st}}
	if len(params.Content) > 0 {
		f.Params = params
	}
	if len(m.Outputs) > 0 {
		outs := &yaml.Node{Kind: yaml.MappingNode}
		for _, o := range m.Outputs {
			var v yaml.Node
			if err := v.Encode(map[string]string{"from": MainStep + "." + o.Name}); err != nil {
				return nil, err
			}
			outs.Content = append(outs.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: o.Name}, &v)
		}
		f.Outputs = outs
	}
	return yaml.Marshal(f)
}

// Permission is one thing a step may do, in words a person adding it reads.
type Permission struct {
	Kind   string // network, secret, write
	Detail string
}

// Permissions lists what the step may do, always: "reach no network" is
// as much a part of the list as a host.
func (m *Manifest) PermissionList() []Permission {
	var out []Permission
	if len(m.Permissions.Network) == 0 {
		out = append(out, Permission{"network", "reach no network"})
	} else {
		out = append(out, Permission{"network", "reach " + strings.Join(m.Permissions.Network, ", ")})
	}
	if s := m.SecretInputs(); len(s) > 0 {
		out = append(out, Permission{"secret", "read the secrets given as " + strings.Join(s, ", ")})
	} else {
		out = append(out, Permission{"secret", "read no secrets"})
	}
	if len(m.Permissions.Write) == 0 {
		out = append(out, Permission{"write", "write only its own output files (the workspace is read-only to it)"})
	} else if contains(m.Permissions.Write, ".") {
		out = append(out, Permission{"write", "change any file in the workspace"})
	} else {
		out = append(out, Permission{"write", "write " + strings.Join(m.Permissions.Write, ", ") + " in the workspace (the rest is read-only)"})
	}
	return out
}

// Sentence is the permissions as one sentence: "this step can reach X and read secrets Y".
func (m *Manifest) Sentence() string {
	ps := m.PermissionList()
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.Detail
	}
	return "This step can " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1] + "."
}

// Change is one difference between two versions of a step.
type Change struct {
	What       string
	Permission bool // the difference is in what the step may do
	Widening   bool // the step may do more than before
}

// Diff says what changed between two versions of a step: its inputs and
// outputs, and, called out, what it may do.
func Diff(old, cur *Manifest) []Change {
	var out []Change
	for _, in := range cur.Inputs {
		o := old.Input(in.Name)
		switch {
		case o == nil && in.Required:
			out = append(out, Change{What: fmt.Sprintf("new required input %s (%s): pipelines that use the step must give it", in.Name, in.Type)})
		case o == nil:
			out = append(out, Change{What: fmt.Sprintf("new input %s (%s)", in.Name, in.Type)})
		case o.Type != in.Type:
			out = append(out, Change{What: fmt.Sprintf("input %s changed type from %s to %s", in.Name, o.Type, in.Type)})
		case !o.Required && in.Required:
			out = append(out, Change{What: fmt.Sprintf("input %s is now required", in.Name)})
		}
	}
	for _, in := range old.Inputs {
		if cur.Input(in.Name) == nil {
			out = append(out, Change{What: fmt.Sprintf("input %s was removed: pipelines that give it fail", in.Name)})
		}
	}
	for _, o := range cur.Outputs {
		p := old.Output(o.Name)
		switch {
		case p == nil:
			out = append(out, Change{What: fmt.Sprintf("new output %s (%s)", o.Name, o.Type)})
		case p.Type != o.Type:
			out = append(out, Change{What: fmt.Sprintf("output %s changed type from %s to %s", o.Name, p.Type, o.Type)})
		}
	}
	for _, o := range old.Outputs {
		if cur.Output(o.Name) == nil {
			out = append(out, Change{What: fmt.Sprintf("output %s was removed: steps that use it fail", o.Name)})
		}
	}
	out = append(out, diffSet("network", "may now reach", "no longer reaches", old.Permissions.Network, cur.Permissions.Network)...)
	out = append(out, diffSet("write", "may now write", "no longer writes", old.Permissions.Write, cur.Permissions.Write)...)
	oldS, curS := old.SecretInputs(), cur.SecretInputs()
	out = append(out, diffSet("secret", "now takes the secret", "no longer takes the secret", oldS, curS)...)
	if old.Image != cur.Image {
		out = append(out, Change{What: fmt.Sprintf("image changed from %s to %s", old.Image, cur.Image)})
	}
	return out
}

func diffSet(kind, grow, shrink string, old, cur []string) []Change {
	var out []Change
	for _, c := range cur {
		if !contains(old, c) {
			out = append(out, Change{What: fmt.Sprintf("permission: the step %s %s", grow, c), Permission: true, Widening: true})
		}
	}
	rem := []string{}
	for _, o := range old {
		if !contains(cur, o) {
			rem = append(rem, o)
		}
	}
	sort.Strings(rem)
	for _, o := range rem {
		out = append(out, Change{What: fmt.Sprintf("permission: the step %s %s", shrink, o), Permission: true})
	}
	_ = kind
	return out
}

// CanWiden reports whether any change is a widening of permissions.
func CanWiden(cs []Change) bool {
	for _, c := range cs {
		if c.Widening {
			return true
		}
	}
	return false
}
