package stepkit

import (
	"fmt"
	"strings"
)

// Note is a summary or an annotation, as the server keeps it.
type Note struct {
	Kind  string // summary or annotation
	Level string
	File  string
	Line  int
	Text  string
}

// MaxNotes and MaxNoteBytes bound what is kept per attempt.
const (
	MaxNotes     = 500
	MaxNoteBytes = 128 << 10
)

// Interpretation is what an attempt of a custom step amounts to: the exit
// code as the run counts it, the typed outputs, and what the results said.
// The Station and `launchway step test` both make it with Interpret, so a
// step behaves the same under test as in a run.
type Interpretation struct {
	Code     int    // the exit code as counted: 0 passed (also for "failed but allowed")
	Why      string // when Code is not 0, in words
	Warning  bool   // the step passed with a warning (exit 64)
	Values   map[string]string
	Types    map[string]string
	Notes    []Note
	SARIF    []byte
	JUnit    []byte
	Findings int
	Tests    int
	Masks    []string // values the step asked to hide, and its secret outputs
	Log      []string // lines for the step's log: what was ignored and why
}

// Interpret reads the outputs and results a step wrote, given its exit code
// and the types of the outputs its manifest declares. mask hides the run's
// own secrets in everything recorded; it may be nil. Nothing in it reads
// what the step printed.
func Interpret(name string, outputTypes map[string]string, code int, outData, resData []byte, mask func(string) string) *Interpretation {
	if mask == nil {
		mask = func(s string) string { return s }
	}
	in := &Interpretation{Code: code}
	results := ParseResults(strings.NewReader(string(resData)))
	for _, n := range results.Notes {
		in.Log = append(in.Log, "Step results: "+n)
	}
	outcome := OutcomeOf(code)
	passed := outcome == OutcomePassed || outcome == OutcomeAllowed
	outs, problems := OutputsManifest(name, outputTypes).ReadOutputs(outData, passed)
	in.Values, in.Types = outs.Values, outs.Types
	in.Masks = append(results.Masks(), outs.Secrets...)
	hide := func(s string) string {
		s = mask(s)
		for _, m := range in.Masks {
			s = strings.ReplaceAll(s, m, "***")
		}
		return s
	}
	for k, v := range in.Values {
		in.Values[k] = hide(v)
	}
	for _, p := range problems {
		in.Log = append(in.Log, "Step contract: "+p)
	}
	switch outcome {
	case OutcomePassed, OutcomeAllowed:
		if len(problems) > 0 {
			in.Code, in.Why = ExitFailed, "the step broke the contract: "+problems[0]
		} else if outcome == OutcomeAllowed {
			in.Code, in.Warning = 0, true
			in.Log = append(in.Log, fmt.Sprintf("The step reported a failure it allows (exit %d); the step passes with a warning", code))
			in.Notes = append(in.Notes, Note{Kind: KindSummary, Text: fmt.Sprintf("**Passed with a warning:** the step exited %d, failed but allowed.", code)})
		}
	case OutcomeNeedsPerson:
		why := hide(results.NeedsPerson())
		if why == "" {
			why = "the step did not say why"
		}
		in.Why = "the step needs a person: " + why
	case OutcomeCrashed:
		in.Why = fmt.Sprintf("the step crashed (exit code %d)", code)
	}
	bytes := 0
	for _, it := range results.Items {
		if len(in.Notes) >= MaxNotes {
			in.Log = append(in.Log, fmt.Sprintf("Step results: more than %d summaries and annotations; the rest were not kept", MaxNotes))
			break
		}
		switch it.Kind {
		case KindSummary:
			if bytes += len(it.Markdown); bytes <= MaxNoteBytes {
				in.Notes = append(in.Notes, Note{Kind: KindSummary, Text: hide(it.Markdown)})
			}
		case KindAnnotation:
			in.Notes = append(in.Notes, Note{Kind: KindAnnotation, Level: it.Level, File: it.File, Line: it.Line, Text: hide(it.Message)})
		}
	}
	masked := func(rs []Result) []Result {
		out := make([]Result, len(rs))
		for i, x := range rs {
			x.Message = hide(x.Message)
			out[i] = x
		}
		return out
	}
	f, t := results.Of(KindFinding), results.Of(KindTest)
	in.Findings, in.Tests = len(f), len(t)
	in.SARIF, in.JUnit = SARIF(name, masked(f)), JUnit(name, masked(t))
	return in
}
