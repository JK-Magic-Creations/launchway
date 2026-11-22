// Package steptest runs a custom step the way a Station would, on one's own
// machine, and checks that it keeps to the step contract. It is what
// `launchway step test` does and what the kits are tested with.
package steptest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
)

// Options say what to run and with what.
type Options struct {
	// Dir is the step's directory (it holds step.yaml). Its parent's parent is
	// the workspace unless Workspace is set.
	Dir string
	// Workspace is the repository root the step works in; it must hold
	// steps/<name>.
	Workspace string
	// With are the values of the inputs, as `with:` would give them. Secrets
	// are given as name=value in Secrets (the value is masked, never printed).
	With    map[string]string
	Secrets map[string]string
	// Executor runs the step; nil: the one a Station would pick (Podman or
	// Docker if there is one, else the host, which does not sandbox).
	Executor executor.Executor
	// Timeout of the run; default 10 minutes.
	Timeout time.Duration
	// Log, if set, receives the step's output and the checks as they happen.
	Log io.Writer
}

// Report is what a test run found.
type Report struct {
	Manifest  *stepkit.Manifest
	Executor  string
	Sandboxed bool
	ExitCode  int
	Outcome   stepkit.Outcome
	Result    *stepkit.Interpretation
	Stdout    string
	// Checks are the conformance checks that failed or warned.
	Problems []string
	Warnings []string
	Took     time.Duration
}

// OK reports whether the step kept to the contract and passed.
func (r *Report) OK() bool { return len(r.Problems) == 0 && r.Result != nil && r.Result.Code == 0 }

// Conforms reports whether the step kept to the contract, whatever its exit code was.
func (r *Report) Conforms() bool { return len(r.Problems) == 0 }

// ReadManifest reads and checks the manifest of the step in dir.
func ReadManifest(dir string) (*stepkit.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "step.yaml"))
	if err != nil {
		return nil, fmt.Errorf("%s has no step.yaml: %w", dir, err)
	}
	m, probs := stepkit.ParseManifest(data)
	if len(probs) > 0 {
		return nil, fmt.Errorf("%s/step.yaml:\n%s", dir, probs.Error())
	}
	if filepath.Base(dir) != m.Name {
		return nil, fmt.Errorf("%s/step.yaml is the step %s, but its directory is called %s: the directory is steps/%s", dir, m.Name, filepath.Base(dir), m.Name)
	}
	return m, nil
}

// Run runs the step once and checks it.
func Run(ctx context.Context, o Options) (*Report, error) {
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	m, err := ReadManifest(dir)
	if err != nil {
		return nil, err
	}
	ws := o.Workspace
	if ws == "" {
		ws = filepath.Dir(filepath.Dir(dir))
	}
	if ws, err = filepath.Abs(ws); err != nil {
		return nil, err
	}
	if st, err := os.Stat(filepath.Join(ws, stepkit.StepDir, m.Name)); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("the workspace %s has no %s/%s: a step lives in steps/<name> of a repository or a library", ws, stepkit.StepDir, m.Name)
	}
	if problems := m.CheckWith(o.With); len(problems) > 0 {
		var lines []string
		for _, k := range stepkit.SortedKeys(problems) {
			lines = append(lines, problems[k])
		}
		return nil, errors.New(strings.Join(lines, "\n"))
	}
	values := map[string]string{}
	for _, in := range m.Inputs {
		if v, ok := o.With[in.Name]; ok {
			if in.Type == stepkit.TypeList {
				items, _ := stepkit.ParseJSONList(v)
				v = stepkit.JSONList(items)
			}
			values[in.Name] = v
		} else if in.Default != nil {
			values[in.Name] = *in.Default
		}
	}
	ex := o.Executor
	if ex == nil {
		ex = executor.Auto()
		if h, ok := ex.(*executor.Host); ok {
			h.AllowUnsandboxed = true
		}
	}
	rel := stepkit.IODir + "/test"
	secretValues := map[string]string{}
	secretEnv := map[string]string{}
	for _, n := range m.SecretInputs() {
		v, ok := o.Secrets[n]
		if !ok {
			return nil, fmt.Errorf("the step takes the secret %s: give its value with --secret %s=VALUE (it is masked in everything shown)", n, n)
		}
		secretValues[stepkit.EnvPart(n)] = v
		secretEnv[n] = stepkit.EnvPart(n)
	}
	types := map[string]string{}
	for _, in := range m.Inputs {
		types[in.Name] = in.Type
	}
	io, err := stepkit.PrepareIO(ws, rel, ex.WorkspacePath(ws), m.Name, types, values, secretEnv)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(io.Host)
	env := io.Env(m.Name, types, values)
	for k, v := range secretValues {
		env[k] = v
	}
	env["PYTHONUNBUFFERED"] = "1"
	mask, _ := secrets.NewMasker(secretValues)
	meta, err := os.MkdirTemp("", "launchway-step-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(meta)
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rep := &Report{Manifest: m, Executor: ex.Name()}
	var mu sync.Mutex
	var stdout bytes.Buffer
	sink := func(stream, text string) {
		mu.Lock()
		defer mu.Unlock()
		text = mask.Mask(text)
		if stream == "stdout" || stream == "stderr" {
			stdout.WriteString(text + "\n")
		}
		if o.Log != nil {
			fmt.Fprintf(o.Log, "  %s\n", text)
		}
	}
	spec := executor.Spec{
		RunID: "step-test", StepID: m.Name, Attempt: 1, LeaseID: "test", Name: "launchway-step-test-" + hash(ws + m.Name)[:10],
		Containers: []executor.ContainerSpec{{Name: "step", Image: m.Image}}, Container: "step", Image: m.Image,
		Script: m.Script(true), Env: env, Workspace: ws, WorkspaceID: "test", MetaDir: meta, Mask: mask.Mask, Hold: mask.Hold,
		Sandbox: &executor.Sandbox{Name: m.Name, Network: m.Permissions.Network, Write: m.Permissions.Write, IODir: rel},
	}
	if _, isHost := ex.(*executor.Host); !isHost {
		rep.Sandboxed = true
	}
	before := snapshot(ws, rel)
	start := time.Now()
	code, err := ex.Run(rctx, spec, sink)
	rep.Took = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("could not run the step: %w", err)
	}
	rep.ExitCode, rep.Outcome = code, stepkit.OutcomeOf(code)
	rep.Stdout = stdout.String()
	outData, _ := os.ReadFile(filepath.Join(io.Host, stepkit.OutputsFile))
	resData, _ := os.ReadFile(filepath.Join(io.Host, stepkit.ResultsFile))
	outTypes := map[string]string{}
	for _, o := range m.Outputs {
		outTypes[o.Name] = o.Type
	}
	rep.Result = stepkit.Interpret(m.Name, outTypes, code, outData, resData, mask.Mask)
	rep.check(m, before, snapshot(ws, rel))
	return rep, nil
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// check makes the conformance checks.
func (r *Report) check(m *stepkit.Manifest, before, after map[string]string) {
	for _, l := range r.Result.Log {
		if strings.HasPrefix(l, "Step contract: ") {
			r.Problems = append(r.Problems, strings.TrimPrefix(l, "Step contract: "))
		} else if strings.HasPrefix(l, "Step results: ") {
			r.Warnings = append(r.Warnings, strings.TrimPrefix(l, "Step results: "))
		}
	}
	if r.Outcome == stepkit.OutcomeCrashed {
		r.Problems = append(r.Problems, fmt.Sprintf("the step exited with %d, which the contract reserves: use 0 (passed), 1 (failed), 64 (failed but allowed) or 65 (needs a person)", r.ExitCode))
	}
	if r.Outcome == stepkit.OutcomeNeedsPerson && !strings.Contains(r.Result.Why, ":") {
		r.Problems = append(r.Problems, "exit 65 (needs a person) should come with a needs_person result saying why")
	}
	// A result written to the output instead of the results file is ignored by a Station.
	for _, line := range strings.Split(r.Stdout, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, `{"t":`) || strings.HasPrefix(t, `{"t" :`) {
			r.Warnings = append(r.Warnings, "a line of the step's output looks like a result; results are read only from $"+stepkit.EnvResults+" and this one is ignored: "+clip(t, 80))
			break
		}
	}
	// Writes outside what the manifest lists. Under a sandbox they cannot happen; on the host
	// they are found by comparing the workspace before and after.
	var changed []string
	for p, h := range after {
		if before[p] != h {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	var outside []string
	for _, p := range changed {
		if !under(p, m.Permissions.Write) {
			outside = append(outside, p)
		}
	}
	if len(outside) > 0 && !r.Sandboxed {
		r.Problems = append(r.Problems, "the step changed files its manifest does not list under permissions.write: "+strings.Join(first(outside, 5), ", ")+
			" (a Station would not let it: the workspace is read-only except what is listed)")
	}
}

func first(l []string, n int) []string {
	if len(l) > n {
		return append(append([]string{}, l[:n]...), fmt.Sprintf("and %d more", len(l)-n))
	}
	return l
}

func under(p string, allowed []string) bool {
	for _, a := range allowed {
		if a == "." || p == a || strings.HasPrefix(p, a+"/") {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// snapshot hashes the workspace's files (not .git, not Launchway's own directory,
// not the contract directory), by relative path, to find what a step changed.
func snapshot(ws, skip string) map[string]string {
	out := map[string]string{}
	// It only reads, in the developer's own checkout, to find what a step changed.
	_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error { //nolint:gosec // G122: a snapshot for comparison; nothing is written or followed out of the workspace
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || rel == ".launchway" || rel == skip):
			return filepath.SkipDir
		case d.IsDir():
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
			out[rel] = fmt.Sprint(info)
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil {
			out[rel] = hash(string(b))
		}
		return nil
	})
	return out
}
