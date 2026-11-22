package runner

// A step that came from a custom step runs under the step contract (see
// internal/stepkit): the Station writes its inputs as a typed JSON document,
// gives it files for outputs, results and artifacts, runs it in the sandbox
// its manifest asked for (and no wider: the permissions are in the run's
// graph, fixed when the run was created), and after it ends reads the outputs
// and results it wrote. Nothing is read from what the step printed.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
)

// customRun is one attempt of a custom step.
type customRun struct {
	job    *engine.Job
	step   *pipeline.CustomStep
	rel    string // the contract directory, relative to the workspace
	host   string // the same on this machine
	inside string // the workspace as the step sees it
	// Done is what finish worked out; collect adds it to the reports.
	done *customDone
}

// customDone is what a custom step's own files said.
type customDone struct {
	code    int
	why     string
	outputs map[string]string
	notes   []engine.StepNote
	sarif   []byte
	junit   []byte
	masks   []string
}

func ioDirFor(stepID string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.ToLower(stepID))
	return stepkit.IODir + "/" + safe
}

// customStart prepares a custom step's attempt: it checks that what the step
// is given is what its manifest names, writes the inputs, and makes the
// directories the step writes to. env gets the contract's variables.
func (r *Runner) customStart(job *engine.Job, ws string, env map[string]string, secretValues map[string]string) (*customRun, error) {
	c := job.Custom
	if c.Contract > stepkit.ContractVersion || c.Contract < stepkit.MinContract {
		return nil, fmt.Errorf("the step %s speaks contract %d; this Station speaks %d to %d", c.Name, c.Contract, stepkit.MinContract, stepkit.ContractVersion)
	}
	// A step is given the secrets its manifest names, and no others, whatever the graph says.
	allowed := map[string]bool{}
	for _, s := range c.Secrets {
		allowed[stepkit.EnvPart(s)] = true
	}
	for name := range job.Secrets {
		if !allowed[name] {
			return nil, fmt.Errorf("the step %s was given the secret %s, which its manifest does not name; refusing to run it", c.Name, name)
		}
	}
	cr := &customRun{job: job, step: c, rel: ioDirFor(job.StepID), inside: r.Exec.WorkspacePath(ws)}
	var err error
	if cr.host, err = stepkit.SafeJoin(ws, cr.rel); err != nil {
		return nil, fmt.Errorf("preparing the step's directory: %w", err)
	}
	if err := os.RemoveAll(cr.host); err != nil {
		return nil, err
	}
	for _, d := range []string{"", stepkit.ArtifactsDir, stepkit.ReportsDir} {
		p := filepath.Join(cr.host, d)
		if err := os.MkdirAll(p, 0o777); err != nil {
			return nil, err
		}
		_ = os.Chmod(p, 0o777)
	}
	secretEnv := map[string]string{}
	for _, n := range c.Secrets {
		if _, ok := secretValues[stepkit.EnvPart(n)]; ok {
			secretEnv[n] = stepkit.EnvPart(n)
		}
	}
	m := &stepkit.Manifest{Name: c.Name}
	for _, k := range stepkit.SortedKeys(c.InputTypes) {
		m.Inputs = append(m.Inputs, &stepkit.Input{Name: k, Type: c.InputTypes[k]})
	}
	doc, err := m.BuildInputs(c.Values, secretEnv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(cr.host, stepkit.InputsFile), doc, 0o644); err != nil {
		return nil, err
	}
	for _, f := range []string{stepkit.OutputsFile, stepkit.ResultsFile} {
		p := filepath.Join(cr.host, f)
		if err := os.WriteFile(p, nil, 0o666); err != nil {
			return nil, err
		}
		_ = os.Chmod(p, 0o666)
	}
	in := cr.inside + "/" + cr.rel
	env[stepkit.EnvContract] = fmt.Sprint(stepkit.ContractVersion)
	env[stepkit.EnvName] = c.Name
	env[stepkit.EnvInputs] = in + "/" + stepkit.InputsFile
	env[stepkit.EnvOutputs] = in + "/" + stepkit.OutputsFile
	env[stepkit.EnvResults] = in + "/" + stepkit.ResultsFile
	env[stepkit.EnvArtifacts] = in + "/" + stepkit.ArtifactsDir
	env[stepkit.EnvReports] = in + "/" + stepkit.ReportsDir
	for _, k := range stepkit.SortedKeys(c.Values) {
		if t := c.InputTypes[k]; t == stepkit.TypeString || t == stepkit.TypeNumber || t == stepkit.TypeBoolean {
			env[stepkit.EnvWithPrefix+stepkit.EnvPart(k)] = c.Values[k]
		}
	}
	return cr, nil
}

// sandbox is the spec's Sandbox for the step.
func (cr *customRun) sandbox() *executor.Sandbox {
	return &executor.Sandbox{Name: cr.step.Name, Network: cr.step.Network, Write: cr.step.Write, IODir: cr.rel}
}

// retarget points the step's report and artifact globs at its own directory,
// so that two custom steps that share a workspace never read each other's files.
func (cr *customRun) retarget(job *engine.Job) {
	if job.Reports != nil {
		if len(job.Reports.Tests) > 0 {
			job.Reports.Tests = []string{cr.rel + "/" + stepkit.ReportsDir + "/**/*.xml"}
		}
		if len(job.Reports.Findings) > 0 {
			job.Reports.Findings = []string{cr.rel + "/" + stepkit.ReportsDir + "/**/*.sarif"}
		}
	}
	if job.Artifacts != nil {
		for i := range job.Artifacts.Keep {
			if job.Artifacts.Keep[i].Name == stepkit.ArtifactName {
				job.Artifacts.Keep[i].Paths = []string{cr.rel + "/" + stepkit.ArtifactsDir + "/**/*"}
			}
		}
	}
}

// finish reads what the step wrote and says how the attempt counts. code is
// the exit code; the result's code and why replace it.
func (cr *customRun) finish(ctx context.Context, r *Runner, spec executor.Spec, code int, mask *secrets.Masker, sys func(string, ...any)) *customDone {
	d := &customDone{code: code}
	cr.done = d
	got, err := executor.Collect(ctx, r.execFor(cr.job), spec,
		[]string{cr.rel + "/" + stepkit.OutputsFile, cr.rel + "/" + stepkit.ResultsFile},
		executor.CollectLimits{MaxFiles: 4, MaxFileBytes: stepkit.MaxResultsBytes, MaxBytes: 2 * stepkit.MaxResultsBytes})
	if err != nil {
		sys("Could not read what the step wrote: %v", err)
	}
	var outData, resData []byte
	if got != nil {
		for _, f := range got.Files {
			switch f.Path {
			case cr.rel + "/" + stepkit.OutputsFile:
				outData = f.Data
			case cr.rel + "/" + stepkit.ResultsFile:
				resData = f.Data
			}
		}
	}
	results := stepkit.ParseResults(strings.NewReader(string(resData)))
	for _, n := range results.Notes {
		sys("Step results: %s", n)
	}
	outcome := stepkit.OutcomeOf(code)
	om := stepkit.OutputsManifest(cr.step.Name, cr.step.OutputTypes)
	outs, problems := om.ReadOutputs(outData, outcome == stepkit.OutcomePassed || outcome == stepkit.OutcomeAllowed)
	d.outputs = outs.Values
	// Everything recorded from here on hides what the step asked to hide, and its secret outputs.
	hide := append(results.Masks(), outs.Secrets...)
	d.masks = hide
	masker := mask
	if len(hide) > 0 {
		extra := map[string]string{}
		for i, h := range hide {
			extra[fmt.Sprint("mask", i)] = h
		}
		masker, _ = secrets.NewMasker(extra)
		if mask != nil {
			// both: the run's secrets, and the step's
			masker = chain(mask, masker)
		}
	}
	for k, v := range d.outputs {
		d.outputs[k] = masker.Mask(v)
	}
	switch outcome {
	case stepkit.OutcomePassed, stepkit.OutcomeAllowed:
		if len(problems) > 0 {
			d.code, d.why = 1, "the step broke the contract: "+problems[0]
			for _, p := range problems {
				sys("Step contract: %s", p)
			}
		}
		if outcome == stepkit.OutcomeAllowed && d.code == stepkit.ExitFailedAllowed {
			sys("The step reported a failure it allows (exit %d); the step passes with a warning", code)
			d.code = 0
			d.notes = append(d.notes, engine.StepNote{Kind: engine.NoteSummary, Text: "**Passed with a warning:** the step exited " + fmt.Sprint(code) + ", failed but allowed."})
		}
	case stepkit.OutcomeNeedsPerson:
		why := masker.Mask(results.NeedsPerson())
		if why == "" {
			why = "the step did not say why"
		}
		d.code, d.why = code, "the step needs a person: "+why
	case stepkit.OutcomeCrashed:
		d.why = fmt.Sprintf("the step crashed (exit code %d)", code)
	default:
		for _, p := range problems {
			sys("Step contract: %s", p)
		}
	}
	// Summaries and annotations, as the server keeps them.
	var summaryBytes int
	for _, it := range results.Items {
		if len(d.notes) >= engine.MaxStepNotes {
			sys("Step results: more than %d summaries and annotations; the rest were not kept", engine.MaxStepNotes)
			break
		}
		switch it.Kind {
		case stepkit.KindSummary:
			if summaryBytes += len(it.Markdown); summaryBytes <= engine.MaxStepNoteBytes {
				d.notes = append(d.notes, engine.StepNote{Kind: engine.NoteSummary, Text: masker.Mask(it.Markdown)})
			}
		case stepkit.KindAnnotation:
			d.notes = append(d.notes, engine.StepNote{Kind: engine.NoteAnnotation, Level: it.Level, File: it.File, Line: it.Line, Text: masker.Mask(it.Message)})
		}
	}
	mk := func(rs []stepkit.Result) []stepkit.Result {
		out := make([]stepkit.Result, len(rs))
		for i, x := range rs {
			x.Message = masker.Mask(x.Message)
			out[i] = x
		}
		return out
	}
	d.sarif = stepkit.SARIF(cr.step.Name, mk(results.Of(stepkit.KindFinding)))
	d.junit = stepkit.JUnit(cr.step.Name, mk(results.Of(stepkit.KindTest)))
	if len(d.notes) > 0 || d.sarif != nil || d.junit != nil {
		sys("Step results: %d summaries and annotations, %d findings, %d tests", len(d.notes), len(results.Of(stepkit.KindFinding)), len(results.Of(stepkit.KindTest)))
	}
	return d
}

// chain masks with two maskers, one after the other.
func chain(a, b *secrets.Masker) *secrets.Masker {
	return secrets.Chain(a, b)
}

// withCustom wraps a step's report collector so that the findings and test
// results the step wrote through the protocol are sent with the files the step
// wrote itself.
func withCustom(inner func(string) collected, cr *customRun) func(string) collected {
	return func(outcome string) collected {
		var c collected
		if inner != nil {
			c = inner(outcome)
		}
		d := cr.done
		if d == nil {
			return c
		}
		c.findings = addReport(c.findings, cr.rel+"/"+stepkit.ReportsDir+"/protocol.sarif", d.sarif)
		c.tests = addReport(c.tests, cr.rel+"/"+stepkit.ReportsDir+"/protocol.xml", d.junit)
		return c
	}
}

func addReport(rep *engine.CollectedReports, path string, data []byte) *engine.CollectedReports {
	if len(data) == 0 {
		return rep
	}
	if rep == nil {
		rep = &engine.CollectedReports{Files: []engine.ReportFile{}}
	}
	if slices.ContainsFunc(rep.Files, func(f engine.ReportFile) bool { return f.Path == path }) {
		return rep
	}
	rep.Files = append(rep.Files, engine.ReportFile{Path: path, Data: data, Size: int64(len(data))})
	return rep
}
