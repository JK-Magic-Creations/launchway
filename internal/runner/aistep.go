package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// AI steps on the runner. Nothing runs in a container: the runner reads the
// model's key from its own secret stores (the store's allow-lists apply, as
// for any secret), puts the step's instructions, prompt and inputs
// together, masks known secrets and redacts what looks like one, and asks
// the model. The answer is untrusted text: it becomes the step's outputs and
// its artifact "response", and nothing runs it.

// aiOutDir is where an ai step's answer is written for keeping.
func aiOutDir(r *Runner, job *engine.Job) string {
	return filepath.Join(r.DataDir, "workspaces", "ai-"+job.LeaseID)
}

// runAI runs an ai step attempt.
func (r *Runner) runAI(ctx context.Context, job *engine.Job, sys func(string, ...any), out executor.Sink) (engine.Result, func(string) collected) {
	a := job.AI
	m := a.Model.Model
	where := m.Provider
	if m.Key == nil {
		where += ", no key"
	}
	sys("Runner %s asks model %s (%s %s, %s)", r.Info.Name, m.Name, m.Provider, m.Model, where)
	if job.Attempt > 1 {
		sys("Attempt %d", job.Attempt)
	}
	if r.AI == nil {
		return engine.Result{Outcome: "lost", Error: "this runner cannot call models"}, nil
	}
	work := aiOutDir(r, job)
	_ = os.RemoveAll(work)
	inDir, outDir := filepath.Join(work, "in"), filepath.Join(work, "out")
	for _, d := range []string{inDir, outDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return engine.Result{Outcome: "lost", Error: err.Error()}, nil
		}
	}
	key := ""
	if m.Key != nil {
		v, err := r.lookup(ctx, job.Project, *m.Key, sys)
		if err != nil {
			_ = os.RemoveAll(work)
			msg := fmt.Sprintf("reading model %s's key %s: %v", m.Name, m.Key.String(), err)
			sys("%s", msg)
			return engine.Result{Outcome: "failed", Error: msg}, nil
		}
		key = v
	}
	mask := verify.MaskWith([]string{key})
	spec := executor.Spec{RunID: job.RunID, Name: "launchway-" + job.LeaseID, Workspace: inDir, MetaDir: filepath.Join(work, "meta"), Mask: mask}
	collectSpec := spec
	collectSpec.Workspace = outDir
	inner := r.collector(job, collectSpec, sys, nil)
	collect := func(outcome string) collected {
		defer os.RemoveAll(work)
		if inner == nil {
			return collected{}
		}
		return inner(outcome)
	}
	// Each artifact goes in a directory of its own, so inputs never mix.
	for i := range job.Fetch {
		job.Fetch[i].Into = job.Fetch[i].From + "/" + job.Fetch[i].Name
	}
	if failed := r.fetch(ctx, job, spec, sys, out); failed != nil {
		_ = os.RemoveAll(work)
		return *failed, nil
	}
	prompt, err := aiPrompt(a, inDir, mask, sys)
	if err != nil {
		sys("%v", err)
		return engine.Result{Outcome: "failed", Error: err.Error()}, collect
	}
	req := ai.Request{System: mask(a.Instructions), Prompt: prompt, MaxTokens: a.Model.MaxTokens, Temperature: a.Temperature}
	if a.Schema != "" {
		req.Schema = json.RawMessage(a.Schema)
	}
	sys("Sending %s to the model (%s of instructions, %d inputs); it may write up to %d tokens",
		sizeText(int64(len(req.Prompt))), sizeText(int64(len(req.System))), len(a.Inputs), req.MaxTokens)
	tctx, cancel := context.WithTimeout(ctx, job.Timeout.D())
	defer cancel()
	start := time.Now()
	res, err := r.AI.Call(tctx, m, key, req)
	took := time.Since(start).Round(100 * time.Millisecond)
	var record *engine.AIRecord
	if res.Usage.Total() > 0 || err == nil {
		record = &engine.AIRecord{Model: m.Name, ModelID: res.Model, Provider: m.Provider, StopReason: res.StopReason,
			Usage: engine.AIUsage{Calls: 1, InputTokens: res.Usage.InputTokens + res.Usage.CacheReadTokens + res.Usage.CacheWriteTokens,
				OutputTokens: res.Usage.OutputTokens, CostUSD: res.CostUSD, DurationMS: took.Milliseconds()}}
		sys("The model used %d input and %d output tokens in %s%s", record.Usage.InputTokens, record.Usage.OutputTokens, took, costText(res.CostUSD))
	}
	if err != nil {
		msg := mask(err.Error())
		sys("%s", msg)
		switch {
		case ctx.Err() != nil:
			return engine.Result{Outcome: "canceled", AI: record}, collect
		case errors.Is(tctx.Err(), context.DeadlineExceeded):
			msg = fmt.Sprintf("timed out after %s: %s", job.Timeout.D(), msg)
		}
		if res.Text != "" {
			_ = os.WriteFile(filepath.Join(outDir, "response.txt"), []byte(mask(res.Text)), 0o600)
		}
		return engine.Result{Outcome: "failed", Error: msg, AI: record}, collect
	}
	text := mask(res.Text)
	if err := os.WriteFile(filepath.Join(outDir, "response.txt"), []byte(text), 0o600); err != nil {
		return engine.Result{Outcome: "lost", Error: err.Error()}, collect
	}
	outputs := map[string]string{"text": text, "model": nonEmpty(res.Model, m.Model),
		"input_tokens": strconv.FormatInt(record.Usage.InputTokens, 10), "output_tokens": strconv.FormatInt(record.Usage.OutputTokens, 10)}
	if len(res.JSON) > 0 {
		js := mask(string(res.JSON))
		if err := os.WriteFile(filepath.Join(outDir, "response.json"), []byte(js), 0o600); err != nil {
			return engine.Result{Outcome: "lost", Error: err.Error()}, collect
		}
		var top map[string]any
		_ = json.Unmarshal([]byte(js), &top)
		for _, k := range pipeline.AISchemaOutputs(a.Schema) {
			switch v := top[k].(type) {
			case string:
				outputs[k] = v
			case bool:
				outputs[k] = strconv.FormatBool(v)
			case float64:
				outputs[k] = strconv.FormatFloat(v, 'f', -1, 64)
			}
		}
	}
	sys("The answer (also kept as the artifact %s):", pipeline.AIResponseArtifact)
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if i == 200 {
			sys("… %d more lines in the artifact", len(lines)-200)
			break
		}
		out("stdout", l)
	}
	sys("Succeeded in %s", took)
	return engine.Result{Outcome: "succeeded", Outputs: outputs, AI: record}, collect
}

func costText(usd float64) string {
	if usd <= 0 {
		return ""
	}
	return fmt.Sprintf(", about $%.4f", usd)
}

// aiPrompt puts the prompt and the step's inputs together. Every input is
// masked and redacted, and cut to its limit, saying so.
func aiPrompt(a *engine.AIJob, inDir string, mask func(string) string, sys func(string, ...any)) (string, error) {
	var b strings.Builder
	b.WriteString(mask(a.Prompt))
	for _, in := range a.Inputs {
		text, cut := in.Text, in.Cut
		if in.Artifact != "" {
			var err error
			text, cut, err = readArtifactInput(inDir, in)
			if err != nil {
				return "", fmt.Errorf("input %s: %w", in.Label, err)
			}
		}
		if len(text) > in.MaxBytes {
			text, cut = clipUTF8(text, in.MaxBytes), true
		}
		text = mask(strings.ToValidUTF8(text, "?"))
		note := ""
		if cut {
			note = fmt.Sprintf(" (cut to its first %s)", sizeText(int64(in.MaxBytes)))
			sys("Input %s is longer than %s; only its start is sent", in.Label, sizeText(int64(in.MaxBytes)))
		}
		fmt.Fprintf(&b, "\n\n<input label=%q%s>\n%s\n</input>", in.Label, note, text)
	}
	return b.String(), nil
}

// readArtifactInput reads one file of a fetched artifact, or all of them,
// from where fetch put them (their paths as kept).
func readArtifactInput(inDir string, in engine.AIJobInput) (string, bool, error) {
	inDir = filepath.Join(inDir, in.From, in.Artifact)
	if in.File != "" {
		p := filepath.Join(inDir, filepath.FromSlash(in.File))
		if _, ok := within(inDir, p); !ok {
			return "", false, fmt.Errorf("%s is outside the artifact", in.File)
		}
		data, err := readCapped(p, in.MaxBytes)
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, fmt.Errorf("artifact %s of %s has no file %s", in.Artifact, in.From, in.File)
		}
		return string(data), len(data) > in.MaxBytes, err
	}
	var files []string
	_ = filepath.WalkDir(inDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	slices.Sort(files)
	var b strings.Builder
	for _, p := range files {
		if b.Len() > in.MaxBytes {
			break
		}
		data, err := readCapped(p, in.MaxBytes)
		if err != nil {
			return "", false, err
		}
		rel, _ := filepath.Rel(inDir, p)
		fmt.Fprintf(&b, "--- %s ---\n%s\n", filepath.ToSlash(rel), data)
	}
	return b.String(), b.Len() > in.MaxBytes, nil
}

func readCapped(p string, n int) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n+1)
	k, err := f.Read(buf)
	for err == nil && k < len(buf) {
		var m int
		m, err = f.Read(buf[k:])
		k += m
	}
	return buf[:k], nil
}

func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
