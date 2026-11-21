package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	project := fs.String("project", "", "also check the file against this project on the server you are signed in to: its libraries, and whether it has what the pipeline needs, such as a forge")
	var libs libraryFlag
	fs.Var(&libs, "library", "expand templates from a library directory, with no server: NAME=DIRECTORY, where NAME is what the pipeline's libraries section calls it (repeat for each library)")
	resolved := fs.Bool("resolved", false, "print the resolved pipeline (templates expanded) as JSON; needs --library for each library the pipeline uses")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	path := pipelineFile(pos)
	if len(libs) > 0 || *resolved {
		if *project != "" {
			return errors.New("--library and --resolved work offline; --project asks a server. Use one or the other")
		}
		return validateOffline(path, libs, *resolved)
	}
	p, err := load(path)
	if err != nil {
		return err
	}
	fmt.Printf("%s: pipeline %q is valid, %d step(s) in %d level(s)\n", path, p.Name, len(p.Steps), len(p.Levels()))
	for _, w := range pipeline.OutputWarnings(p) {
		fmt.Fprintf(os.Stderr, "%s:%d: warning: %s: %s\n", path, w.Line, w.Path, w.Message)
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
		fmt.Printf("%s: accepted by the server for project %s\n", path, project)
		return nil
	})
}
