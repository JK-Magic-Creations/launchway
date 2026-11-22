package runner

// A step that came from a custom step runs under the step contract (see
// internal/stepkit): the Station writes its inputs as a typed JSON document,
// gives it files for outputs, results and artifacts, runs it in the sandbox
// its manifest asked for (and no wider: the permissions are in the run's
// graph, fixed when the run was created), and after it ends reads the outputs
// and results it wrote. Nothing is read from what the step printed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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
	secretEnv := map[string]string{}
	for _, n := range c.Secrets {
		if _, ok := secretValues[stepkit.EnvPart(n)]; ok {
			secretEnv[n] = stepkit.EnvPart(n)
		}
	}
	if err := verifyManifest(ws, c); err != nil {
		return nil, err
	}
	cr := &customRun{job: job, step: c, rel: ioDirFor(job.StepID), inside: r.Exec.WorkspacePath(ws)}
	io, err := stepkit.PrepareIO(ws, cr.rel, cr.inside, c.Name, c.InputTypes, c.Values, secretEnv)
	if err != nil {
		return nil, err
	}
	cr.host = io.Host
	for k, v := range io.Env(c.Name, c.InputTypes, c.Values) {
		env[k] = v
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
	in := stepkit.Interpret(cr.step.Name, cr.step.OutputTypes, code, outData, resData, mask.Mask)
	for _, l := range in.Log {
		sys("%s", l)
	}
	d.code, d.why, d.outputs, d.sarif, d.junit = in.Code, in.Why, in.Values, in.SARIF, in.JUnit
	for _, n := range in.Notes {
		d.notes = append(d.notes, engine.StepNote{Kind: n.Kind, Level: n.Level, File: n.File, Line: n.Line, Text: n.Text})
	}
	if len(d.notes) > 0 || in.Findings > 0 || in.Tests > 0 {
		sys("Step results: %d summaries and annotations, %d findings, %d tests", len(d.notes), in.Findings, in.Tests)
	}
	return d
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

// startCustom prepares a custom step's attempt, if the job is one: the customRun, or a failed result.
func (r *Runner) startCustom(job *engine.Job, ws string, env, values map[string]string, sys func(string, ...any)) (*customRun, *engine.Result) {
	if job.Custom == nil {
		return nil, nil
	}
	cr, err := r.customStart(job, ws, env, values)
	if err != nil {
		sys("Could not prepare the custom step: %v", err)
		return nil, &engine.Result{Outcome: "failed", Cause: pipeline.CauseInfrastructure, Error: "preparing the custom step: " + err.Error()}
	}
	cr.retarget(job)
	return cr, nil
}

// settle says how a custom step's attempt counts and what it gave, after it ran: the outputs, notes, reason and
// exit code to report. For any other step (cr nil) it returns what it was given.
func (cr *customRun) settle(ctx context.Context, r *Runner, spec executor.Spec, code int, mask *secrets.Masker, sys func(string, ...any), outputs map[string]string) (map[string]string, []engine.StepNote, string, int) {
	if cr == nil {
		return outputs, nil, "", code
	}
	d := cr.finish(ctx, r, spec, code, mask, sys)
	return d.outputs, d.notes, d.why, d.code
}

// verifyManifest checks that the manifest in the workspace is the one the Sortie was made with: what a step
// may do was read from it when the Sortie was created, and it is not run if its files are not those.
func verifyManifest(ws string, c *pipeline.CustomStep) error {
	rel := stepkit.ManifestPath(c.Name)
	if c.Library != pipeline.SelfLibrary {
		rel = LibDir + "/" + c.Library + "/" + rel
	}
	path, err := stepkit.SafeJoin(ws, rel)
	if err != nil {
		return fmt.Errorf("the step's manifest: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("the step's manifest %s is not in the workspace: %w", rel, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != c.Digest {
		return fmt.Errorf("%s is not the manifest this Sortie was made with (what the step may do was read from that one); not running it", rel)
	}
	return nil
}
