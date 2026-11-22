package main

// launchway step new|test|validate: write a custom step, run it the way a
// Station would, and check its manifest. Everything here works on files; no
// server is needed.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
	"github.com/jk-magic-creations/launchway/internal/steptest"
)

const stepUsage = `Usage:
  launchway step new --lang go|python|ts|sh [--name NAME] [--dir REPOSITORY]
                                      scaffold steps/NAME: manifest, kit, code, test and README
  launchway step test [DIR] [--with NAME=VALUE]... [--inputs FILE] [--secret NAME=VALUE]...
                      [--executor auto|podman|docker|host] [--json]
                                      run the step as a Station would and check that it keeps to the step contract
  launchway step validate [DIR...] [--against GITREF]
                                      check manifests; show what each step may do; with --against, what changed since a version

A step lives in steps/NAME of a repository or a library (steps/NAME/step.yaml and its code). A pipeline uses
it as 'uses: ./steps/NAME' (the same repository) or 'uses: LIBRARY/NAME'.
`

func cmdStep(args []string) error {
	if len(args) == 0 {
		fmt.Print(stepUsage)
		return exitErr(2)
	}
	switch args[0] {
	case "new":
		return stepNew(args[1:])
	case "test":
		return stepTest(args[1:])
	case "validate":
		return stepValidate(args[1:])
	}
	fmt.Print(stepUsage)
	return exitErr(2)
}

func stepNew(args []string) error {
	fs := flag.NewFlagSet("step new", flag.ContinueOnError)
	lang := fs.String("lang", "", "the language: "+strings.Join(steptest.Languages, ", "))
	name := fs.String("name", "", "the step's name (default: my-step)")
	dir := fs.String("dir", ".", "the repository or library to put steps/NAME in")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	if *lang == "" {
		return usageErr("say the language: --lang %s", strings.Join(steptest.Languages, "|"))
	}
	if *name == "" {
		*name = "my-step"
	}
	made, err := scaffoldFor(*dir, *name, *lang)
	if err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n\nNext:\n  launchway step test %s --with who=Ada      run it as a Station would\n  edit %s/step.yaml and the code, then test again\n  uses: ./steps/%s     in a pipeline of this repository (launchway validate checks it)\n",
		made, made, made, *name)
	return nil
}

type kvFlag []string

func (k *kvFlag) String() string     { return strings.Join(*k, ",") }
func (k *kvFlag) Set(v string) error { *k = append(*k, v); return nil }

func parseKV(list kvFlag, what string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range list {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, usageErr("--%s %q: write NAME=VALUE", what, kv)
		}
		out[k] = v
	}
	return out, nil
}

func stepTest(args []string) error {
	fs := flag.NewFlagSet("step test", flag.ContinueOnError)
	var with, secretFlags kvFlag
	fs.Var(&with, "with", "an input value, name=value, as a pipeline's with: gives it (a list: name=a,b,c or a JSON array); give it once for each input")
	fs.Var(&secretFlags, "secret", "the value of a secret input, name=value, or name=env:VARIABLE to read it from your environment (never printed)")
	inputsFile := fs.String("inputs", "", "a JSON file of input values (an object of name: value), as the step would receive them")
	exe := fs.String("executor", "auto", "auto (a container if Podman or Docker is there, else this machine), podman, docker or host")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	values, err := parseKV(with, "with")
	if err != nil {
		return err
	}
	if *inputsFile != "" {
		b, err := os.ReadFile(*inputsFile)
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s is not a JSON object: %w", *inputsFile, err)
		}
		for k, v := range doc {
			if _, given := values[k]; given {
				continue
			}
			switch x := v.(type) {
			case string:
				values[k] = x
			default:
				enc, _ := json.Marshal(x)
				values[k] = string(enc)
			}
		}
	}
	secretValues, err := parseKV(secretFlags, "secret")
	if err != nil {
		return err
	}
	for k, v := range secretValues {
		if name, ok := strings.CutPrefix(v, "env:"); ok {
			secretValues[k] = os.Getenv(name)
		}
	}
	ex, err := executor.ByName(*exe, executor.Options{RunnerID: "step-test", AllowUnsandboxed: true})
	if err != nil {
		return err
	}
	if _, host := ex.(*executor.Host); host && *exe != "host" {
		fmt.Fprintln(os.Stderr, "No Podman or Docker was found: running on this machine, which cannot enforce the step's sandbox (a Station with a container or Kubernetes executor does). The conformance checks still run.")
	}
	m, merr := steptest.ReadManifest(dir)
	if merr != nil {
		return merr
	}
	if !*asJSON {
		fmt.Printf("Step %s (contract %d) in %s\n%s\n\n", m.Name, m.Contract, m.Image, m.Sentence())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := steptest.Options{Dir: dir, With: values, Secrets: secretValues, Executor: ex}
	if !*asJSON {
		opts.Log = os.Stdout
	}
	rep, err := steptest.Run(ctx, opts)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{
			"step": m.Name, "executor": rep.Executor, "sandboxed": rep.Sandboxed, "exit_code": rep.ExitCode, "outcome": rep.Outcome.String(),
			"outputs": rep.Result.Values, "notes": rep.Result.Notes, "findings": rep.Result.Findings, "tests": rep.Result.Tests,
			"problems": rep.Problems, "warnings": rep.Warnings, "counted_as_exit": rep.Result.Code, "why": rep.Result.Why,
		}, "", "  ")
		fmt.Println(string(b))
	} else {
		printStepReport(rep)
	}
	if !rep.Conforms() {
		return exitErr(1)
	}
	if rep.Result.Code != 0 {
		return exitErr(1)
	}
	return nil
}

func printStepReport(rep *steptest.Report) {
	fmt.Printf("\nExit code %d: %s (took %s, executor %s", rep.ExitCode, rep.Outcome, rep.Took.Round(1e6), rep.Executor)
	if !rep.Sandboxed {
		fmt.Print(", NOT sandboxed")
	}
	fmt.Println(")")
	if rep.Result.Why != "" {
		fmt.Println("  " + rep.Result.Why)
	}
	if len(rep.Result.Values) > 0 {
		fmt.Println("Outputs:")
		for _, k := range stepkit.SortedKeys(rep.Result.Values) {
			fmt.Printf("  %s (%s) = %s\n", k, rep.Result.Types[k], rep.Result.Values[k])
		}
	}
	for _, n := range rep.Result.Notes {
		switch n.Kind {
		case stepkit.KindSummary:
			fmt.Printf("Summary: %s\n", strings.ReplaceAll(n.Text, "\n", "\n         "))
		default:
			fmt.Printf("%s: %s:%d: %s\n", n.Level, n.File, n.Line, n.Text)
		}
	}
	if rep.Result.Findings > 0 || rep.Result.Tests > 0 {
		fmt.Printf("Findings: %d, test results: %d\n", rep.Result.Findings, rep.Result.Tests)
	}
	for _, w := range rep.Warnings {
		fmt.Println("warning: " + w)
	}
	if len(rep.Problems) == 0 {
		fmt.Println("Conformance: the step keeps to the contract.")
		return
	}
	for _, p := range rep.Problems {
		fmt.Println("CONTRACT: " + p)
	}
}

func stepValidate(args []string) error {
	fs := flag.NewFlagSet("step validate", flag.ContinueOnError)
	against := fs.String("against", "", "a Git tag, branch or commit of this repository to compare each step with: what changed in its inputs, outputs and permissions")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		pos, _ = filepath.Glob(filepath.Join(stepkit.StepDir, "*"))
		sort.Strings(pos)
		if len(pos) == 0 {
			return errors.New("there is no steps/ directory here: give the step's directory, or run this at the root of a repository or library")
		}
	}
	bad := 0
	for _, dir := range pos {
		m, err := steptest.ReadManifest(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			bad++
			continue
		}
		fmt.Printf("%s: step %s, contract %d, %d input(s), %d output(s)\n  %s\n", dir, m.Name, m.Contract, len(m.Inputs), len(m.Outputs), m.Sentence())
		if *against != "" {
			old, err := manifestAt(dir, *against)
			switch {
			case err != nil:
				fmt.Printf("  not in %s (a new step)\n", *against)
			default:
				ch := stepkit.Diff(old, m)
				if len(ch) == 0 {
					fmt.Printf("  nothing changed since %s\n", *against)
				}
				for _, c := range ch {
					mark := "  changed: "
					if c.Widening {
						mark = "  WIDENS: "
					}
					fmt.Println(mark + c.What)
				}
			}
		}
	}
	if bad > 0 {
		return exitErr(1)
	}
	return nil
}

// manifestAt reads a step's manifest as of a Git version of the repository.
func manifestAt(dir, ref string) (*stepkit.Manifest, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	top, err := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(strings.TrimSpace(string(top)), abs)
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("git", "-C", abs, "show", ref+":"+filepath.ToSlash(rel)+"/step.yaml").Output()
	if err != nil {
		return nil, err
	}
	m, probs := stepkit.ParseManifest(out)
	if len(probs) > 0 {
		return nil, probs
	}
	return m, nil
}

func scaffoldFor(dir, name, lang string) (string, error) { return steptest.Scaffold(dir, name, lang) }
