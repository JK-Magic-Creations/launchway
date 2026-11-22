package pipeline

// Custom steps: a directory with a step.yaml and code in any language, used
// as `uses: <library>/<step>` or `uses: ./steps/<step>`. A library answers
// the template loader with a template made from the manifest (see
// stepkit.Manifest.Template), so a custom step is expanded, checked, pinned
// and recorded exactly like a template, and becomes one ordinary step that
// carries what the manifest asks for as Step.Custom.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/stepkit"
	"gopkg.in/yaml.v3"
)

// SchemaCustomSteps is the server schema that brought custom steps (63):
// a run's step notes (summaries and annotations). A pipeline that uses a
// custom step carries "requires": 63 so that no older server or runner
// runs it without the contract.
const SchemaCustomSteps = 63

// SelfLibrary is the name of the pseudo library a `uses: ./steps/<name>` step
// belongs to: the repository the pipeline is in, at the commit of the run.
const SelfLibrary = "self"

// CustomStep is what a step that came from a custom step carries: who it is,
// what version of whom, and what it may do. It is fixed when the run is
// created, from the manifest at the pinned commit, so nothing the step does
// at run time can widen it.
type CustomStep struct {
	Name     string `json:"name"`
	Library  string `json:"library,omitempty"` // what the pipeline calls the library; SelfLibrary for a step of the same repository
	Repo     string `json:"repo,omitempty"`    // the server's name for the library, which its step pages and the record of uses go by
	Commit   string `json:"commit,omitempty"`  // the commit of the library the manifest was read at
	Contract int    `json:"contract"`
	Digest   string `json:"digest"` // sha256 of the manifest
	// What it may do.
	Network []string `json:"network,omitempty"`
	Write   []string `json:"write,omitempty"`
	Secrets []string `json:"secrets,omitempty"` // names of the inputs that are secrets
	// What it takes and gives, by name and type, for the typed inputs the
	// runner builds and the outputs it checks.
	InputTypes  map[string]string `json:"input_types,omitempty"`
	OutputTypes map[string]string `json:"output_types,omitempty"`
	// Values are the values of the inputs that are not secrets, as text, the
	// defaults included.
	Values map[string]string `json:"values,omitempty"`
}

// Permissions says in a sentence what the step may do.
func (c *CustomStep) Permissions() string {
	m := &stepkit.Manifest{Name: c.Name, Permissions: stepkit.Permissions{Network: c.Network, Write: c.Write}}
	for _, s := range c.Secrets {
		m.Inputs = append(m.Inputs, &stepkit.Input{Name: s, Type: stepkit.TypeSecret})
	}
	return m.Sentence()
}

func customOf(m *stepkit.Manifest) *CustomStep {
	c := &CustomStep{Name: m.Name, Contract: m.Contract, Digest: m.Digest,
		Network: m.Permissions.Network, Write: m.Permissions.Write, Secrets: m.SecretInputs(),
		InputTypes: map[string]string{}, OutputTypes: map[string]string{}}
	for _, in := range m.Inputs {
		c.InputTypes[in.Name] = in.Type
	}
	for _, o := range m.Outputs {
		c.OutputTypes[o.Name] = o.Type
	}
	return c
}

// readCustom reads the manifest a template carries, if it is a custom
// step's. Its problems are the template's.
func (ps *parser) readCustom(n *yaml.Node) *stepkit.Manifest {
	if n == nil {
		return nil
	}
	text := ps.str(n, stepkit.TemplateKey)
	m, probs := stepkit.ParseManifest([]byte(text))
	for _, p := range probs {
		ps.add(n, stepkit.TemplateKey, "%s", p.Error())
	}
	return m
}

// checkCustomValues checks what a use gives a custom step against its inputs (ok says nothing was wrong before):
// the limits (length, range, pattern, entries) and the types that a
// template's parameters do not know (records).
func checkCustomValues(m *stepkit.Manifest, values map[string]string, withLines map[string]*yamlPos, usesLine int,
	withProblem func(line, col int, path, msg string), ok bool) bool {
	if m == nil {
		return ok
	}
	for _, in := range m.Inputs {
		v, set := values[in.Name]
		if !set || in.Type == stepkit.TypeSecret {
			continue
		}
		if msg := in.Check(v); msg != "" {
			ok = false
			line, col := usesLine, 0
			if pos := withLines[in.Name]; pos != nil {
				line, col = pos.Line, pos.Column
			}
			withProblem(line, col, "with."+in.Name, msg)
		}
	}
	return ok
}

// stampCustom gives the steps of a template made from a custom step what the step is and may do.
func (t *Template) stampCustom() {
	if t.Custom == nil {
		return
	}
	for _, s := range t.Steps {
		s.Custom = customOf(t.Custom)
		s.Custom.Values = stepValues(t.Custom, t.Values)
	}
}

// stepValues are the values of a custom step's inputs that are not secrets.
func stepValues(m *stepkit.Manifest, values map[string]string) map[string]string {
	out := map[string]string{}
	for _, in := range m.Inputs {
		if in.Type == stepkit.TypeSecret {
			continue
		}
		if v, ok := values[in.Name]; ok {
			out[in.Name] = v
		}
	}
	return out
}

// customUse is the library and the version a custom step is used from, for
// the record of the run.
func (c *CustomStep) setSource(lib *Library) {
	if lib == nil {
		return
	}
	c.Library, c.Repo, c.Commit = lib.Name, lib.Repo, lib.Commit
}

// IsCustom reports whether the step came from a custom step.
func (s *Step) IsCustom() bool { return s != nil && s.Custom != nil }

// UsesSchemaCustomSteps reports whether a step is a custom step.
func (p *Pipeline) UsesSchemaCustomSteps() bool {
	for _, s := range p.Steps {
		if s.IsCustom() {
			return true
		}
	}
	return false
}

// ValidStepName reports whether name can name a custom step.
func ValidStepName(name string) bool { return stepkit.StepNameRe.MatchString(name) }

// SplitUses splits a step's uses into the library alias and the template or
// step name; `./steps/name` is the repository's own step.
func SplitUses(uses string) (alias, name string, self bool) {
	if rest, ok := strings.CutPrefix(uses, "./"+stepkit.StepDir+"/"); ok {
		return SelfLibrary, rest, true
	}
	alias, name, _ = strings.Cut(uses, "/")
	return alias, name, false
}

var _ = fmt.Sprintf

// SelfSource gives the files of the repository the pipeline is in, at the
// commit of the run, so that `uses: ./steps/<name>` can be read.
type SelfSource struct {
	// Library names the repository for the record: Name SelfLibrary, Commit
	// the commit (or OfflineCommit for a directory).
	Library *Library
	// Read returns a file by its path from the repository's root, or
	// ErrNoTemplate if there is none.
	Read func(path string) ([]byte, error)
}

// TemplateOrStep returns the text of the template called name that read can
// supply: templates/<name>.yaml, or, when there is none (and a step of the
// same name is the thing asked for), the template made from the manifest in
// steps/<name>/step.yaml. self is true for a step of the pipeline's own
// repository, which has no templates.
func TemplateOrStep(read func(path string) ([]byte, error), name string, self bool) ([]byte, error) {
	if !self {
		if data, err := read(TemplatePath(name)); err == nil || !isNoTemplate(err) {
			return data, err
		}
	}
	if !ValidStepName(name) {
		return nil, ErrNoTemplate
	}
	data, err := read(stepkit.ManifestPath(name))
	if err != nil {
		return nil, err
	}
	m, probs := stepkit.ParseManifest(data)
	if len(probs) > 0 {
		return nil, fmt.Errorf("%s is not a valid step manifest: %s", stepkit.ManifestPath(name), probs[0].Error())
	}
	if m.Name != name {
		return nil, fmt.Errorf("%s is the step %s; its directory says %s", stepkit.ManifestPath(name), m.Name, name)
	}
	return m.Template(self)
}

func isNoTemplate(err error) bool { return errors.Is(err, ErrNoTemplate) }

// isStepText reports whether template text was made from a step manifest.
func isStepText(data []byte) bool {
	return strings.Contains(string(data), "\n"+stepkit.TemplateKey+":") || strings.HasPrefix(string(data), stepkit.TemplateKey+":")
}

// unknownInputMessage is unknownParamMessage in the words of a custom step.
func unknownInputMessage(step, key string, params []*Param) string {
	names := make([]string, len(params))
	for i, p := range params {
		names[i] = p.Name
	}
	hint := ""
	for _, n := range names {
		if strings.HasPrefix(n, key) || strings.HasPrefix(key, n) {
			hint = " (did you mean " + n + "?)"
			break
		}
	}
	return fmt.Sprintf("the step %s has no input %q%s; its inputs are %s", step, clipText(key, 40), hint, strings.Join(names, ", "))
}

// usesSelf reports whether a step uses a step of the pipeline's own repository.
func (p *Pipeline) usesSelf() bool {
	for _, s := range p.Steps {
		if _, _, self := SplitUses(s.Uses); self && s.IsUse() {
			return true
		}
	}
	return false
}

// RecordedLibrary is the library's name in the record of uses and on the step's page: the server's name for it,
// not the alias a pipeline gave it (the alias is only what that pipeline calls it).
func (c *CustomStep) RecordedLibrary() string {
	if c.Repo != "" {
		return c.Repo
	}
	return c.Library
}
