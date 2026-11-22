package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	project := fs.String("project", "", "also check the file against this Flight on the server you are signed in to: its libraries, and whether it has what the pipeline needs, such as a forge")
	var libs libraryFlag
	fs.Var(&libs, "library", "expand templates from a library directory, with no server: NAME=DIRECTORY, where NAME is what the pipeline's libraries section calls it (repeat for each library)")
	var envs libraryFlag
	fs.Var(&envs, "environment", "offline: pretend the Flight may deliver to this environment, in the order given: NAME, or NAME:protected for one that asks a person first (repeat for each; a server supplies the Flight's real ones)")
	var vars libraryFlag
	fs.Var(&vars, "variable", "offline: a variable of the Flight's levels, NAME=VALUE (a list is written [a, b]); starters take image_namespace, registry, gitops_repo, delivery_overrides and health_path from them")
	projectName := fs.String("project-name", "", "offline: the Flight's name, which starters take as the service's name")
	resolved := fs.Bool("resolved", false, "print the resolved pipeline (templates expanded) as JSON; needs --library for each library the pipeline uses")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	path := pipelineFile(pos)
	if len(libs) > 0 || *resolved || len(envs) > 0 || len(vars) > 0 || *projectName != "" {
		if *project != "" {
			return errors.New("--library and --resolved work offline; --project asks a server. Use one or the other")
		}
		return validateOffline(path, libs, *resolved, pipeline.ProjectFacts{Project: *projectName, Environments: envNames(envs), Protected: protectedNames(envs)}, offlineVariables(vars))
	}
	p, err := load(path)
	if err != nil {
		return err
	}
	if *project == "" && !p.Expanded() && (usesOnlyStarters(p) || usesOwnSteps(p)) {
		// The starters ship with Launchway, so they can be checked here: a
		// mistyped starter or input is found now, not by the first run.
		return validateOffline(path, nil, false, pipeline.ProjectFacts{}, nil)
	}
	fmt.Printf("%s: pipeline %q is valid, %d step(s) in %d level(s)\n", path, p.Name, len(p.Steps), len(p.Levels()))
	for _, w := range append(append(append(pipeline.OutputWarnings(p), pipeline.CacheWarnings(p)...), pipeline.DeliveryWarnings(p)...), pipeline.ServiceWarnings(p)...) {
		fmt.Fprintf(os.Stderr, "%s:%d: warning: %s: %s\n", path, w.Line, w.Path, w.Message)
	}
	for _, n := range p.PathNotes() {
		fmt.Printf("  %s\n", n)
	}
	if *project != "" {
		return validateOnServer(path, *project)
	}
	if !p.Expanded() {
		// Templates come from the libraries a server knows; this command
		// reads only the file.
		fmt.Printf("Steps that use templates are not expanded here: check the pipeline in a server's editor to see them.\n")
	}
	return nil
}

// validateOnServer has the server check a pipeline file against a project,
// as the editor does, and prints what it says: problems, and warnings, such
// as that the pipeline filters pull requests by their target branch and the
// project has no forge connection.
func validateOnServer(path, project string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return runClient(func() error {
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		var got struct {
			OK       bool              `json:"ok"`
			Problems pipeline.Problems `json:"problems"`
			Warnings []string          `json:"warnings"`
		}
		if err := newClient(c, os.Stdout).call("POST", "/api/v1/validate?project="+url.QueryEscape(project), rawText(data), &got); err != nil {
			return err
		}
		for _, pr := range got.Problems {
			where := ""
			if pr.Path != "" {
				where = pr.Path + ": "
			}
			fmt.Fprintf(os.Stderr, "%s:%d: %s%s\n", path, pr.Line, where, pr.Message)
		}
		for _, w := range got.Warnings {
			fmt.Fprintf(os.Stderr, "%s: warning: %s\n", path, w)
		}
		if !got.OK {
			return exitErr(1)
		}
		fmt.Printf("%s: accepted by the server for Flight %s\n", path, project)
		return nil
	})
}

// explainProblems prints pipeline problems as validate does.
func explainProblems(path string, err error) error {
	var ps pipeline.Problems
	if errors.As(err, &ps) {
		for _, pr := range ps {
			loc := path
			if pr.Line > 0 {
				loc = fmt.Sprintf("%s:%d", path, pr.Line)
			}
			if pr.File != "" {
				loc += " (" + pr.File + ")"
			}
			where := ""
			if pr.Path != "" {
				where = pr.Path + ": "
			}
			fmt.Fprintf(os.Stderr, "%s: %s%s\n", loc, where, pr.Message)
		}
		return exitErr(1)
	}
	return err
}

// usesOnlyStarters reports whether every library the pipeline names is the
// one that ships with Launchway.
func usesOnlyStarters(p *pipeline.Pipeline) bool {
	for _, l := range p.Libraries {
		if l.Repo != starters.Name || l.Version != starters.Version {
			return false
		}
	}
	return len(p.Libraries) > 0
}

func envNames(list []string) []string {
	var out []string
	for _, e := range list {
		n, _, _ := strings.Cut(e, ":")
		out = append(out, n)
	}
	return out
}

func protectedNames(list []string) []string {
	var out []string
	for _, e := range list {
		if n, flag, _ := strings.Cut(e, ":"); flag == "protected" {
			out = append(out, n)
		}
	}
	return out
}

// offlineVariables reads --variable NAME=VALUE: a value written [a, b] is a list.
func offlineVariables(list []string) pipeline.Variables {
	out := pipeline.Variables{}
	for _, e := range list {
		name, val, _ := strings.Cut(e, "=")
		v := pipeline.BoundVariable{Name: name, Type: "string", Text: val, Display: val, Source: "--variable"}
		if strings.HasPrefix(val, "[") {
			v.Type = "list"
			items := []string{}
			for _, it := range strings.Split(strings.Trim(val, "[]"), ",") {
				if it = strings.Trim(strings.TrimSpace(it), `"'`); it != "" {
					items = append(items, it)
				}
			}
			b, _ := json.Marshal(items)
			v.Text = string(b)
		}
		out[name] = v
	}
	return out
}

// usesOwnSteps reports whether every template-like step of the pipeline is a
// step of its own repository (uses: ./steps/NAME) or a starter, which can be
// expanded here from the files of the repository, with no server.
func usesOwnSteps(p *pipeline.Pipeline) bool {
	any := false
	for _, s := range p.Steps {
		if !s.IsUse() {
			continue
		}
		if _, _, self := pipeline.SplitUses(s.Uses); self {
			any = true
		} else if !strings.HasPrefix(s.Uses, "launchway/") {
			return false
		}
	}
	return any
}
