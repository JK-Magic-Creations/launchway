package pipeline

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// AI steps (kind: ai) ask a model the server's operator configured
// (ai.models) one question: instructions, a prompt, and inputs taken from
// earlier steps' outputs and artifacts. They run no script. A runner makes
// the call with a key from its own secret stores; the key never enters a
// container. The answer is untrusted text: it can become the step's outputs
// and an artifact, and nothing else; it never runs anything.
const KindAI = "ai"

// Defaults and limits of ai steps.
const (
	DefaultAITimeout   = 10 * time.Minute
	MaxAITimeout       = time.Hour
	MaxAIPromptBytes   = 64 << 10
	MaxAIInputs        = 20
	MaxAISchemaBytes   = 16 << 10
	DefaultAIMaxTokens = 2048
	MaxAIMaxTokens     = 128000
	// AIResponseArtifact is the artifact an ai step keeps its answer in:
	// response.txt, and response.json when it has a schema.
	AIResponseArtifact = "response"
)

// AIOutputs are the outputs every ai step sets; with a schema, each
// top-level property of a string, number, integer or boolean type is an
// output too.
var AIOutputs = []string{"text", "model", "input_tokens", "output_tokens"}

// AIStep is what an ai step asks.
type AIStep struct {
	Model        string    `json:"model"`
	Instructions string    `json:"instructions,omitempty"` // the system prompt
	Prompt       string    `json:"prompt"`
	Inputs       []AIInput `json:"inputs,omitempty"`
	// Schema, when set, is a JSON schema (an object) the answer must
	// follow; the answer is then JSON.
	Schema      string   `json:"schema,omitempty"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature,omitempty"`
}

// AIInput is one input of an ai step: an output of a step it needs, or a
// file of an artifact such a step kept. Label names it in the prompt.
type AIInput struct {
	Label    string     `json:"label"`
	Output   *OutputRef `json:"output,omitempty"`
	From     string     `json:"from,omitempty"` // artifact: the step that kept it
	Artifact string     `json:"artifact,omitempty"`
	File     string     `json:"file,omitempty"` // a file of the artifact; empty for every file
	// MaxBytes is how much of it is sent; more is cut, saying so. Default
	// 64 KiB.
	MaxBytes int `json:"max_bytes,omitempty"`
	Line     int `json:"-"`
}

var (
	aiKeys      = []string{"kind", "description", "needs", "when", "timeout", "retry", "model", "instructions", "prompt", "inputs", "schema", "max_tokens", "temperature"}
	aiInputKeys = []string{"label", "output", "artifact", "file", "max_bytes"}
	modelNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	aiLabelRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,59}$`)
)

// IsAI reports whether the step asks a model.
func (s *Step) IsAI() bool { return s.Kind == KindAI }

// AISchemaOutputs lists the outputs an ai step's schema adds: its
// top-level properties of a scalar type, sorted.
func AISchemaOutputs(schema string) []string {
	if schema == "" {
		return nil
	}
	var s struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal([]byte(schema), &s) != nil {
		return nil
	}
	var out []string
	for name, p := range s.Properties {
		t, _ := p.Type.(string)
		if (t == "string" || t == "number" || t == "integer" || t == "boolean") && outputKeyRe.MatchString(name) && !slices.Contains(AIOutputs, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (ps *parser) parseAIStep(k, v *yaml.Node, at string) *Step {
	s := &Step{ID: k.Value, Line: k.Line, Kind: KindAI}
	a := &AIStep{MaxTokens: DefaultAIMaxTokens}
	s.AI = a
	if v.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(v.Content); i += 2 {
			key := v.Content[i]
			switch {
			case contains(aiKeys, key.Value):
			case contains(loopKeys, key.Value):
				ps.add(key, at+"."+key.Value, "an ai step cannot be repeated by for_each yet")
			case key.Value == "run" || key.Value == "image" || key.Value == "container" || key.Value == "env" || key.Value == "secrets" || key.Value == "runner":
				ps.add(key, at+"."+key.Value, "an ai step runs no script: the runner asks the model, with a key the step never sees; "+
					"put scripts in a step this one needs, and pass what they write as inputs")
			case key.Value == "artifacts" || key.Value == "reports" || key.Value == "fetch":
				ps.add(key, at+"."+key.Value, "an ai step keeps its answer as the artifact %q by itself, and takes files of other steps' artifacts under inputs", AIResponseArtifact)
			case key.Value == "on_failure":
				ps.add(key, at+".on_failure", "ai steps have their own failure handling, so they take no on_failure: give it to a step that runs a script or builds an image")
			default:
				ps.add(key, at+"."+key.Value, "%q is not a key of ai steps; expected one of: %s", key.Value, strings.Join(aiKeys, ", "))
			}
		}
	}
	var timeoutNode *yaml.Node
	var timeout time.Duration
	ps.eachKey(v, at, append(append([]string{}, aiKeys...), loopKeys...), func(key, n *yaml.Node) {
		where := at + "." + key.Value
		switch key.Value {
		case "kind":
		case "description":
			s.Description = strings.TrimSpace(ps.str(n, where))
			switch {
			case strings.ContainsAny(s.Description, "\r\n"):
				ps.add(n, where, "a description is one line; put longer notes in a comment")
			case utf8.RuneCountInString(s.Description) > MaxDescriptionLen:
				ps.add(n, where, "the description is longer than %d characters", MaxDescriptionLen)
			}
		case "needs":
			s.Needs = ps.strList(n, where)
			s.NeedsLine = n.Line
		case "when":
			s.When = ps.parseWhen(n, where)
		case "timeout":
			d, err := time.ParseDuration(ps.scalar(n, where))
			if err != nil {
				ps.add(n, where, "write a duration such as 5m")
			} else {
				timeoutNode, timeout = n, d
				s.timeoutSet = true
			}
		case "retry":
			ps.parseRetry(s, n, where)
		case "model":
			a.Model = ps.scalar(n, where)
			if !modelNameRe.MatchString(a.Model) {
				ps.add(n, where, "%q is not a model name; name one of the server's ai.models", a.Model)
			}
		case "instructions":
			a.Instructions = ps.scalar(n, where)
			if len(a.Instructions) > MaxAIPromptBytes {
				ps.add(n, where, "instructions are longer than %d KiB", MaxAIPromptBytes>>10)
			}
		case "prompt":
			a.Prompt = ps.scalar(n, where)
			if len(a.Prompt) > MaxAIPromptBytes {
				ps.add(n, where, "the prompt is longer than %d KiB", MaxAIPromptBytes>>10)
			}
		case "inputs":
			if n.Kind != yaml.SequenceNode {
				ps.add(n, where, "inputs is a list, for example: - { label: commits, output: changes.log }")
				return
			}
			if len(n.Content) > MaxAIInputs {
				ps.add(n, where, "at most %d inputs", MaxAIInputs)
			}
			ps.eachEntry(n.Content, where, func(in *yaml.Node, at string) {
				a.Inputs = append(a.Inputs, ps.parseAIInput(in, at))
			})
		case "schema":
			var m any
			if err := n.Decode(&m); err != nil || n.Kind != yaml.MappingNode {
				ps.add(n, where, "schema is a JSON schema written as a mapping, for example: { type: object, properties: { notes: { type: string } }, required: [notes] }")
				return
			}
			b, err := json.Marshal(jsonable(m))
			switch {
			case err != nil:
				ps.add(n, where, "the schema cannot be written as JSON: %v", err)
			case len(b) > MaxAISchemaBytes:
				ps.add(n, where, "the schema is larger than %d KiB", MaxAISchemaBytes>>10)
			default:
				var top struct {
					Type any `json:"type"`
				}
				_ = json.Unmarshal(b, &top)
				if t, _ := top.Type.(string); t != "object" {
					ps.add(n, where, "the schema's top level must be type: object")
				}
				a.Schema = string(b)
			}
		case "max_tokens":
			a.MaxTokens = ps.int(n, where)
			if a.MaxTokens < 1 || a.MaxTokens > MaxAIMaxTokens {
				ps.add(n, where, "max_tokens is 1 to %d (the model's own limit, set by the operator, may be lower)", MaxAIMaxTokens)
			}
		case "temperature":
			t := ps.verifyNumber(n, where)
			if t != nil && (*t < 0 || *t > 2) {
				ps.add(n, where, "temperature is 0 to 2")
			}
			a.Temperature = t
		}
	})
	if v.Kind != yaml.MappingNode {
		return s
	}
	if a.Model == "" {
		ps.add(k, at+".model", "name the model to ask: model: <one of the server's ai.models>")
	}
	if strings.TrimSpace(a.Prompt) == "" {
		ps.add(k, at+".prompt", "a prompt is required: what to ask the model")
	}
	labels := map[string]bool{}
	for _, in := range a.Inputs {
		if labels[in.Label] && in.Label != "" {
			ps.problems = append(ps.problems, Problem{Line: in.Line, Path: at + ".inputs", Message: fmt.Sprintf("%s: the label %q is used twice", s.ID, in.Label)})
		}
		labels[in.Label] = true
		if in.Artifact != "" {
			s.Fetch = append(s.Fetch, &Fetch{From: in.From, Name: in.Artifact, Line: in.Line})
		}
	}
	s.Timeout = Duration(DefaultAITimeout)
	if timeoutNode != nil {
		if timeout < 10*time.Second || timeout > MaxAITimeout {
			ps.add(timeoutNode, at+".timeout", "timeout must be between 10s and %s", Duration(MaxAITimeout).Short())
		} else {
			s.Timeout = Duration(timeout)
		}
	}
	// The answer, kept like any step's files.
	files := []string{"response.txt"}
	if a.Schema != "" {
		files = append(files, "response.json")
	}
	s.Artifacts = []*Artifact{{Name: AIResponseArtifact, Paths: files, When: ArtifactAlways, Line: s.Line}}
	return s
}

func (ps *parser) parseAIInput(n *yaml.Node, at string) AIInput {
	in := AIInput{Line: n.Line}
	ps.eachKey(n, at, aiInputKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "label":
			in.Label = ps.str(v, where)
			if !aiLabelRe.MatchString(in.Label) {
				ps.add(v, where, "a label is a short name: letters, digits, spaces, _, . and -")
			}
		case "output":
			ref := ps.str(v, where)
			step, key, ok := strings.Cut(ref, ".")
			if !ok || step == "" || key == "" || strings.ContainsAny(key, " \t") {
				ps.add(v, where, "write the step and the output's key, for example: output: build.version")
				return
			}
			in.Output = &OutputRef{Step: step, Key: key}
		case "artifact":
			ref := ps.str(v, where)
			from, name, ok := strings.Cut(ref, "/")
			if !ok || from == "" || name == "" {
				ps.add(v, where, "write the step and the artifact's name, for example: artifact: changes/commit-log")
				return
			}
			in.From, in.Artifact = from, name
		case "file":
			in.File = ps.str(v, where)
			if !cleanRelPath(in.File) {
				ps.add(v, where, "%q must be a path inside the artifact, without .. or a leading /", in.File)
			}
		case "max_bytes":
			in.MaxBytes = ps.int(v, where)
			if in.MaxBytes < 1 || in.MaxBytes > 1<<20 {
				ps.add(v, where, "max_bytes is 1 to 1048576")
			}
		}
	})
	if n.Kind != yaml.MappingNode {
		return in
	}
	switch {
	case in.Output == nil && in.Artifact == "":
		ps.add(n, at, "say where the input comes from: output: step.key, or artifact: step/name (with file: for one of its files)")
	case in.Output != nil && in.Artifact != "":
		ps.add(n, at, "an input is an output or an artifact, not both")
	case in.File != "" && in.Artifact == "":
		ps.add(n, at+".file", "file names a file of an artifact; give artifact: step/name too")
	}
	if in.Label == "" {
		switch {
		case in.Output != nil:
			in.Label = in.Output.Step + "." + in.Output.Key
		case in.Artifact != "":
			in.Label = in.From + "/" + in.Artifact
			if in.File != "" {
				in.Label += "/" + in.File
			}
		}
	}
	if in.MaxBytes == 0 {
		in.MaxBytes = 64 << 10
	}
	return in
}

// checkAI validates what ai steps say about other steps.
func (ps *parser) checkAI(p *Pipeline) {
	for _, s := range p.Steps {
		if !s.IsAI() || s.AI == nil {
			continue
		}
		at := "steps." + s.ID
		for _, in := range s.AI.Inputs {
			if in.Output == nil {
				continue // artifacts are checked with fetch
			}
			switch {
			case p.Step(in.Output.Step) == nil && p.Group(in.Output.Step) == nil:
				ps.problems = append(ps.problems, Problem{Line: in.Line, Path: at + ".inputs", Message: fmt.Sprintf("%s refers to %s.%s, but there is no step %s",
					s.ID, in.Output.Step, in.Output.Key, in.Output.Step)})
			case !p.Upstream(s.ID)[in.Output.Step] && !contains(s.NeedsGroups, in.Output.Step):
				ps.problems = append(ps.problems, Problem{Line: in.Line, Path: at + ".inputs", Message: fmt.Sprintf("%s refers to %s.%s, but %s is not in this step's needs",
					s.ID, in.Output.Step, in.Output.Key, in.Output.Step)})
			default:
				if known := fixedOutputs(p.Step(in.Output.Step)); known != nil && !contains(known, in.Output.Key) {
					ps.problems = append(ps.problems, Problem{Line: in.Line, Path: at + ".inputs", Message: fmt.Sprintf("%s refers to %s.%s, but that step's outputs are %s",
						s.ID, in.Output.Step, in.Output.Key, strings.Join(known, ", "))})
				}
			}
		}
	}
	// Steps that read an ai step's outputs may read only the ones it sets.
	for _, s := range p.Steps {
		var refs []*Value
		refs = append(refs, deployValues(s)...)
		refs = append(refs, imageValues(s)...)
		refs = append(refs, verifyValues(s)...)
		for _, v := range refs {
			if v.Output == nil {
				continue
			}
			if src := p.Step(v.Output.Step); src != nil && src.IsAI() && src.AI != nil {
				known := append(append([]string{}, AIOutputs...), AISchemaOutputs(src.AI.Schema)...)
				if !contains(known, v.Output.Key) {
					ps.problems = append(ps.problems, Problem{Line: s.Line, Path: "steps." + s.ID,
						Message: fmt.Sprintf("%s refers to %s.%s, but that ai step's outputs are %s", s.ID, v.Output.Step, v.Output.Key, strings.Join(known, ", "))})
				}
			}
		}
	}
}

// fixedOutputs lists the outputs a step of a kind that sets its own outputs
// sets (image, verify and ai steps), or nil for a step whose script may
// write any.
func fixedOutputs(s *Step) []string {
	switch {
	case s == nil:
		return nil
	case s.IsImage():
		return ImageOutputs
	case s.IsVerify():
		return VerifyOutputs
	case s.IsAI() && s.AI != nil:
		return append(append([]string{}, AIOutputs...), AISchemaOutputs(s.AI.Schema)...)
	}
	return nil
}
