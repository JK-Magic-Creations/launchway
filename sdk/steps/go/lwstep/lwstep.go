// Package lwstep is the Go kit for Launchway custom steps: a small wrapper of
// the step contract (docs/reference/step-contract.md) that reads the inputs,
// writes the outputs, and adds results (a summary, an annotation, a finding, a
// test result) through the results file, never through the output. It has no
// dependency but the standard library, and is meant to be copied into a step
// (`launchway step new --lang go` does) or imported.
//
//	func main() {
//		lwstep.Run(func(s *lwstep.Step) error {
//			who := s.String("who")
//			s.Output("greeting", "Hello, "+who)
//			s.Summary("Greeted **" + who + "**")
//			return nil
//		})
//	}
//
// A step that returns an error fails (exit 1) with the message on stderr;
// ErrAllowed makes it "failed but allowed" (64), NeedsPerson "needs a person" (65).
package lwstep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Contract is the contract version the kit speaks.
const Contract = 1

// ErrAllowed, returned (or wrapped) by the step's function, ends the step as "failed but allowed".
var ErrAllowed = errors.New("failed but allowed")

type needsPerson struct{ msg string }

func (e needsPerson) Error() string { return "needs a person: " + e.msg }

// NeedsPerson returns an error that ends the step as "needs a person" with the question to ask.
func NeedsPerson(question string) error { return needsPerson{question} }

// Step is a running step.
type Step struct {
	inputs  map[string]json.RawMessage
	outputs map[string]any
	order   []string
	results *os.File
	outPath string
}

// Open reads the contract's environment and the inputs. It fails when the
// step is not being run by a Station (or by `launchway step test`).
func Open() (*Step, error) {
	path := os.Getenv("LAUNCHWAY_STEP_INPUTS")
	if path == "" {
		return nil, errors.New("LAUNCHWAY_STEP_INPUTS is not set: run this step with `launchway step test`, or in a pipeline")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Contract int                        `json:"contract"`
		Inputs   map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("the inputs file is not the contract's document: %w", err)
	}
	if doc.Contract > Contract {
		return nil, fmt.Errorf("the Station speaks contract %d; this kit speaks %d: update the kit", doc.Contract, Contract)
	}
	s := &Step{inputs: doc.Inputs, outputs: map[string]any{}, outPath: os.Getenv("LAUNCHWAY_STEP_OUTPUTS")}
	if p := os.Getenv("LAUNCHWAY_STEP_RESULTS"); p != "" {
		if s.results, err = os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Run opens the step, runs fn, writes the outputs and exits with the code the
// result means. It does not return.
func Run(fn func(s *Step) error) {
	s, err := Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "launchway step:", err)
		os.Exit(1)
	}
	os.Exit(s.finish(fn(s)))
}

func (s *Step) finish(err error) int {
	if werr := s.WriteOutputs(); werr != nil {
		fmt.Fprintln(os.Stderr, "launchway step: writing the outputs:", werr)
		return 1
	}
	var np needsPerson
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrAllowed):
		fmt.Fprintln(os.Stderr, err)
		return 64
	case errors.As(err, &np):
		s.result(map[string]any{"t": "needs_person", "message": np.msg})
		fmt.Fprintln(os.Stderr, err)
		return 65
	default:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
}

// Has reports whether an input was given.
func (s *Step) Has(name string) bool { _, ok := s.inputs[name]; return ok }

func (s *Step) raw(name string) json.RawMessage {
	r, ok := s.inputs[name]
	if !ok {
		panic(fmt.Sprintf("the step has no input %q (is it in step.yaml?)", name))
	}
	return r
}

// String returns a string input ("" if an optional one was not given).
func (s *Step) String(name string) string {
	if !s.Has(name) {
		return ""
	}
	var v string
	if err := json.Unmarshal(s.raw(name), &v); err != nil {
		panic(fmt.Sprintf("input %s is not a string", name))
	}
	return v
}

// Number returns a number input.
func (s *Step) Number(name string) float64 {
	if !s.Has(name) {
		return 0
	}
	var v float64
	if err := json.Unmarshal(s.raw(name), &v); err != nil {
		panic(fmt.Sprintf("input %s is not a number", name))
	}
	return v
}

// Bool returns a boolean input.
func (s *Step) Bool(name string) bool {
	if !s.Has(name) {
		return false
	}
	var v bool
	if err := json.Unmarshal(s.raw(name), &v); err != nil {
		panic(fmt.Sprintf("input %s is not a boolean", name))
	}
	return v
}

// List returns a list input.
func (s *Step) List(name string) []string {
	if !s.Has(name) {
		return nil
	}
	var v []string
	if err := json.Unmarshal(s.raw(name), &v); err != nil {
		panic(fmt.Sprintf("input %s is not a list of strings", name))
	}
	return v
}

// Record returns a record input.
func (s *Step) Record(name string) map[string]any {
	if !s.Has(name) {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal(s.raw(name), &v); err != nil {
		panic(fmt.Sprintf("input %s is not a record", name))
	}
	return v
}

// Secret returns the value of a secret input, which the Station put in an
// environment variable. It is masked in the log; do not write it to an output
// unless that output is of type secret.
func (s *Step) Secret(name string) string {
	var ref struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(s.raw(name), &ref); err != nil || ref.Secret == "" {
		panic(fmt.Sprintf("input %s is not a secret reference", name))
	}
	return os.Getenv(ref.Secret)
}

// Output sets an output; its type is the one step.yaml declares. It is
// written when the step ends (Run does it).
func (s *Step) Output(name string, value any) {
	if _, seen := s.outputs[name]; !seen {
		s.order = append(s.order, name)
	}
	s.outputs[name] = value
}

// SecretOutput sets an output of type secret and asks for its value to be hidden from now on.
func (s *Step) SecretOutput(name, value string) {
	s.Mask(value)
	s.Output(name, value)
}

// WriteOutputs writes the outputs document. Run calls it; a step that does not use Run calls it before it exits.
func (s *Step) WriteOutputs() error {
	if s.outPath == "" || len(s.outputs) == 0 {
		return nil
	}
	b, err := json.Marshal(s.outputs)
	if err != nil {
		return err
	}
	return os.WriteFile(s.outPath, b, 0o666)
}

func (s *Step) result(m map[string]any) {
	if s.results == nil {
		return
	}
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(m) // one line
	_, _ = s.results.Write(b.Bytes())
}

// Summary adds a Markdown summary, shown on the step's page.
func (s *Step) Summary(markdown string) {
	s.result(map[string]any{"t": "summary", "markdown": markdown})
}

// Annotate adds a note on a line of a file of the workspace; level is note, warning or error.
func (s *Step) Annotate(level, file string, line int, message string) {
	s.result(map[string]any{"t": "annotation", "level": level, "file": file, "line": line, "message": message})
}

// Finding adds a finding to the run's findings; severity is critical, high, medium or low.
func (s *Step) Finding(rule, severity, file string, line int, message string) {
	s.result(map[string]any{"t": "finding", "rule": rule, "severity": severity, "file": file, "line": line, "message": message})
}

// Test adds a test result; status is passed, failed or skipped.
func (s *Step) Test(name, status string, millis int64, message string) {
	s.result(map[string]any{"t": "test", "name": name, "status": status, "duration_ms": millis, "message": message})
}

// Mask asks for a value to be hidden in the log from now on and in everything Launchway keeps.
func (s *Step) Mask(value string) { s.result(map[string]any{"t": "mask", "value": value}) }

// Environ is os.Environ without the variables that let a program write results
// and outputs. Use it for the programs the step starts (a build tool, a
// scanner), so that nothing they run can forge a result: cmd.Env = lwstep.Environ().
func Environ() []string {
	var out []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "LAUNCHWAY_STEP_RESULTS=") || strings.HasPrefix(e, "LAUNCHWAY_STEP_OUTPUTS=") {
			continue
		}
		out = append(out, e)
	}
	return out
}
